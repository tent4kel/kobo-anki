package main

import (
	"kobo-anki/core"
	"testing"
)

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
