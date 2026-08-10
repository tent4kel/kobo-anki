package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// loadConfigFrom points loadConfig at a temp file by writing it where the
// function looks, then restores the previous working directory.
func loadConfigFrom(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "anki-fbink.conf"), []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })

	// loadConfig mutates package globals; reset the ones it touches.
	cfg = Style{SizeTitle: 24, SizeCard: 28, SizeMenu: 16, LineHeight: 1.0}
	deckOverrides = make(map[string]*StyleOverride)
	touchCooldown = 300 * time.Millisecond

	loadConfig()
}

func TestLoadConfigAppliesGlobalKeys(t *testing.T) {
	loadConfigFrom(t, `
font_dir=/fonts
size_card=44
line_height=1.4
margin=12
darkmode=yes
touch_cooldown=500
`)
	if cfg.FontDir != "/fonts" {
		t.Errorf("FontDir = %q", cfg.FontDir)
	}
	if cfg.SizeCard != 44 {
		t.Errorf("SizeCard = %d, want 44", cfg.SizeCard)
	}
	if cfg.LineHeight != 1.4 {
		t.Errorf("LineHeight = %v, want 1.4", cfg.LineHeight)
	}
	if cfg.Margin != 12 {
		t.Errorf("Margin = %d, want 12", cfg.Margin)
	}
	if !cfg.DarkMode {
		t.Error(`DarkMode = false, want true (from "yes")`)
	}
	if touchCooldown != 500*time.Millisecond {
		t.Errorf("touchCooldown = %v, want 500ms", touchCooldown)
	}
	// Untouched defaults survive.
	if cfg.SizeMenu != 16 {
		t.Errorf("SizeMenu = %d, want the 16 default", cfg.SizeMenu)
	}
}

func TestLoadConfigDeckOverrideBeatsGlobal(t *testing.T) {
	loadConfigFrom(t, `
size_card=28
margin=4

[deck:vocab-nl-de]
size_card=40
`)
	if cfg.SizeCard != 28 {
		t.Errorf("global SizeCard = %d, want 28", cfg.SizeCard)
	}
	s := effectiveStyle("vocab-nl-de")
	if s.SizeCard != 40 {
		t.Errorf("deck SizeCard = %d, want the 40 override", s.SizeCard)
	}
	if s.Margin != 4 {
		t.Errorf("deck Margin = %d, want the global 4 inherited", s.Margin)
	}
	// A deck with no section gets the globals unchanged.
	if other := effectiveStyle("french"); other.SizeCard != 28 {
		t.Errorf("unlisted deck SizeCard = %d, want 28", other.SizeCard)
	}
}

func TestLoadConfigWarnsOnUnknownAndBadValues(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	loadConfigFrom(t, "size_crad=40\nmargin=lots\n")
	log.SetOutput(old)

	out := buf.String()
	if !bytes.Contains([]byte(out), []byte(`unknown setting "size_crad"`)) {
		t.Errorf("no warning for the typo:\n%s", out)
	}
	if !bytes.Contains([]byte(out), []byte(`margin="lots"`)) {
		t.Errorf("no warning for the bad value:\n%s", out)
	}
}

func TestLoadConfigTouchCooldownIsGlobalOnly(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	loadConfigFrom(t, "[deck:x]\ntouch_cooldown=900\n")
	log.SetOutput(old)

	if touchCooldown != 300*time.Millisecond {
		t.Errorf("touchCooldown = %v, want the default - it is not a per-deck setting", touchCooldown)
	}
	if !bytes.Contains(buf.Bytes(), []byte("touch_cooldown")) {
		t.Errorf("using it inside a deck section should warn:\n%s", buf.String())
	}
}
