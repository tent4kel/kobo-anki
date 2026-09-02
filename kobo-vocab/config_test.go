package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeGroupsCountsAcrossLanguagePairs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	createTestDB(dbPath) // 15 words, all -nl-en, per its hardcoded list

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	cfg := loadConfig("") // no config file - defaults, OutDir="."
	cfg.OutDir = t.TempDir()

	groups, totalNew, err := computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups: %v", err)
	}
	if totalNew != 15 {
		t.Errorf("totalNew = %d, want 15", totalNew)
	}
	g, ok := groups["-nl-en"]
	if !ok {
		t.Fatalf("expected a -nl-en group")
	}
	if len(g.newWords) != 15 {
		t.Errorf("len(newWords) = %d, want 15", len(g.newWords))
	}
	if g.fromLang != "nl" || g.toLang != "en" {
		t.Errorf("fromLang/toLang = %q/%q, want nl/en", g.fromLang, g.toLang)
	}
}

func TestComputeGroupsExcludesExistingAndMissedWords(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	createTestDB(dbPath)

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	cfg := loadConfig("")
	cfg.OutDir = t.TempDir()

	// First pass: everything is new.
	_, totalNew, err := computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups: %v", err)
	}
	if totalNew != 15 {
		t.Fatalf("first pass totalNew = %d, want 15", totalNew)
	}

	// Simulate one word already exported and one already a recorded miss.
	csvPath := filepath.Join(cfg.OutDir, "vocab-nl-en.csv")
	exportCSV(csvPath, nil, []Translation{{Word: "behoefte", Translation: "need", FromLang: "nl", ToLang: "en", Source: "dict"}})
	missesPath := filepath.Join(cfg.OutDir, "vocab-misses-nl-en.txt")
	appendMisses(missesPath, []string{"leek"})

	_, totalNew, err = computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups (second pass): %v", err)
	}
	if totalNew != 13 {
		t.Errorf("second pass totalNew = %d, want 13 (15 - 1 exported - 1 miss)", totalNew)
	}
}

func TestComputeGroupsRecognizesWordsWithStyledFrontAsExisting(t *testing.T) {
	// Regression test: dict_examples/lexicala/chatgpt override Entry.Front
	// with styled card content (e.g. "**behoefte**\n*een voorbeeldzin*")
	// instead of leaving it as the plain word. readExistingCSV's dedup key
	// is that CSV front column, so once Front diverges from the raw Kobo
	// word, the CSV-based check alone can never match it again - the word
	// looks "new" every single run, duplicating it forever. vocab-seen-*.txt
	// (independent of card content, see computeGroups/appendMisses call
	// sites in main()) is the fix - verify it alone is enough to exclude
	// the word even though its CSV front doesn't match the raw word at all.
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	createTestDB(dbPath)

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	cfg := loadConfig("")
	cfg.OutDir = t.TempDir()

	_, totalNew, err := computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups: %v", err)
	}
	if totalNew != 15 {
		t.Fatalf("first pass totalNew = %d, want 15", totalNew)
	}

	// Simulate exporting "behoefte" with a styled front that no longer
	// matches the raw word - what dict_examples/lexicala/chatgpt actually
	// produce - and record it as seen, exactly as main()'s export step now
	// does after this fix.
	csvPath := filepath.Join(cfg.OutDir, "vocab-nl-en.csv")
	exportCSV(csvPath, nil, []Translation{{
		Word:        "**behoefte**\n*een voorbeeldzin*",
		Translation: "**need**\n*an example sentence*",
		FromLang:    "nl", ToLang: "en", Source: "dict",
	}})
	seenPath := filepath.Join(cfg.OutDir, "vocab-seen-nl-en.txt")
	appendMisses(seenPath, []string{"behoefte"})

	groups, totalNew, err := computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups (second pass): %v", err)
	}
	if totalNew != 14 {
		t.Errorf("second pass totalNew = %d, want 14 (15 - 1 seen, despite the CSV front not matching the raw word)", totalNew)
	}
	for _, w := range groups["-nl-en"].newWords {
		if strings.ToLower(w.Text) == "behoefte" {
			t.Errorf("behoefte was re-offered as new - vocab-seen-*.txt was not consulted")
		}
	}
}

func TestComputeGroupsUsesIDColumnInsteadOfSeenFile(t *testing.T) {
	// The duplicate-word bug, now solved by the id column rather than the
	// vocab-seen sidecar: a card whose rendered front looks nothing like the
	// source word is still recognized as already-done, with no
	// vocab-seen-*.txt present at all.
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	createTestDB(dbPath)

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	cfg := loadConfig("")
	cfg.OutDir = t.TempDir()

	csvPath := filepath.Join(cfg.OutDir, "vocab-nl-en.csv")
	exportCSV(csvPath, nil, []Translation{{
		SourceWord:  "behoefte",
		Word:        "**behoefte**\nnood\n*een dringende behoefte.*",
		Translation: "**need**\nrequirement",
	}})

	if _, err := os.Stat(filepath.Join(cfg.OutDir, "vocab-seen-nl-en.txt")); !os.IsNotExist(err) {
		t.Fatal("vocab-seen file should not be written any more")
	}

	groups, totalNew, err := computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups: %v", err)
	}
	if totalNew != 14 {
		t.Errorf("totalNew = %d, want 14 (15 - 1 already in the deck)", totalNew)
	}
	for _, w := range groups["-nl-en"].newWords {
		if strings.ToLower(w.Text) == "behoefte" {
			t.Error("behoefte re-offered as new despite its id being in the deck")
		}
	}
}

func TestLabelSetIgnoresUnknownClasses(t *testing.T) {
	got := labelSet([]string{"gen", "nonsense", "reg", ""})
	if !got["gen"] || !got["reg"] {
		t.Errorf("known classes missing: %v", got)
	}
	if got["nonsense"] {
		t.Errorf("unknown class accepted: %v", got)
	}
}
