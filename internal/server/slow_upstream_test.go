package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// A slow upstream past MISS_WAIT_SECONDS gets an interim answer; the real mix
// is stored when the upstream answers and the next request is a hit.
func TestSlowUpstreamGetsInterimThenHit(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"MISS_WAIT_SECONDS": "0.1", "PRECACHE_ON_PLAY": "false"})
	h.up.similarGate = make(chan struct{})
	resp, body := h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=seed&f=json", nil)
	if resp.Header.Get(HeaderOutcome) != "interim" {
		t.Fatalf("outcome %q, want interim: %s", resp.Header.Get(HeaderOutcome), body)
	}
	if n := mixCount(t, body); n < 5 || n > 25 {
		t.Fatalf("interim has %d tracks, want 5-25", n)
	}
	if strings.Contains(body, `"id":"seed"`) {
		t.Fatalf("interim contains the seed: %s", body)
	}
	if !strings.Contains(body, "getTopSongs-") || !strings.Contains(body, "getRandomSongs-") {
		t.Fatalf("interim should mix the artist's songs and the genre's: %s", body)
	}
	if _, err := h.store.Get(context.Background(), "alice", "seed"); err == nil {
		t.Fatal("the interim was stored")
	}
	close(h.up.similarGate)
	waitStored(t, h, "alice", "seed")
	entry, _ := h.store.Get(context.Background(), "alice", "seed")
	for _, song := range entry.Songs {
		if id, _ := song["id"].(string); !strings.HasPrefix(id, "a") || strings.Contains(id, "-") {
			t.Fatalf("stored mix holds a non-upstream track %q", id)
		}
	}
	resp, _ = h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=seed&f=json", nil)
	if !strings.HasPrefix(resp.Header.Get(HeaderOutcome), "hit;") {
		t.Fatalf("after the upstream answered: %q, want a hit", resp.Header.Get(HeaderOutcome))
	}
	counters := h.srv.StatusDocument(context.Background()).Record.Counters
	if counters["interim"] != 1 || counters["interim-then-real"] != 1 {
		t.Fatalf("counters %v", counters)
	}
}

// Without enough related tracks no interim is sent: the request waits for the
// upstream and gets the real answer.
func TestShortInterimKeepsWaiting(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"MISS_WAIT_SECONDS": "0.05", "PRECACHE_ON_PLAY": "false"})
	h.up.similarGate = make(chan struct{})
	h.up.libraryCount = 1
	go func() {
		time.Sleep(300 * time.Millisecond)
		close(h.up.similarGate)
	}()
	resp, body := h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=seed&f=json", nil)
	if resp.Header.Get(HeaderOutcome) != "miss" || mixCount(t, body) != 20 {
		t.Fatalf("outcome %q with %d tracks, want miss with 20", resp.Header.Get(HeaderOutcome), mixCount(t, body))
	}
}

// A client that hangs up does not cancel the upstream call: the mix is still
// stored and the hang-up is recorded.
func TestClientLeavingStillStoresTheMix(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"MISS_WAIT_SECONDS": "0", "PRECACHE_ON_PLAY": "false"})
	h.up.similarGate = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.front.URL+"/rest/getSimilarSongs?"+alice+"&id=seed&f=json", nil)
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Fatal("the request should have timed out on the client side")
	}
	time.Sleep(50 * time.Millisecond) // let the server notice the hang-up
	close(h.up.similarGate)
	waitStored(t, h, "alice", "seed")
	doc := h.srv.StatusDocument(context.Background())
	if doc.Record.Counters["client-left"] != 1 {
		t.Fatalf("client-left not counted: %v", doc.Record.Counters)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !hasEvent(h, "client-left") {
		if time.Now().After(deadline) {
			t.Fatal("no client-left event")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A tap on a song whose pre-cache build is already running joins that call.
func TestTapJoinsRunningBackgroundBuild(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"MISS_WAIT_SECONDS": "0"})
	h.up.similarGate = make(chan struct{})
	h.get(t, "/rest/stream?"+alice+"&id=song1", nil)
	deadline := time.Now().Add(2 * time.Second)
	for h.up.count("getSimilarSongs2") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the background build never reached the upstream")
		}
		time.Sleep(5 * time.Millisecond)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(h.up.similarGate)
	}()
	resp, body := h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=song1&f=json", nil)
	if resp.Header.Get(HeaderOutcome) != "miss" || mixCount(t, body) != 20 {
		t.Fatalf("outcome %q, %d tracks", resp.Header.Get(HeaderOutcome), mixCount(t, body))
	}
	waitStored(t, h, "alice", "song1")
	if n := h.up.count("getSimilarSongs2") + h.up.count("getSimilarSongs"); n != 1 {
		t.Fatalf("upstream Instant Mix called %d times, want 1", n)
	}
}

// SwiftSonic (spec 5.2) sends count=50 and count=100 for one seed at once,
// as getSimilarSongs. They share one upstream call.
func TestSwiftSonicPairSharesOneCall(t *testing.T) {
	h := newHarness(t, 30, map[string]string{"PRECACHE_ON_PLAY": "false"})
	h.up.similarGate = make(chan struct{})
	var wg sync.WaitGroup
	outcomes := make([]string, 2)
	for i, count := range []string{"50", "100"} {
		wg.Add(1)
		go func(i int, count string) {
			defer wg.Done()
			resp, _ := h.get(t, "/rest/getSimilarSongs?u=alice&t=x&s=y&p=secret&v=1.16.1&c=SwiftSonic&f=json&id=seed&count="+count, nil)
			outcomes[i] = resp.Header.Get(HeaderOutcome)
		}(i, count)
	}
	time.Sleep(100 * time.Millisecond)
	close(h.up.similarGate)
	wg.Wait()
	if outcomes[0] != "miss" || outcomes[1] != "miss" {
		t.Fatalf("outcomes %v, want two misses", outcomes)
	}
	if n := h.up.count("getSimilarSongs"); n != 1 {
		t.Fatalf("upstream getSimilarSongs called %d times, want 1", n)
	}
}

func hasEvent(h *harness, kind string) bool {
	for _, e := range h.srv.StatusDocument(context.Background()).Record.Events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}
