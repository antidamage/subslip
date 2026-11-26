package mix

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/antidamage/subslip/internal/listenbrainz"
	"github.com/antidamage/subslip/internal/subsonic"
	"github.com/antidamage/subslip/internal/upstream"
)

func subsonicReply(w http.ResponseWriter, body map[string]any) {
	body["status"] = "ok"
	json.NewEncoder(w).Encode(map[string]any{"subsonic-response": body})
}

func TestFinishAppendsMatchedListenBrainzTracks(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/rest/") {
		case "getSong":
			subsonicReply(w, map[string]any{"song": map[string]any{"id": "seed", "artistId": "ar1"}})
		case "getArtist":
			subsonicReply(w, map[string]any{"artist": map[string]any{"musicBrainzId": "artist-mbid"}})
		case "search3":
			q := r.URL.Query().Get("query")
			var song map[string]any
			switch q {
			case "First Example Band Open Window":
				song = map[string]any{"id": "owned1", "title": "Open Window", "artist": "First Example Band"}
			case "Second Example Band Quiet Hour":
				// search3 returns a different song: must be rejected
				song = map[string]any{"id": "wrong", "title": "Loud Hour", "artist": "Second Example Band"}
			}
			songs := []any{}
			if song != nil {
				songs = append(songs, song)
			}
			subsonicReply(w, map[string]any{"searchResult3": map[string]any{"song": songs}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	lb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/1/lb-radio/artist/") {
			io.WriteString(w, `{"x":[{"recording_mbid":"r1","similar_artist_name":"First Example Band","total_listen_count":9}],
			                    "y":[{"recording_mbid":"r2","similar_artist_name":"Second Example Band","total_listen_count":5}]}`)
			return
		}
		io.WriteString(w, `{"r1":{"recording":{"name":"Open Window"},"artist":{"name":"First Example Band"}},
		                    "r2":{"recording":{"name":"Quiet Hour"},"artist":{"name":"Second Example Band"}}}`)
	}))
	defer lb.Close()

	base, _ := url.Parse(up.URL)
	b := &Builder{
		Up: upstream.New(base), LB: listenbrainz.New(lb.URL, "test"),
		MinTracks: 3, MaxTracks: 10, MaxSearchLookups: 20, SupplementDeadline: 10 * time.Second,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	stage1 := &Stage1Result{Songs: []subsonic.Song{{"id": "u1"}, {"id": "owned1"}}, Meta: subsonic.DefaultMeta()}
	res := b.Finish(context.Background(), subsonic.Auth{User: "a"}, "seed", stage1)

	// owned1 is already in stage 1; wrong is rejected. Order of stage 1 kept.
	ids := []string{}
	for _, s := range res.Songs {
		ids = append(ids, upstream.Str(s, "id"))
	}
	if strings.Join(ids, ",") != "u1,owned1" || res.Supplemented != 0 {
		t.Fatalf("got %v supplemented=%d", ids, res.Supplemented)
	}

	stage1 = &Stage1Result{Songs: []subsonic.Song{{"id": "u1"}}, Meta: subsonic.DefaultMeta()}
	res = b.Finish(context.Background(), subsonic.Auth{User: "a"}, "seed", stage1)
	ids = ids[:0]
	for _, s := range res.Songs {
		ids = append(ids, upstream.Str(s, "id"))
	}
	if strings.Join(ids, ",") != "u1,owned1" || res.Supplemented != 1 || res.Source != "upstream+listenbrainz" {
		t.Fatalf("got %v supplemented=%d source=%s", ids, res.Supplemented, res.Source)
	}
}
