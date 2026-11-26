// match.go decides whether a search3 result is the recording that was asked
// for. search3 is a fuzzy full-text search, so its first hit for
// "Artist Title" is often a different song by the same artist, or the same
// title by someone else. Both are rejected.
//
// Works with: supplement.go (resolve).
package mix

import (
	"strings"
	"unicode"

	"github.com/antidamage/subslip/internal/subsonic"
	"github.com/antidamage/subslip/internal/upstream"
)

// Matches reports whether song is artist's title. Comparison ignores case,
// punctuation and spacing, and accepts containment either way, so
// "Song (Remastered)" matches "Song" and "Artist feat. X" matches "Artist".
func Matches(song subsonic.Song, artist, title string) bool {
	return similar(upstream.Str(song, "title"), title) &&
		(similar(upstream.Str(song, "artist"), artist) || similar(upstream.Str(song, "displayArtist"), artist))
}

func similar(a, b string) bool {
	na, nb := normalise(a), normalise(b)
	if na == "" || nb == "" {
		return false
	}
	// Pad with spaces so containment is by whole words: "go" is not in "gold".
	pa, pb := " "+na+" ", " "+nb+" "
	return strings.Contains(pa, pb) || strings.Contains(pb, pa)
}

// normalise lower-cases, maps every run of non-letters/digits to one space and
// trims, so "AC/DC" and "ac dc" compare equal.
func normalise(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
