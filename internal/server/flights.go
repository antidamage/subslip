// flights.go makes sure SubSlip never issues the same Instant Mix call twice
// at once, and applies the per-user flood rule (spec 5.4).
//
// Works with: instantmix.go (begin/end around a miss, join for the upstream
// call, interim), playhook.go (background builds join the same flights).
//
// A flight's upstream call runs detached from every request: a client that
// hangs up does not cancel it, so its answer is still stored (spec 5.3).
package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/antidamage/subslip/internal/mix"
	"github.com/antidamage/subslip/internal/subsonic"
)

// flight is one upstream Instant Mix call for (user, song).
type flight struct {
	done chan struct{}
	res  *mix.Stage1Result
	err  error

	// The interim is assembled at most once per flight and shared by joiners.
	interimOnce sync.Once
	interim     []subsonic.Song

	mu          sync.Mutex
	sentInterim bool
}

// answered reports whether the upstream call has finished.
func (fl *flight) answered() bool {
	select {
	case <-fl.done:
		return true
	default:
		return false
	}
}

// interimSongs builds the interim once; later callers get the same list.
func (fl *flight) interimSongs(build func() []subsonic.Song) []subsonic.Song {
	fl.interimOnce.Do(func() { fl.interim = build() })
	return fl.interim
}

func (fl *flight) markInterimSent() {
	fl.mu.Lock()
	fl.sentInterim = true
	fl.mu.Unlock()
}

func (fl *flight) interimWasSent() bool {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	return fl.sentInterim
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
	// panicked, when set, reports a recovered panic (where, value).
	panicked func(where string, value any)
}

func newFlights(rate time.Duration, now func() time.Time) *flights {
	return &flights{rate: rate, now: now, calls: map[string]*flight{}, users: map[string]*userFlight{}}
}

// begin reports whether user may run a miss for id now. It refuses while the
// user has a request for a different song in flight, or started one for a
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

// errFlightPanicked is what every waiter gets when the shared call panicked.
var errFlightPanicked = errors.New("shared upstream call panicked")

// join returns the flight for (user, id), starting it when none is running.
// leader is true for the caller that started it. The call runs in its own
// goroutine with its own deadline, never on a request's context. When it
// returns, done is closed and then the leader's onDone (when set) runs in
// that goroutine.
func (f *flights) join(user, id string, timeout time.Duration,
	call func(ctx context.Context) (*mix.Stage1Result, error),
	onDone func(fl *flight)) (fl *flight, leader bool) {
	key := user + "\x00" + id
	f.mu.Lock()
	if existing, ok := f.calls[key]; ok {
		f.mu.Unlock()
		return existing, false
	}
	fl = &flight{done: make(chan struct{}), err: errFlightPanicked}
	f.calls[key] = fl
	f.mu.Unlock()
	go f.run(key, fl, timeout, call, onDone)
	return fl, true
}

// run makes the call. A panic leaves err as errFlightPanicked and is reported
// through panicked; onDone runs after done is closed, under its own recover,
// since this goroutine has no request to fall back to.
func (f *flights) run(key string, fl *flight, timeout time.Duration,
	call func(ctx context.Context) (*mix.Stage1Result, error), onDone func(fl *flight)) {
	func() {
		defer func() {
			if p := recover(); p != nil && f.panicked != nil {
				f.panicked("upstream call", p)
			}
			f.mu.Lock()
			delete(f.calls, key)
			f.mu.Unlock()
			close(fl.done)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		fl.res, fl.err = call(ctx)
	}()
	if onDone != nil {
		defer func() {
			if p := recover(); p != nil && f.panicked != nil {
				f.panicked("finishing a flight", p)
			}
		}()
		onDone(fl)
	}
}

func (f *flights) sweepLocked() {
	now := f.now()
	for user, u := range f.users {
		if u.inflight == 0 && now.Sub(u.lastStart) > f.rate {
			delete(f.users, user)
		}
	}
}
