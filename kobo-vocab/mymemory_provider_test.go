package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMyMemoryProviderMultipleTranslations(t *testing.T) {
	// Shaped like the real API's response for a single-word query: the
	// primary pick plus a "matches" array of alternates, not sorted by
	// quality (quality=0 sits between two higher-quality entries here,
	// same as observed in the live API - see the type doc comment).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"responseData": {"translatedText": "haus"},
			"responseStatus": 200,
			"matches": [
				{"translation": "haus", "quality": "74", "match": 1},
				{"translation": "Haus", "quality": "0", "match": 1},
				{"translation": "Gestell", "quality": "68", "match": 0.99}
			]
		}`))
	}))
	defer server.Close()

	origURL := myMemoryAPIURL
	myMemoryAPIURL = server.URL
	defer func() { myMemoryAPIURL = origURL }()

	p := NewMyMemoryProvider("")
	e, err := p.Lookup("huis", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	// "haus" first (the primary pick), then "Haus" deduped away
	// (case-insensitive match against "haus"), then "Gestell" as the next
	// distinct alternate - capped at maxTranslations (3).
	want := "haus, Gestell"
	if e.Definition != want {
		t.Errorf("Definition = %q, want %q", e.Definition, want)
	}
	if e.Source != "mymemory" {
		t.Errorf("Source = %q, want mymemory", e.Source)
	}
}

func TestMyMemoryProviderHandlesBareNumberQuality(t *testing.T) {
	// Regression: verified live that MyMemory sends "quality" as a quoted
	// string for some matches and a bare JSON number for others, in the
	// same response. A plain `string` field would fail json.Unmarshal
	// entirely on the second entry below.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"responseData": {"translatedText": "haus"},
			"responseStatus": 200,
			"matches": [
				{"translation": "haus", "quality": "74", "match": 1},
				{"translation": "Gestell", "quality": 0, "match": 1}
			]
		}`))
	}))
	defer server.Close()

	origURL := myMemoryAPIURL
	myMemoryAPIURL = server.URL
	defer func() { myMemoryAPIURL = origURL }()

	p := NewMyMemoryProvider("")
	e, err := p.Lookup("huis", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error (bare-number quality must not break decoding), got %v", err)
	}
	want := "haus, Gestell"
	if e.Definition != want {
		t.Errorf("Definition = %q, want %q", e.Definition, want)
	}
}

func TestMyMemoryProviderNoTranslationIsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"responseData": {"translatedText": ""}, "responseStatus": 200}`))
	}))
	defer server.Close()

	origURL := myMemoryAPIURL
	myMemoryAPIURL = server.URL
	defer func() { myMemoryAPIURL = origURL }()

	p := NewMyMemoryProvider("")
	if _, err := p.Lookup("xyzzy", "nl", "de"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
