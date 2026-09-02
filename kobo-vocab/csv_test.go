package main

import (
	"path/filepath"
	"testing"

	"kobo-anki/core"
)

func TestExportCSVWritesSourceWordAsID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.csv")
	if err := exportCSV(p, nil, []Translation{{
		SourceWord: "liepen", Word: "**lopen**", Translation: "**laufen**",
	}}); err != nil {
		t.Fatalf("exportCSV: %v", err)
	}
	cards, err := core.LoadCards(p)
	if err != nil {
		t.Fatalf("LoadCards: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	if cards[0].ID != "liepen" {
		t.Errorf("ID = %q, want the source word", cards[0].ID)
	}
	if cards[0].Key() != "liepen" {
		t.Errorf("Key() = %q, want the source word", cards[0].Key())
	}
}
