// backoff.go rests a user's background pre-caching after repeated failed
// builds, so a failing upstream gets no extra load from plays (spec 4.9).
//
// Works with: playhook.go (playHook checks resting; backgroundJob reports
// each outcome), status.go (lists resting users).
package server

import (
	"sync"
	"time"
)

const (
	backoffFailures = 3
	backoffRest     = 10 * time.Minute
)

type backoff struct {
	now func() time.Time

	mu        sync.Mutex
	failures  map[string]int
	restUntil map[string]time.Time
}

func newBackoff(now func() time.Time) *backoff {
	return &backoff{now: now, failures: map[string]int{}, restUntil: map[string]time.Time{}}
}

// resting reports whether user's pre-caching is resting now.
func (b *backoff) resting(user string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.restUntil[user]
	if !ok {
		return false
	}
	if b.now().Before(until) {
		return true
	}
	delete(b.restUntil, user)
	return false
}

// failed counts a failed or timed-out build. It returns true when this
// failure starts a rest.
func (b *backoff) failed(user string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures[user]++
	if b.failures[user] < backoffFailures {
		return false
	}
	delete(b.failures, user)
	b.restUntil[user] = b.now().Add(backoffRest)
	return true
}

// succeeded clears user's failure count.
func (b *backoff) succeeded(user string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.failures, user)
}

// snapshot lists resting users and when they resume.
func (b *backoff) snapshot() map[string]time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	out := map[string]time.Time{}
	for user, until := range b.restUntil {
		if now.Before(until) {
			out[user] = until
		}
	}
	return out
}
