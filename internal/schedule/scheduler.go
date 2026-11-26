// Package schedule runs background mix builds per user: a debounce on plays,
// then at most one running ("active") and one waiting ("pending") build, with
// a minimum gap between starts. Rules: specs/subslip.md section 4.
//
// File map:
//   - scheduler.go  (this file) the state machine.
//   - clock.go      the Clock seam that lets tests drive time.
//
// Used by internal/server: the playback hook calls Play, the Instant Mix
// handler calls CancelDebounce and Enqueue, and status reads Snapshot.
package schedule

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Job is one background build. It must respect ctx.
type Job func(ctx context.Context)

// Event reports a decision, for logs and the status ring.
type Event func(user, song, what string)

// Scheduler is safe for concurrent use.
type Scheduler struct {
	clock   Clock
	delay   time.Duration // debounce after a play
	gap     time.Duration // minimum spacing between job starts per user
	timeout time.Duration // deadline for one job
	event   Event

	mu    sync.Mutex
	users map[string]*userState
	jobs  sync.WaitGroup
}

type queued struct {
	song string
	job  Job
}

type userState struct {
	debounce      *queued
	debounceTimer Timer
	active        string
	pending       *queued
	pendingTimer  Timer
	lastStart     time.Time
}

// New returns a scheduler. event may be nil.
func New(clock Clock, delay, gap, timeout time.Duration, event Event) *Scheduler {
	if event == nil {
		event = func(string, string, string) {}
	}
	return &Scheduler{clock: clock, delay: delay, gap: gap, timeout: timeout, event: event, users: map[string]*userState{}}
}

// Play notes that user started song. Repeats for a song already debouncing,
// pending or running are ignored, since clients send many Range requests per
// track. Otherwise the user's debounce restarts with this song; a song the user
// skips before the delay ends never runs. Returns whether it was accepted.
func (s *Scheduler) Play(user, song string, job Job) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state(user)
	if st.holds(song) {
		return false
	}
	if st.debounce != nil {
		st.debounceTimer.Stop()
		s.event(user, st.debounce.song, "debounce-replaced")
	}
	st.debounce = &queued{song: song, job: job}
	st.debounceTimer = s.clock.AfterFunc(s.delay, func() { s.debounceFired(user, song) })
	return true
}

// Enqueue queues a job with no debounce (used after an Instant Mix miss).
func (s *Scheduler) Enqueue(user, song string, job Job) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state(user)
	if st.active == song || (st.pending != nil && st.pending.song == song) {
		return false
	}
	s.enqueueLocked(user, st, &queued{song: song, job: job})
	return true
}

// CancelDebounce drops user's debounce if it is for song.
func (s *Scheduler) CancelDebounce(user, song string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.users[user]; ok && st.debounce != nil && st.debounce.song == song {
		st.debounceTimer.Stop()
		st.debounce = nil
	}
}

// Wait blocks until every started job has returned. For tests and shutdown.
func (s *Scheduler) Wait() { s.jobs.Wait() }

// UserQueue is one user's state, for status.
type UserQueue struct {
	User       string `json:"user"`
	Debouncing string `json:"debouncing,omitempty"`
	Active     string `json:"active,omitempty"`
	Pending    string `json:"pending,omitempty"`
}

// Snapshot lists users with anything debouncing, running or waiting.
func (s *Scheduler) Snapshot() []UserQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []UserQueue
	for user, st := range s.users {
		q := UserQueue{User: user, Active: st.active}
		if st.debounce != nil {
			q.Debouncing = st.debounce.song
		}
		if st.pending != nil {
			q.Pending = st.pending.song
		}
		if q.Debouncing != "" || q.Active != "" || q.Pending != "" {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out
}

func (s *Scheduler) state(user string) *userState {
	st, ok := s.users[user]
	if !ok {
		if len(s.users) >= 1000 {
			s.sweepLocked()
		}
		st = &userState{}
		s.users[user] = st
	}
	return st
}

// sweepLocked forgets idle users whose last start is older than the gap, so
// arbitrary usernames cannot grow the map without bound.
func (s *Scheduler) sweepLocked() {
	now := s.clock.Now()
	for user, st := range s.users {
		if st.idle() && now.Sub(st.lastStart) > s.gap {
			delete(s.users, user)
		}
	}
}

func (st *userState) holds(song string) bool {
	return (st.debounce != nil && st.debounce.song == song) || st.active == song ||
		(st.pending != nil && st.pending.song == song)
}

func (st *userState) idle() bool {
	return st.debounce == nil && st.active == "" && st.pending == nil && st.pendingTimer == nil
}

func (s *Scheduler) debounceFired(user, song string) {
	defer s.recoverTimer(user, song)
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.users[user]
	if !ok || st.debounce == nil || st.debounce.song != song {
		return // replaced or cancelled after the timer fired
	}
	q := st.debounce
	st.debounce = nil
	s.enqueueLocked(user, st, q)
}

// enqueueLocked starts q now when nothing is running and the gap has passed;
// otherwise q becomes pending, replacing any older pending job.
func (s *Scheduler) enqueueLocked(user string, st *userState, q *queued) {
	if st.active == "" && st.pendingTimer == nil {
		if wait := s.gap - s.clock.Now().Sub(st.lastStart); wait > 0 {
			st.pending = q
			st.pendingTimer = s.clock.AfterFunc(wait, func() { s.promote(user) })
			s.event(user, q.song, "waiting-gap")
			return
		}
		s.startLocked(user, st, q)
		return
	}
	if st.pending != nil {
		s.event(user, st.pending.song, "pending-replaced")
	}
	st.pending = q
	s.event(user, q.song, "pending")
}

func (s *Scheduler) startLocked(user string, st *userState, q *queued) {
	st.active = q.song
	st.lastStart = s.clock.Now()
	s.event(user, q.song, "started")
	s.jobs.Add(1)
	go s.run(user, q)
}

func (s *Scheduler) run(user string, q *queued) {
	defer s.jobs.Done()
	defer s.finished(user)
	defer func() {
		if recover() != nil {
			s.event(user, q.song, "panicked")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	q.job(ctx)
}

func (s *Scheduler) finished(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.users[user]
	st.active = ""
	if st.pending == nil || st.pendingTimer != nil {
		return
	}
	if wait := s.gap - s.clock.Now().Sub(st.lastStart); wait > 0 {
		st.pendingTimer = s.clock.AfterFunc(wait, func() { s.promote(user) })
		return
	}
	q := st.pending
	st.pending = nil
	s.startLocked(user, st, q)
}

func (s *Scheduler) promote(user string) {
	defer s.recoverTimer(user, "")
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.users[user]
	if !ok {
		return
	}
	st.pendingTimer = nil
	if st.active != "" || st.pending == nil {
		return
	}
	q := st.pending
	st.pending = nil
	s.startLocked(user, st, q)
}

// recoverTimer keeps a panic in a timer callback from stopping the process.
// It runs after the deferred Unlock, so the lock is already released.
func (s *Scheduler) recoverTimer(user, song string) {
	if recover() != nil {
		s.event(user, song, "panicked")
	}
}
