package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antidamage/subslip/internal/cache"
	"github.com/antidamage/subslip/internal/subsonic"
)

// holdFirstFlight keeps the first Instant Mix call in flight until release is
// called, so a request for another song is turned away by the flood rule.
func holdFirstFlight(t *testing.T, h *harness) (release func()) {
	t.Helper()
	inside, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	testHookStage1 = func() { once.Do(func() { close(inside); <-gate }) }
	done := make(chan struct{})
	go func() {
		rawGet(h.front.URL + "/rest/getSimilarSongs2?" + alice + "&id=first&f=json")
		close(done)
	}()
	<-inside
	return func() {
		close(gate)
		<-done
		testHookStage1 = nil
	}
}

func waitCounter(t *testing.T, h *harness, name string, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.srv.StatusDocument(context.Background()).Record.Counters[name] < want {
		if time.Now().After(deadline) {
			t.Fatalf("counter %s never reached %d: %v", name, want, h.srv.StatusDocument(context.Background()).Record.Counters)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A busy answer is passed through unchanged, then stored, and the next
// request for that song is a hit counted as a used follow-up.
func TestBusyAnswerIsStoredForNextTime(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"PRECACHE_ON_PLAY": "false"})
	release := holdFirstFlight(t, h)
	resp, body := h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=second&f=json", nil)
	release()
	if resp.Header.Get(HeaderOutcome) != "busy" || mixCount(t, body) != 20 {
		t.Fatalf("busy request: %s %s", resp.Header.Get(HeaderOutcome), body)
	}
	waitStored(t, h, "alice", "second")
	entry, _ := h.store.Get(context.Background(), "alice", "second")
	if entry.Source != busySource || len(entry.Songs) != 20 {
		t.Fatalf("stored %d songs from %q", len(entry.Songs), entry.Source)
	}
	resp, _ = h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=second&f=json", nil)
	if !strings.HasPrefix(resp.Header.Get(HeaderOutcome), "hit;") {
		t.Fatalf("after storing: %q", resp.Header.Get(HeaderOutcome))
	}
	waitCounter(t, h, "busy-stored", 1)
	waitCounter(t, h, "busy-stored-then-hit", 1)
}

// A busy copy never replaces a fresh stored mix.
func TestBusyAnswerNeverOverwrites(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"PRECACHE_ON_PLAY": "false"})
	existing := []subsonic.Song{}
	for i := 0; i < 25; i++ {
		existing = append(existing, subsonic.Song{"id": fmt.Sprintf("kept%d", i)})
	}
	h.store.Put(context.Background(), &cache.Mix{User: "alice", SongID: "seed", Songs: existing,
		Meta: subsonic.DefaultMeta(), Source: "upstream", BuiltAt: time.Now()})
	ids := []string{}
	for i := 0; i < 20; i++ {
		ids = append(ids, fmt.Sprintf(`{"id":"busy%d"}`, i))
	}
	h.srv.storeBusy("alice", "seed", "getsimilarsongs2",
		[]byte(`{"subsonic-response":{"status":"ok","version":"1.16.1","similarSongs2":{"song":[`+strings.Join(ids, ",")+`]}}}`))
	entry, _ := h.store.Get(context.Background(), "alice", "seed")
	if entry.Source != "upstream" || len(entry.Songs) != 25 {
		t.Fatalf("busy copy replaced a fresh entry: %d songs from %q", len(entry.Songs), entry.Source)
	}
	if n := h.srv.StatusDocument(context.Background()).Record.Counters["busy-stored"]; n != 0 {
		t.Fatalf("busy-stored %d, want 0", n)
	}
}

// XML busy answers are proxied only.
func TestBusyXMLIsNotStored(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"PRECACHE_ON_PLAY": "false"})
	release := holdFirstFlight(t, h)
	resp, body := h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=second", nil)
	release()
	if resp.Header.Get(HeaderOutcome) != "busy" || body == "" {
		t.Fatalf("busy XML request: %s", resp.Header.Get(HeaderOutcome))
	}
	waitCounter(t, h, "busy-skipped", 1)
	time.Sleep(50 * time.Millisecond)
	if _, err := h.store.Get(context.Background(), "alice", "second"); err == nil {
		t.Fatal("an XML busy answer was stored")
	}
}
