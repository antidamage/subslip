package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// leakCheckPassword must never appear in logs or status output.
const leakCheckPassword = "Zq9leakcheckPW"

// syncWriter makes a bytes.Buffer safe for the concurrent log writes of
// background jobs.
type syncWriter struct {
	mu  sync.Mutex
	buf *bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func TestCredentialsNeverWritten(t *testing.T) {
	h := newHarness(t, 20, nil)
	carol := "u=carol&p=" + leakCheckPassword + "&t=" + leakCheckPassword + "&s=" + leakCheckPassword + "&apiKey=" + leakCheckPassword + "&c=leakclient"
	h.get(t, "/rest/getSimilarSongs2?"+carol+"&id=seed&f=json", nil)
	h.get(t, "/rest/stream?"+carol+"&id=s2", nil)
	h.get(t, "/rest/getSimilarSongs2?u=carol&p=wrong"+leakCheckPassword+"&id=seed", nil)
	waitStored(t, h, "carol", "seed")

	// Now take the upstream away, so net/http errors carrying full URLs are
	// logged by the proxy and by SubSlip's own calls.
	h.up.Close()
	h.get(t, "/rest/getAlbumList2?"+carol, nil)
	h.get(t, "/rest/getSimilarSongs2?"+carol+"&id=other&f=json", nil)
	h.srv.Scheduler().Wait()

	_, status := h.get(t, "/subslip/status", http.Header{"Authorization": {"Bearer tok"}})
	for name, text := range map[string]string{"logs": h.logs.String(), "status": status} {
		if strings.Contains(text, leakCheckPassword) {
			t.Fatalf("password found in %s:\n%s", name, text)
		}
	}
	if !strings.Contains(h.logs.String(), "upstream") {
		t.Fatal("expected upstream errors in the log; the test did not exercise the error path")
	}
}

// The interim and client-left paths log and record events too; the password
// must not reach either.
func TestCredentialsNeverWrittenOnSlowPaths(t *testing.T) {
	h := newHarness(t, 20, map[string]string{"MISS_WAIT_SECONDS": "0.05", "PRECACHE_ON_PLAY": "false"})
	h.up.similarGate = make(chan struct{})
	carol := "u=carol&p=" + leakCheckPassword + "&c=leakclient"
	resp, _ := h.get(t, "/rest/getSimilarSongs2?"+carol+"&id=seed&f=json", nil)
	if resp.Header.Get(HeaderOutcome) != "interim" {
		t.Fatalf("outcome %q, want interim", resp.Header.Get(HeaderOutcome))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.front.URL+"/rest/getSimilarSongs2?"+carol+"&id=other&f=json", nil)
	http.DefaultClient.Do(req)
	time.Sleep(50 * time.Millisecond)
	close(h.up.similarGate)
	waitStored(t, h, "carol", "seed")
	waitStored(t, h, "carol", "other")
	time.Sleep(50 * time.Millisecond)
	_, status := h.get(t, "/subslip/status", http.Header{"Authorization": {"Bearer tok"}})
	for name, text := range map[string]string{"logs": h.logs.String(), "status": status} {
		if strings.Contains(text, leakCheckPassword) {
			t.Fatalf("password found in %s:\n%s", name, text)
		}
	}
}
