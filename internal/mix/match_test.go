package mix

import "testing"

func TestMatches(t *testing.T) {
	song := map[string]any{"title": "Lantern Song (2009 Remaster)", "artist": "The Example Band"}
	if !Matches(song, "The Example Band", "Lantern Song") {
		t.Fatal("remaster suffix should match")
	}
	if Matches(song, "The Example Band", "Other Song") {
		t.Fatal("different title matched")
	}
	if Matches(song, "Example Tribute Band", "Lantern Song") {
		t.Fatal("different artist matched")
	}
	if Matches(map[string]any{"title": "Gold", "artist": "X"}, "X", "Go") {
		t.Fatal("partial word matched")
	}
	if !Matches(map[string]any{"title": "Back In Town", "artist": "A/B"}, "a b", "back in town") {
		t.Fatal("punctuation should be ignored")
	}
}
