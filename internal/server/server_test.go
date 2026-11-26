package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antidamage/subslip/internal/cache"
	"github.com/antidamage/subslip/internal/config"
	"github.com/antidamage/subslip/internal/logging"
)

var fakeModTime = time.Unix(1_700_000_000, 0)

type harness struct {
	up    *fakeUpstream
	srv   *Server
	front *httptest.Server
	store *cache.Store
	logs  *bytes.Buffer
}

func newHarness(t *testing.T, similar int, extra map[string]string) *harness {
	t.Helper()
	up := newFakeUpstream(t, similar)
	values := map[string]string{
		"UPSTREAM_URL": up.URL + "/base", "LISTENBRAINZ_ENABLED": "false", "STATUS_TOKEN": "tok",
		"FETCH_ON_PLAY_DELAY_SECONDS": "0", "REMOTE_STATION_API_QUERY_RATE_LIMIT_SECONDS": "0",
		"MIX_RATE_LIMIT_SECONDS": "0", "LOG_LEVEL": "debug",
	}
	for k, v := range extra {
		values[k] = v
	}
	cfg := config.FromMap(values)
	logs := &bytes.Buffer{}
	h := &harness{up: up, logs: logs}
	var storeErr string
	if values["DB_PATH"] != "unavailable" {
		store, err := cache.Open(context.Background(), filepath.Join(t.TempDir(), "c.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		h.store = store
	} else {
		storeErr = "test: no store"
	}
	h.srv = New(Options{Config: cfg, Logger: logging.NewWithWriter(&syncWriter{buf: logs}, "debug", "text"), Store: h.store, StoreError: storeErr, Version: "test"})
	h.front = httptest.NewServer(h.srv)
	t.Cleanup(h.front.Close)
	return h
}

func (h *harness) get(t *testing.T, path string, header http.Header) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.front.URL+path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

const alice = "u=alice&p=secret&v=1.16.1&c=testclient"

func mixCount(t *testing.T, body string) int {
	var out struct {
		R struct {
			S struct {
				Song []map[string]any `json:"song"`
			} `json:"similarSongs2"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return len(out.R.S.Song)
}

func waitStored(t *testing.T, h *harness, user, song string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := h.store.Get(context.Background(), user, song); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("mix for %s/%s never stored", user, song)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMissThenHit(t *testing.T) {
	h := newHarness(t, 20, nil)
	resp, body := h.get(t, "/rest/getSimilarSongs2.view?"+alice+"&id=seed&f=json", nil)
	if resp.Header.Get(HeaderOutcome) != "miss" || mixCount(t, body) != 20 {
		t.Fatalf("first: %s %s", resp.Header.Get(HeaderOutcome), body)
	}
	waitStored(t, h, "alice", "seed")
	resp, body = h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=seed&f=json&count=5", nil)
	if !strings.HasPrefix(resp.Header.Get(HeaderOutcome), "hit;age=") || mixCount(t, body) != 5 {
		t.Fatalf("second: %s %s", resp.Header.Get(HeaderOutcome), body)
	}
	if n := h.up.count("getSimilarSongs2"); n != 1 {
		t.Fatalf("upstream getSimilarSongs2 called %d times, want 1", n)
	}
}

func TestHitIsPerUser(t *testing.T) {
	h := newHarness(t, 20, nil)
	h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=seed&f=json", nil)
	waitStored(t, h, "alice", "seed")
	resp, body := h.get(t, "/rest/getSimilarSongs2?u=bob&p=bobpw&id=seed&f=json", nil)
	if resp.Header.Get(HeaderOutcome) != "miss" || strings.Contains(body, `"a0"`) {
		t.Fatalf("bob got alice's mix: %s %s", resp.Header.Get(HeaderOutcome), body)
	}
}

func TestAuthFailureIsSubsonicError(t *testing.T) {
	h := newHarness(t, 20, nil)
	resp, body := h.get(t, "/rest/getSimilarSongs2?u=alice&p=wrong&id=seed", nil)
	if resp.Header.Get(HeaderOutcome) != "auth-failed" || !strings.Contains(body, `status="failed"`) || !strings.Contains(body, `code="40"`) {
		t.Fatalf("%s %s", resp.Header.Get(HeaderOutcome), body)
	}
	if h.up.count("getSimilarSongs2") != 0 {
		t.Fatal("a rejected caller reached getSimilarSongs2")
	}
}

func TestXMLHitAndV1Element(t *testing.T) {
	h := newHarness(t, 20, nil)
	_, body := h.get(t, "/rest/getSimilarSongs?"+alice+"&id=seed", nil)
	if !strings.Contains(body, "<similarSongs>") || !strings.Contains(body, `id="a0"`) {
		t.Fatalf("xml miss: %s", body)
	}
}

func TestRangeStreamPassesThrough(t *testing.T) {
	h := newHarness(t, 20, nil)
	resp, body := h.get(t, "/rest/stream?"+alice+"&id=seed", http.Header{"Range": {"bytes=10-19"}})
	if resp.StatusCode != http.StatusPartialContent || body != "0123456789" || resp.Header.Get("Content-Range") == "" {
		t.Fatalf("status %d range %q body %q", resp.StatusCode, resp.Header.Get("Content-Range"), body)
	}
}

func TestPlayPrecachesOnlyAfterFirstInstantMix(t *testing.T) {
	h := newHarness(t, 20, nil)
	h.get(t, "/rest/stream?"+alice+"&id=song1", nil)
	h.srv.Scheduler().Wait()
	time.Sleep(50 * time.Millisecond)
	if h.up.count("getSimilarSongs2") != 0 {
		t.Fatal("auto mode pre-cached a user who never used Instant Mix")
	}
	h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=seed&f=json", nil)
	h.get(t, "/rest/stream?"+alice+"&id=song2", nil)
	waitStored(t, h, "alice", "song2")
}

func TestShortMixIsNotStoredWithoutSupplement(t *testing.T) {
	h := newHarness(t, 3, map[string]string{"LISTENBRAINZ_ENABLED": "true", "LISTENBRAINZ_API_URL": "http://127.0.0.1:1"})
	resp, body := h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=seed&f=json", nil)
	if resp.Header.Get(HeaderOutcome) != "miss" || mixCount(t, body) != 3 {
		t.Fatalf("%s %s", resp.Header.Get(HeaderOutcome), body)
	}
	h.srv.Scheduler().Wait()
	waitStored(t, h, "alice", "seed") // supplement failed; upstream's 3 tracks are the finished mix
}

func TestNoStoreMeansPassthrough(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"DB_PATH": "unavailable"})
	resp, body := h.get(t, "/rest/getSimilarSongs2?"+alice+"&id=seed&f=json", nil)
	if resp.Header.Get(HeaderOutcome) != "fallback:cache" || mixCount(t, body) != 20 {
		t.Fatalf("%s %s", resp.Header.Get(HeaderOutcome), body)
	}
	resp, _ = h.get(t, "/rest/stream?"+alice+"&id=seed", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d", resp.StatusCode)
	}
}

func TestStatusNeedsToken(t *testing.T) {
	h := newHarness(t, 20, nil)
	if resp, _ := h.get(t, "/subslip/status", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without token: %d", resp.StatusCode)
	}
	resp, body := h.get(t, "/subslip/status", http.Header{"Authorization": {"Bearer tok"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"mode"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if _, body := h.get(t, "/subslip/health", nil); strings.TrimSpace(body) != "{\n  \"status\": \"ok\"\n}" {
		t.Fatalf("health says more than ok: %s", body)
	}
}
