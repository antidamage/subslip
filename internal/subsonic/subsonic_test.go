package subsonic

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMethodFromPath(t *testing.T) {
	cases := map[string]string{
		"/rest/getSimilarSongs2.view": "getsimilarsongs2",
		"/navidrome/rest/stream":      "stream",
		"/rest/":                      "",
		"/app/index.html":             "",
		"/rest/getCoverArt/extra":     "",
	}
	for path, want := range cases {
		if got := MethodFromPath(path); got != want {
			t.Errorf("%s: got %q want %q", path, got, want)
		}
	}
}

func TestParseFormPostRestoresBody(t *testing.T) {
	body := "u=alice&t=tok&s=salt&id=42&f=json"
	r := httptest.NewRequest(http.MethodPost, "/rest/getSimilarSongs2?c=app", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req, ok := Parse(r)
	if !ok || req.Params.Get("id") != "42" || req.Auth.Client != "app" || req.Auth.Style() != "token" || req.Format() != "json" {
		t.Fatalf("parsed %+v ok=%v", req, ok)
	}
	again, _ := io.ReadAll(r.Body)
	if string(again) != body {
		t.Fatalf("body not restored: %q", again)
	}
}

func TestParseOversizedFormKeepsWholeBody(t *testing.T) {
	body := "id=1&pad=" + strings.Repeat("x", maxFormBody+10)
	r := httptest.NewRequest(http.MethodPost, "/rest/scrobble", strings.NewReader(body))
	r.ContentLength = -1
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, ok := Parse(r); ok {
		t.Fatal("oversized form should not be parsed")
	}
	again, _ := io.ReadAll(r.Body)
	if string(again) != body {
		t.Fatalf("body truncated: %d of %d bytes", len(again), len(body))
	}
}

func sampleSong() Song {
	return Song{
		"id": "s1", "title": "A & B", "duration": json.Number("200"), "starred": nil, "isDir": false,
		"genres":     []any{map[string]any{"name": "Rock"}, map[string]any{"name": "Pop"}},
		"replayGain": map[string]any{"trackGain": json.Number("-6.5")},
		"moods":      []any{"calm"},
	}
}

func TestWriteSongsXML(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteSongs(rec, "xml", Meta{"version": "1.16.1", "type": "navidrome"}, "similarSongs2", []Song{sampleSong()}); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		XMLName xml.Name `xml:"subsonic-response"`
		Status  string   `xml:"status,attr"`
		Type    string   `xml:"type,attr"`
		Songs   []struct {
			ID     string `xml:"id,attr"`
			Title  string `xml:"title,attr"`
			Genres []struct {
				Name string `xml:"name,attr"`
			} `xml:"genres"`
			Gain struct {
				Track string `xml:"trackGain,attr"`
			} `xml:"replayGain"`
			Moods []string `xml:"moods"`
		} `xml:"similarSongs2>song"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("%v\n%s", err, rec.Body)
	}
	s := doc.Songs[0]
	if doc.Status != "ok" || doc.Type != "navidrome" || s.Title != "A & B" || len(s.Genres) != 2 || s.Gain.Track != "-6.5" || s.Moods[0] != "calm" {
		t.Fatalf("decoded %+v from %s", doc, rec.Body)
	}
}

func TestWriteSongsAndErrorJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteSongs(rec, "json", DefaultMeta(), "similarSongs", nil)
	if !strings.Contains(rec.Body.String(), `"similarSongs":{"song":[]}`) {
		t.Fatalf("empty list shape: %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	WriteError(rec, "json", DefaultMeta(), 40, "Wrong username or password")
	var out map[string]map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["subsonic-response"]["status"] != "failed" {
		t.Fatalf("error envelope: %s", rec.Body)
	}
}
