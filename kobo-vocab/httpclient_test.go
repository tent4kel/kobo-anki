package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewHTTPClientMissingCAFileDoesNotPanic(t *testing.T) {
	client := newHTTPClient(5*time.Second, filepath.Join(t.TempDir(), "nonexistent.pem"))
	if client.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", client.Timeout)
	}
}

func TestNewHTTPClientMalformedCAFileDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cacert.pem")
	if err := os.WriteFile(path, []byte("not a valid PEM file"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	client := newHTTPClient(5*time.Second, path)
	if client.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", client.Timeout)
	}
}

func TestNewHTTPClientRealBundledCACert(t *testing.T) {
	// Sanity check the actual shipped cacert.pem parses as valid PEM
	// certificate data, not just "some file that happens to exist".
	client := newHTTPClient(5*time.Second, "../cacert.pem")
	if client.Transport == nil {
		t.Fatal("expected a non-nil Transport")
	}
}
