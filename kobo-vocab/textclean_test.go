package main

import (
	"strings"
	"testing"
)

func TestCleanTextEntityDecoding(t *testing.T) {
	got := cleanText(`<div><b>B&amp;B</b></div>`)
	if got != "**B&B**" {
		t.Errorf("got %q, want %q (entity decoded, bold preserved)", got, "**B&B**")
	}
}

func TestCleanTextNBSP(t *testing.T) {
	got := cleanText("foo&nbsp;bar")
	if got != "foo bar" {
		t.Errorf("got %q, want NBSP collapsed to a plain space", got)
	}
}

func TestToMarkdownAdjacentSpansMerge(t *testing.T) {
	got := toMarkdown(`<i> (</i><i>die</i><i>;</i>`)
	if strings.Count(got, "*") != 2 {
		t.Errorf("got %q, want adjacent <i> pairs merged into a single '*...*' span (2 stars)", got)
	}
}

func TestToMarkdownNoAmbiguousStarRun(t *testing.T) {
	got := toMarkdown(`<b>Synchronie</b><i> (die</i>)`)
	if strings.Contains(got, "***") {
		t.Errorf("got %q, want no ambiguous run of 3+ literal asterisks", got)
	}
}

func TestTruncateMarkdownVisibleNeverUnbalanced(t *testing.T) {
	cases := []string{
		"**bold word here** and *italic text* and more plain words to push past the limit",
		"- first sense here\n- second sense that is quite a bit longer than the first one",
		"*a*",
		"**",
		"no markdown at all just plain text that is long enough to truncate",
	}
	for _, s := range cases {
		for maxChars := 1; maxChars <= 20; maxChars++ {
			got := truncateMarkdownVisible(s, maxChars)
			if strings.Count(got, "*")%2 != 0 {
				t.Errorf("truncateMarkdownVisible(%q, %d) = %q: odd number of '*' (unbalanced)", s, maxChars, got)
			}
			trimmed := strings.TrimRight(got, "\n")
			if idx := strings.LastIndexByte(trimmed, '\n'); idx >= 0 {
				if strings.TrimSpace(trimmed[idx+1:]) == "-" {
					t.Errorf("truncateMarkdownVisible(%q, %d) = %q: dangling empty list item", s, maxChars, got)
				}
			} else if strings.TrimSpace(trimmed) == "-" {
				t.Errorf("truncateMarkdownVisible(%q, %d) = %q: dangling empty list item", s, maxChars, got)
			}
		}
	}
}

func TestTruncateMarkdownVisibleZeroMeansUnlimited(t *testing.T) {
	s := "some text **with bold** that should not be cut"
	if got := truncateMarkdownVisible(s, 0); got != s {
		t.Errorf("got %q, want unchanged input when maxChars=0", got)
	}
}
