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
	if filepath.Base(cfg.OutDir) != "decks" {
		t.Errorf("OutDir = %q, want it to follow data_dir=decks", cfg.OutDir)
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
	if filepath.Base(cfg.OutDir) != "words" {
		t.Errorf("OutDir = %q, want out= honoured when there is no anki-core.conf", cfg.OutDir)
	}
}
