package core

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// writeTestMediaZip builds a minimal deck media zip for a resolver test -
// intentionally not using cmd/apkgimport's own writer, so this test doesn't
// just check the writer against itself.
func writeTestMediaZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatalf("CreateHeader %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
}

func TestDeckMediaResolverReadsAnEntryByBaseName(t *testing.T) {
	dir := t.TempDir()
	writeTestMediaZip(t, DeckMediaZip(dir, "MyDeck"), map[string]string{
		"cat.jpg":         "fake-jpeg-bytes",
		"style-basic.css": "body{color:red}",
	})

	lookup := DeckMediaResolver(dir, "MyDeck")

	data, ok := lookup("cat.jpg")
	if !ok || string(data) != "fake-jpeg-bytes" {
		t.Errorf("lookup(cat.jpg) = %q, %v; want fake-jpeg-bytes, true", data, ok)
	}

	// A caller passing a src/url with a leading path (as an <img src> or
	// CSS url() might) must still resolve, matching the old loose-file
	// resolver's filepath.Base behavior.
	data, ok = lookup("media/subdir/cat.jpg")
	if !ok || string(data) != "fake-jpeg-bytes" {
		t.Errorf("lookup with path prefix = %q, %v; want fake-jpeg-bytes, true", data, ok)
	}

	if _, ok := lookup("style-basic.css"); !ok {
		t.Error("lookup(style-basic.css) = false, want true")
	}
}

func TestDeckMediaResolverMissingEntryOrZipIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	writeTestMediaZip(t, DeckMediaZip(dir, "HasMedia"), map[string]string{"a.png": "x"})

	// Entry not present in an existing zip.
	if _, ok := DeckMediaResolver(dir, "HasMedia")("nope.png"); ok {
		t.Error("lookup of a missing entry returned ok=true")
	}

	// No zip at all - the common case for a hand-written or kobo-vocab deck.
	if _, ok := DeckMediaResolver(dir, "NoMediaAtAll")("anything.png"); ok {
		t.Error("lookup against a deck with no media zip returned ok=true")
	}
}

func TestDeckMediaZipEntriesAreStoredNotDeflated(t *testing.T) {
	dir := t.TempDir()
	path := DeckMediaZip(dir, "Deck")
	writeTestMediaZip(t, path, map[string]string{"a.png": "some bytes here"})

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	if len(zr.File) != 1 {
		t.Fatalf("got %d entries, want 1", len(zr.File))
	}
	if zr.File[0].Method != zip.Store {
		t.Errorf("Method = %d, want zip.Store (%d)", zr.File[0].Method, zip.Store)
	}
}

func TestDeckMediaZipPath(t *testing.T) {
	got := DeckMediaZip("words", "My Deck")
	want := filepath.Join("words", "My Deck.media.zip")
	if got != want {
		t.Errorf("DeckMediaZip = %q, want %q", got, want)
	}
}
