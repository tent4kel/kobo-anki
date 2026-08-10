package core

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureLog collects everything written to the standard logger during fn.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(old)
		log.SetFlags(flags)
	}()
	fn()
	return buf.String()
}

func TestConfigWarningsReportTyposAndBadValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "anki-core.conf")
	os.WriteFile(p, []byte("data_dir=words\nrevrse=true\nrequest_retention=high\n"), 0644)

	var cfg CoreConfig
	out := captureLog(t, func() { cfg = LoadCoreConfig(p) })

	if !strings.Contains(out, `unknown setting "revrse"`) {
		t.Errorf("no warning for the misspelled key:\n%s", out)
	}
	if !strings.Contains(out, `request_retention="high"`) {
		t.Errorf("no warning for the unparseable value:\n%s", out)
	}
	if !strings.Contains(out, "2 setting(s) ignored") {
		t.Errorf("no summary line:\n%s", out)
	}
	// Parsing still succeeds; only the bad settings keep their defaults.
	if cfg.DataDir != "words" {
		t.Errorf("DataDir = %q, want the good setting still applied", cfg.DataDir)
	}
	if cfg.RequestRetention != 0.9 {
		t.Errorf("RequestRetention = %v, want the 0.9 default retained", cfg.RequestRetention)
	}
}

func TestConfigWarningsSilentOnCleanFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "anki-core.conf")
	os.WriteFile(p, []byte("# a comment\n\ndata_dir=decks\nreverse=true\n"), 0644)

	out := captureLog(t, func() { LoadCoreConfig(p) })
	if out != "" {
		t.Errorf("a valid config should log nothing, got:\n%s", out)
	}
}

func TestConfigWarningsBoolAcceptsCommonSpellings(t *testing.T) {
	// The old `val == "true"` idiom silently treated "yes"/"TRUE"/"on" as
	// false. Accept the obvious spellings, and warn on anything else rather
	// than quietly picking a value the user didn't ask for.
	w := NewConfigWarnings("test.conf")
	for _, in := range []string{"true", "TRUE", "1", "yes", "on"} {
		if v, ok := w.Bool(1, "k", in); !ok || !v {
			t.Errorf("Bool(%q) = %v,%v; want true,true", in, v, ok)
		}
	}
	for _, in := range []string{"false", "FALSE", "0", "no", "off"} {
		if v, ok := w.Bool(1, "k", in); !ok || v {
			t.Errorf("Bool(%q) = %v,%v; want false,true", in, v, ok)
		}
	}
	if w.Count != 0 {
		t.Errorf("valid spellings produced %d warnings", w.Count)
	}
	if _, ok := w.Bool(1, "k", "maybe"); ok {
		t.Error(`Bool("maybe") reported ok; want a warning instead`)
	}
	if w.Count != 1 {
		t.Errorf("Count = %d, want 1 after the invalid value", w.Count)
	}
}

func TestConfigWarningsNilIsSafe(t *testing.T) {
	// A nil collector is a no-op, so callers never need a nil check.
	var w *ConfigWarnings
	w.UnknownKey(1, "k")
	w.BadValue(1, "k", "v", "a number")
	w.Summary()
	if _, ok := w.Int(1, "k", "12"); !ok {
		t.Error("Int should still parse through a nil collector")
	}
}
