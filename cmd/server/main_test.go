package main

import (
	"html/template"
	"kobo-anki/core"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewStudyDataFrontHeadwordHTMLIsHeadwordOnly(t *testing.T) {
	// FrontHTML (used on the front page) must stay the full front content;
	// only the new FrontHeadwordHTML (used on the back page's gray repeat
	// field) is trimmed to the headword.
	display := &core.Card{Front: "**ruim**, wijd, breed\n*een ruime kamer.*", Back: "**weit**"}
	data := newStudyData(display, "deck", "ruim", false)

	wantFront := wrapCardFace(string(renderMD(display.Front)))
	if data.FrontHTML != wantFront {
		t.Errorf("FrontHTML = %q, want the full rendered front unchanged (wrapped in .card-face): %q", data.FrontHTML, wantFront)
	}
	wantHeadword := renderMD("**ruim**")
	if data.FrontHeadwordHTML != wantHeadword {
		t.Errorf("FrontHeadwordHTML = %q, want %q", data.FrontHeadwordHTML, wantHeadword)
	}
}

// TestIndexHandlerReusesCachedDeckForActiveDeck guards the indexHandler fix
// that reuses the already-decoded cards/csvFile cache for whichever deck it
// matches, instead of calling core.LoadCards for every deck unconditionally
// (see ensureDeckLoaded's doc comment for why that cache exists).
func TestIndexHandlerReusesCachedDeckForActiveDeck(t *testing.T) {
	dir := t.TempDir()
	origDataDir, origTmpl, origCards, origCSVFile := dataDir, tmpl, cards, csvFile
	defer func() { dataDir, tmpl, cards, csvFile = origDataDir, origTmpl, origCards, origCSVFile }()

	dataDir = dir
	tmpl = template.Must(template.New("index").Parse(`{{range .}}{{.Name}}:{{.Due}} {{end}}`))

	activePath := core.DeckCSVPath(dir, "active")
	if err := core.SaveCards(activePath, []core.Card{{Front: "a", Back: "b"}}); err != nil {
		t.Fatal(err)
	}
	otherPath := core.DeckCSVPath(dir, "other")
	if err := core.SaveCards(otherPath, []core.Card{{Front: "c", Back: "d"}}); err != nil {
		t.Fatal(err)
	}

	// "active" is already loaded and cached, as if a study session had it
	// open. Then the on-disk file is mutated to add a second card - if
	// indexHandler re-parsed it instead of using the cache, the due count
	// below would come out as 2, not the cached value of 1.
	loaded, err := core.LoadCards(activePath)
	if err != nil {
		t.Fatal(err)
	}
	csvFile, cards = activePath, loaded
	if err := core.SaveCards(activePath, []core.Card{{Front: "a", Back: "b"}, {Front: "x", Back: "y"}}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	indexHandler(w, httptest.NewRequest("GET", "/", nil))

	body := w.Body.String()
	if !strings.Contains(body, "active:1") {
		t.Errorf("body = %q, want it to report active:1 (from the cache, not the mutated on-disk file)", body)
	}
	if !strings.Contains(body, "other:1") {
		t.Errorf("body = %q, want it to report other:1 (freshly loaded, since it isn't the cached deck)", body)
	}
}
