// Package listenbrainz reads the two public ListenBrainz endpoints SubSlip
// uses to supplement a short Instant Mix: LB Radio by artist, and recording
// metadata for the titles. No account or token is needed.
//
// Used by internal/mix (supplement.go) and internal/doctor (reachability).
// One Client is shared process-wide so the one-request-per-second spacing
// holds across concurrent builds.
package listenbrainz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Candidate is a recording ListenBrainz suggests.
type Candidate struct {
	RecordingMBID string
	Artist        string
	Title         string
	Listens       int64
}

// Client talks to the ListenBrainz API at BaseURL.
type Client struct {
	BaseURL   string
	UserAgent string
	http      *http.Client
	gap       time.Duration

	mu   sync.Mutex
	last time.Time
}

// New returns a client with a 10 s timeout and 1 s spacing between requests.
func New(baseURL, userAgent string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		UserAgent: userAgent,
		http:      &http.Client{Timeout: 10 * time.Second},
		gap:       time.Second,
	}
}

// RadioArtist returns recordings by artists similar to artistMBID, spread
// across artists (most-listened first within each) so one artist does not
// fill the mix. Titles are not set; see FillTitles.
func (c *Client) RadioArtist(ctx context.Context, artistMBID string) ([]Candidate, error) {
	path := "/1/lb-radio/artist/" + url.PathEscape(artistMBID) +
		"?mode=easy&max_similar_artists=10&max_recordings_per_artist=3&pop_begin=0&pop_end=100"
	var byArtist map[string][]struct {
		RecordingMBID string `json:"recording_mbid"`
		ArtistName    string `json:"similar_artist_name"`
		Listens       int64  `json:"total_listen_count"`
	}
	if err := c.getJSON(ctx, path, &byArtist); err != nil {
		return nil, err
	}
	groups := make([][]Candidate, 0, len(byArtist))
	for _, recs := range byArtist {
		group := make([]Candidate, 0, len(recs))
		for _, r := range recs {
			if r.RecordingMBID != "" {
				group = append(group, Candidate{RecordingMBID: r.RecordingMBID, Artist: r.ArtistName, Listens: r.Listens})
			}
		}
		sort.SliceStable(group, func(i, j int) bool { return group[i].Listens > group[j].Listens })
		if len(group) > 0 {
			groups = append(groups, group)
		}
	}
	// Order artists by their best recording so the result is stable.
	sort.SliceStable(groups, func(i, j int) bool { return groups[i][0].Listens > groups[j][0].Listens })
	return interleave(groups), nil
}

// FillTitles sets Title (and Artist when missing) from recording metadata,
// 25 recordings per request. Candidates without a title afterwards are
// dropped.
func (c *Client) FillTitles(ctx context.Context, candidates []Candidate) ([]Candidate, error) {
	titles := map[string][2]string{}
	for start := 0; start < len(candidates); start += 25 {
		end := min(start+25, len(candidates))
		ids := make([]string, 0, end-start)
		for _, cand := range candidates[start:end] {
			ids = append(ids, cand.RecordingMBID)
		}
		var meta map[string]struct {
			Recording struct {
				Name string `json:"name"`
			} `json:"recording"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
		}
		path := "/1/metadata/recording/?inc=artist&recording_mbids=" + url.QueryEscape(strings.Join(ids, ","))
		if err := c.getJSON(ctx, path, &meta); err != nil {
			return nil, err
		}
		for id, m := range meta {
			titles[id] = [2]string{m.Recording.Name, m.Artist.Name}
		}
	}
	out := candidates[:0:0]
	for _, cand := range candidates {
		t, ok := titles[cand.RecordingMBID]
		if !ok || t[0] == "" {
			continue
		}
		cand.Title = t[0]
		if cand.Artist == "" {
			cand.Artist = t[1]
		}
		out = append(out, cand)
	}
	return out, nil
}

// Reachable makes one cheap request, for doctor.
func (c *Client) Reachable(ctx context.Context) error {
	var ignored any
	return c.getJSON(ctx, "/1/stats/sitewide/artists?count=1", &ignored)
}

func (c *Client) getJSON(ctx context.Context, path string, into any) error {
	if err := c.wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("listenbrainz %s: HTTP %d", strings.SplitN(path, "?", 2)[0], resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(into)
}

// wait enforces the spacing between requests.
func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	next := c.last.Add(c.gap)
	now := time.Now()
	if next.Before(now) {
		next = now
	}
	c.last = next
	c.mu.Unlock()
	if delay := time.Until(next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

// interleave takes one candidate from each group in turn.
func interleave(groups [][]Candidate) []Candidate {
	var out []Candidate
	for i := 0; ; i++ {
		added := false
		for _, g := range groups {
			if i < len(g) {
				out = append(out, g[i])
				added = true
			}
		}
		if !added {
			return out
		}
	}
}
