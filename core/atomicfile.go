package core

import (
	"io"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes to path via a temporary file in the same directory,
// then renames it into place, so readers only ever observe the old file or
// the complete new one - never a half-written mix.
//
// This matters more here than in a typical desktop program: a deck is
// rewritten in full on *every* card rating, and the device it runs on is a
// battery-powered e-reader that suspends aggressively and can be
// force-powered-off mid-write. Truncating the real file up front (the
// os.Create approach this replaces) meant any interruption in that window
// destroyed the deck and its entire FSRS review history.
//
// The temp file is created in the target's own directory because rename is
// only atomic within a single filesystem - os.TempDir may well be another
// one. On any failure the temp file is removed and the original is left
// exactly as it was.
func WriteFileAtomic(path string, perm os.FileMode, write func(io.Writer) error) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	defer func() {
		if err != nil {
			tmp.Close() // no-op if already closed below
			os.Remove(tmpName)
		}
	}()

	if err = write(tmp); err != nil {
		return err
	}
	// Sync before rename: rename only orders the directory entry, it does
	// not guarantee the file's contents have reached storage first.
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
