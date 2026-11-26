package server

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"testing"
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
