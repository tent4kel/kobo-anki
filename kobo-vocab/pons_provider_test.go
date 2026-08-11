package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ponsHuisResponse is the live API's answer for q=huis&l=denl&in=nl, cut
// down to the rows that matter. The markup is verbatim - the interleaved
// apparatus, the newlines inside attributes and the doubled spaces are all
// real, and are exactly what the extraction has to survive.
const ponsHuisResponse = `[{
  "lang": "nl",
  "hits": [{
    "type": "entry",
    "opendict": false,
    "roms": [{
      "headword": "huis",
      "headword_full": "huis  <span class=\"phonetics\">[hœys]</span> <span class=\"wordclass\"><acronym title=\"noun\">N</acronym></span>",
      "wordclass": "noun",
      "arabs": [{
        "header": "",
        "translations": [
          {"source": "<strong class=\"headword\">huis</strong>",
           "target": "Haus  <span class=\"genus\">\n <abbr title=\"onzijdig\">nt</abbr>\n </span> \n <span class=\"complement\">huizen</span>"},
          {"source": "<strong class=\"headword\">huis</strong>",
           "target": "Heim  <span class=\"genus\"><abbr title=\"onzijdig\">nt</abbr></span>"},
          {"source": "<strong class=\"headword\">huis</strong> <span class=\"topic\"><abbr title=\"techniek\">TECH</abbr></span>",
           "target": "Gehäuse  <span class=\"genus\"><abbr title=\"onzijdig\">nt</abbr></span>"},
          {"source": "<span class=\"example\">open <strong class=\"tilde\">huis\n</strong>         </span> <span class=\"style\"><abbr title=\"figuurlijk\">fig</abbr></span>",
           "target": "Tag  <span class=\"genus\"><abbr title=\"mannelijk\">m</abbr></span> der offenen Tür <span class=\"style\"><abbr title=\"figuurlijk\">fig</abbr></span>"},
          {"source": "<span class=\"example\">aan <strong class=\"tilde\">huis</strong> brengen</span>",
           "target": "ins Haus bringen"}
        ]
      }]
    }]
  }]
}]`

func newTestPonsProvider(t *testing.T, serverURL string) *PonsProvider {
	t.Helper()
	dir := t.TempDir()
	conf := filepath.Join(dir, "pons.json")
	body, _ := json.Marshal(ponsCreds{BaseURL: serverURL, Token: "test-secret"})
	if err := os.WriteFile(conf, body, 0644); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	p, err := NewPonsProvider(conf, compileCardFormatOrDefault(""), "")
	if err != nil {
		t.Fatalf("NewPonsProvider: %v", err)
	}
	return p
}

func TestPonsProviderFullLookup(t *testing.T) {
	var gotQuery url.Values
	var gotSecret string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		gotSecret = r.Header.Get("X-Secret")
		w.Write([]byte(ponsHuisResponse))
	}))
	defer server.Close()

	p := newTestPonsProvider(t, server.URL)
	e, err := p.Lookup("huis", "nl", "de")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	// The credential goes in a header, not the query string.
	if gotSecret != "test-secret" {
		t.Errorf("X-Secret = %q, want the configured token", gotSecret)
	}
	// Both languages sorted alphabetically, plus the direction - without
	// "in" the API answers in both directions at once.
	if got := gotQuery.Get("l"); got != "denl" {
		t.Errorf("l = %q, want denl", got)
	}
	if got := gotQuery.Get("in"); got != "nl" {
		t.Errorf("in = %q, want nl", got)
	}
	if got := gotQuery.Get("fm"); got != "" {
		t.Errorf("fm = %q, want fuzzy matching left off entirely", got)
	}

	// Apparatus stripped, three senses kept, the example row excluded from
	// the alternatives and used as the example instead.
	wantBack := "**Haus**\n\nHeim, Gehäuse\n\n*Tag der offenen Tür*"
	if e.Definition != wantBack {
		t.Errorf("Definition =\n%q\nwant\n%q", e.Definition, wantBack)
	}
	wantFront := "**huis**\n\n*open huis*"
	if e.Front != wantFront {
		t.Errorf("Front =\n%q\nwant\n%q", e.Front, wantFront)
	}
	if e.Source != "pons" {
		t.Errorf("Source = %q, want pons", e.Source)
	}
}

// 204 is the API's documented "nothing found", and the single most common
// response for a vocabulary list: it has to read as not-found so the chain
// moves on quietly, not as an error worth logging on every word.
func TestPonsProviderNoContentIsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	_, err := newTestPonsProvider(t, server.URL).Lookup("zzqxwvfoo", "nl", "de")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// Everything else is a real failure. These must NOT be ErrNotFound, or a
// bad token would look like 500 words that simply are not in PONS, and the
// circuit breaker would never trip.
func TestPonsProviderErrorsAreNotSilent(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusForbidden, "not authorized"},
		{http.StatusNotFound, "no dictionary"},
		{http.StatusServiceUnavailable, "out of allowance"},
		{http.StatusInternalServerError, "HTTP 500"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		_, err := newTestPonsProvider(t, server.URL).Lookup("huis", "nl", "de")
		server.Close()

		if err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("HTTP %d: err = %v, want a hard error", tc.status, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("HTTP %d: err = %q, want it to mention %q", tc.status, err, tc.want)
		}
	}
}

// The response carries one object per direction and the dictionary name
// does not imply one, so a de->nl block must not leak into an nl->de card.
func TestPonsProviderKeepsOnlyTheRequestedDirection(t *testing.T) {
	const bothDirections = `[
      {"lang": "de", "hits": [{"type": "entry", "roms": [{"headword": "Haus",
        "arabs": [{"translations": [{"source": "<strong class=\"headword\">Haus</strong>", "target": "verkeerd"}]}]}]}]},
      {"lang": "nl", "hits": [{"type": "entry", "roms": [{"headword": "huis",
        "arabs": [{"translations": [{"source": "<strong class=\"headword\">huis</strong>", "target": "Haus"}]}]}]}]}
    ]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(bothDirections))
	}))
	defer server.Close()

	e, err := newTestPonsProvider(t, server.URL).Lookup("huis", "nl", "de")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if strings.Contains(e.Definition, "verkeerd") {
		t.Errorf("Definition %q picked up the de->nl direction", e.Definition)
	}
	if !strings.Contains(e.Definition, "Haus") {
		t.Errorf("Definition = %q, want the nl->de translation", e.Definition)
	}
}

// ref=true wraps the real entry in a primary/secondary structure. The
// translations live in there and have to be found.
func TestPonsProviderFollowsReferencedEntries(t *testing.T) {
	const withRefs = `[{"lang": "nl", "hits": [{
      "type": "entry_with_secondary_entries",
      "primary_entry": {"type": "entry", "roms": [{"headword": "ging", "arabs": []}]},
      "secondary_entries": [{"type": "entry", "roms": [{"headword": "gaan",
        "arabs": [{"translations": [{"source": "<strong class=\"headword\">gaan</strong>", "target": "gehen"}]}]}]}]
    }]}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(withRefs))
	}))
	defer server.Close()

	e, err := newTestPonsProvider(t, server.URL).Lookup("ging", "nl", "de")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !strings.Contains(e.Definition, "gehen") {
		t.Errorf("Definition = %q, want the referenced entry's translation", e.Definition)
	}
	// The primary entry names the form actually looked up, so it wins the
	// front even though its own arabs are empty.
	if !strings.Contains(e.Front, "ging") {
		t.Errorf("Front = %q, want the queried headword", e.Front)
	}
}

func TestPonsPlainTextStripsApparatus(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`Haus  <span class="genus"><abbr title="onzijdig">nt</abbr></span> <span class="complement">huizen</span>`, "Haus"},
		// Apparatus interleaved mid-phrase: the parts outside the spans
		// still have to join up in order.
		{`Tag <span class="genus"><abbr>m</abbr></span> der offenen Tür <span class="style"><abbr>fig</abbr></span>`, "Tag der offenen Tür"},
		// Leading usage note, translation after it.
		{`<span class="info"><abbr title="meestal">meist</abbr></span>  Haus-`, "Haus-"},
		{`ins Haus bringen`, "ins Haus bringen"},
		{`nicht zu Hause sein, unterwegs sein`, "nicht zu Hause sein, unterwegs sein"},
		{``, ""},
		// Nothing but apparatus is nothing at all, not a blank alternative.
		{`<span class="genus">nt</span>`, ""},
	} {
		if got := ponsPlainText(tc.in); got != tc.want {
			t.Errorf("ponsPlainText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPonsExamplePhraseDistinguishesRowKinds(t *testing.T) {
	// A headword row is not an example, however it is decorated.
	if got := ponsExamplePhrase(`<strong class="headword">huis</strong> <span class="topic"><abbr>TECH</abbr></span>`); got != "" {
		t.Errorf("headword row reported example %q, want none", got)
	}
	// An example keeps the tilde-substituted headword and drops the
	// apparatus sitting beside it.
	got := ponsExamplePhrase(`<span class="example">open <strong class="tilde">huis
</strong>         </span> <span class="style"><abbr>fig</abbr></span>`)
	if got != "open huis" {
		t.Errorf("example phrase = %q, want %q", got, "open huis")
	}
}

func TestPonsDictKeyIsAlphabetical(t *testing.T) {
	// The API names a dictionary by both languages sorted, regardless of
	// which way the lookup goes - "denl" serves nl->de and de->nl alike.
	for _, tc := range []struct{ a, b, want string }{
		{"nl", "de", "denl"},
		{"de", "nl", "denl"},
		{"de", "en", "deen"},
		{"EN", "De", "deen"},
		{"fr", "de", "defr"},
	} {
		if got := ponsDictKey(tc.a, tc.b); got != tc.want {
			t.Errorf("ponsDictKey(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestPonsEndpointNormalizesBaseURL(t *testing.T) {
	// /dictionaries (the listing) and /dictionary (the lookup) sit next to
	// each other in the API docs; pointing base_url at the wrong one should
	// not make every word fail.
	for _, tc := range []struct{ in, want string }{
		{"https://api.pons.com/v1/dictionary", "https://api.pons.com/v1/dictionary"},
		{"https://api.pons.com/v1/dictionaries", "https://api.pons.com/v1/dictionary"},
		{"https://api.pons.com/v1", "https://api.pons.com/v1/dictionary"},
		{"https://api.pons.com/v1/", "https://api.pons.com/v1/dictionary"},
	} {
		if got := ponsEndpoint(tc.in); got != tc.want {
			t.Errorf("ponsEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Once the API says the allowance is gone, stop asking. The standdown has
// to bite within the same run too, or a few hundred words would each pay
// their own 503.
func TestPonsProviderStandsDownAfter503(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	p := newTestPonsProvider(t, server.URL)

	// First lookup discovers the limit and reports it loudly.
	_, err := p.Lookup("huis", "nl", "de")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("first lookup err = %v, want a reported error", err)
	}
	if !strings.Contains(err.Error(), "disabled until tomorrow") {
		t.Errorf("err = %q, want it to say the provider is standing down", err)
	}

	// Every later one is skipped silently, so the chain just moves on.
	for i := 0; i < 4; i++ {
		if _, err := p.Lookup("huis", "nl", "de"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("lookup %d err = %v, want ErrNotFound", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("made %d requests, want 1 (the one that found the limit)", calls)
	}
}

// The standdown must survive between runs, or each run re-discovers the
// limit at the cost of another call.
func TestPonsCooldownPersistsAndExpiresNextDay(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(ponsHuisResponse))
	}))
	defer server.Close()

	dir := t.TempDir()
	conf := filepath.Join(dir, "pons.json")
	body, _ := json.Marshal(ponsCreds{BaseURL: server.URL, Token: "test-secret"})
	if err := os.WriteFile(conf, body, 0644); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	newRun := func(day string) *PonsProvider {
		p, err := NewPonsProvider(conf, compileCardFormatOrDefault(""), "")
		if err != nil {
			t.Fatalf("NewPonsProvider: %v", err)
		}
		p.now = func() time.Time {
			d, _ := time.Parse("2006-01-02", day)
			return d
		}
		return p
	}

	if _, err := newRun("2026-08-11").Lookup("huis", "nl", "de"); err == nil {
		t.Fatal("expected the 503 to be reported")
	}
	// A fresh provider on the same day reads the state file and skips.
	if _, err := newRun("2026-08-11").Lookup("huis", "nl", "de"); !errors.Is(err, ErrNotFound) {
		t.Errorf("same-day rerun err = %v, want ErrNotFound", err)
	}
	if calls != 1 {
		t.Fatalf("made %d requests on the blocked day, want 1", calls)
	}

	// Next day it tries again.
	if _, err := newRun("2026-08-12").Lookup("huis", "nl", "de"); err != nil {
		t.Errorf("next-day lookup err = %v, want it to resume", err)
	}
	if calls != 2 {
		t.Errorf("made %d requests total, want 2 (one per day)", calls)
	}
}

// A state file this provider no longer writes - or any garbage - must fail
// open rather than silently disabling PONS forever.
func TestPonsCooldownFailsOpenOnUnreadableState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pons.state.json")
	for _, content := range []string{
		`{"date":"2026-08","used":3}`, // the old counter shape
		`not json at all`,
		``,
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if loadPonsCooldown(path).blocking("2026-08-11") {
			t.Errorf("state %q read as a standdown, want not blocked", content)
		}
	}
	if loadPonsCooldown(filepath.Join(dir, "missing.json")).blocking("2026-08-11") {
		t.Error("missing state file read as a standdown")
	}
}

// A same-language pair has no dictionary and would just waste a call.
func TestPonsProviderSkipsDegenerateLanguagePairs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should have been made")
	}))
	defer server.Close()

	p := newTestPonsProvider(t, server.URL)
	for _, tc := range [][2]string{{"nl", "nl"}, {"nl", ""}, {"", "de"}} {
		if _, err := p.Lookup("huis", tc[0], tc[1]); !errors.Is(err, ErrNotFound) {
			t.Errorf("Lookup(%q, %q) err = %v, want ErrNotFound", tc[0], tc[1], err)
		}
	}
}
