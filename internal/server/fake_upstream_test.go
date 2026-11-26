package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeUpstream is a small Subsonic server. Users: alice/secret, bob/bobpw.
// Similar songs are per user (alice gets a*, bob gets b*), so a leak of one
// user's cached mix to the other is visible. similarCount controls how many
// tracks getSimilarSongs2 returns. When similarGate is set, Instant Mix calls
// block until it is closed (a slow upstream). libraryCount sets how many songs
// getTopSongs and getRandomSongs return, for the interim.
type fakeUpstream struct {
	*httptest.Server
	similarCount int
	libraryCount int
	similarGate  chan struct{}
	calls        sync.Map // method -> *int64
	streamBody   string
}

func newFakeUpstream(t *testing.T, similarCount int) *fakeUpstream {
	f := &fakeUpstream{similarCount: similarCount, libraryCount: 8, streamBody: strings.Repeat("0123456789", 100)}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeUpstream) count(method string) int64 {
	if v, ok := f.calls.Load(method); ok {
		return atomic.LoadInt64(v.(*int64))
	}
	return 0
}

func (f *fakeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/base/rest/"), ".view")
	counter, _ := f.calls.LoadOrStore(method, new(int64))
	atomic.AddInt64(counter.(*int64), 1)

	if method == "stream" {
		http.ServeContent(w, r, "a.mp3", fakeModTime, strings.NewReader(f.streamBody))
		return
	}
	if method == "getOpenSubsonicExtensions" {
		reply(w, map[string]any{"openSubsonic": true, "type": "navidrome",
			"openSubsonicExtensions": []any{map[string]any{"name": "octoStations"}}})
		return
	}
	user := r.Form.Get("u")
	pass := strings.TrimPrefix(r.Form.Get("p"), "enc:")
	apiKeys := map[string]string{"keyA": "x", "keyB": "y"} // API key -> song id prefix
	prefix, keyOK := apiKeys[r.Form.Get("apiKey")]
	if !keyOK {
		if !(user == "alice" && (pass == "secret" || pass == "736563726574")) && !(user == "bob" && pass == "bobpw") &&
			!(user == "carol" && pass == leakCheckPassword) {
			reply(w, map[string]any{"status": "failed", "error": map[string]any{"code": 40, "message": "Wrong username or password"}})
			return
		}
		prefix = user[:1]
	}
	switch method {
	case "ping", "scrobble":
		reply(w, map[string]any{})
	case "getSong":
		reply(w, map[string]any{"song": map[string]any{"id": r.Form.Get("id"), "title": "Seed", "artist": "Art", "genre": "Jazz"}})
	case "getTopSongs", "getRandomSongs":
		songs := []any{map[string]any{"id": "seed", "title": "Seed", "artist": "Art"}} // the seed must be dropped
		for i := 0; i < f.libraryCount; i++ {
			songs = append(songs, map[string]any{"id": fmt.Sprintf("%s-%s%d", method, prefix, i), "title": "L", "artist": "Art"})
		}
		element := map[string]string{"getTopSongs": "topSongs", "getRandomSongs": "randomSongs"}[method]
		reply(w, map[string]any{element: map[string]any{"song": songs}})
	case "getSimilarSongs2", "getSimilarSongs":
		if f.similarGate != nil {
			<-f.similarGate
		}
		songs := []any{}
		for i := 0; i < f.similarCount; i++ {
			songs = append(songs, map[string]any{"id": fmt.Sprintf("%s%d", prefix, i), "title": fmt.Sprintf("T%d", i), "artist": "Art"})
		}
		element := "similarSongs2"
		if method == "getSimilarSongs" {
			element = "similarSongs"
		}
		reply(w, map[string]any{element: map[string]any{"song": songs}})
	default:
		reply(w, map[string]any{"status": "failed", "error": map[string]any{"code": 0, "message": "not in fake"}})
	}
}

func reply(w http.ResponseWriter, body map[string]any) {
	if _, ok := body["status"]; !ok {
		body["status"] = "ok"
	}
	body["version"] = "1.16.1"
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"subsonic-response": body})
}
