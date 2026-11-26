// interim.go builds the stand-in list sent when the upstream has not answered
// an Instant Mix by MISS_WAIT_SECONDS (specs/subslip.md section 5.3).
//
// Works with: builder.go (Builder, dedupe, withoutSeed), internal/upstream.
// Used by internal/server/instantmix.go.
//
// Only material related to the seed goes in, in this order: the seed artist's
// top songs interleaved with songs from the seed's genre. Nothing unrelated is
// added to fill it up. The result is never cached.
package mix

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"sync"

	"github.com/antidamage/subslip/internal/subsonic"
	"github.com/antidamage/subslip/internal/upstream"
)

// MinInterimTracks is the shortest interim worth sending. Below it the
// request keeps waiting on the upstream instead.
const MinInterimTracks = 5

// errNoSeed is returned when the seed song cannot be read.
var errNoSeed = errors.New("interim: seed song not found")

// Interim returns up to MaxTracks songs related to seed, or an error. ctx
// carries the whole budget; any call still running when it ends is dropped.
func (b *Builder) Interim(ctx context.Context, auth subsonic.Auth, seed string) ([]subsonic.Song, error) {
	resp, err := b.Up.Call(ctx, "getSong", auth, url.Values{"id": {seed}})
	if err != nil {
		return nil, err
	}
	song, _ := resp.Body["song"].(map[string]any)
	if song == nil {
		return nil, errNoSeed
	}
	artist, genre := upstream.Str(song, "artist"), upstream.Str(song, "genre")

	var wg sync.WaitGroup
	var top, sameGenre []subsonic.Song
	if artist != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			top = b.songsFrom(ctx, auth, "getTopSongs", "topSongs", url.Values{"artist": {artist}, "count": {"10"}})
		}()
	}
	if genre != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sameGenre = b.songsFrom(ctx, auth, "getRandomSongs", "randomSongs",
				url.Values{"genre": {genre}, "size": {strconv.Itoa(b.MaxTracks)}})
		}()
	}
	wg.Wait()
	return capSongs(dedupe(withoutSeed(interleave(top, sameGenre), seed)), b.MaxTracks), nil
}

// songsFrom makes one call and returns its songs, or nothing on any error.
func (b *Builder) songsFrom(ctx context.Context, auth subsonic.Auth, method, element string, params url.Values) []subsonic.Song {
	resp, err := b.Up.Call(ctx, method, auth, params)
	if err != nil {
		if b.Log != nil {
			b.Log.Debug("interim call failed", "method", method, "error", err)
		}
		return nil
	}
	return upstream.Songs(resp.Body, element)
}

// interleave alternates a and b, starting with a, then appends what is left.
func interleave(a, b []subsonic.Song) []subsonic.Song {
	out := make([]subsonic.Song, 0, len(a)+len(b))
	for i := 0; i < len(a) || i < len(b); i++ {
		if i < len(a) {
			out = append(out, a[i])
		}
		if i < len(b) {
			out = append(out, b[i])
		}
	}
	return out
}
