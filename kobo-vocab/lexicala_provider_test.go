package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// newTestLexicalaProvider starts a fake Lexicala API returning canned
// responses shaped like the real ones (verified against the live API
// during design), and wires a LexicalaProvider at it via a temp
// credentials file - exercising the real credential-loading and
// host-derivation path, not just the HTTP-call logic.
func newTestLexicalaProvider(t *testing.T, handler http.HandlerFunc) *LexicalaProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	confPath := filepath.Join(dir, "lexicala.json")
	creds := fmt.Sprintf(`{"base_url": %q, "token": "test-token"}`, server.URL+"/")
	if err := os.WriteFile(confPath, []byte(creds), 0644); err != nil {
		t.Fatalf("write test creds: %v", err)
	}

	p, err := NewLexicalaProvider(confPath, defaultCardFormat(), "")
	if err != nil {
		t.Fatalf("NewLexicalaProvider: %v", err)
	}
	return p
}

func fullLexicalaHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lemmatize":
			if got := r.URL.Query().Get("word"); got != "liepen" {
				t.Errorf("lemmatize word = %q, want liepen", got)
			}
			fmt.Fprint(w, `{"results":[{"headwords":[{"text":"lopen","pos":"verb"}]}]}`)
		case "/translate-to":
			if got := r.URL.Query().Get("text"); got != "lopen" {
				t.Errorf("translate-to text = %q, want lopen (the lemma, not the inflected form)", got)
			}
			if got := r.URL.Query().Get("targetLang"); got != "de" {
				t.Errorf("translate-to targetLang = %q, want de", got)
			}
			fmt.Fprint(w, `{"results":[{"lemma":"lopen","translations":{"de":["laufen","gehen","rennen","funktionieren"]}}]}`)
		case "/examples":
			fmt.Fprint(w, `{"results":[{"lemma":"lopen","examples":[{"text":"Het is een kwartiertje lopen hiervandaan."}]}]}`)
		case "/translate-example":
			if got := r.URL.Query().Get("target_language"); got != "de" {
				t.Errorf("translate-example target_language = %q, want de", got)
			}
			fmt.Fprint(w, `{"results":[{"lemma":"lopen","translation":[{"text":"Von hier aus ist es eine Viertelstunde zu gehen."}]}]}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestLexicalaProviderFullLookup(t *testing.T) {
	p := newTestLexicalaProvider(t, fullLexicalaHandler(t))

	e, err := p.Lookup("liepen", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if e.Source != "lexicala" {
		t.Errorf("Source = %q, want lexicala", e.Source)
	}
	// Same theme as ChatGPTProvider: bold headword, plain alternatives,
	// italic example. Back: primary translation bold, remaining 2
	// alternatives plain, translated example italic.
	wantDef := "**laufen**, gehen, rennen *Von hier aus ist es eine Viertelstunde zu gehen.*"
	if e.Definition != wantDef {
		t.Errorf("Definition = %q, want %q", e.Definition, wantDef)
	}
	// Front: source-language lemma bold, untranslated example italic - no
	// alternatives segment (see Lookup's doc comment on why).
	wantFront := "**lopen** *Het is een kwartiertje lopen hiervandaan.*"
	if e.Front != wantFront {
		t.Errorf("Front = %q, want %q", e.Front, wantFront)
	}
	if e.Example != "" {
		t.Errorf("Example = %q, want empty (embedded into Front/Definition directly, not the generic append)", e.Example)
	}
}

func TestLexicalaProviderNoTranslationsIsNotFound(t *testing.T) {
	p := newTestLexicalaProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lemmatize":
			fmt.Fprint(w, `{"results":[]}`)
		case "/translate-to":
			fmt.Fprint(w, `{"n_results":0,"results":[]}`)
		}
	})

	_, err := p.Lookup("xyzzy", "nl", "de")
	if err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestLexicalaProviderExampleFailureIsNonFatal(t *testing.T) {
	// /examples and /translate-example erroring shouldn't fail the whole
	// lookup - a missing example is far less bad than a missing translation.
	p := newTestLexicalaProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lemmatize":
			fmt.Fprint(w, `{"results":[{"headwords":[{"text":"lopen"}]}]}`)
		case "/translate-to":
			fmt.Fprint(w, `{"results":[{"translations":{"de":["laufen"]}}]}`)
		case "/examples":
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	e, err := p.Lookup("lopen", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if e.Definition != "**laufen**" {
		t.Errorf("Definition = %q, want **laufen** (single translation, no example)", e.Definition)
	}
	if e.Front != "**lopen**" {
		t.Errorf("Front = %q, want **lopen** (no example on examples-endpoint failure)", e.Front)
	}
}

func TestLexicalaProviderLemmatizeFailureFallsBackToOriginalWord(t *testing.T) {
	p := newTestLexicalaProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lemmatize":
			w.WriteHeader(http.StatusInternalServerError)
		case "/translate-to":
			if got := r.URL.Query().Get("text"); got != "lopen" {
				t.Errorf("translate-to text = %q, want the original word lopen (lemmatize failed)", got)
			}
			fmt.Fprint(w, `{"results":[{"translations":{"de":["laufen"]}}]}`)
		case "/examples":
			fmt.Fprint(w, `{"results":[]}`)
		}
	})

	e, err := p.Lookup("lopen", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if e.Definition != "**laufen**" {
		t.Errorf("Definition = %q, want **laufen**", e.Definition)
	}
}

func TestLexicalaProviderQuotaExhausted(t *testing.T) {
	calls := 0
	p := newTestLexicalaProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"results":[]}`)
	})
	p.quota.used = p.quota.limit // exhaust it up front

	_, err := p.Lookup("lopen", "nl", "de")
	if err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound when quota is exhausted", err)
	}
	if calls != 0 {
		t.Errorf("made %d HTTP calls, want 0 - quota check must happen before the request", calls)
	}
}

func TestLexicalaProviderQuotaPersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "lexicala.state.json")

	q := loadLexicalaQuota(statePath, 45)
	q.record()
	q.record()

	q2 := loadLexicalaQuota(statePath, 45)
	if q2.used != 2 {
		t.Errorf("used = %d, want 2 (loaded from state file)", q2.used)
	}
}

func TestNewLexicalaProviderMissingCredsFile(t *testing.T) {
	if _, err := NewLexicalaProvider(filepath.Join(t.TempDir(), "nonexistent.json"), defaultCardFormat(), ""); err == nil {
		t.Error("expected an error for a missing credentials file")
	}
}

func TestNewLexicalaProviderIncompleteCreds(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "lexicala.json")
	data, _ := json.Marshal(map[string]string{"base_url": "https://example.com/"})
	os.WriteFile(confPath, data, 0644)

	if _, err := NewLexicalaProvider(confPath, defaultCardFormat(), ""); err == nil {
		t.Error("expected an error for creds missing a token")
	}
}
