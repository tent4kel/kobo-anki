package main

import "testing"

// flatten re-joins a page split back into one slice, for asserting
// paginate lost or duplicated nothing.
func flatten(pages [][]layoutLine) []layoutLine {
	var out []layoutLine
	for _, p := range pages {
		out = append(out, p...)
	}
	return out
}

func TestPaginateCoversEveryLineExactlyOnce(t *testing.T) {
	lines := wrap(t, "one two three four five six seven eight nine ten eleven twelve", 3)
	const lineHeight, blockSpace, ascent = 10, 5, 8

	pages := paginate(lines, lineHeight, blockSpace, ascent, 25)
	if len(pages) < 2 {
		t.Fatalf("expected content this long to need more than one page, got %d", len(pages))
	}
	got := flatten(pages)
	if len(got) != len(lines) {
		t.Fatalf("flatten(paginate(lines)) has %d lines, want %d - pagination dropped or duplicated lines", len(got), len(lines))
	}
	for i := range lines {
		if lineText(got[i]) != lineText(lines[i]) {
			t.Fatalf("line %d = %q, want %q - pagination reordered content", i, lineText(got[i]), lineText(lines[i]))
		}
	}
}

// Every page's own rendered height (blockHeight, the same measurement
// drawLines' vertical centring already trusts) must fit within availH -
// paginate exists specifically so no page needs a caller-side clip.
func TestPaginateEachPageFitsBudget(t *testing.T) {
	lines := wrap(t, "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi", 2)
	const lineHeight, blockSpace, ascent, availH = 12, 4, 10, 40

	for i, p := range paginate(lines, lineHeight, blockSpace, ascent, availH) {
		if h := blockHeight(p, lineHeight, blockSpace); h > availH {
			t.Errorf("page %d height %d exceeds budget %d", i, h, availH)
		}
		if len(p) == 0 {
			t.Errorf("page %d is empty", i)
		}
	}
}

// A single line taller than availH must still get its own page rather than
// paginate looping forever trying to produce a page that fits - fitCount's
// "always at least 1" floor exists for exactly this.
func TestPaginateContentTallerThanBudgetStillTerminates(t *testing.T) {
	lines := wrap(t, "one", 3)
	const lineHeight, blockSpace, ascent = 500, 0, 400

	pages := paginate(lines, lineHeight, blockSpace, ascent, 10)
	if len(pages) != 1 || len(pages[0]) != 1 {
		t.Fatalf("paginate(one oversized line) = %v pages, want exactly 1 page holding it", pages)
	}
}

func TestPaginateContentThatFitsIsOnePage(t *testing.T) {
	lines := wrap(t, "short line", 20)
	pages := paginate(lines, 10, 5, 8, 1000)
	if len(pages) != 1 {
		t.Fatalf("paginate(content shorter than budget) = %d pages, want 1", len(pages))
	}
}

func TestPaginateEmptyContentReturnsOnePage(t *testing.T) {
	pages := paginate(nil, 10, 5, 8, 100)
	if len(pages) != 1 {
		t.Fatalf("paginate(nil) = %d pages, want 1 (so callers never see zero pages)", len(pages))
	}
}
