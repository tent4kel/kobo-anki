package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// --- Kobo database ---

func readWordList(db *sql.DB) ([]VocabWord, error) {
	rows, err := db.Query(`SELECT Text, VolumeId, DictSuffix, DateCreated FROM WordList ORDER BY DateCreated`)
	if err != nil {
		return nil, fmt.Errorf("query WordList: %w", err)
	}
	defer rows.Close()

	var words []VocabWord
	for rows.Next() {
		var w VocabWord
		if err := rows.Scan(&w.Text, &w.VolumeID, &w.DictSuffix, &w.DateCreated); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		words = append(words, w)
	}
	return words, rows.Err()
}

func parseLangPair(dictSuffix string) (from, to string) {
	s := strings.TrimPrefix(dictSuffix, "-")
	parts := strings.SplitN(s, "-", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

// --- Word-list tracking (misses and seen words) ---
//
// Both a "miss" (no provider found anything) and a "seen" word (a real
// translation was produced) are, on disk, just a plain lowercased
// word-per-line file - readMisses/appendMisses aren't miss-specific despite
// the name, and are reused as-is for vocab-seen-*.txt too.

func readMisses(path string) map[string]bool {
	result := make(map[string]bool)
	f, err := os.Open(path)
	if err != nil {
		return result
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		word := strings.TrimSpace(scanner.Text())
		if word != "" {
			result[word] = true
		}
	}
	return result
}

func appendMisses(path string, words []string) {
	os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to write misses: %v", err)
		return
	}
	defer f.Close()
	for _, w := range words {
		fmt.Fprintln(f, w)
	}
}
