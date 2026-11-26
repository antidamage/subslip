package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antidamage/subslip/internal/cache"
)

// Two callers authenticating only with API keys must not share cache rows.
func TestAPIKeyCallersAreSeparate(t *testing.T) {
	h := newHarness(t, 20, nil)
	_, body := h.get(t, "/rest/getSimilarSongs2?apiKey=keyA&id=seed&f=json", nil)
	if !strings.Contains(body, `"x0"`) {
		t.Fatalf("key A miss: %s", body)
	}
	h.srv.Scheduler().Wait()
	deadline := 0
	for {
		resp, body := h.get(t, "/rest/getSimilarSongs2?apiKey=keyA&id=seed&f=json", nil)
		if strings.HasPrefix(resp.Header.Get(HeaderOutcome), "hit") {
			if !strings.Contains(body, `"x0"`) {
				t.Fatalf("key A hit has the wrong songs: %s", body)
			}
			break
		}
		if deadline++; deadline > 100 {
			t.Fatal("key A mix never cached")
		}
	}
	resp, body := h.get(t, "/rest/getSimilarSongs2?apiKey=keyB&id=seed&f=json", nil)
	if resp.Header.Get(HeaderOutcome) != "miss" || strings.Contains(body, `"x0"`) {
		t.Fatalf("key B got key A's mix: %s %s", resp.Header.Get(HeaderOutcome), body)
	}
}

// While a user's call for one song is in flight, a request for another song is
// passed straight to the upstream and still gets a full answer.
func TestBusyIsProxied(t *testing.T) {
	h := newHarness(t, 20, nil)
	inside, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	testHookStage1 = func() {
		once.Do(func() { close(inside); <-release })
	}
	defer func() { testHookStage1 = nil }()

	done := make(chan struct{})
	go func() {
		rawGet(h.front.URL + "/rest/getSimilarSongs2?" + alice + "&id=first&f=json")
		close(done)
	}()
	<-inside
	resp, body := h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=second&f=json", nil)
	close(release)
	<-done
	if resp.Header.Get(HeaderOutcome) != "busy" || mixCount(t, body) != 20 {
		t.Fatalf("second request: %s %s", resp.Header.Get(HeaderOutcome), body)
	}
}

// A panic inside SubSlip's handling must end in the request being proxied.
func TestPanicFallsBackToProxy(t *testing.T) {
	h := newHarness(t, 20, nil)
	testHookStage1 = func() { panic("injected") }
	defer func() { testHookStage1 = nil }()
	resp, body := h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=seed&f=json", nil)
	if resp.Header.Get(HeaderOutcome) != "fallback:panic" || mixCount(t, body) != 20 {
		t.Fatalf("%s %s", resp.Header.Get(HeaderOutcome), body)
	}
	testHookStage1 = nil
	// The panicked flight must not block the next request for the same song.
	resp, _ = h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=seed&f=json", nil)
	if resp.Header.Get(HeaderOutcome) != "miss" {
		t.Fatalf("after the panic: %s", resp.Header.Get(HeaderOutcome))
	}
}

// A cache path that cannot be created is an error from Open, which the server
// turns into pass-through mode (TestNoStoreMeansPassthrough).
func TestUnwritableCachePath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Open(context.Background(), filepath.Join(blocker, "sub", "c.db")); err == nil {
		t.Fatal("opening a cache under a regular file should fail")
	}
}

// A request sharing a flight whose leader panics is proxied, not given a 502.
func TestPanicFollowerFallsBack(t *testing.T) {
	h := newHarness(t, 20, nil)
	inside, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	testHookStage1 = func() {
		once.Do(func() { close(inside); <-release; panic("injected") })
	}
	defer func() { testHookStage1 = nil }()
	go rawGet(h.front.URL + "/rest/getSimilarSongs2?" + alice + "&id=seed&f=json")
	<-inside
	result := make(chan string, 1)
	go func() {
		outcome, body := rawGet(h.front.URL + "/rest/getSimilarSongs2?" + alice + "&id=seed&f=json")
		result <- outcome + " " + body
	}()
	time.Sleep(50 * time.Millisecond) // let the follower join the flight
	close(release)
	got := <-result
	if !strings.HasPrefix(got, "fallback:panic ") || mixCount(t, strings.SplitN(got, " ", 2)[1]) != 20 {
		t.Fatalf("follower got %s", got)
	}
}

// rawGet is h.get for goroutines, where t.Fatal must not be called.
func rawGet(url string) (outcome, body string) {
	resp, err := http.Get(url)
	if err != nil {
		return "error", err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.Header.Get(HeaderOutcome), string(b)
}
