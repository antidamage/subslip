// clock.go is the time seam for scheduler.go. Production uses RealClock;
// tests use FakeClock and move time by hand, so debounce and gap rules are
// tested without sleeping.
package schedule

import (
	"sort"
	"sync"
	"time"
)

// Clock is the part of the time package the scheduler uses.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer can be stopped.
type Timer interface{ Stop() bool }

// RealClock is the wall clock.
type RealClock struct{}

func (RealClock) Now() time.Time                            { return time.Now() }
func (RealClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// FakeClock is a manual clock for tests. Advance runs due timers in order.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func (t *fakeTimer) Stop() bool { was := !t.stopped; t.stopped = true; return was }

// NewFakeClock starts at start.
func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{now: start} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves time forward by d, firing each due timer at its own time.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].at.Before(c.timers[j].at) })
		var due *fakeTimer
		for i, t := range c.timers {
			if !t.at.After(end) {
				due = t
				c.timers = append(c.timers[:i], c.timers[i+1:]...)
				break
			}
		}
		if due == nil {
			c.now = end
			c.mu.Unlock()
			return
		}
		if due.at.After(c.now) {
			c.now = due.at
		}
		c.mu.Unlock()
		if !due.stopped {
			due.f()
		}
	}
}
