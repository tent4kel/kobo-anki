package core

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSpreadsheetCSVRoundTripWithNewlinesAndUmlauts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.csv")
	in := []Card{{Front: "**Größe**\n*schöner, weiß*", Back: "**size**\nbigger"}}
	if err := SaveCards(p, in); err != nil {
		t.Fatalf("SaveCards: %v", err)
	}

	raw, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(raw), utf8BOM) {
		t.Error("file does not start with a UTF-8 BOM - Excel will mis-decode umlauts")
	}
	if !strings.Contains(string(raw), "\r\n") {
		t.Error("file does not use CRLF row terminators")
	}

	out, err := LoadCards(p)
	if err != nil {
		t.Fatalf("LoadCards: %v", err)
	}
	if len(out) != 1 || out[0].Front != in[0].Front || out[0].Back != in[0].Back {
		t.Errorf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

func TestLoadCardsReadsPreBOMFiles(t *testing.T) {
	// Decks written before BOMs were added (i.e. everything currently on the
	// user's device) must still load unchanged.
	p := filepath.Join(t.TempDir(), "old.csv")
	os.WriteFile(p, []byte("front,back,due,stability,difficulty,elapsed_days,scheduled_days,reps,lapses,state,last_review\nhuis,house,,0,0,0,0,0,0,0,\n"), 0644)

	out, err := LoadCards(p)
	if err != nil {
		t.Fatalf("LoadCards: %v", err)
	}
	if len(out) != 1 || out[0].Front != "huis" || out[0].Back != "house" {
		t.Errorf("got %+v, want the legacy BOM-less row parsed normally", out)
	}
}

func TestLoadCardsBOMDoesNotLeakIntoFirstColumn(t *testing.T) {
	// A deck re-saved by Excel gains a BOM; it must not end up glued to the
	// first header cell (which would leave the header row looking like data).
	p := filepath.Join(t.TempDir(), "excel.csv")
	os.WriteFile(p, []byte(utf8BOM+"front,back\r\nhuis,house\r\n"), 0644)

	out, err := LoadCards(p)
	if err != nil {
		t.Fatalf("LoadCards: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d cards, want 1 (header row must still be recognized)", len(out))
	}
	if out[0].Front != "huis" {
		t.Errorf("Front = %q, want %q", out[0].Front, "huis")
	}
}

func TestSampleDeckParsesAsRealCards(t *testing.T) {
	// The shipped sample deck must load as actual vocabulary, not as its own
	// id column (the pre-2026-08-10 file was in a 7-column SM-2 schema, which
	// LoadCards mis-sliced into Front="1", Back="bonjour").
	out, err := LoadCards("../french.csv")
	if err != nil {
		t.Fatalf("LoadCards: %v", err)
	}
	if len(out) != 10 {
		t.Fatalf("got %d cards, want 10", len(out))
	}
	if out[0].Front != "bonjour" || out[0].Back != "hello" {
		t.Errorf("first card = %q/%q, want bonjour/hello", out[0].Front, out[0].Back)
	}
	for _, c := range out {
		if _, err := strconv.Atoi(c.Front); err == nil {
			t.Errorf("card front %q is a bare number - id column leaked into front", c.Front)
		}
	}
}
