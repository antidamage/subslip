// supplement.go finds extra tracks for a short mix.
//
// Works with: builder.go (Finish calls supplement), match.go (accepts or
// rejects each search3 hit), internal/listenbrainz (candidates and titles).
//
// Only tracks the upstream can serve are added: every candidate is looked up
// with the upstream's search3 and kept only when the hit matches. SubSlip never
// calls stream or download here, so it never starts an acquisition in Octo.
package mix

import (
	"context"
	"errors"
	"net/url"
	"sync"

	"github.com/antidamage/subslip/internal/subsonic"
	"github.com/antidamage/subslip/internal/upstream"
)

// searchConcurrency is how many search3 calls run at once.
const searchConcurrency = 3

// errNoArtistMBID means the seed's artist has no MusicBrainz id, so
// ListenBrainz cannot be asked.
var errNoArtistMBID = errors.New("seed artist has no MusicBrainz id")

// supplement returns the matched songs and how many candidates were looked up.
func (b *Builder) supplement(ctx context.Context, auth subsonic.Auth, seedID string, have []subsonic.Song) ([]subsonic.Song, int, error) {
	artistMBID, err := b.seedArtistMBID(ctx, auth, seedID)
	if err != nil {
		return nil, 0, err
	}
	candidates, err := b.LB.RadioArtist(ctx, artistMBID)
	if err != nil {
		return nil, 0, err
	}
	if len(candidates) > b.MaxSearchLookups {
		candidates = candidates[:b.MaxSearchLookups]
	}
	candidates, err = b.LB.FillTitles(ctx, candidates)
	if err != nil {
		return nil, 0, err
	}

	exclude := map[string]bool{seedID: true}
	for _, s := range have {
		exclude[upstream.Str(s, "id")] = true
	}
	found := make([]subsonic.Song, len(candidates))
	sem := make(chan struct{}, searchConcurrency)
	var wg sync.WaitGroup
	for i, cand := range candidates {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, artist, title string) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() { recover() }() // a bad search3 answer is a miss, not a crash
			found[i] = b.resolve(ctx, auth, artist, title)
		}(i, cand.Artist, cand.Title)
	}
	wg.Wait()

	var out []subsonic.Song
	for _, song := range found {
		if song == nil || exclude[upstream.Str(song, "id")] {
			continue
		}
		exclude[upstream.Str(song, "id")] = true
		out = append(out, song)
	}
	return out, len(candidates), ctx.Err()
}

// seedArtistMBID reads the seed with getSong, then its artist with getArtist.
func (b *Builder) seedArtistMBID(ctx context.Context, auth subsonic.Auth, seedID string) (string, error) {
	songResp, err := b.Up.Call(ctx, "getSong", auth, url.Values{"id": {seedID}})
	if err != nil {
		return "", err
	}
	song, _ := songResp.Body["song"].(map[string]any)
	if mbid := firstArtistMBID(song); mbid != "" {
		return mbid, nil
	}
	artistID := upstream.Str(song, "artistId")
	if artistID == "" {
		return "", errNoArtistMBID
	}
	artistResp, err := b.Up.Call(ctx, "getArtist", auth, url.Values{"id": {artistID}})
	if err != nil {
		return "", err
	}
	artist, _ := artistResp.Body["artist"].(map[string]any)
	if mbid := upstream.Str(artist, "musicBrainzId"); mbid != "" {
		return mbid, nil
	}
	return "", errNoArtistMBID
}

// firstArtistMBID reads OpenSubsonic's artists[0].musicBrainzId when present.
func firstArtistMBID(song map[string]any) string {
	artists, _ := song["artists"].([]any)
	if len(artists) == 0 {
		return ""
	}
	first, _ := artists[0].(map[string]any)
	return upstream.Str(first, "musicBrainzId")
}

// resolve looks one candidate up with search3 and returns the song only when
// it matches. Errors are treated as "not found".
func (b *Builder) resolve(ctx context.Context, auth subsonic.Auth, artist, title string) subsonic.Song {
	resp, err := b.Up.Call(ctx, "search3", auth, url.Values{
		"query": {artist + " " + title}, "songCount": {"1"}, "artistCount": {"0"}, "albumCount": {"0"},
	})
	if err != nil {
		return nil
	}
	for _, song := range upstream.Songs(resp.Body, "searchResult3") {
		if Matches(song, artist, title) {
			return song
		}
	}
	return nil
}
