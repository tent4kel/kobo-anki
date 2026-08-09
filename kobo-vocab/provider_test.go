package main

import "testing"

type fakeProvider struct {
	name  string
	entry Entry
	err   error
}

func (f fakeProvider) Name() string { return f.name }
func (f fakeProvider) Lookup(word, fromLang, toLang string) (Entry, error) {
	return f.entry, f.err
}

func TestChainReturnsFirstHit(t *testing.T) {
	c := NewChain(
		fakeProvider{name: "a", err: ErrNotFound},
		fakeProvider{name: "b", entry: Entry{Definition: "found", Source: "b"}},
		fakeProvider{name: "c", entry: Entry{Definition: "should not reach this"}},
	)
	e, err := c.Lookup("word", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if e.Definition != "found" || e.Source != "b" {
		t.Errorf("got %+v, want the second provider's entry", e)
	}
}

func TestChainFallsThroughOnHardError(t *testing.T) {
	// A transient error (network failure, etc.) should still let the chain
	// try the next provider, not abort the whole lookup.
	c := NewChain(
		fakeProvider{name: "a", err: errUnreachable},
		fakeProvider{name: "b", entry: Entry{Definition: "found", Source: "b"}},
	)
	e, err := c.Lookup("word", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if e.Source != "b" {
		t.Errorf("got source %q, want fallthrough to provider b", e.Source)
	}
}

func TestChainReturnsNotFoundWhenAllMiss(t *testing.T) {
	c := NewChain(
		fakeProvider{name: "a", err: ErrNotFound},
		fakeProvider{name: "b", err: errUnreachable},
	)
	_, err := c.Lookup("word", "nl", "de")
	if err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestChainEmpty(t *testing.T) {
	c := NewChain()
	_, err := c.Lookup("word", "nl", "de")
	if err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

var errUnreachable = errTest("simulated network failure")

type errTest string

func (e errTest) Error() string { return string(e) }

func TestDictProviderExactMatch(t *testing.T) {
	dict := &KoboDict{Entries: map[string]string{"huis": "house"}}
	p := NewDictProvider(dict, nil)
	e, err := p.Lookup("Huis", "nl", "en")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if e.Definition != "house" || e.Source != "dict" {
		t.Errorf("got %+v, want exact dict match", e)
	}
}

func TestDictProviderStemMatch(t *testing.T) {
	dict := &KoboDict{Entries: map[string]string{"redden": "to save"}}
	rules := &LangRules{Strips: []StripRule{{Suffix: "t", Replacement: "den"}}}
	p := NewDictProvider(dict, rules)
	e, err := p.Lookup("redt", "nl", "en")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if e.Source != "dict-stem" {
		t.Errorf("got source %q, want dict-stem", e.Source)
	}
	if e.Definition != "to save" {
		t.Errorf("got %q, want the plain definition - the stem note belongs in Source/logs, not on the card", e.Definition)
	}
}

func TestDictProviderExactMatchNoFrontsMap(t *testing.T) {
	// Fronts is nil unless dict_examples was on during parsing - reading a
	// nil map must not panic, and Entry.Front should just stay unset.
	dict := &KoboDict{Entries: map[string]string{"huis": "house"}}
	p := NewDictProvider(dict, nil)
	e, err := p.Lookup("huis", "nl", "en")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if e.Front != "" {
		t.Errorf("Front = %q, want empty", e.Front)
	}
}

func TestDictProviderExactMatchWithFront(t *testing.T) {
	dict := &KoboDict{
		Entries: map[string]string{"huis": "**Haus** *Haus an Haus wohnen*"},
		Fronts:  map[string]string{"huis": "**huis** *huis aan huis wonen*"},
	}
	p := NewDictProvider(dict, nil)
	e, err := p.Lookup("Huis", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if e.Front != "**huis** *huis aan huis wonen*" {
		t.Errorf("Front = %q, want the dict_examples-built front", e.Front)
	}
}

func TestDictProviderStemMatchWithFront(t *testing.T) {
	dict := &KoboDict{
		Entries: map[string]string{"redden": "**retten**"},
		Fronts:  map[string]string{"redden": "**redden** *ik red je*"},
	}
	rules := &LangRules{Strips: []StripRule{{Suffix: "t", Replacement: "den"}}}
	p := NewDictProvider(dict, rules)
	e, err := p.Lookup("redt", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if e.Front != "**redden** *ik red je*" {
		t.Errorf("Front = %q, want the stemmed entry's front", e.Front)
	}
}

func TestDictProviderNotFound(t *testing.T) {
	dict := &KoboDict{Entries: map[string]string{"huis": "house"}}
	p := NewDictProvider(dict, nil)
	if _, err := p.Lookup("nonexistent", "nl", "en"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDictProviderNilDict(t *testing.T) {
	p := NewDictProvider(nil, nil)
	if _, err := p.Lookup("huis", "nl", "en"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound for nil dict", err)
	}
}

// countingFailer always returns a hard (non-ErrNotFound) error and counts
// how many times Lookup was actually called through to it.
type countingFailer struct {
	name  string
	calls int
}

func (f *countingFailer) Name() string { return f.name }
func (f *countingFailer) Lookup(word, fromLang, toLang string) (Entry, error) {
	f.calls++
	return Entry{}, errUnreachable
}

func TestCircuitBreakerTripsAfterThreshold(t *testing.T) {
	inner := &countingFailer{name: "flaky"}
	b := NewCircuitBreaker(inner, 3)

	for i := 0; i < 3; i++ {
		if _, err := b.Lookup("w", "nl", "de"); err != errUnreachable {
			t.Fatalf("call %d: err = %v, want the underlying error while not yet tripped", i, err)
		}
	}
	if inner.calls != 3 {
		t.Fatalf("inner.calls = %d, want 3", inner.calls)
	}

	// Threshold reached - further calls must not reach the inner provider.
	for i := 0; i < 5; i++ {
		if _, err := b.Lookup("w", "nl", "de"); err != ErrNotFound {
			t.Errorf("post-trip call %d: err = %v, want ErrNotFound", i, err)
		}
	}
	if inner.calls != 3 {
		t.Errorf("inner.calls = %d after tripping, want still 3 (no further calls to a tripped provider)", inner.calls)
	}
}

func TestCircuitBreakerResetsOnSuccess(t *testing.T) {
	calls := 0
	var p fakeProviderFunc = func(word, fromLang, toLang string) (Entry, error) {
		calls++
		if calls == 3 {
			return Entry{Definition: "ok"}, nil
		}
		return Entry{}, errUnreachable
	}
	b := NewCircuitBreaker(p, 3)

	b.Lookup("a", "nl", "de") // fail 1
	b.Lookup("b", "nl", "de") // fail 2
	if e, err := b.Lookup("c", "nl", "de"); err != nil || e.Definition != "ok" {
		t.Fatalf("call 3: got (%+v, %v), want a successful lookup", e, err)
	}

	// A success should have reset the counter - two more failures shouldn't
	// trip it (would need a 3rd in a row after the reset).
	b.Lookup("d", "nl", "de")
	if _, err := b.Lookup("e", "nl", "de"); err != errUnreachable {
		t.Errorf("err = %v, want the underlying error - breaker should not have tripped yet", err)
	}
}

type fakeProviderFunc func(word, fromLang, toLang string) (Entry, error)

func (f fakeProviderFunc) Name() string { return "fake" }
func (f fakeProviderFunc) Lookup(word, fromLang, toLang string) (Entry, error) {
	return f(word, fromLang, toLang)
}
