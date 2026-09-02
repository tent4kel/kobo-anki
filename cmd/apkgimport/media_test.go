package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"kobo-anki/core"
)

func TestWriteMediaZipStoresEntriesUncompressed(t *testing.T) {
	dir := t.TempDir()
	apkgPath := filepath.Join(dir, "source.apkg")
	if err := os.WriteFile(apkgPath, []byte("fake apkg bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	zipPath := core.DeckMediaZip(dir, "Deck")
	media := map[string][]byte{"1": []byte("fake-image-bytes")}
	styles := map[string]string{"basic": "body{color:blue}"}

	if err := writeMediaZip(zipPath, media, styles, nil, apkgPath); err != nil {
		t.Fatalf("writeMediaZip: %v", err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open written zip: %v", err)
	}
	defer zr.Close()

	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		entries[f.Name] = f
		if f.Method != zip.Store {
			t.Errorf("%s: Method = %d, want zip.Store (%d)", f.Name, f.Method, zip.Store)
		}
	}

	want := []string{"1", "style-basic.css", "source.apkg"}
	for _, name := range want {
		f, ok := entries[name]
		if !ok {
			t.Errorf("missing entry %q", name)
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Errorf("open entry %q: %v", name, err)
			continue
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		if len(data) == 0 {
			t.Errorf("entry %q is empty", name)
		}
	}
}

func TestWriteMediaZipPreservesHandEditedStyleOnReimport(t *testing.T) {
	dir := t.TempDir()
	apkgPath := filepath.Join(dir, "source.apkg")
	os.WriteFile(apkgPath, []byte("v1"), 0o644)
	zipPath := core.DeckMediaZip(dir, "Deck")

	// First import.
	if err := writeMediaZip(zipPath, nil, map[string]string{"basic": "original css"}, nil, apkgPath); err != nil {
		t.Fatalf("first writeMediaZip: %v", err)
	}

	// Simulate the user hand-editing the stylesheet, then re-importing:
	// readExistingStyles must pick up the hand-edited content, and passing
	// it back into writeMediaZip must make it win over the apkg's own
	// (different) CSS for the same model.
	preserved := readExistingStyles(zipPath)
	if preserved["basic"] != "original css" {
		t.Fatalf("readExistingStyles = %q, want %q", preserved["basic"], "original css")
	}
	preserved["basic"] = "hand-edited css"

	os.WriteFile(apkgPath, []byte("v2"), 0o644)
	if err := writeMediaZip(zipPath, nil, map[string]string{"basic": "fresh apkg css"}, preserved, apkgPath); err != nil {
		t.Fatalf("second writeMediaZip: %v", err)
	}

	got, ok := core.DeckMediaResolver(dir, "Deck")("style-basic.css")
	if !ok {
		t.Fatal("style-basic.css missing after re-import")
	}
	if string(got) != "hand-edited css" {
		t.Errorf("style-basic.css = %q, want the preserved hand-edited content", got)
	}
}

func TestReadExistingStylesEmptyForNoPriorZip(t *testing.T) {
	dir := t.TempDir()
	got := readExistingStyles(core.DeckMediaZip(dir, "NeverImported"))
	if len(got) != 0 {
		t.Errorf("readExistingStyles = %v, want empty for a deck with no prior media zip", got)
	}
}
