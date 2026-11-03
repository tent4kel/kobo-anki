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

	got := FilterByTags(cards, nil, false)

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

	got := FilterByTags(cards, []string{"verb", "irregular"}, false)

	want := []string{"b", "c"}
	if gk := cardKeys(got); !equalStrings(gk, want) {
		t.Fatalf("FilterByTags(cards, [verb irregular]) = %v, want %v", gk, want)
	}
}

func TestFilterByTagsNoMatchesReturnsEmpty(t *testing.T) {
	cards := []Card{{Front: "a", Tags: []string{"noun"}}}

	got := FilterByTags(cards, []string{"adjective"}, false)

	if len(got) != 0 {
		t.Fatalf("FilterByTags with no matching tag = %d cards, want 0", len(got))
	}
}

func TestFilterByTagsMatchAllRequiresEverySelectedTag(t *testing.T) {
	cards := []Card{
		{Front: "a", Tags: []string{"noun"}},
		{Front: "b", Tags: []string{"noun", "irregular"}},
		{Front: "c", Tags: []string{"irregular", "verb", "noun"}},
		{Front: "d"},
		{Front: "e", Tags: []string{"noun", "noun"}},
	}

	got := FilterByTags(cards, []string{"noun", "irregular"}, true)

	want := []string{"b", "c"}
	if gk := cardKeys(got); !equalStrings(gk, want) {
		t.Fatalf("FilterByTags(cards, [noun irregular], all) = %v, want %v", gk, want)
	}
}

func TestFilterByTagsMatchAllNoSelectionReturnsAllCards(t *testing.T) {
	cards := []Card{{Front: "a", Tags: []string{"noun"}}, {Front: "b"}}

	if got := FilterByTags(cards, nil, true); len(got) != len(cards) {
		t.Fatalf("FilterByTags(cards, nil, all) = %d cards, want %d (unfiltered)", len(got), len(cards))
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
