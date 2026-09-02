package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func writeConfs(t *testing.T, mywords, core string) string {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "anki-mywords.conf"), []byte(mywords), 0644)
	if core != "" {
		os.WriteFile(filepath.Join(dir, "anki-core.conf"), []byte(core), 0644)
	}
	return filepath.Join(dir, "anki-mywords.conf")
}

func TestOutDirInheritsDataDir(t *testing.T) {
	p := writeConfs(t, "providers=dict\n", "data_dir=decks\n")
	cfg := loadConfig(p)
	// OutDir must resolve relative to the process's working directory, the
	// same way cmd/fbink and cmd/server resolve data_dir - not relative to
	// the config file's own directory (confDir), which those two binaries
	// have no notion of at all. Joining it against confDir would put
	// kobo-vocab's decks somewhere those two binaries never look.
	if cfg.OutDir != "decks" {
		t.Errorf("OutDir = %q, want exactly %q (cwd-relative, not confDir-relative)", cfg.OutDir, "decks")
	}
}

func TestOutDirDisagreementWarns(t *testing.T) {
	p := writeConfs(t, "out=elsewhere\n", "data_dir=decks\n")
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	loadConfig(p)
	log.SetOutput(old)
	if !bytes.Contains(buf.Bytes(), []byte("disagrees with data_dir")) {
		t.Errorf("no warning for the mismatch:\n%s", buf.String())
	}
}

func TestOutDirAgreementIsSilent(t *testing.T) {
	p := writeConfs(t, "out=decks\n", "data_dir=decks\n")
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	loadConfig(p)
	log.SetOutput(old)
	if bytes.Contains(buf.Bytes(), []byte("disagrees")) {
		t.Errorf("matching values should not warn:\n%s", buf.String())
	}
}

func TestOutDirWithoutCoreConfKeepsOwnValue(t *testing.T) {
	p := writeConfs(t, "out=words\n", "")
	cfg := loadConfig(p)
	if cfg.OutDir != "words" {
		t.Errorf("OutDir = %q, want exactly %q (cwd-relative, not confDir-relative)", cfg.OutDir, "words")
	}
}
