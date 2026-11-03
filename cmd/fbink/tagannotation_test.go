package main

import (
	"testing"

	"golang.org/x/image/math/fixed"
	"kobo-anki/core"
)

// charMeasure is a deterministic width function for truncateTagAnnotation
// tests - one unit per rune, so widths are exact integers instead of
// depending on any real font being installed on the build machine (same
// reasoning as layout_test.go's fixed-width testResolve).
func charMeasure(s string) fixed.Int26_6 {
	return fixed.I(len([]rune(s)))
}

func TestTruncateTagAnnotationNoTagsReturnsBareName(t *testing.T) {
	got := truncateTagAnnotation("french", nil, ", ", fixed.I(1000), charMeasure)
	if got != "french" {
		t.Fatalf("truncateTagAnnotation(no tags) = %q, want %q", got, "french")
	}
}

func TestTruncateTagAnnotationAllTagsFit(t *testing.T) {
	got := truncateTagAnnotation("french", []string{"noun", "verb"}, ", ", fixed.I(1000), charMeasure)
	want := "french (noun, verb)"
	if got != want {
		t.Fatalf("truncateTagAnnotation(fits) = %q, want %q", got, want)
	}
}

func TestTruncateTagAnnotationDropsTagsThatDontFit(t *testing.T) {
	full := "french (noun, verb, irregular)"
	got := truncateTagAnnotation("french", []string{"noun", "verb", "irregular"}, ", ", fixed.I(len(full)-1), charMeasure)

	want := "french (noun, verb, …)"
	if got != want {
		t.Fatalf("truncateTagAnnotation(overflow) = %q, want %q", got, want)
	}
}

func TestTruncateTagAnnotationUsesSeparator(t *testing.T) {
	full := "french (noun + verb + irregular)"
	got := truncateTagAnnotation("french", []string{"noun", "verb", "irregular"}, " + ", fixed.I(len(full)-1), charMeasure)

	want := "french (noun + verb + …)"
	if got != want {
		t.Fatalf("truncateTagAnnotation(sep +) = %q, want %q", got, want)
	}
}

func TestTruncateTagAnnotationFallsBackToBareNameWhenNoTagFits(t *testing.T) {
	got := truncateTagAnnotation("french", []string{"noun"}, ", ", fixed.I(len("french")), charMeasure)
	if got != "french" {
		t.Fatalf("truncateTagAnnotation(nothing fits) = %q, want %q", got, "french")
	}
}

func TestUniqueSortedTagsDedupesAndSorts(t *testing.T) {
	cards := []core.Card{
		{Front: "a", Tags: []string{"noun", "irregular"}},
		{Front: "b", Tags: []string{"verb"}},
		{Front: "c", Tags: []string{"noun"}},
		{Front: "d"},
	}

	got := uniqueSortedTags(cards)

	want := []string{"irregular", "noun", "verb"}
	if !equalStringSlices(got, want) {
		t.Fatalf("uniqueSortedTags = %v, want %v", got, want)
	}
}

func TestUniqueSortedTagsNoTagsReturnsEmpty(t *testing.T) {
	cards := []core.Card{{Front: "a"}, {Front: "b"}}

	got := uniqueSortedTags(cards)

	if len(got) != 0 {
		t.Fatalf("uniqueSortedTags(no tags) = %v, want empty", got)
	}
}
