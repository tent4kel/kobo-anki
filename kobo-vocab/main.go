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
	DictDir        string
	DBPath         string
	OutDir         string
	MaxSenses      int      // max senses (<li> entries) to include per word; 0 = unlimited
	MaxDepth       int      // max direct-child <div> blocks to keep per sense (drops examples etc.); 0 = unlimited
	MaxChars       int      // max characters of definition text per word; 0 = unlimited
	TranslationTag string   // if set (e.g. "b"), nested-format entries keep only this tag's spans, comma-joined; "" = keep full formatted text. No effect on flat-format entries - see extractEntry.
	StripLabels    []string // annotation classes (gen/gram/reg/dom/geo) to remove from card text - see dict_strip_labels and CLASSES.md in the dict source
	DictExamples   bool     // if true, nested-format entries try to build a ChatGPT/Lexicala-style card (bold headword + alternatives + one example, on both Front and Definition) from the dict's own example phrases. Verified against dicthtml-nl-de.zip only - see extractCardEntry. No effect without TranslationTag also set, and no effect on flat-format entries.
	Providers      []string // content providers to try in order, e.g. []string{"dict", "lexicala"} - see provider.go
	LexicalaConf   string   // path to Lexicala credentials JSON (base_url/token), only read if "lexicala" is in Providers
	PonsConf       string   // path to PONS credentials JSON (base_url/token), only read if "pons" is in Providers
	CACertPath     string   // path to a PEM CA bundle, appended to the system trust store for every network provider's TLS verification - see newHTTPClient. Needed on Kobo firmware that ships no CA bundle at all.

	// CardFormat is the card_format template (see parseCardTemplate in
	// cardtemplate.go) shared by dict (when DictExamples is on), lexicala,
	// and chatgpt - global only, no per-[lang] override: lexicala/chatgpt
	// are built once and cached across all language groups in a run (see
	// providerCache in main()), so a per-language override would silently
	// only apply to whichever group happened to build them first.
	CardFormat string

	// ChatGPT provider settings - all "variables to build the request" live
	// here rather than in APIs/chatgpt.json, which holds only credentials
	// (base_url/token), mirroring the Lexicala split. Only read if
	// "chatgpt" is in Providers - see chatgpt_provider.go.
	ChatGPTConf            string
	ChatGPTModel           string
	ChatGPTTemperature     float64
	ChatGPTMaxTokens       int
	ChatGPTMaxAlternatives int

	Langs map[string]*LangRules

	// Dicts holds one entry per [dict:xx-yy] section, keyed by language pair
	// ("nl-de"). A dictionary's structure is a property of the pair, not of
	// the source language: nl-de and nl-en can come from entirely different
	// generators, which a [nl] section has no way to express - it is why
	// translation_tag had to carry "has no effect on flat dictionaries" as a
	// caveat rather than simply not applying.
	Dicts map[string]*DictSection
}

// DictSection is a parsed [dict:xx-yy] section: a base profile by name, plus
// any selectors given inline. Naming a base and overriding one selector is
// allowed, which is usually all a slightly-off dictionary needs.
type DictSection struct {
	Base      string // "", "auto", or a name from builtinProfiles
	Overrides dictProfile
	HasFields bool
}

type LangRules struct {
	Strips       []StripRule // suffix rules
	ReduceDouble bool        // try removing doubled final consonant after strip
	PrefixStrips []string    // prefixes to try removing

	// Optional per-language overrides of the global max_senses/max_depth/
	// max_chars/translation_tag/dict_examples defaults. Pointers so "not set
	// in this [lang] section" (nil, inherit the global default) is
	// distinguishable from an explicit zero/"unlimited" (or, for
	// TranslationTag/DictExamples, explicitly turning it back off).
	MaxSenses      *int
	MaxDepth       *int
	MaxChars       *int
	TranslationTag *string
	DictExamples   *bool
	Providers      *[]string
}

// effectiveLimits resolves max_senses/max_depth/max_chars/translation_tag/
// dict_examples for a language, preferring that language's [lang] section
// override (if set) over the global default - e.g. a language with terser
// dict glosses might want a higher max_chars than one whose entries are
// typically more verbose.
func effectiveLimits(cfg Config, rules *LangRules) (maxSenses, maxDepth, maxChars int, translationTag string, dictExamples bool) {
	maxSenses, maxDepth, maxChars = cfg.MaxSenses, cfg.MaxDepth, cfg.MaxChars
	translationTag = cfg.TranslationTag
	dictExamples = cfg.DictExamples
	if rules == nil {
		return
	}
	if rules.MaxSenses != nil {
		maxSenses = *rules.MaxSenses
	}
	if rules.MaxDepth != nil {
		maxDepth = *rules.MaxDepth
	}
	if rules.MaxChars != nil {
		maxChars = *rules.MaxChars
	}
	if rules.TranslationTag != nil {
		translationTag = *rules.TranslationTag
	}
	if rules.DictExamples != nil {
		dictExamples = *rules.DictExamples
	}
	return
}

// effectiveProviders resolves the ordered list of content providers to try
// for a language, preferring that language's [lang] section override (if
// set) over the global default - same override pattern as effectiveLimits,
// kept separate since "which providers, in what order" is an orthogonal
// concern from the dict-format formatting knobs above.
func effectiveProviders(cfg Config, rules *LangRules) []string {
	if rules != nil && rules.Providers != nil {
		return *rules.Providers
	}
	return cfg.Providers
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
		MaxSenses:    1,
		MaxDepth:     1,
		MaxChars:     150,
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
		Dicts: make(map[string]*DictSection),
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
	var currentLang *LangRules
	var currentDict *DictSection
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}

		// Section header: [nl] for a language, [dict:nl-de] for a dictionary.
		if line[0] == '[' && line[len(line)-1] == ']' {
			name := line[1 : len(line)-1]
			currentLang, currentDict = nil, nil
			if pair, ok := strings.CutPrefix(name, "dict:"); ok {
				currentDict = &DictSection{}
				cfg.Dicts[pair] = currentDict
			} else {
				currentLang = &LangRules{}
				cfg.Langs[name] = currentLang
			}
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		if currentDict != nil {
			// Inside a [dict:xx-yy] section. Every key here is a selector
			// describing where this dictionary keeps something; see
			// dictprofile.go for the shape and selector.go for the syntax.
			p := &currentDict.Overrides
			if key == "profile" {
				if slices.Contains(profileNames(), val) {
					currentDict.Base = val
				} else {
					warn.BadValue(lineNo, key, val, "one of "+strings.Join(profileNames(), ", "))
				}
				continue
			}
			// HasFields distinguishes "this section describes structure" from
			// "this section only names a base profile". It accumulates over
			// the keys that were understood: a typo further down warns but
			// must not discard the valid selectors above it.
			known := true
			switch key {
			case "sense":
				p.sense = val
			case "translation":
				// A chain, written with "|" between alternatives: one
				// generator can emit more than one entry shape.
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
				warn.UnknownKey(lineNo, key)
			}
			currentDict.HasFields = currentDict.HasFields || known
			continue
		}

		if currentLang != nil {
			// Inside a language section
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
			case "max_senses":
				if v, ok := warn.Int(lineNo, key, val); ok {
					currentLang.MaxSenses = &v
				}
			case "max_depth":
				if v, ok := warn.Int(lineNo, key, val); ok {
					currentLang.MaxDepth = &v
				}
			case "max_chars":
				if v, ok := warn.Int(lineNo, key, val); ok {
					currentLang.MaxChars = &v
				}
			case "translation_tag":
				v := val
				currentLang.TranslationTag = &v
			case "dict_examples":
				if v, ok := warn.Bool(lineNo, key, val); ok {
					currentLang.DictExamples = &v
				}
			case "providers":
				v := splitProviders(val)
				currentLang.Providers = &v
			default:
				warn.UnknownKey(lineNo, key)
			}
		} else {
			// Global settings
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
			case "max_senses":
				if v, ok := warn.Int(lineNo, key, val); ok {
					cfg.MaxSenses = v
				}
			case "max_depth":
				if v, ok := warn.Int(lineNo, key, val); ok {
					cfg.MaxDepth = v
				}
			case "max_chars":
				if v, ok := warn.Int(lineNo, key, val); ok {
					cfg.MaxChars = v
				}
			case "translation_tag":
				cfg.TranslationTag = val
			case "dict_examples":
				if v, ok := warn.Bool(lineNo, key, val); ok {
					cfg.DictExamples = v
				}
			case "dict_strip_labels":
				cfg.StripLabels = splitProviders(val)
			case "card_format":
				cfg.CardFormat = val
			case "providers":
				cfg.Providers = splitProviders(val)
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
				warn.UnknownKey(lineNo, key)
			}
		}
	}

	// providers= not set at all: local dict only, which is the one provider
	// that needs no credentials and no network.
	if cfg.Providers == nil {
		cfg.Providers = []string{"dict"}
	}

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
	confPath := flag.String("conf", "anki-mywords.conf", "path to config file")
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

	log.Printf("Config: dict_dir=%s db=%s providers=%v max_senses=%d max_depth=%d max_chars=%d langs=%v",
		cfg.DictDir, cfg.DBPath, cfg.Providers, cfg.MaxSenses, cfg.MaxDepth, cfg.MaxChars, langList(cfg.Langs))

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

	// Dictionaries are loaded lazily, per language group, rather than all
	// upfront: max_senses/max_depth/max_chars can be overridden per [lang]
	// section, so each dict must be parsed with its own group's effective
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
		maxSenses, maxDepth, maxChars, translationTag, dictExamples := effectiveLimits(cfg, rules)
		providerNames := effectiveProviders(cfg, rules)

		var dict *KoboDict
		if slices.Contains(providerNames, "dict") {
			dict = dictCache[suffix]
			if dict == nil {
				path := filepath.Join(cfg.DictDir, "dicthtml"+suffix+".zip")
				prof, origin := resolveDictProfile(cfg, strings.TrimPrefix(suffix, "-"), path, translationTag)
				log.Printf("  dictionary profile: %s (%s)", prof.name, origin)
				render := dictRender{maxSenses: maxSenses, maxDepth: maxDepth, maxChars: maxChars, asCard: dictExamples, cardFormat: cardFormat}
				if parsed, err := parseDict(path, compileProfile(prof, cfg.StripLabels), render); err != nil {
					log.Printf("No dictionary for suffix %s (dicthtml%s.zip): %v", suffix, suffix, err)
				} else {
					if len(parsed.Entries) == 0 {
						log.Printf("WARNING: dicthtml%s.zip parsed with 0 entries - dictionary format may be unsupported", suffix)
					} else {
						log.Printf("Loaded dicthtml%s.zip: %d entries (max_senses=%d max_depth=%d max_chars=%d translation_tag=%q dict_examples=%v)",
							suffix, len(parsed.Entries), maxSenses, maxDepth, maxChars, translationTag, dictExamples)
					}
					dict = parsed
					dictCache[suffix] = dict
				}
			}
		}

		chain := buildChain(cfg, providerNames, NewDictProvider(dict, rules), providerCache)

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
// defaults, the config file, and each [lang] section's overrides have been
// merged - so "did my edit take effect?" can be answered without deploying
// and watching the log. Companion to the warnings emitted while parsing:
// those say what was rejected, this says what won.
func printEffectiveConfig(cfg Config) {
	fmt.Printf("dict_dir     %s\n", cfg.DictDir)
	fmt.Printf("db           %s\n", cfg.DBPath)
	fmt.Printf("out          %s\n", cfg.OutDir)
	fmt.Printf("ca_cert      %s\n", cfg.CACertPath)
	fmt.Printf("card_format  %q\n", cfg.CardFormat)
	fmt.Printf("strip_labels %v\n", cfg.StripLabels)
	fmt.Printf("providers    %v\n", cfg.Providers)
	fmt.Printf("chatgpt      model=%s temperature=%v max_tokens=%d max_alternatives=%d conf=%s\n",
		cfg.ChatGPTModel, cfg.ChatGPTTemperature, cfg.ChatGPTMaxTokens, cfg.ChatGPTMaxAlternatives, cfg.ChatGPTConf)
	fmt.Printf("lexicala     conf=%s\n", cfg.LexicalaConf)
	fmt.Printf("pons         conf=%s\n", cfg.PonsConf)

	fmt.Printf("\n%-8s %-10s %-9s %-9s %-15s %-13s %s\n",
		"lang", "max_senses", "max_depth", "max_chars", "translation_tag", "dict_examples", "providers")
	fmt.Printf("%-8s %-10d %-9d %-9d %-15q %-13v %v\n",
		"(global)", cfg.MaxSenses, cfg.MaxDepth, cfg.MaxChars, cfg.TranslationTag, cfg.DictExamples, cfg.Providers)
	for _, lang := range langList(cfg.Langs) {
		rules := cfg.Langs[lang]
		senses, depth, chars, tag, examples := effectiveLimits(cfg, rules)
		fmt.Printf("%-8s %-10d %-9d %-9d %-15q %-13v %v\n",
			lang, senses, depth, chars, tag, examples, effectiveProviders(cfg, rules))
	}

	printDictProfiles(cfg)
}

// printDictProfiles lists the dictionary each pair would be parsed with. The
// profile is usually detected rather than written down, so without this the
// only way to learn which one is in force is to read the log of a real run -
// or to notice the cards coming out wrong. Resolving means opening the zips,
// which is why this is at the end.
func printDictProfiles(cfg Config) {
	pairs := dictPairs(cfg)
	if len(pairs) == 0 {
		return
	}
	fmt.Printf("\n%-10s %-10s %-22s %s\n", "dict", "profile", "origin", "file")
	for _, pair := range pairs {
		path := filepath.Join(cfg.DictDir, "dicthtml-"+pair+".zip")
		if _, err := os.Stat(path); err != nil {
			// Configured but not installed: say so rather than sniffing a
			// missing file and reporting the fallback as if it were real.
			fmt.Printf("%-10s %-10s %-22s %s\n", pair, "?", "not resolved", "missing: "+path)
			continue
		}
		fromLang, _, _ := strings.Cut(pair, "-")
		_, _, _, tag, _ := effectiveLimits(cfg, cfg.Langs[fromLang])
		prof, origin := resolveDictProfile(cfg, pair, path, tag)
		fmt.Printf("%-10s %-10s %-22s %s\n", pair, prof.name, origin, filepath.Base(path))
	}
}

// dictPairs is every language pair worth reporting on: the dictionaries
// present in dict_dir, plus any pair with a [dict:] section, so a section
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
	for pair := range cfg.Dicts {
		if !seen[pair] {
			pairs = append(pairs, pair)
		}
	}
	slices.Sort(pairs)
	return pairs
}

func langList(m map[string]*LangRules) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
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
// [dict:] syntax, ready to paste into the config if it needs adjusting.
func testDictFile(cfg Config, path string) {
	pair := ""
	if base := filepath.Base(path); strings.HasPrefix(base, "dicthtml-") {
		pair = strings.TrimSuffix(strings.TrimPrefix(base, "dicthtml-"), ".zip")
	}
	// Resolve through the [lang] section for the pair's source language, so
	// this shows what a real run produces rather than what the global
	// defaults alone would.
	fromLang, _, _ := strings.Cut(pair, "-")
	maxSenses, maxDepth, maxChars, translationTag, dictExamples := effectiveLimits(cfg, cfg.Langs[fromLang])
	prof, origin := resolveDictProfile(cfg, pair, path, translationTag)

	render := dictRender{
		maxSenses: maxSenses, maxDepth: maxDepth, maxChars: maxChars,
		asCard: dictExamples, cardFormat: compileCardFormatOrDefault(cfg.CardFormat),
	}
	dict, err := parseDict(path, compileProfile(prof, cfg.StripLabels), render)
	if err != nil {
		log.Fatalf("Failed to parse %s: %v", path, err)
	}

	fmt.Printf("%s: %d entries\n", path, len(dict.Entries))
	fmt.Printf("profile: %s (%s)\n", prof.name, origin)
	if pair != "" {
		fmt.Printf("  [dict:%s]\n  profile=%s\n", pair, prof.name)
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
