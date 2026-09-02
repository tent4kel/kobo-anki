package main

import (
	"bufio"
	"database/sql"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"kobo-anki/core"

	_ "modernc.org/sqlite"
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
			case "auto_parse":
				// Read by start.sh (shell grep), not by this binary - listed
				// here only so it isn't reported as an unknown setting.
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
	if !filepath.IsAbs(cfg.DictDir) {
		cfg.DictDir = filepath.Join(confDir, cfg.DictDir)
	}
	if !filepath.IsAbs(cfg.DBPath) {
		cfg.DBPath = filepath.Join(confDir, cfg.DBPath)
	}
	if !filepath.IsAbs(cfg.OutDir) {
		cfg.OutDir = filepath.Join(confDir, cfg.OutDir)
	}
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

// --- Main ---

func main() {
	confPath := flag.String("conf", "config/anki-mywords.conf", "path to config file")
	dbOverride := flag.String("db", "", "override db path from config")
	createTest := flag.Bool("create-test-db", false, "create a test KoboReader.sqlite and exit")
	testDict := flag.String("test-dict", "", "parse a single dicthtml-*.zip and print entry count + samples, then exit")
	countNew := flag.Bool("count-new", false, "print the number of new words pending lookup, then exit (no dict loading or network calls - for a startup toast)")
	printConfig := flag.Bool("print-config", false, "print the effective settings after defaults and [lang] overrides are applied, then exit")
	flag.Parse()

	if *createTest {
		db := "test.sqlite"
		if *dbOverride != "" {
			db = *dbOverride
		}
		createTestDB(db)
		return
	}

	cfg := loadConfig(*confPath)
	if *dbOverride != "" {
		cfg.DBPath = *dbOverride
	}

	if *testDict != "" {
		testDictFile(cfg, *testDict)
		return
	}

	if *printConfig {
		printEffectiveConfig(cfg)
		return
	}

	log.Printf("Config: dict_dir=%s db=%s providers=%v max_senses=%d max_chars=%d langs=%v",
		cfg.DictDir, cfg.DBPath, cfg.Defaults.Providers, cfg.Defaults.MaxSenses, cfg.Defaults.MaxChars, langList(cfg.Langs))

	// Open Kobo database read-only
	koboDB, err := sql.Open("sqlite", cfg.DBPath+"?mode=ro")
	if err != nil {
		log.Fatalf("Cannot open Kobo DB: %v", err)
	}
	defer koboDB.Close()

	groups, totalNew, err := computeGroups(cfg, koboDB)
	if err != nil {
		log.Fatalf("%v", err)
	}

	if *countNew {
		fmt.Println(totalNew)
		return
	}

	if totalNew == 0 {
		log.Printf("No new words, done")
		return
	}

	// Dictionaries are loaded lazily, per pair, rather than all upfront: the
	// card settings and the profile can both be overridden per [pair:]
	// section, so each dict must be parsed with its own pair's effective
	// settings rather than one global pass over every dicthtml-*.zip.
	dictCache := make(map[string]*KoboDict)
	// Providers other than "dict" are shared across all language groups in
	// this run (one Lexicala credentials load / quota counter, not one per
	// group) - built lazily on first use and cached here.
	providerCache := make(map[string]Provider)
	// card_format is global, not per-[lang] (see Config.CardFormat) -
	// compiled once here rather than per group.
	cardFormat := compileCardFormatOrDefault(cfg.CardFormat)

	for suffix, g := range groups {
		if len(g.newWords) == 0 {
			continue
		}

		log.Printf("Processing %s → %s", g.fromLang, g.toLang)

		rules := cfg.Langs[g.fromLang]
		// The pair, not its source language: nl-de and nl-en are different
		// dictionaries and can want different settings and different
		// providers.
		pair := strings.TrimPrefix(suffix, "-")
		set := effectivePair(cfg, pair)

		var dict *KoboDict
		if slices.Contains(set.Providers, "dict") {
			dict = dictCache[suffix]
			if dict == nil {
				path := filepath.Join(cfg.DictDir, "dicthtml"+suffix+".zip")
				prof, origin := resolveDictProfile(set, path)
				log.Printf("  dictionary profile: %s (%s)", prof.name, origin)
				render := dictRender{maxSenses: set.MaxSenses, maxChars: set.MaxChars, asCard: set.DictExamples, cardFormat: cardFormat}
				if parsed, err := parseDict(path, compileProfile(prof, cfg.StripLabels), render); err != nil {
					log.Printf("No dictionary for suffix %s (dicthtml%s.zip): %v", suffix, suffix, err)
				} else {
					if len(parsed.Entries) == 0 {
						log.Printf("WARNING: dicthtml%s.zip parsed with 0 entries - dictionary format may be unsupported", suffix)
					} else {
						log.Printf("Loaded dicthtml%s.zip: %d entries (max_senses=%d max_chars=%d dict_examples=%v)",
							suffix, len(parsed.Entries), set.MaxSenses, set.MaxChars, set.DictExamples)
					}
					dict = parsed
					dictCache[suffix] = dict
				}
			}
		}

		chain := buildChain(cfg, set.Providers, NewDictProvider(dict, rules), providerCache)

		var newTranslations []Translation
		var newMisses []string

		for _, w := range g.newWords {
			entry, err := chain.Lookup(w.Text, g.fromLang, g.toLang)
			if err != nil {
				newMisses = append(newMisses, strings.ToLower(w.Text))
				fmt.Printf("  [miss]       %s\n", w.Text)
				continue
			}

			front := w.Text
			if entry.Front != "" {
				front = entry.Front
			}
			def := entry.Definition
			if entry.Example != "" {
				def += "\n\n*" + entry.Example + "*"
			}
			t := Translation{
				SourceWord:  strings.ToLower(w.Text),
				Word:        front,
				Translation: def,
				FromLang:    g.fromLang,
				ToLang:      g.toLang,
				Source:      entry.Source,
			}
			newTranslations = append(newTranslations, t)
			fmt.Printf("  [%-10s] %s → %s\n", entry.Source, w.Text, entry.Definition)
		}

		// Export: existing rows + new translations (skip if there's nothing to write)
		if len(g.existingRows) > 0 || len(newTranslations) > 0 {
			csvPath := filepath.Join(cfg.OutDir, fmt.Sprintf("vocab-%s-%s.csv", g.fromLang, g.toLang))
			if err := exportCSV(csvPath, g.existingRows, newTranslations); err != nil {
				log.Printf("Failed to write %s: %v", csvPath, err)
			} else {
				log.Printf("Wrote %s (%d existing + %d new)", csvPath, len(g.existingRows), len(newTranslations))
			}
		}

		// Save misses
		if len(newMisses) > 0 {
			missesPath := filepath.Join(cfg.OutDir, fmt.Sprintf("vocab-misses-%s-%s.txt", g.fromLang, g.toLang))
			appendMisses(missesPath, newMisses)
		}
	}
}

// printEffectiveConfig dumps the settings actually in force - after
// defaults, the config file, and each section's overrides have been merged -
// so "did my edit take effect?" can be answered without deploying and
// watching the log. Companion to the warnings emitted while parsing: those
// say what was rejected, this says what won.
func printEffectiveConfig(cfg Config) {
	fmt.Printf("dict_dir      %s\n", cfg.DictDir)
	fmt.Printf("db            %s\n", cfg.DBPath)
	fmt.Printf("out           %s\n", cfg.OutDir)
	fmt.Printf("ca_cert       %s\n", cfg.CACertPath)
	fmt.Printf("card_format   %q\n", cfg.CardFormat)
	fmt.Printf("strip_labels  %v\n", cfg.StripLabels)
	fmt.Printf("chatgpt       model=%s temperature=%v max_tokens=%d max_alternatives=%d conf=%s\n",
		cfg.ChatGPTModel, cfg.ChatGPTTemperature, cfg.ChatGPTMaxTokens, cfg.ChatGPTMaxAlternatives, cfg.ChatGPTConf)
	fmt.Printf("lexicala      conf=%s\n", cfg.LexicalaConf)
	fmt.Printf("pons          conf=%s\n", cfg.PonsConf)

	d := cfg.Defaults
	fmt.Printf("\ndefaults for any pair: max_senses=%d max_chars=%d dict_examples=%v providers=%s\n",
		d.MaxSenses, d.MaxChars, d.DictExamples, strings.Join(d.Providers, ","))

	langs := langList(cfg.Langs)
	if len(langs) > 0 {
		fmt.Printf("\n%-8s %-8s %-9s %s\n", "lang", "strips", "prefixes", "reduce_double")
		for _, lang := range langs {
			r := cfg.Langs[lang]
			fmt.Printf("%-8s %-8d %-9d %v\n", lang, len(r.Strips), len(r.PrefixStrips), r.ReduceDouble)
		}
	}

	printPairs(cfg)
}

// printPairs lists what each language pair resolves to. This is per pair
// rather than per source language because that is the granularity the
// settings actually have: nl-de and nl-en are different dictionaries, and
// the table could not previously show them differing even when they did.
//
// The profile is usually detected rather than written down, so without this
// the only way to learn which one is in force is to read the log of a real
// run - or to notice the cards coming out wrong. Resolving means opening the
// zips, which is why this comes last.
func printPairs(cfg Config) {
	pairs := dictPairs(cfg)
	if len(pairs) == 0 {
		return
	}
	fmt.Printf("\n%-8s %-9s %-22s %-10s %-9s %-13s %-24s %s\n",
		"pair", "profile", "origin", "max_senses", "max_chars", "dict_examples", "providers", "file")
	for _, pair := range pairs {
		s := effectivePair(cfg, pair)
		path := filepath.Join(cfg.DictDir, "dicthtml-"+pair+".zip")
		profName, origin, file := "?", "not resolved", "missing: "+path
		if _, err := os.Stat(path); err == nil {
			prof, o := resolveDictProfile(s, path)
			profName, origin, file = prof.name, o, filepath.Base(path)
		}
		fmt.Printf("%-8s %-9s %-22s %-10d %-9d %-13v %-24s %s\n",
			pair, profName, origin, s.MaxSenses, s.MaxChars, s.DictExamples,
			strings.Join(s.Providers, ","), file)
	}
}

// dictPairs is every language pair worth reporting on: the dictionaries
// present in dict_dir, plus any pair with a [pair:] section, so a section
// naming a pair with no dictionary installed still shows up (as a typo
// usually would).
func dictPairs(cfg Config) []string {
	seen := map[string]bool{}
	var pairs []string
	matches, _ := filepath.Glob(filepath.Join(cfg.DictDir, "dicthtml-*.zip"))
	for _, m := range matches {
		pair := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "dicthtml-"), ".zip")
		seen[pair] = true
		pairs = append(pairs, pair)
	}
	for pair := range cfg.Pairs {
		if !seen[pair] {
			pairs = append(pairs, pair)
		}
	}
	slices.Sort(pairs)
	return pairs
}

// langList is sorted, so two runs of -print-config can be diffed.
func langList(m map[string]*LangRules) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// buildChain resolves a providers= name list (e.g. []string{"dict",
// "lexicala"}) into an actual Chain. "dict" always resolves to the
// group-specific dictProvider passed in; any other name is looked up in
// cache, constructing it on first use. A provider that fails to construct
// (e.g. lexicala with no APIs/lexicala.json) logs a warning and is left out
// of the chain rather than aborting the run - the remaining providers still
// run.
func buildChain(cfg Config, names []string, dictProvider Provider, cache map[string]Provider) Chain {
	var providers []Provider
	for _, name := range names {
		if name == "dict" {
			providers = append(providers, dictProvider)
			continue
		}
		if p, ok := cache[name]; ok {
			if p != nil {
				providers = append(providers, p)
			}
			continue
		}
		p := newNamedProvider(cfg, name)
		cache[name] = p
		if p != nil {
			providers = append(providers, p)
		}
	}
	return NewChain(providers...)
}

// networkFailureThreshold is how many consecutive hard failures a
// network-backed provider tolerates in one run before CircuitBreaker gives
// up on it - see provider.go. Not user-configurable: it's a robustness
// bound, not a tuning knob anyone should need to reach for.
const networkFailureThreshold = 3

func newNamedProvider(cfg Config, name string) Provider {
	switch name {
	case "lexicala":
		p, err := NewLexicalaProvider(cfg.LexicalaConf, compileCardFormatOrDefault(cfg.CardFormat), cfg.CACertPath)
		if err != nil {
			log.Printf("Provider %q unavailable (%v), skipping", name, err)
			return nil
		}
		return NewCircuitBreaker(p, networkFailureThreshold)
	case "pons":
		p, err := NewPonsProvider(cfg.PonsConf, compileCardFormatOrDefault(cfg.CardFormat), cfg.CACertPath)
		if err != nil {
			log.Printf("Provider %q unavailable (%v), skipping", name, err)
			return nil
		}
		return NewCircuitBreaker(p, networkFailureThreshold)
	case "chatgpt":
		p, err := NewChatGPTProvider(cfg.ChatGPTConf, cfg.ChatGPTModel, cfg.ChatGPTTemperature, cfg.ChatGPTMaxTokens, cfg.ChatGPTMaxAlternatives, compileCardFormatOrDefault(cfg.CardFormat), cfg.CACertPath)
		if err != nil {
			log.Printf("Provider %q unavailable (%v), skipping", name, err)
			return nil
		}
		return NewCircuitBreaker(p, networkFailureThreshold)
	default:
		log.Printf("Unknown provider %q in config, skipping", name)
		return nil
	}
}

// --- Stemming (config-driven) ---

func stemLookup(dict *KoboDict, word string, rules *LangRules) (definition, stem string) {
	w := strings.ToLower(word)
	candidates := generateStems(w, rules)
	for _, c := range candidates {
		if def, ok := dict.Entries[c]; ok {
			return def, c
		}
	}
	return "", ""
}

func generateStems(word string, rules *LangRules) []string {
	seen := make(map[string]bool)
	var stems []string
	add := func(s string) {
		if s != "" && s != word && len(s) >= 2 && !seen[s] {
			seen[s] = true
			stems = append(stems, s)
		}
	}

	// Apply replacement rules first (more specific), then plain strips
	// This ensures "redt" tries "redden" before "red"
	for _, rule := range rules.Strips {
		if rule.Replacement == "" || !strings.HasSuffix(word, rule.Suffix) {
			continue
		}
		base := word[:len(word)-len(rule.Suffix)] + rule.Replacement
		add(base)
		if rules.ReduceDouble && len(base) >= 3 && base[len(base)-1] == base[len(base)-2] {
			add(base[:len(base)-1])
		}
	}
	for _, rule := range rules.Strips {
		if rule.Replacement != "" || !strings.HasSuffix(word, rule.Suffix) {
			continue
		}
		base := word[:len(word)-len(rule.Suffix)]
		add(base)
		if rules.ReduceDouble && len(base) >= 3 && base[len(base)-1] == base[len(base)-2] {
			add(base[:len(base)-1])
		}
	}

	// Apply prefix strip rules (combined with suffix rules)
	for _, prefix := range rules.PrefixStrips {
		if !strings.HasPrefix(word, prefix) || len(word) <= len(prefix)+2 {
			continue
		}
		stripped := word[len(prefix):]
		add(stripped)
		// Also try suffix rules on the prefix-stripped form
		for _, rule := range rules.Strips {
			if strings.HasSuffix(stripped, rule.Suffix) {
				base := stripped[:len(stripped)-len(rule.Suffix)] + rule.Replacement
				add(base)
			}
		}
	}

	return stems
}

// --- Kobo dictionary loading ---
//
// Kobo dicthtml zips come in (at least) two entry layouts:
//
//   - Flat (older dictgen / stock Kobo dicts):
//     <w><a name="Aagje"/><div><b>Aagje</b><br/>noun<br>female first name</div></w>
//
//   - Nested (newer dictgen output, e.g. from custom StarDict/.df conversions):
//     <w><p><a name="a" /><b>a</b></p><var></var><ol>
//       <li><div>first sense...</div></li>
//       <li><div>second sense...</div></li>
//     </ol></w>
//
// extractEntry below handles both by branching on whether <li> sense blocks
// are present, rather than assuming one fixed structure.
//
// Note: this format is NOT well-formed HTML5 - <a name="x" /> is written as
// self-closing but <a> is not a void element per the HTML5 spec, so a real
// spec-compliant parser (tried: goquery/golang.org/x/net/html) leaves it open
// and reparents unrelated later entries into it via the adoption agency
// algorithm, silently destroying ~99% of entries. Regex-based extraction is
// deliberate here, not an oversight.

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)
var whitespaceRe = regexp.MustCompile(`\s+`)

var inlineTagRe = regexp.MustCompile(`(?i)<b>|</b>|<i>|</i>|<br\s*/?>`)
var mergeItalicRe = regexp.MustCompile(`(?i)</i>(\s*)<i>`)
var mergeBoldRe = regexp.MustCompile(`(?i)</b>(\s*)<b>`)

// brSentinel stands in for a <br> line break through the tag-strip and
// whitespace-collapse steps in cleanText (a literal '\n' would just get
// collapsed away by whitespaceRe), then gets restored to '\n' at the end.
const brSentinel = "\x00BR\x00"

// testDictFile parses a single dict zip and prints diagnostics, for checking
// whether a dictionary is supported before deploying it to the Kobo. It
// leads with the profile and where it came from, since that is the setting
// that decides whether anything below it makes sense - and prints it in
// [pair:] syntax, ready to paste into the config if it needs adjusting.
func testDictFile(cfg Config, path string) {
	pair := ""
	if base := filepath.Base(path); strings.HasPrefix(base, "dicthtml-") {
		pair = strings.TrimSuffix(strings.TrimPrefix(base, "dicthtml-"), ".zip")
	}
	// Resolve through this pair's own [pair:] section, so it shows what a
	// real run produces rather than what the global defaults alone would.
	set := effectivePair(cfg, pair)
	prof, origin := resolveDictProfile(set, path)

	render := dictRender{
		maxSenses: set.MaxSenses, maxChars: set.MaxChars,
		asCard: set.DictExamples, cardFormat: compileCardFormatOrDefault(cfg.CardFormat),
	}
	dict, err := parseDict(path, compileProfile(prof, cfg.StripLabels), render)
	if err != nil {
		log.Fatalf("Failed to parse %s: %v", path, err)
	}

	fmt.Printf("%s: %d entries\n", path, len(dict.Entries))
	fmt.Printf("profile: %s (%s)\n", prof.name, origin)
	if pair != "" {
		fmt.Printf("  [pair:%s]\n  profile=%s\n", pair, prof.name)
		for _, line := range describeProfile(prof) {
			fmt.Printf("  %s\n", line)
		}
	}
	if len(dict.Entries) == 0 {
		fmt.Println("No entries parsed - the profile does not match this dictionary's structure.")
		return
	}
	fmt.Println()
	n := 0
	for word, def := range dict.Entries {
		if front, ok := dict.Fronts[word]; ok {
			fmt.Printf("  %s -> front=%q back=%q\n", word, front, def)
		} else {
			fmt.Printf("  %s -> %s\n", word, def)
		}
		n++
		if n >= 10 {
			break
		}
	}
}

// cleanPlainText strips all tags to bare text with no Markdown conversion,
// unlike cleanText - for a card's example halves, which get their own outer
// *italic* wrapping from formatCard. Converting an inner
// "<i>(note)</i>" to markdown too, as cleanText would, nests emphasis
// markers into broken output like "**(ook)* text*" - verified against
// "verschrikkelijk"'s second sense, whose German half opens with a bare
// "<i>(ook)</i>" usage note.
func cleanPlainText(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, " ", " ")
	s = whitespaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func cleanText(s string) string {
	s = toMarkdown(s)
	s = htmlTagRe.ReplaceAllString(s, " ")
	// Unescape entities (&amp; &lt; &nbsp; &#233; ...) after tags are gone,
	// so an entity that decodes to '<' or '>' can't be mistaken for a tag.
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ") // &nbsp; decodes to NBSP, not ASCII space
	s = whitespaceRe.ReplaceAllString(s, " ")
	// Restore <br> line breaks after whitespace collapse (must happen last,
	// or whitespaceRe would have collapsed a literal '\n' back into a space).
	s = strings.ReplaceAll(s, " "+brSentinel, brSentinel)
	s = strings.ReplaceAll(s, brSentinel+" ", brSentinel)
	s = strings.ReplaceAll(s, brSentinel, "\n")
	return strings.TrimSpace(s)
}

// toMarkdown converts the small set of inline tags dictionary entries
// actually use into Markdown markers, before htmlTagRe strips everything
// else. <b>/<i> become **/*, handling two real-world quirks of this source:
//
//   - Adjacent tag boundaries: "</b><i>...</i>" would naively produce an
//     ambiguous run of literal asterisks ("**word***...") that CommonMark
//     can't parse the way we intend - a zero-width space is inserted
//     whenever a new marker would land directly against an existing one.
//   - Whitespace hugging a tag boundary: spans like "<i> (die)</i>" put a
//     leading space *inside* the tag. CommonMark requires an opening
//     delimiter not be followed by whitespace (and a closing one not be
//     preceded by it), so naively emitting "* (die)*" fails to parse as
//     emphasis at all. Hugging whitespace is relocated outside the marker.
//   - Adjacent same-type spans: this source often wraps runs of text as many
//     separate single-word/punctuation <i>...</i> pairs in a row (e.g.
//     "<i> (</i><i>die</i><i>;</i>"). Converting each independently produces
//     a long run of single-'*' spans that CommonMark's emphasis resolution
//     can pair up wrong (nesting instead of a flat sequence). Adjacent
//     same-type tag pairs are merged into one span first, which changes
//     nothing about what text ends up styled, only how many span
//     boundaries there are to convert.
//
// Known accepted limitation: a single '*' marker landing directly against
// punctuation/digits with no space (e.g. "CO<i>2</i>-neutraal") can still
// occasionally fail CommonMark's flanking rules and render as literal
// asterisks - verified against a full real dictionary (102,272 entries)
// at ~0.005% of entries, always as a few stray visible '*' characters in
// otherwise-readable text, never lost content or a parse error. Not worth
// reimplementing CommonMark's delimiter-run resolution to close that gap.
func toMarkdown(s string) string {
	s = mergeItalicRe.ReplaceAllString(s, "$1")
	s = mergeBoldRe.ReplaceAllString(s, "$1")

	out := ""
	last := 0
	for _, m := range inlineTagRe.FindAllStringIndex(s, -1) {
		if m[0] > last {
			out += s[last:m[0]]
		}
		tag := strings.ToLower(s[m[0]:m[1]])
		last = m[1]

		var marker string
		var isOpen bool
		switch tag {
		case "<b>":
			marker, isOpen = "**", true
		case "</b>":
			marker, isOpen = "**", false
		case "<i>":
			marker, isOpen = "*", true
		case "</i>":
			marker, isOpen = "*", false
		default: // <br>, <br/>, <br />
			out += brSentinel
			continue
		}

		if isOpen {
			skip := 0
			for last+skip < len(s) && isMDSpace(s[last+skip]) {
				skip++
			}
			out += s[last : last+skip] // whitespace moves before the marker
			last += skip
			if strings.HasSuffix(out, "*") {
				out += "\u200b"
			}
			out += marker
		} else {
			trimmed := strings.TrimRight(out, " \t\n")
			ws := out[len(trimmed):] // whitespace moves after the marker
			out = trimmed
			if strings.HasSuffix(out, "*") {
				out += "\u200b"
			}
			out += marker + ws
		}
	}
	out += s[last:]
	return out
}

func isMDSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' }

// truncateMarkdownVisible truncates s to roughly maxChars visible (non-'*')
// characters, extending the cut to the next word boundary with a balanced
// '*' count. kobo-vocab only ever emits well-formed, self-paired ** and *
// markers, so an even running count of '*' runes means we're not currently
// inside an open emphasis span - a global counter is enough, no need for
// full CommonMark delimiter-run resolution. This guarantees a cut never
// leaves a dangling emphasis marker or an empty trailing "-" list item.
func truncateMarkdownVisible(s string, maxChars int) string {
	if maxChars <= 0 {
		return s
	}

	runes := []rune(s)
	visible, stars, cut := 0, 0, -1

	for i, r := range runes {
		if r == '*' {
			stars++
		} else {
			visible++
		}
		atBoundary := i+1 == len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n'
		if visible >= maxChars && stars%2 == 0 && atBoundary {
			cut = i + 1
			break
		}
	}
	if cut < 0 || cut >= len(runes) {
		return s
	}

	result := strings.TrimRight(string(runes[:cut]), " \n")
	if idx := strings.LastIndexByte(result, '\n'); idx >= 0 {
		if strings.TrimSpace(result[idx+1:]) == "-" {
			result = strings.TrimRight(result[:idx], " \n")
		}
	} else if strings.TrimSpace(result) == "-" {
		result = ""
	}
	return result
}

// --- Text cleanup ---

func stripPunctuation(word string) string {
	return strings.TrimRight(strings.TrimLeft(word, "\"'([{¿¡"), ".,;:!?\"')]}…")
}

// --- Kobo database ---

func readWordList(db *sql.DB) ([]VocabWord, error) {
	rows, err := db.Query(`SELECT Text, VolumeId, DictSuffix, DateCreated FROM WordList ORDER BY DateCreated`)
	if err != nil {
		return nil, fmt.Errorf("query WordList: %w", err)
	}
	defer rows.Close()

	var words []VocabWord
	for rows.Next() {
		var w VocabWord
		if err := rows.Scan(&w.Text, &w.VolumeID, &w.DictSuffix, &w.DateCreated); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		words = append(words, w)
	}
	return words, rows.Err()
}

func parseLangPair(dictSuffix string) (from, to string) {
	s := strings.TrimPrefix(dictSuffix, "-")
	parts := strings.SplitN(s, "-", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

// --- Word-list tracking (misses and seen words) ---
//
// Both a "miss" (no provider found anything) and a "seen" word (a real
// translation was produced) are, on disk, just a plain lowercased
// word-per-line file - readMisses/appendMisses aren't miss-specific despite
// the name, and are reused as-is for vocab-seen-*.txt too.

func readMisses(path string) map[string]bool {
	result := make(map[string]bool)
	f, err := os.Open(path)
	if err != nil {
		return result
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		word := strings.TrimSpace(scanner.Text())
		if word != "" {
			result[word] = true
		}
	}
	return result
}

func appendMisses(path string, words []string) {
	os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to write misses: %v", err)
		return
	}
	defer f.Close()
	for _, w := range words {
		fmt.Fprintln(f, w)
	}
}

// --- CSV export ---

// exportCSV rewrites a deck in full (existing rows plus newly translated
// ones). Written atomically for the same reason SaveCards is: this replaces
// a deck that may already carry review history, and a half-written file
// would lose it.
func exportCSV(path string, existingRows [][]string, newTranslations []Translation) error {
	return core.WriteFileAtomic(path, 0644, func(f io.Writer) error {
		w, err := core.NewSpreadsheetCSVWriter(f)
		if err != nil {
			return err
		}
		w.Write(core.DeckHeader)

		// Preserve existing rows (already widened by readExistingCSV)
		for _, row := range existingRows {
			w.Write(row)
		}

		for _, t := range newTranslations {
			w.Write(core.NewCardRow(t.SourceWord, t.Word, t.Translation))
		}

		w.Flush()
		return w.Error()
	})
}

// readExistingCSV returns a deck's rows, and an index of them keyed by the
// word each card came from.
//
// The key is the row's id column when it has one - the source word, written
// by exportCSV - and falls back to its front text otherwise, which is all a
// deck written before that column (or a hand-written one) can offer. The
// fallback is why vocab-seen-*.txt is still consulted in computeGroups: for
// those older rows the front may since have been re-rendered into something
// that no longer resembles the word at all.
func readExistingCSV(path string) (map[string][]string, [][]string) {
	byWord := make(map[string][]string)
	var rows [][]string

	f, err := os.Open(path)
	if err != nil {
		return byWord, rows
	}
	defer f.Close()

	allRows, err := core.NewSpreadsheetCSVReader(f).ReadAll()
	if err != nil {
		return byWord, rows
	}
	for i, row := range allRows {
		if i == 0 {
			continue // header
		}
		normalized, ok := core.NormalizeRow(row)
		if !ok {
			continue
		}
		key := normalized[core.ColID]
		if key == "" {
			key = normalized[core.ColFront]
		}
		byWord[strings.ToLower(key)] = normalized
		rows = append(rows, normalized)
	}
	return byWord, rows
}

// --- Test DB ---

func createTestDB(path string) {
	os.Remove(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	db.Exec(`CREATE TABLE WordList (Text TEXT, VolumeId TEXT, DictSuffix TEXT, DateCreated TEXT)`)
	for _, w := range []struct{ t, v, d, c string }{
		{"behoefte", "1939166523", "-nl-en", "2025-12-03T07:32:30Z"},
		{"besmettelijk", "1973941266", "-nl-en", "2026-02-07T14:06:17Z"},
		{"felle", "1934193436", "-nl-en", "2025-11-22T16:59:33Z"},
		{"kwaad", "1934193436", "-nl-en", "2025-11-22T17:00:17Z"},
		{"leek", "1934639860", "-nl-en", "2025-11-23T12:05:15Z"},
		{"letten", "1936121537", "-nl-en", "2025-11-26T19:56:27Z"},
		{"onafhankelijk", "1937667690", "-nl-en", "2025-11-28T23:59:59Z"},
		{"ongemakkelijk", "1940692249", "-nl-en", "2025-12-08T22:03:32Z"},
		{"raakt", "1936121537", "-nl-en", "2025-11-26T19:47:49Z"},
		{"ramp", "1967258575", "-nl-en", "2026-01-31T18:42:36Z"},
		{"redt", "1936121537", "-nl-en", "2025-11-26T19:45:43Z"},
		{"vaardigheid", "1936121537", "-nl-en", "2025-11-26T19:49:42Z"},
		{"verschuift", "1936121537", "-nl-en", "2025-11-26T19:49:14Z"},
		{"voorwerpen", "1933028413", "-nl-en", "2025-11-20T21:54:29Z"},
		{"zoals", "1973941266", "-nl-en", "2026-02-07T14:07:14Z"},
	} {
		db.Exec(`INSERT INTO WordList VALUES(?,?,?,?)`, w.t, w.v, w.d, w.c)
	}
	log.Printf("Created test DB %s", path)
}
