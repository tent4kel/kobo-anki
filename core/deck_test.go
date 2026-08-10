package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeRowWidensShortRows(t *testing.T) {
	got, ok := NormalizeRow([]string{"huis", "Haus"})
	if !ok {
		t.Fatal("ok = false, want a 2-column row accepted")
	}
	if len(got) != DeckColumns {
		t.Fatalf("width = %d, want %d", len(got), DeckColumns)
	}
	if got[ColFront] != "huis" || got[ColBack] != "Haus" {
		t.Errorf("front/back = %q/%q", got[ColFront], got[ColBack])
	}
	if got[ColState] != "0" {
		t.Errorf("state = %q, want 0 (New)", got[ColState])
	}
}

func TestNormalizeRowRejectsTooShort(t *testing.T) {
	if _, ok := NormalizeRow([]string{"lonely"}); ok {
		t.Error("a 1-column row should not be accepted as a card")
	}
}

func TestNormalizeRowPreservesUnknownTrailingColumns(t *testing.T) {
	// Columns are append-only; a row written by a future version with extra
	// columns must survive a read/write cycle rather than being truncated.
	wide := make([]string, DeckColumns+2)
	for i := range wide {
		wide[i] = "x"
	}
	wide[ColFront], wide[ColBack] = "huis", "Haus"

	got, ok := NormalizeRow(wide)
	if !ok {
		t.Fatal("ok = false")
	}
	if len(got) != DeckColumns+2 {
		t.Errorf("width = %d, want the original %d kept", len(got), DeckColumns+2)
	}
}

func TestLooksLikeDeckHeader(t *testing.T) {
	if !LooksLikeDeckHeader(DeckHeader) {
		t.Error("the canonical header should be recognised")
	}
	if !LooksLikeDeckHeader([]string{"Front", "BACK"}) {
		t.Error("header match should be case-insensitive")
	}
	if LooksLikeDeckHeader([]string{"id", "front", "back"}) {
		t.Error("a foreign schema's header should not be recognised")
	}
}

func TestLoadCardsWarnsOnForeignSchema(t *testing.T) {
	// The exact shape of the old bundled french.csv: a 7-column SM-2 deck
	// whose id column would be mis-read as the card front. Loading still
	// succeeds, but it must not do so silently.
	p := filepath.Join(t.TempDir(), "old.csv")
	os.WriteFile(p, []byte("id,front,back,ef,interval,reps,next_review\n1,bonjour,hello,2.5,0,0,2020-01-01\n"), 0644)

	out := captureLog(t, func() {
		if _, err := LoadCards(p); err != nil {
			t.Fatalf("LoadCards: %v", err)
		}
	})
	if !strings.Contains(out, "not the expected") {
		t.Errorf("no warning about the unexpected header:\n%s", out)
	}
}

func TestLoadCardsQuietOnProperHeader(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.csv")
	if err := SaveCards(p, []Card{{Front: "huis", Back: "Haus"}}); err != nil {
		t.Fatal(err)
	}
	out := captureLog(t, func() { LoadCards(p) })
	if out != "" {
		t.Errorf("a deck we wrote ourselves should load silently, got:\n%s", out)
	}
}

func TestNewCardRowMatchesHeaderWidth(t *testing.T) {
	if len(DeckHeader) != DeckColumns {
		t.Fatalf("DeckHeader has %d columns, DeckColumns says %d", len(DeckHeader), DeckColumns)
	}
	if len(NewCardRow("a", "b")) != len(DeckHeader) {
		t.Error("NewCardRow width does not match the header")
	}
}
