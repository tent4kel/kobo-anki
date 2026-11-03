package main

import (
	"slices"
	"strings"

	"golang.org/x/image/math/fixed"
	"kobo-anki/core"
)

// uniqueSortedTags is the union of every Tags entry across cards, sorted so
// the deck-settings tag list pages in a stable order across redraws.
func uniqueSortedTags(cards []core.Card) []string {
	seen := make(map[string]bool)
	var out []string
	for _, c := range cards {
		for _, t := range c.Tags {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	slices.Sort(out)
	return out
}

// truncateTagAnnotation appends a deck's selected tags to its name for the
// decks-screen row - "Deckname (tag1, tag2)", joined by sep - dropping tags from the end
// (replaced by a trailing "…") until the result fits maxWidth, measured by
// measure (the real xfont.MeasureString path in production, a fixed-width
// stub in tests). If even the shortest annotated form doesn't fit, the bare
// deck name is returned rather than truncating mid-word.
func truncateTagAnnotation(deckName string, tags []string, sep string, maxWidth fixed.Int26_6, measure func(string) fixed.Int26_6) string {
	if len(tags) == 0 {
		return deckName
	}

	for n := len(tags); n > 0; n-- {
		s := deckName + " (" + strings.Join(tags[:n], sep)
		if n < len(tags) {
			s += sep + "…"
		}
		s += ")"
		if measure(s) <= maxWidth {
			return s
		}
	}
	return deckName
}
