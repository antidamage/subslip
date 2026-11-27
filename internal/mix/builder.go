// Package mix builds Instant Mixes.
//
// File map:
//   - builder.go     (this file) the Builder, stage 1 (the upstream's own
//     getSimilarSongs/getSimilarSongs2) and Finish (stage 1 + supplement).
//   - supplement.go  extra tracks from ListenBrainz, resolved with search3.
//   - match.go       decides whether a search3 hit is the recording asked for.
//
// Used by internal/server: the Instant Mix handler calls Stage1 for the reply
// and Finish in the background; the playback hook calls Build.
// Rules are in specs/subslip.md section 6.
package mix

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/antidamage/subslip/internal/listenbrainz"
	"github.com/antidamage/subslip/internal/subsonic"
	"github.com/antidamage/subslip/internal/upstream"
)

// Builder holds what a build needs. LB may be nil (ListenBrainz disabled).
type Builder struct {
	Up                 *upstream.Client
	LB                 *listenbrainz.Client
	MinTracks          int
	MaxTracks          int
	MaxSearchLookups   int
	SupplementDeadline time.Duration
	Log                *slog.Logger
}

// Stage1Result is the upstream's own answer.
type Stage1Result struct {
	Songs []subsonic.Song
	Meta  subsonic.Meta
	Took  time.Duration
}

// Result is a finished mix.
type Result struct {
	Songs        []subsonic.Song
	Meta         subsonic.Meta
	Source       string
	Took         time.Duration
	Supplemented int
	// Note says why the supplement stopped or was skipped, for status/logs.
	Note string
}

// Stage1 makes the client's call once: method ("getsimilarsongs" or
// "getsimilarsongs2") for id, asking for count tracks (at least MaxTracks).
func (b *Builder) Stage1(ctx context.Context, auth subsonic.Auth, method, id string, count int) (*Stage1Result, error) {
	if count < b.MaxTracks {
		count = b.MaxTracks
	}
	wire := "getSimilarSongs2"
	if method == "getsimilarsongs" {
		wire = "getSimilarSongs"
	}
	resp, err := b.Up.Call(ctx, wire, auth, url.Values{"id": {id}, "count": {strconv.Itoa(count)}})
	if err != nil {
		return nil, err
	}
	songs := upstream.Songs(resp.Body, subsonic.ElementFor(method))
	return &Stage1Result{Songs: withoutSeed(songs, id), Meta: subsonic.MetaFrom(resp.Body), Took: resp.Took}, nil
}

// Build runs stage 1 with getSimilarSongs2 and then Finish. Used for
// background pre-caching, where nothing is waiting on stage 1.
func (b *Builder) Build(ctx context.Context, auth subsonic.Auth, id string) (*Result, error) {
	stage1, err := b.Stage1(ctx, auth, "getsimilarsongs2", id, b.MaxTracks)
	if err != nil {
		return nil, err
	}
	return b.Finish(ctx, auth, id, stage1), nil
}

// Finish turns a stage-1 answer into the finished mix: stage 1's tracks first,
// in upstream's order, then supplements appended when stage 1 was short. It
// never makes another getSimilarSongs call and never fails: a supplement
// problem is logged and the stage-1 tracks are kept.
func (b *Builder) Finish(ctx context.Context, auth subsonic.Auth, id string, stage1 *Stage1Result) *Result {
	started := time.Now()
	songs := capSongs(dedupe(stage1.Songs), b.MaxTracks)
	result := &Result{Songs: songs, Meta: stage1.Meta, Source: "upstream"}
	if len(songs) >= b.MinTracks || b.LB == nil {
		if b.LB == nil && len(songs) < b.MinTracks {
			result.Note = "ListenBrainz supplement disabled"
		}
		result.Took = stage1.Took
		return result
	}
	supplementCtx, cancel := context.WithTimeout(ctx, b.SupplementDeadline)
	defer cancel()
	extra, looked, err := b.supplement(supplementCtx, auth, id, songs)
	if err != nil {
		result.Note = "supplement: " + err.Error()
		b.Log.Debug("supplement stopped early", "song", id, "reason", err.Error(), "found", len(extra))
	}
	if err == nil && len(extra) == 0 {
		result.Note = fmt.Sprintf("supplement: none of %d ListenBrainz suggestions are in the library", looked)
	}
	if len(extra) > 0 {
		result.Songs = capSongs(dedupe(append(songs, extra...)), b.MaxTracks)
		result.Supplemented = len(result.Songs) - len(songs)
		result.Source = "upstream+listenbrainz"
	}
	result.Took = stage1.Took + time.Since(started)
	return result
}

// Tidy applies the rules every stored list follows (section 6): seed left out,
// duplicates removed, at most max tracks.
func Tidy(songs []subsonic.Song, seed string, max int) []subsonic.Song {
	return capSongs(dedupe(withoutSeed(songs, seed)), max)
}

func withoutSeed(songs []subsonic.Song, seed string) []subsonic.Song {
	out := make([]subsonic.Song, 0, len(songs))
	for _, s := range songs {
		if upstream.Str(s, "id") != seed {
			out = append(out, s)
		}
	}
	return out
}

func dedupe(songs []subsonic.Song) []subsonic.Song {
	seen := make(map[string]bool, len(songs))
	out := make([]subsonic.Song, 0, len(songs))
	for _, s := range songs {
		id := upstream.Str(s, "id")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, s)
	}
	return out
}

func capSongs(songs []subsonic.Song, max int) []subsonic.Song {
	if len(songs) > max {
		return songs[:max]
	}
	return songs
}
