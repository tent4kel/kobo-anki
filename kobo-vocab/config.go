package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"kobo-anki/core"
)

// --- Types ---

type Config struct {
	DictDir      string
	DBPath       string
	OutDir       string
	StripLabels  []string // annotation classes (gen/gram/reg/dom/geo) to remove from card text - see dict_strip_labels and CLASSES.md in the dict source
	LexicalaConf string   // path to Lexicala credentials JSON (base_url/token), only read if "lexicala" is in a pair's providers
	PonsConf     string   // path to PONS credentials JSON (base_url/token), only read if "pons" is in a pair's providers
	CACertPath   string   // path to a PEM CA bundle, appended to the system trust store for every network provider's TLS verification - see newHTTPClient. Needed on Kobo firmware that ships no CA bundle at all.

	// CardFormat is the card_format template (see parseCardTemplate in
	// cardtemplate.go) shared by dict (when DictExamples is on), lexicala,
	// and chatgpt - global only, no per-pair override: lexicala/chatgpt are
	// built once and cached across every pair in a run (see providerCache in
	// main()), so a per-pair override would silently only apply to whichever
	// pair happened to build them first.
	CardFormat string

	// ChatGPT provider settings - all "variables to build the request" live
	// here rather than in APIs/chatgpt.json, which holds only credentials
	// (base_url/token), mirroring the Lexicala split. Global for the same
	// reason as CardFormat: one instance is shared by every pair.
	ChatGPTConf            string
	ChatGPTModel           string
	ChatGPTTemperature     float64
	ChatGPTMaxTokens       int
	ChatGPTMaxAlternatives int

	// Defaults is the pair-scoped settings as they stand before any [pair:]
	// section: the built-in defaults with the file's global keys folded on.
	Defaults PairSettings

	// Langs holds one entry per [lang:xx] section, keyed by source language.
	// Stemming is the one thing that genuinely depends on the source
	// language alone - nl reduces the same way whether the target is de or
	// en - so it is all that lives here.
	Langs map[string]*LangRules

	// Pairs holds one entry per [pair:xx-yy] section, keyed by language pair
	// ("nl-de"). Everything about a dictionary belongs here rather than in
	// Langs: nl-de and nl-en can come from different generators and want
	// different providers, which a per-language section cannot express. It
	// used to paper over that with caveats - "has no effect on flat
	// dictionaries" - instead of simply not applying.
	Pairs map[string]*PairOverride
}

// PairSettings is everything that depends on a language pair: the dictionary
// named by that pair, and the cards built from it.
type PairSettings struct {
	MaxSenses    int      // senses to include per word; 0 = unlimited
	MaxChars     int      // visible characters per card face; 0 = unlimited
	DictExamples bool     // build a themed card (headword + alternatives + example) from the dict's own example phrases
	Providers    []string // content providers to try in order - see provider.go

	// Profile names the dictionary's structure: "" or "auto" to detect it
	// from the markup, otherwise a name from builtinProfiles, or "custom" to
	// start from nothing. Selectors are layered on top of whichever wins.
	//
	// Unlike the four above, these have no global default and cannot be set
	// outside a [pair:] section: a selector describes one dictionary's
	// markup, so there is nothing sensible for it to mean across all of them.
	Profile      string
	Selectors    dictProfile
	HasSelectors bool
}

// PairOverride is one [pair:xx-yy] section - and also the global keys before
// the first section, which are parsed into one of these and then folded onto
// the built-in defaults. Every value field is a pointer so "not set here"
// stays distinguishable from an explicit zero, false, or empty list.
//
// One override type serving both scopes is deliberate, and copied from
// Style/StyleOverride in cmd/fbink: the keys valid in both places go through
// exactly one switch (applyPairKey) instead of two parallel ones that have to
// be kept in sync. Two such switches are how this config ended up with only
// some of its settings overridable, and with no way to say for one dictionary
// what could be said for all of them.
type PairOverride struct {
	MaxSenses    *int
	MaxChars     *int
	DictExamples *bool
	Providers    *[]string

	Profile      *string
	Selectors    dictProfile
	HasSelectors bool
}

// defaultPairSettings are the built-in defaults, before any config file.
func defaultPairSettings() PairSettings {
	return PairSettings{
		MaxSenses: 1,
		MaxChars:  150,
		// The local dict is the one provider needing no credentials and no
		// network, so it is what you get without saying anything.
		Providers: []string{"dict"},
	}
}

// applyPairOverride returns s with every field o actually sets replaced.
func applyPairOverride(s PairSettings, o *PairOverride) PairSettings {
	if o == nil {
		return s
	}
	if o.MaxSenses != nil {
		s.MaxSenses = *o.MaxSenses
	}
	if o.MaxChars != nil {
		s.MaxChars = *o.MaxChars
	}
	if o.DictExamples != nil {
		s.DictExamples = *o.DictExamples
	}
	if o.Providers != nil {
		s.Providers = *o.Providers
	}
	if o.Profile != nil {
		s.Profile = *o.Profile
	}
	if o.HasSelectors {
		s.Selectors = o.Selectors
		s.HasSelectors = true
	}
	return s
}

// effectivePair resolves the settings for one language pair: the global
// defaults with that pair's [pair:] section folded on top.
//
// This replaces a pair of resolvers that looked the pair up by its *source
// language*, handing nl-de and nl-en the same settings - including which
// providers to call, which is why a pair PONS has no dictionary for could
// take PONS away from a pair it does.
func effectivePair(cfg Config, pair string) PairSettings {
	return applyPairOverride(cfg.Defaults, cfg.Pairs[pair])
}

// LangRules is a [lang:xx] section: how a word in that language is reduced to
// a form the dictionary might actually list.
type LangRules struct {
	Strips       []StripRule // suffix rules
	ReduceDouble bool        // try removing doubled final consonant after strip
	PrefixStrips []string    // prefixes to try removing
}

type StripRule struct {
	Suffix      string // suffix to remove
	Replacement string // what to add instead (empty = just remove)
}

type VocabWord struct {
	Text, VolumeID, DictSuffix, DateCreated string
}

type Translation struct {
	// SourceWord is the word as it appears in the Kobo vocabulary list,
	// lowercased. It becomes the card's stable id (core.ColID), which is
	// what makes a card findable again after its rendered faces change -
	// Word below is display text and can be re-rendered at any time.
	SourceWord string

	Word, Translation, FromLang, ToLang, Source string
}

type KoboDict struct {
	Entries map[string]string
	// Fronts holds a card-front override per word, only populated when
	// DictExamples is on and extractCardEntry found a usable example for
	// that word - same "" = no override contract as Entry.Front.
	Fronts map[string]string
}

// --- Config parsing ---

func loadConfig(path string) Config {
	cfg := Config{
		DictDir:      "dict",
		DBPath:       "KoboReader.sqlite",
		OutDir:       ".",
		Defaults:     defaultPairSettings(),
		LexicalaConf: "APIs/lexicala.json",
		PonsConf:     "APIs/pons.json",
		CardFormat:   DefaultCardFormat,
		CACertPath:   "cacert.pem",
		// Gender and grammar markers - "(der)", "(4e naamval)" - are noise
		// on a flashcard. Register, subject-field and regional markers say
		// something about when the word is used, so they are kept.
		StripLabels: []string{"gen", "gram"},

		ChatGPTConf: "APIs/chatgpt.json",
		// gpt-4o-mini/0.3/400/3 are a cheap, fairly deterministic default -
		// any OpenAI-compatible model id works, see chatgpt_provider.go.
		ChatGPTModel:           "gpt-4o-mini",
		ChatGPTTemperature:     0.1,
		ChatGPTMaxTokens:       400,
		ChatGPTMaxAlternatives: 3,

		Langs: make(map[string]*LangRules),
		Pairs: make(map[string]*PairOverride),
	}

	f, err := os.Open(path)
	if err != nil {
		log.Printf("No config file %s, using defaults", path)
		return cfg
	}
	defer f.Close()

	warn := core.NewConfigWarnings(path)
	defer warn.Summary()

	outWasSet := false
	// Keys before the first section land in a PairOverride too, so the ones
	// valid in both places are read by exactly one switch (applyPairKey)
	// rather than by two that could drift apart.
	var global PairOverride
	var currentLang *LangRules
	var currentPair *PairOverride
	// An unrecognised section header swallows its keys rather than letting
	// them fall through to the global scope, where they would either be
	// applied to every pair or reported as typos they are not.
	skipping := false
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}

		// Section header. Two forms, each naming the narrowest thing its
		// settings depend on: [lang:nl] is how an nl word is reduced to a
		// form worth looking up, [pair:nl-de] is the nl->de dictionary and
		// the cards built from it. A repeated header reopens the same
		// section rather than starting an empty one, which used to discard
		// everything above it without a word.
		if line[0] == '[' && line[len(line)-1] == ']' {
			name := line[1 : len(line)-1]
			currentLang, currentPair, skipping = nil, nil, false
			switch {
			case strings.HasPrefix(name, "lang:"):
				lang := strings.TrimPrefix(name, "lang:")
				if currentLang = cfg.Langs[lang]; currentLang == nil {
					currentLang = &LangRules{}
					cfg.Langs[lang] = currentLang
				}
			case strings.HasPrefix(name, "pair:"):
				pair := strings.TrimPrefix(name, "pair:")
				if currentPair = cfg.Pairs[pair]; currentPair == nil {
					currentPair = &PairOverride{}
					cfg.Pairs[pair] = currentPair
				}
			default:
				warn.UnknownSection(lineNo, name, "[lang:xx] or [pair:xx-yy]")
				skipping = true
			}
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		if skipping {
			// The header already said this section is ignored; repeating it
			// per key would bury the one message that matters.
			continue
		}
		if retiredKey(warn, lineNo, key) {
			continue
		}

		switch {
		case currentLang != nil:
			// A [lang:xx] section: stemming, and nothing else. Anything that
			// describes a dictionary belongs to a pair, since one source
			// language can feed several dictionaries.
			switch key {
			case "strip":
				sr := StripRule{}
				if idx := strings.Index(val, ">"); idx >= 0 {
					sr.Suffix = val[:idx]
					sr.Replacement = val[idx+1:]
				} else {
					sr.Suffix = val
				}
				currentLang.Strips = append(currentLang.Strips, sr)
			case "reduce_double":
				if v, ok := warn.Bool(lineNo, key, val); ok {
					currentLang.ReduceDouble = v
				}
			case "prefix_strip":
				currentLang.PrefixStrips = append(currentLang.PrefixStrips, val)
			default:
				warn.UnknownKey(lineNo, key)
			}

		case currentPair != nil:
			// A [pair:xx-yy] section: the shared keys, then the ones that
			// only mean something for a single dictionary.
			if applyPairKey(currentPair, key, val, lineNo, warn) {
				continue
			}
			if applyProfileKey(currentPair, key, val, lineNo, warn) {
				continue
			}
			warn.UnknownKey(lineNo, key)

		default:
			// Global: the shared keys set the defaults for every pair, and
			// the rest are genuinely app-wide.
			if applyPairKey(&global, key, val, lineNo, warn) {
				continue
			}
			switch key {
			case "dict_dir":
				cfg.DictDir = val
			case "db":
				cfg.DBPath = val
			case "out":
				cfg.OutDir = val
				outWasSet = true
			case "api_fallback":
				// Retired along with the mymemory provider it used to switch
				// on. Still accepted so an old config doesn't trip the
				// unknown-key warning; it no longer does anything.
			case "dict_strip_labels":
				cfg.StripLabels = splitProviders(val)
			case "card_format":
				cfg.CardFormat = val
			case "lexicala_conf":
				cfg.LexicalaConf = val
			case "pons_conf":
				cfg.PonsConf = val
			case "ca_cert":
				cfg.CACertPath = val
			case "chatgpt_conf":
				cfg.ChatGPTConf = val
			case "chatgpt_model":
				cfg.ChatGPTModel = val
			case "chatgpt_temperature":
				if v, ok := warn.Float(lineNo, key, val); ok {
					cfg.ChatGPTTemperature = v
				}
			case "chatgpt_max_tokens":
				if v, ok := warn.Int(lineNo, key, val); ok {
					cfg.ChatGPTMaxTokens = v
				}
			case "chatgpt_max_alternatives":
				if v, ok := warn.Int(lineNo, key, val); ok {
					cfg.ChatGPTMaxAlternatives = v
				}
			default:
				// A selector outside a [pair:] section is the wrong-scope
				// mistake worth naming: it looks like it should work, and it
				// is not a typo, so "unknown setting" would send you hunting
				// for one.
				if profileKeyNames[key] {
					warn.WrongScope(lineNo, key, "inside a [pair:xx-yy] section")
					continue
				}
				if key == "strip" || key == "prefix_strip" || key == "reduce_double" {
					warn.WrongScope(lineNo, key, "inside a [lang:xx] section")
					continue
				}
				warn.UnknownKey(lineNo, key)
			}
		}
	}

	cfg.Defaults = applyPairOverride(cfg.Defaults, &global)

	// Resolve relative paths against config file directory
	absPath, _ := filepath.Abs(path)
	confDir := filepath.Dir(absPath)

	// Where decks go is one decision, but it used to be spelled twice: out=
	// here and data_dir= in anki-core.conf, which the review UIs read. Set
	// only one of them and kobo-vocab happily wrote decks the UIs never
	// looked at, with no error from either side. anki-core.conf is now the
	// single source of truth; out= remains as an override, and disagreeing
	// with data_dir is reported rather than silently honoured.
	coreConfPath := filepath.Join(confDir, "anki-core.conf")
	if _, err := os.Stat(coreConfPath); err == nil {
		dataDir := core.LoadCoreConfig(coreConfPath).DataDir
		switch {
		case !outWasSet:
			cfg.OutDir = dataDir
		case cfg.OutDir != dataDir:
			log.Printf("%s: out=%q disagrees with data_dir=%q in %s - kobo-vocab will write decks "+
				"where the flashcard app is not looking; remove out= to follow anki-core.conf",
				path, cfg.OutDir, dataDir, coreConfPath)
		}
	}
	// DictDir, DBPath, and OutDir are data locations shared with cmd/fbink
	// and cmd/server, which have no confDir concept at all - they always
	// resolve data_dir relative to the process's working directory. They
	// must NOT be joined against confDir here, or kobo-vocab silently
	// starts reading/writing decks (and dictionaries, and the Kobo DB)
	// somewhere the other binaries never look. Only credentials/certs
	// below are genuinely "next to this config file".
	if !filepath.IsAbs(cfg.LexicalaConf) {
		cfg.LexicalaConf = filepath.Join(confDir, cfg.LexicalaConf)
	}
	if !filepath.IsAbs(cfg.PonsConf) {
		cfg.PonsConf = filepath.Join(confDir, cfg.PonsConf)
	}
	if !filepath.IsAbs(cfg.ChatGPTConf) {
		cfg.ChatGPTConf = filepath.Join(confDir, cfg.ChatGPTConf)
	}
	if !filepath.IsAbs(cfg.CACertPath) {
		cfg.CACertPath = filepath.Join(confDir, cfg.CACertPath)
	}

	return cfg
}

// applyPairKey sets one key=value on a PairOverride, reporting whether it
// recognised the key. These are the settings that make sense both globally
// (as a default for every pair) and inside a [pair:] section, so both scopes
// call this and neither can grow a key the other lacks.
func applyPairKey(o *PairOverride, key, val string, lineNo int, warn *core.ConfigWarnings) bool {
	switch key {
	case "max_senses":
		if v, ok := warn.Int(lineNo, key, val); ok {
			o.MaxSenses = &v
		}
	case "max_chars":
		if v, ok := warn.Int(lineNo, key, val); ok {
			o.MaxChars = &v
		}
	case "dict_examples":
		if v, ok := warn.Bool(lineNo, key, val); ok {
			o.DictExamples = &v
		}
	case "providers":
		v := splitProviders(val)
		o.Providers = &v
	default:
		return false
	}
	return true
}

// profileKeyNames is the set applyProfileKey handles, so the global scope can
// tell "you wrote a selector in the wrong place" from "you made a typo".
var profileKeyNames = map[string]bool{
	"profile": true, "sense": true, "translation": true,
	"translation_reject": true, "translation_text": true,
	"example": true, "example_target": true, "article": true,
	"entry": true, "head": true, "drop_tags": true, "split_on": true,
}

// applyProfileKey sets one key=value describing where a dictionary keeps the
// parts of an entry - see dictprofile.go for the shape and selector.go for
// the syntax. Pair-only: a selector describes one dictionary's markup, so
// there is nothing for it to mean globally.
func applyProfileKey(o *PairOverride, key, val string, lineNo int, warn *core.ConfigWarnings) bool {
	if key == "profile" {
		if slices.Contains(profileNames(), val) {
			o.Profile = &val
		} else {
			warn.BadValue(lineNo, key, val, "one of "+strings.Join(profileNames(), ", "))
		}
		return true
	}

	p := &o.Selectors
	// HasSelectors distinguishes "this section describes structure" from
	// "this section only names a base profile". It accumulates over the keys
	// that were understood: a typo further down warns but must not discard
	// the valid selectors above it.
	known := true
	switch key {
	case "sense":
		p.sense = val
	case "translation":
		// A chain, written with "|" between alternatives: one generator can
		// emit more than one entry shape.
		for _, alt := range strings.Split(val, "|") {
			if alt = strings.TrimSpace(alt); alt != "" {
				p.translation = append(p.translation, alt)
			}
		}
	case "translation_reject":
		p.translationDrop = val
	case "translation_text":
		if val == "afterLastBR" {
			p.translationText = val
		} else {
			warn.BadValue(lineNo, key, val, "afterLastBR")
		}
	case "example":
		p.example = val
	case "example_target":
		p.exampleTarget = val
	case "article":
		p.article = val
	case "entry":
		p.entry = val
	case "head":
		p.headAttr = val
	case "drop_tags":
		p.dropTags = append(p.dropTags, splitProviders(val)...)
	case "split_on":
		p.splitOn = val
	default:
		known = false
	}
	o.HasSelectors = o.HasSelectors || known
	return known
}

// retiredKey reports a setting that has been removed, naming what to write
// instead. Both were ways of describing a dictionary from outside the profile
// that describes it, which is why neither could say what it meant without a
// paragraph of caveats about the dictionaries it silently did nothing to.
func retiredKey(warn *core.ConfigWarnings, lineNo int, key string) bool {
	switch key {
	case "translation_tag":
		warn.Removed(lineNo, key, `it was a translation selector, so write it as one: `+
			`translation=<the profile's own chain> | <tag> in a [pair:xx-yy] section`)
		return true
	case "max_depth":
		warn.Removed(lineNo, key, "the raw-block fallback now keeps the first block; "+
			"to choose a different one, write a translation= selector for that [pair:xx-yy]")
		return true
	}
	return false
}

// labelSet turns a dict_strip_labels list into a lookup set, ignoring any
// class name the dict pipeline doesn't emit so a typo can't silently strip
// something unintended.
func labelSet(names []string) map[string]bool {
	known := map[string]bool{"gen": true, "gram": true, "reg": true, "dom": true, "geo": true}
	out := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if known[n] {
			out[n] = true
		} else if n != "" {
			log.Printf("dict_strip_labels: unknown annotation class %q - ignored (want gen, gram, reg, dom or geo)", n)
		}
	}
	return out
}

// splitProviders parses a comma-separated providers= value into a trimmed,
// non-empty list, e.g. "dict, lexicala" -> []string{"dict", "lexicala"}.
func splitProviders(val string) []string {
	var out []string
	for _, p := range strings.Split(val, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// langGroup holds one fromLang->toLang pair's existing CSV state and the
// subset of its vocab words not yet in that CSV or the misses file.
type langGroup struct {
	fromLang, toLang string
	existing         map[string][]string // existing CSV rows keyed by word
	existingRows     [][]string          // existing CSV rows in order
	newWords         []VocabWord
}

// computeGroups reads the Kobo word list, groups it by language pair, and
// diffs each group against its existing CSV/misses file to find words that
// still need a lookup. This is deliberately separate from the actual
// lookup/export loop in main(): it's the fast, dict/network-free part of a
// run, shared between the real run and -count-new (which needs the exact
// same new-word count without paying for any dict loading or network
// calls).
func computeGroups(cfg Config, koboDB *sql.DB) (groups map[string]*langGroup, totalNew int, err error) {
	words, err := readWordList(koboDB)
	if err != nil {
		return nil, 0, fmt.Errorf("read WordList: %w", err)
	}
	for i := range words {
		words[i].Text = stripPunctuation(words[i].Text)
	}
	log.Printf("Found %d words in vocabulary", len(words))

	byLang := make(map[string][]VocabWord)
	for _, w := range words {
		byLang[w.DictSuffix] = append(byLang[w.DictSuffix], w)
	}

	groups = make(map[string]*langGroup)
	for suffix, langWords := range byLang {
		fromLang, toLang := parseLangPair(suffix)
		if fromLang == "" || toLang == "" {
			log.Printf("Skipping unknown dict suffix: %q", suffix)
			continue
		}

		csvPath := filepath.Join(cfg.OutDir, fmt.Sprintf("vocab-%s-%s.csv", fromLang, toLang))
		missesPath := filepath.Join(cfg.OutDir, fmt.Sprintf("vocab-misses-%s-%s.txt", fromLang, toLang))
		seenPath := filepath.Join(cfg.OutDir, fmt.Sprintf("vocab-seen-%s-%s.txt", fromLang, toLang))

		existing, existingRows := readExistingCSV(csvPath)
		misses := readMisses(missesPath)
		// vocab-seen-*.txt is legacy: it exists for decks written before the
		// id column, whose rows can only be matched by their (re-renderable)
		// front text. Rows written since carry the source word in
		// core.ColID, which readExistingCSV keys on directly, so nothing new
		// is added to this file - it is only still read so an older deck
		// doesn't get every word re-added as a duplicate.
		seen := readMisses(seenPath)

		var newWords []VocabWord
		for _, w := range langWords {
			key := strings.ToLower(w.Text)
			if _, ok := existing[key]; ok {
				continue
			}
			if misses[key] {
				continue
			}
			if seen[key] {
				continue
			}
			newWords = append(newWords, w)
		}

		groups[suffix] = &langGroup{fromLang, toLang, existing, existingRows, newWords}
		if len(newWords) > 0 {
			totalNew += len(newWords)
			log.Printf("%s→%s: %d new words to translate", fromLang, toLang, len(newWords))
		}
	}

	return groups, totalNew, nil
}
