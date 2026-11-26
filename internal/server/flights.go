// flights.go makes sure SubSlip never issues the same Instant Mix call twice
// at once, and applies the per-user flood rule (spec 5.4).
//
// Works with: instantmix.go, which calls begin/end around a miss and do for
// the upstream call itself.
package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/antidamage/subslip/internal/mix"
)

type flight struct {
	done chan struct{}
	res  *mix.Stage1Result
	err  error
}

type userFlight struct {
	inflightID string
	inflight   int
	lastID     string
	lastStart  time.Time
}

type flights struct {
	rate time.Duration
	now  func() time.Time

	mu    sync.Mutex
	calls map[string]*flight
	users map[string]*userFlight
}

func newFlights(rate time.Duration, now func() time.Time) *flights {
	return &flights{rate: rate, now: now, calls: map[string]*flight{}, users: map[string]*userFlight{}}
}

// begin reports whether user may run a miss for id now. It refuses while the
// user has a call for a different song in flight, or started one for a
// different song less than rate ago. A refused request is proxied upstream,
// so no request ever waits on SubSlip. Every true must be paired with end.
func (f *flights) begin(user, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[user]
	if !ok {
		if len(f.users) > 1000 {
			f.sweepLocked()
		}
		u = &userFlight{}
		f.users[user] = u
	}
	now := f.now()
	if u.inflight > 0 && u.inflightID != id {
		return false
	}
	if u.inflight == 0 && u.lastID != "" && u.lastID != id && now.Sub(u.lastStart) < f.rate {
		return false
	}
	if u.inflight == 0 {
		u.inflightID = id
		u.lastID = id
		u.lastStart = now
	}
	u.inflight++
	return true
}

func (f *flights) end(user string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[user]; ok && u.inflight > 0 {
		u.inflight--
		if u.inflight == 0 {
			u.inflightID = ""
		}
	}
}

// errFlightPanicked is what followers get when the shared call panicked.
var errFlightPanicked = errors.New("shared upstream call panicked")

// do runs call once per (user, id); concurrent callers for the same key share
// its result. leader is true for the caller that ran call, so only it starts
// the background finish. A caller whose own context ends stops waiting. If
// call panics, followers are released with an error and the panic continues
// in the leader, where the server's recovery proxies the request.
func (f *flights) do(ctx context.Context, user, id string, call func() (*mix.Stage1Result, error)) (res *mix.Stage1Result, leader bool, err error) {
	key := user + "\x00" + id
	f.mu.Lock()
	if fl, ok := f.calls[key]; ok {
		f.mu.Unlock()
		select {
		case <-fl.done:
			return fl.res, false, fl.err
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	fl := &flight{done: make(chan struct{}), err: errFlightPanicked}
	f.calls[key] = fl
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.calls, key)
		f.mu.Unlock()
		close(fl.done)
	}()
	fl.res, fl.err = call()
	return fl.res, true, fl.err
}

func (f *flights) sweepLocked() {
	now := f.now()
	for user, u := range f.users {
		if u.inflight == 0 && now.Sub(u.lastStart) > f.rate {
			delete(f.users, user)
		}
	}
}
