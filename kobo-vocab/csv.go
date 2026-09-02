package main

import (
	"io"
	"os"
	"strings"

	"kobo-anki/core"
)

// --- CSV export ---

// exportCSV rewrites a deck in full (existing rows plus newly translated
// ones). Written atomically for the same reason SaveCards is: this replaces
// a deck that may already carry review history, and a half-written file
// would lose it.
func exportCSV(path string, existingRows [][]string, newTranslations []Translation) error {
	return core.WriteFileAtomic(path, 0644, func(f io.Writer) error {
		w, err := core.NewSpreadsheetCSVWriter(f)
		if err != nil {
			return err
		}
		w.Write(core.DeckHeader)

		// Preserve existing rows (already widened by readExistingCSV)
		for _, row := range existingRows {
			w.Write(row)
		}

		for _, t := range newTranslations {
			w.Write(core.NewCardRow(t.SourceWord, t.Word, t.Translation))
		}

		w.Flush()
		return w.Error()
	})
}

// readExistingCSV returns a deck's rows, and an index of them keyed by the
// word each card came from.
//
// The key is the row's id column when it has one - the source word, written
// by exportCSV - and falls back to its front text otherwise, which is all a
// deck written before that column (or a hand-written one) can offer. The
// fallback is why vocab-seen-*.txt is still consulted in computeGroups: for
// those older rows the front may since have been re-rendered into something
// that no longer resembles the word at all.
func readExistingCSV(path string) (map[string][]string, [][]string) {
	byWord := make(map[string][]string)
	var rows [][]string

	f, err := os.Open(path)
	if err != nil {
		return byWord, rows
	}
	defer f.Close()

	allRows, err := core.NewSpreadsheetCSVReader(f).ReadAll()
	if err != nil {
		return byWord, rows
	}
	for i, row := range allRows {
		if i == 0 {
			continue // header
		}
		normalized, ok := core.NormalizeRow(row)
		if !ok {
			continue
		}
		key := normalized[core.ColID]
		if key == "" {
			key = normalized[core.ColFront]
		}
		byWord[strings.ToLower(key)] = normalized
		rows = append(rows, normalized)
	}
	return byWord, rows
}
