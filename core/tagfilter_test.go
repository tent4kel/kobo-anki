package core

import "testing"

func cardKeys(cards []Card) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.Key()
	}
	return out
}

func TestFilterByTagsNoSelectionReturnsAllCards(t *testing.T) {
	cards := []Card{
		{Front: "a", Tags: []string{"noun"}},
		{Front: "b"},
	}

	got := FilterByTags(cards, nil)

	if len(got) != len(cards) {
		t.Fatalf("FilterByTags(cards, nil) = %d cards, want %d (unfiltered)", len(got), len(cards))
	}
}

func TestFilterByTagsMatchesAnySelectedTag(t *testing.T) {
	cards := []Card{
		{Front: "a", Tags: []string{"noun"}},
		{Front: "b", Tags: []string{"verb"}},
		{Front: "c", Tags: []string{"noun", "irregular"}},
		{Front: "d"},
	}

	got := FilterByTags(cards, []string{"verb", "irregular"})

	want := []string{"b", "c"}
	if gk := cardKeys(got); !equalStrings(gk, want) {
		t.Fatalf("FilterByTags(cards, [verb irregular]) = %v, want %v", gk, want)
	}
}

func TestFilterByTagsNoMatchesReturnsEmpty(t *testing.T) {
	cards := []Card{{Front: "a", Tags: []string{"noun"}}}

	got := FilterByTags(cards, []string{"adjective"})

	if len(got) != 0 {
		t.Fatalf("FilterByTags with no matching tag = %d cards, want 0", len(got))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
