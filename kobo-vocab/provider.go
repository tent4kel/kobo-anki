package main

import (
	"errors"
	"log"
)

// Entry is what any content provider returns for a successful lookup.
type Entry struct {
	Front      string // optional override for the card front; "" = use the raw vocab word as-is. Set by providers that build a matching front+back card (ChatGPTProvider, LexicalaProvider, and DictProvider when dict_examples finds one); other providers leave this unset
	Definition string // markdown, already formatted/truncated
	Example    string // optional short example sentence; "" if none
	Source     string // provenance label for logs/CSV, e.g. "dict", "dict-stem", "lexicala", "mymemory", "chatgpt"
}

// ErrNotFound means this provider has nothing for this word - the chain
// should try the next one, silently. Any other error (network failure,
// malformed response, exhausted quota) is logged before falling through,
// so a flaky provider doesn't take the whole run down but also doesn't
// fail invisibly.
var ErrNotFound = errors.New("not found")

// Provider is a single source of translations/definitions: the local dict
// files, an online dictionary API (Lexicala), a translation API (MyMemory),
// or (future) an LLM-based provider. All are looked up the same way, so
// they can be freely reordered or combined via Chain.
type Provider interface {
	Name() string
	Lookup(word, fromLang, toLang string) (Entry, error)
}

// Chain tries providers in order and returns the first hit. This is the
// generalized form of the old dict-exact -> dict-stem -> API-fallback
// branching: each step just becomes one more Provider in the list.
type Chain struct {
	providers []Provider
}

func NewChain(providers ...Provider) Chain {
	return Chain{providers: providers}
}

func (c Chain) Lookup(word, fromLang, toLang string) (Entry, error) {
	for _, p := range c.providers {
		e, err := p.Lookup(word, fromLang, toLang)
		if err == nil {
			return e, nil
		}
		if !errors.Is(err, ErrNotFound) {
			log.Printf("  [%s] %s: %v", p.Name(), word, err)
		}
	}
	return Entry{}, ErrNotFound
}

// CircuitBreaker wraps a Provider and gives up on it for the rest of the
// process once it has failed too many times in a row with a hard error
// (network down, DNS failure, etc.) - not ErrNotFound, which is a normal,
// expected outcome and doesn't count against the threshold. Without this, a
// device with no connectivity would retry every network provider on every
// single word for the whole run, each paying its own timeout, instead of
// noticing once that the network is unreachable. Meant to wrap
// network-backed providers (Lexicala, MyMemory) at construction time, not
// the local dict provider.
type CircuitBreaker struct {
	inner     Provider
	threshold int

	consecutive int
	tripped     bool
}

func NewCircuitBreaker(inner Provider, threshold int) *CircuitBreaker {
	return &CircuitBreaker{inner: inner, threshold: threshold}
}

func (b *CircuitBreaker) Name() string { return b.inner.Name() }

func (b *CircuitBreaker) Lookup(word, fromLang, toLang string) (Entry, error) {
	if b.tripped {
		return Entry{}, ErrNotFound
	}

	e, err := b.inner.Lookup(word, fromLang, toLang)
	if err == nil || errors.Is(err, ErrNotFound) {
		b.consecutive = 0
		return e, err
	}

	b.consecutive++
	if b.consecutive >= b.threshold {
		b.tripped = true
		log.Printf("  [%s] %d consecutive failures, giving up on this provider for the rest of the run", b.inner.Name(), b.consecutive)
	}
	return Entry{}, err
}
