package schedule

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recorder collects the songs whose jobs ran, in order.
type recorder struct {
	mu   sync.Mutex
	ran  []string
	gate chan struct{} // when set, jobs block until it is closed
}

func (r *recorder) job(song string) Job {
	return func(ctx context.Context) {
		if r.gate != nil {
			<-r.gate
		}
		r.mu.Lock()
		r.ran = append(r.ran, song)
		r.mu.Unlock()
	}
}

func (r *recorder) songs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ran...)
}

func newTest() (*Scheduler, *FakeClock) {
	clock := NewFakeClock(time.Unix(1_000_000, 0))
	return New(clock, 10*time.Second, 10*time.Second, time.Minute, nil), clock
}

func TestSkippedSongNeverRuns(t *testing.T) {
	s, clock := newTest()
	rec := &recorder{}
	s.Play("u", "a", rec.job("a"))
	clock.Advance(5 * time.Second)
	s.Play("u", "b", rec.job("b"))
	clock.Advance(10 * time.Second)
	s.Wait()
	if got := rec.songs(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("ran %v, want only b", got)
	}
}

func TestRepeatedPlayIgnored(t *testing.T) {
	s, clock := newTest()
	rec := &recorder{}
	if !s.Play("u", "a", rec.job("a")) {
		t.Fatal("first play refused")
	}
	clock.Advance(9 * time.Second)
	if s.Play("u", "a", rec.job("a")) {
		t.Fatal("repeat accepted; it would restart the debounce")
	}
	clock.Advance(time.Second)
	s.Wait()
	if got := rec.songs(); len(got) != 1 {
		t.Fatalf("ran %v", got)
	}
}

func TestOneActiveOnePendingWithGap(t *testing.T) {
	s, clock := newTest()
	rec := &recorder{gate: make(chan struct{})}
	s.Enqueue("u", "a", rec.job("a")) // starts now, blocks on gate
	s.Enqueue("u", "b", rec.job("b")) // pending
	s.Enqueue("u", "c", rec.job("c")) // replaces b
	if q := s.Snapshot(); len(q) != 1 || q[0].Active != "a" || q[0].Pending != "c" {
		t.Fatalf("snapshot %+v", q)
	}
	close(rec.gate)
	waitFor(t, func() bool { return len(rec.songs()) == 1 })
	// c may not start until 10 s after a started.
	time.Sleep(20 * time.Millisecond)
	if len(rec.songs()) != 1 {
		t.Fatal("pending job started before the gap")
	}
	clock.Advance(10 * time.Second)
	s.Wait()
	if got := rec.songs(); len(got) != 2 || got[1] != "c" {
		t.Fatalf("ran %v, want a then c", got)
	}
}

func TestUsersAreIndependent(t *testing.T) {
	s, _ := newTest()
	rec := &recorder{}
	s.Enqueue("u1", "a", rec.job("a"))
	s.Enqueue("u2", "a", rec.job("a"))
	s.Wait()
	if len(rec.songs()) != 2 {
		t.Fatalf("ran %v", rec.songs())
	}
}

func TestCancelDebounce(t *testing.T) {
	s, clock := newTest()
	rec := &recorder{}
	s.Play("u", "a", rec.job("a"))
	s.CancelDebounce("u", "a")
	clock.Advance(time.Minute)
	s.Wait()
	if len(rec.songs()) != 0 {
		t.Fatal("cancelled debounce ran")
	}
}

func TestPanickingJobFreesSlot(t *testing.T) {
	s, _ := newTest()
	s.Enqueue("u", "a", func(context.Context) { panic("boom") })
	s.Wait()
	if q := s.Snapshot(); len(q) != 0 {
		t.Fatalf("slot still held: %+v", q)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
