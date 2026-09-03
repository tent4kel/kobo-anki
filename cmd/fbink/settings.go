package main

import (
	"encoding/json"
	"io"
	"kobo-anki/core"
	"os"
	"path/filepath"
)

// globalStateData is the app-owned, UI-toggled state that isn't part of
// anki-fbink.conf (hand-edited config stays in anki-fbink.conf; state the
// app itself writes back lives here instead).
type globalStateData struct {
	DarkMode bool `json:"dark_mode"`
}

// deckSettingsData is one deck's reverse/tag-filter choice, made from the
// deck-settings screen and persisted so it survives a relaunch.
type deckSettingsData struct {
	Reverse bool     `json:"reverse"`
	Tags    []string `json:"tags"`
}

// statePath and deckSettingsPath both live under config/, alongside
// anki-fbink.conf - not under dataDir (words/), which is the deck-CSV
// directory the Kobo's own content scanner watches (see the media-zip
// rework in d002059 for why that matters).
func statePath() string {
	return filepath.Join("config", "state.json")
}

func deckSettingsPath(deck string) string {
	return filepath.Join("config", "decksettings", deck+".json")
}

// loadJSONFile decodes path into v and reports whether the file existed. A
// missing or unparsable file leaves v at its zero value and returns false,
// so callers can fall back to defaults exactly as they do for a deck with
// no anki-fbink.conf [deck:NAME] section.
func loadJSONFile(path string, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}

// saveJSONFile writes v to path atomically (see core.WriteFileAtomic),
// creating any missing parent directory (e.g. config/decksettings/) along
// the way.
func saveJSONFile(path string, v any) error {
	return core.WriteFileAtomic(path, 0644, func(w io.Writer) error {
		return json.NewEncoder(w).Encode(v)
	})
}
