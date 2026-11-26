// Package auth checks a caller's Subsonic credentials against the upstream
// before SubSlip answers from its cache, since a cache hit never reaches the
// upstream. Rules: specs/subslip.md section 7.
//
// Used by internal/server (Instant Mix handler). The proof cache is in memory
// only and holds hashes, never credentials.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/antidamage/subslip/internal/subsonic"
	"github.com/antidamage/subslip/internal/upstream"
)

// Verdict is the result of a check.
type Verdict int

const (
	// Allowed: the upstream accepted these credentials (now or within the TTL).
	Allowed Verdict = iota
	// Rejected: the upstream refused them; Failure holds its error.
	Rejected
	// Unknown: the upstream could not be asked. The caller proxies the request
	// so the upstream decides, rather than SubSlip guessing either way.
	Unknown
)

// Gate verifies credentials with upstream ping and remembers successes.
type Gate struct {
	up  *upstream.Client
	ttl time.Duration
	now func() time.Time

	mu     sync.Mutex
	proofs map[string]time.Time
}

// New returns a gate. ttl is AUTH_CACHE_TTL_SECONDS.
func New(up *upstream.Client, ttl time.Duration) *Gate {
	return &Gate{up: up, ttl: ttl, now: time.Now, proofs: map[string]time.Time{}}
}

// Check asks the upstream (or the proof cache) about a. On Rejected, failure
// is the upstream's error to pass back to the client.
func (g *Gate) Check(ctx context.Context, a subsonic.Auth) (Verdict, *upstream.FailedError) {
	key := proofKey(a)
	if g.proven(key) {
		return Allowed, nil
	}
	_, err := g.up.Call(ctx, "ping", a, nil)
	if err == nil {
		g.remember(key)
		return Allowed, nil
	}
	if upstream.IsAuthFailure(err) {
		var failed *upstream.FailedError
		if errors.As(err, &failed) {
			return Rejected, failed
		}
		return Rejected, &upstream.FailedError{Code: 40, Message: "Wrong username or password"}
	}
	return Unknown, nil
}

// proofKey hashes the whole credential set, so a different token, salt,
// password or API key is a different proof.
func proofKey(a subsonic.Auth) string {
	sum := sha256.Sum256([]byte(a.User + "\x00" + a.Token + "\x00" + a.Salt + "\x00" + a.Password + "\x00" + a.APIKey))
	return hex.EncodeToString(sum[:])
}

func (g *Gate) proven(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	expiry, ok := g.proofs[key]
	if ok && g.now().Before(expiry) {
		return true
	}
	if ok {
		delete(g.proofs, key)
	}
	return false
}

func (g *Gate) remember(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if len(g.proofs) > 10000 {
		for k, exp := range g.proofs {
			if now.After(exp) {
				delete(g.proofs, k)
			}
		}
	}
	g.proofs[key] = now.Add(g.ttl)
}
