package main

import "strings"

// DictProvider wraps a single already-parsed local dict (see parseKoboDict
// in dictparse.go) plus that language's stemming rules. It's built fresh
// per language group in main(), since the underlying dict and rules are
// specific to one fromLang->toLang pair. Always first in the chain: it's
// free, instant, and offline.
type DictProvider struct {
	dict  *KoboDict
	rules *LangRules
}

func NewDictProvider(dict *KoboDict, rules *LangRules) *DictProvider {
	return &DictProvider{dict: dict, rules: rules}
}

func (p *DictProvider) Name() string { return "dict" }

func (p *DictProvider) Lookup(word, fromLang, toLang string) (Entry, error) {
	if p.dict == nil {
		return Entry{}, ErrNotFound
	}

	key := strings.ToLower(word)
	if def, ok := p.dict.Entries[key]; ok {
		return Entry{Front: p.dict.Fronts[key], Definition: def, Source: "dict"}, nil
	}

	if p.rules != nil {
		if def, stem := stemLookup(p.dict, word, p.rules); def != "" {
			return Entry{
				Front:      p.dict.Fronts[stem],
				Definition: def,
				Source:     "dict-stem",
			}, nil
		}
	}

	return Entry{}, ErrNotFound
}
