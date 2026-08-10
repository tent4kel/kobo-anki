package main

import (
	"kobo-anki/core"
	"testing"
)

func TestHeadwordOnly(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want string
	}{
		{
			"headword, alternatives, and example",
			"**ruim**, wijd, breed\n*een ruime kamer.*",
			"**ruim**",
		},
		{
			"headword only",
			"**afkicken**",
			"**afkicken**",
		},
		{
			"multi-word bold headword",
			"**van a tot z**, alles\n*van a tot z.*",
			"**van a tot z**",
		},
		{
			"no bold run at all - falls back to the full string",
			"plain text, no headword marked up",
			"plain text, no headword marked up",
		},
		{
			"empty string",
			"",
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := headwordOnly(c.s); got != c.want {
				t.Errorf("headwordOnly(%q) = %q, want %q", c.s, got, c.want)
			}
		})
	}
}

func TestNewStudyDataFrontHeadwordHTMLIsHeadwordOnly(t *testing.T) {
	// FrontHTML (used on the front page) must stay the full front content;
	// only the new FrontHeadwordHTML (used on the back page's gray repeat
	// field) is trimmed to the headword.
	display := &core.Card{Front: "**ruim**, wijd, breed\n*een ruime kamer.*", Back: "**weit**"}
	data := newStudyData(display, "deck", "ruim", false)

	if data.FrontHTML != renderMD(display.Front) {
		t.Errorf("FrontHTML = %q, want the full rendered front unchanged", data.FrontHTML)
	}
	wantHeadword := renderMD("**ruim**")
	if data.FrontHeadwordHTML != wantHeadword {
		t.Errorf("FrontHeadwordHTML = %q, want %q", data.FrontHeadwordHTML, wantHeadword)
	}
}
