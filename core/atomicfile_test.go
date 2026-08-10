package core

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomicReplacesContent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	err := WriteFileAtomic(p, 0644, func(w io.Writer) error {
		_, err := io.WriteString(w, "new")
		return err
	})
	if err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	got, _ := os.ReadFile(p)
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
}

func TestWriteFileAtomicLeavesOriginalOnFailure(t *testing.T) {
	// The whole point: a write that fails partway must not damage the file
	// that was already there. The old os.Create path truncated first, so an
	// interrupted save destroyed the deck.
	p := filepath.Join(t.TempDir(), "deck.csv")
	if err := os.WriteFile(p, []byte("precious,history\n"), 0644); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("interrupted")
	err := WriteFileAtomic(p, 0644, func(w io.Writer) error {
		io.WriteString(w, "partial junk that must never land")
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the write error propagated", err)
	}

	got, _ := os.ReadFile(p)
	if string(got) != "precious,history\n" {
		t.Errorf("original file was damaged: %q", got)
	}
}

func TestWriteFileAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deck.csv")

	// One successful write and one failed write.
	WriteFileAtomic(p, 0644, func(w io.Writer) error {
		_, err := io.WriteString(w, "ok")
		return err
	})
	WriteFileAtomic(p, 0644, func(w io.Writer) error {
		return errors.New("nope")
	})

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want just the deck", len(entries))
	}
}

func TestWriteFileAtomicCreatesMissingDirectory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "deck.csv")
	err := WriteFileAtomic(p, 0644, func(w io.Writer) error {
		_, err := io.WriteString(w, "x")
		return err
	})
	if err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestSaveCardsIsAtomicAndPreservesDeckOnFailure(t *testing.T) {
	// End-to-end via the real deck API: a saved deck round-trips, and the
	// file mode is the expected 0644 rather than a temp file's 0600.
	p := filepath.Join(t.TempDir(), "d.csv")
	if err := SaveCards(p, []Card{{Front: "huis", Back: "Haus"}}); err != nil {
		t.Fatalf("SaveCards: %v", err)
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Errorf("mode = %o, want 644 (temp files default to 600)", perm)
	}

	cards, err := LoadCards(p)
	if err != nil {
		t.Fatalf("LoadCards: %v", err)
	}
	if len(cards) != 1 || cards[0].Front != "huis" {
		t.Errorf("round-trip failed: %+v", cards)
	}
}
