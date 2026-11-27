package server

import (
	"context"
	"testing"
	"time"
)

// An empty background build is retried once, and the retry stores the mix.
func TestEmptyBuildRetriesOnceAndFills(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"EMPTY_RETRY_SECONDS": "0.05"})
	h.up.emptyFirst = 1
	h.get(t, "/rest/stream?"+alice+"&id=flaky", nil)
	waitStored(t, h, "alice", "flaky")
	waitCounter(t, h, "empty-retry", 1)
	waitCounter(t, h, "empty-retry-filled", 1)
	if n := h.up.count("getSimilarSongs2"); n != 2 {
		t.Fatalf("upstream called %d times, want 2", n)
	}
}

// A seed that stays empty gets exactly one retry, and the retry does not
// count toward the backoff.
func TestEmptyRetryNeverRepeats(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"EMPTY_RETRY_SECONDS": "0.05"})
	h.up.emptyFirst = 100
	h.get(t, "/rest/stream?"+alice+"&id=nothing", nil)
	waitCounter(t, h, "empty-retry", 1)
	deadline := time.Now().Add(2 * time.Second)
	for h.up.count("getSimilarSongs2") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the retry never reached the upstream")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if n := h.up.count("getSimilarSongs2"); n != 2 {
		t.Fatalf("upstream called %d times, want 2 (one build, one retry)", n)
	}
	doc := h.srv.StatusDocument(context.Background())
	if doc.Record.Counters["empty-retry"] != 1 || doc.Record.Counters["empty-retry-filled"] != 0 || len(doc.Precache.Resting) != 0 {
		t.Fatalf("counters %v resting %v", doc.Record.Counters, doc.Precache.Resting)
	}
}

// EMPTY_RETRY_SECONDS=0 turns the retry off.
func TestEmptyRetryOff(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"EMPTY_RETRY_SECONDS": "0"})
	h.up.emptyFirst = 1
	h.get(t, "/rest/stream?"+alice+"&id=flaky", nil)
	deadline := time.Now().Add(2 * time.Second)
	for h.up.count("getSimilarSongs2") < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the build never reached the upstream")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if n := h.up.count("getSimilarSongs2"); n != 1 {
		t.Fatalf("upstream called %d times with the retry off", n)
	}
}
