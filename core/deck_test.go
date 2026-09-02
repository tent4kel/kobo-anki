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

func TestNormalizeRowKeepsExistingSchedulingWhenWidening(t *testing.T) {
	// A deck written before the id column existed is 11 wide. Widening it
	// must keep its review history, not rebuild the row from front/back -
	// an earlier version did the latter and would have silently reset every
	// card to New.
	old := []string{"huis", "Haus", "2026-01-01T00:00:00Z", "12.5", "3.1", "4", "5", "6", "7", "2", "2025-12-01T00:00:00Z"}
	got, ok := NormalizeRow(old)
	if !ok {
		t.Fatal("ok = false")
	}
	if len(got) != DeckColumns {
		t.Fatalf("width = %d, want %d", len(got), DeckColumns)
	}
	if got[ColStability] != "12.5" || got[ColReps] != "6" || got[ColState] != "2" {
		t.Errorf("scheduling fields lost: %q", got)
	}
	if got[ColID] != "" {
		t.Errorf("ColID = %q, want empty for a pre-id deck", got[ColID])
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

func TestTagsRoundTripThroughSaveAndLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.csv")
	in := []Card{
		{Front: "a", Back: "b", Tags: []string{"chapter1", "verb"}},
		{Front: "c", Back: "d"}, // no tags at all - must round-trip as empty, not [""]
	}
	if err := SaveCards(p, in); err != nil {
		t.Fatalf("SaveCards: %v", err)
	}
	out, err := LoadCards(p)
	if err != nil {
		t.Fatalf("LoadCards: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d cards, want 2", len(out))
	}
	if got := out[0].Tags; len(got) != 2 || got[0] != "chapter1" || got[1] != "verb" {
		t.Errorf("Tags = %v, want [chapter1 verb]", got)
	}
	if got := out[1].Tags; len(got) != 0 {
		t.Errorf("Tags = %v, want empty for a card with none", got)
	}
}

func TestNormalizeRowWidensPreTagsRowWithEmptyTags(t *testing.T) {
	// A row written before the tags column existed (13 fields) must still
	// widen cleanly to 14, with tags coming out empty rather than the row
	// being rejected or misaligned.
	row := []string{"front", "back", "", "0", "0", "0", "0", "0", "0", "0", "", "id1", "html"}
	normalized, ok := NormalizeRow(row)
	if !ok {
		t.Fatal("NormalizeRow rejected a valid pre-tags row")
	}
	if len(normalized) != DeckColumns {
		t.Fatalf("normalized width = %d, want %d", len(normalized), DeckColumns)
	}
	if normalized[ColTags] != "" {
		t.Errorf("ColTags = %q, want empty for a widened pre-tags row", normalized[ColTags])
	}
}

func TestNewCardRowMatchesHeaderWidth(t *testing.T) {
	if len(DeckHeader) != DeckColumns {
		t.Fatalf("DeckHeader has %d columns, DeckColumns says %d", len(DeckHeader), DeckColumns)
	}
	if len(NewCardRow("id1", "a", "b")) != len(DeckHeader) {
		t.Error("NewCardRow width does not match the header")
	}
}

func TestCardKeyPrefersIDOverFront(t *testing.T) {
	withID := Card{Front: "**ruim**\nwijd\n*een ruime kamer.*", ID: "ruim"}
	if got := withID.Key(); got != "ruim" {
		t.Errorf("Key() = %q, want the stable id", got)
	}
	// A hand-written or pre-id card has no id and keeps its old identity.
	noID := Card{Front: "bonjour"}
	if got := noID.Key(); got != "bonjour" {
		t.Errorf("Key() = %q, want the front text as fallback", got)
	}
}

func TestCardStaysFindableAfterItsFrontIsRerendered(t *testing.T) {
	// The bug this column exists to fix: a card re-rendered by a different
	// provider, or after a card_format change, used to become unfindable -
	// its review history was orphaned and the word re-added as a duplicate.
	p := filepath.Join(t.TempDir(), "d.csv")
	original := []Card{{ID: "ruim", Front: "**ruim**\nwijd", Back: "**weit**", Reps: 7}}
	if err := SaveCards(p, original); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadCards(p)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a re-render: same card, completely different front text.
	loaded[0].Front = "**ruim**\nwijd, breed, ruimtelijk\n*een ruime kamer.*"
	if err := SaveCards(p, loaded); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadCards(p)
	if err != nil {
		t.Fatal(err)
	}
	found := FindCard(reloaded, "ruim")
	if found == nil {
		t.Fatal("card not findable by id after its front changed")
	}
	if found.Reps != 7 {
		t.Errorf("Reps = %d, want 7 - review history was lost", found.Reps)
	}
}

func TestHandWrittenTwoColumnDeckStillWorks(t *testing.T) {
	// The constraint that keeps a leading id column off the table: a plain
	// front,back file must load as cards, be findable, and survive a save.
	p := filepath.Join(t.TempDir(), "hand.csv")
	os.WriteFile(p, []byte("front,back\nbonjour,hello\nmerci,thank you\n"), 0644)

	cards, err := LoadCards(p)
	if err != nil {
		t.Fatalf("LoadCards: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(cards))
	}
	if cards[0].Front != "bonjour" || cards[0].Back != "hello" {
		t.Errorf("first card = %q/%q", cards[0].Front, cards[0].Back)
	}
	if cards[0].ID != "" {
		t.Errorf("ID = %q, want empty for a hand-written deck", cards[0].ID)
	}
	if FindCard(cards, "bonjour") == nil {
		t.Error("hand-written card not findable by its front text")
	}

	// Round-trip: saving gains the full schema without inventing an id.
	if err := SaveCards(p, cards); err != nil {
		t.Fatal(err)
	}
	again, _ := LoadCards(p)
	if len(again) != 2 || again[0].Front != "bonjour" || again[0].ID != "" {
		t.Errorf("round-trip changed a hand-written deck: %+v", again)
	}
}
