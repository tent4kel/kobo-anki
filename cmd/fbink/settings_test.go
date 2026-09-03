package main

import (
	"path/filepath"
	"testing"
)

func TestLoadJSONFileMissingReturnsZeroValueNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")

	var got deckSettingsData
	found := loadJSONFile(path, &got)

	if found {
		t.Fatalf("loadJSONFile(missing) found = true, want false")
	}
	if got.Reverse || len(got.Tags) != 0 {
		t.Fatalf("loadJSONFile(missing) = %+v, want zero value", got)
	}
}

func TestSaveThenLoadJSONFileRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decksettings", "french.json")
	want := deckSettingsData{Reverse: true, Tags: []string{"noun", "irregular"}}

	if err := saveJSONFile(path, want); err != nil {
		t.Fatalf("saveJSONFile: %v", err)
	}

	var got deckSettingsData
	found := loadJSONFile(path, &got)
	if !found {
		t.Fatalf("loadJSONFile after save: found = false, want true")
	}
	if got.Reverse != want.Reverse || !equalStringSlices(got.Tags, want.Tags) {
		t.Fatalf("loadJSONFile after save = %+v, want %+v", got, want)
	}
}

func TestDeckSettingsPathIsHiddenFromWordsDir(t *testing.T) {
	got := deckSettingsPath("french")
	want := filepath.Join("config", "decksettings", "french.json")
	if got != want {
		t.Fatalf("deckSettingsPath(%q) = %q, want %q", "french", got, want)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
