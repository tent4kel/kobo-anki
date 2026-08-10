package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
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
	"strconv"
	"strings"

	"kobo-anki/core"

	_ "modernc.org/sqlite"
)

// --- Types ---

type Config struct {
	DictDir        string
	DBPath         string
	OutDir         string
	APIFallback    bool
	MaxSenses      int      // max senses (<li> entries) to include per word; 0 = unlimited
	MaxDepth       int      // max direct-child <div> blocks to keep per sense (drops examples etc.); 0 = unlimited
	MaxChars       int      // max characters of definition text per word; 0 = unlimited
	TranslationTag string   // if set (e.g. "b"), nested-format entries keep only this tag's spans, comma-joined; "" = keep full formatted text. No effect on flat-format entries - see extractEntry.
	DictExamples   bool     // if true, nested-format entries try to build a ChatGPT/Lexicala-style card (bold headword + alternatives + one example, on both Front and Definition) from the dict's own example phrases. Verified against dicthtml-nl-de.zip only - see extractCardEntry. No effect without TranslationTag also set, and no effect on flat-format entries.
	Providers      []string // content providers to try in order, e.g. []string{"dict", "lexicala"} - see provider.go
	LexicalaConf   string   // path to Lexicala credentials JSON (base_url/token), only read if "lexicala" is in Providers
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
		APIFallback:  false,
		MaxSenses:    1,
		MaxDepth:     1,
		MaxChars:     150,
		LexicalaConf: "APIs/lexicala.json",
		CardFormat:   DefaultCardFormat,
		CACertPath:   "cacert.pem",

		ChatGPTConf: "APIs/chatgpt.json",
		// gpt-4o-mini/0.3/400/3 are a cheap, fairly deterministic default -
		// any OpenAI-compatible model id works, see chatgpt_provider.go.
		ChatGPTModel:           "gpt-4o-mini",
		ChatGPTTemperature:     0.1,
		ChatGPTMaxTokens:       400,
		ChatGPTMaxAlternatives: 3,

		Langs: make(map[string]*LangRules),
	}

	f, err := os.Open(path)
	if err != nil {
		log.Printf("No config file %s, using defaults", path)
		return cfg
	}
	defer f.Close()

	var currentLang *LangRules
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}

		// Language section header: [nl]
		if line[0] == '[' && line[len(line)-1] == ']' {
			lang := line[1 : len(line)-1]
			currentLang = &LangRules{}
			cfg.Langs[lang] = currentLang
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

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
				currentLang.ReduceDouble = val == "true"
			case "prefix_strip":
				currentLang.PrefixStrips = append(currentLang.PrefixStrips, val)
			case "max_senses":
				if v, err := strconv.Atoi(val); err == nil {
					currentLang.MaxSenses = &v
				}
			case "max_depth":
				if v, err := strconv.Atoi(val); err == nil {
					currentLang.MaxDepth = &v
				}
			case "max_chars":
				if v, err := strconv.Atoi(val); err == nil {
					currentLang.MaxChars = &v
				}
			case "translation_tag":
				v := val
				currentLang.TranslationTag = &v
			case "dict_examples":
				v := val == "true"
				currentLang.DictExamples = &v
			case "providers":
				v := splitProviders(val)
				currentLang.Providers = &v
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
			case "api_fallback":
				cfg.APIFallback = val == "true"
			case "max_senses":
				if v, err := strconv.Atoi(val); err == nil {
					cfg.MaxSenses = v
				}
			case "max_depth":
				if v, err := strconv.Atoi(val); err == nil {
					cfg.MaxDepth = v
				}
			case "max_chars":
				if v, err := strconv.Atoi(val); err == nil {
					cfg.MaxChars = v
				}
			case "translation_tag":
				cfg.TranslationTag = val
			case "dict_examples":
				cfg.DictExamples = val == "true"
			case "card_format":
				cfg.CardFormat = val
			case "providers":
				cfg.Providers = splitProviders(val)
			case "lexicala_conf":
				cfg.LexicalaConf = val
			case "ca_cert":
				cfg.CACertPath = val
			case "chatgpt_conf":
				cfg.ChatGPTConf = val
			case "chatgpt_model":
				cfg.ChatGPTModel = val
			case "chatgpt_temperature":
				if v, err := strconv.ParseFloat(val, 64); err == nil {
					cfg.ChatGPTTemperature = v
				}
			case "chatgpt_max_tokens":
				if v, err := strconv.Atoi(val); err == nil {
					cfg.ChatGPTMaxTokens = v
				}
			case "chatgpt_max_alternatives":
				if v, err := strconv.Atoi(val); err == nil {
					cfg.ChatGPTMaxAlternatives = v
				}
			}
		}
	}

	// providers= not set at all: default to the pre-provider-chain behavior
	// (dict only, plus MyMemory iff api_fallback=true) so existing configs
	// keep working unchanged.
	if cfg.Providers == nil {
		cfg.Providers = []string{"dict"}
		if cfg.APIFallback {
			cfg.Providers = append(cfg.Providers, "mymemory")
		}
	}

	// Resolve relative paths against config file directory
	absPath, _ := filepath.Abs(path)
	confDir := filepath.Dir(absPath)
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
	if !filepath.IsAbs(cfg.ChatGPTConf) {
		cfg.ChatGPTConf = filepath.Join(confDir, cfg.ChatGPTConf)
	}
	if !filepath.IsAbs(cfg.CACertPath) {
		cfg.CACertPath = filepath.Join(confDir, cfg.CACertPath)
	}

	return cfg
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
	seenPath         string // vocab-seen-*.txt for this pair - see readSeenWords
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
		// existing keys the CSV's front column against the raw Kobo word,
		// which only still matches once a provider starts overriding
		// Entry.Front with styled card content (dict_examples, lexicala,
		// chatgpt) instead of leaving it as the plain word - e.g. front
		// becomes "**afspraak**\nmet iemand..." while the Kobo word list
		// still says "afspraak", so the CSV check alone stopped matching
		// and every such word looked "new" again on every run, duplicating
		// it. seen tracks the raw word independently of card content -
		// see readMisses/appendMisses below main(), reused as-is (a plain
		// word-list file, nothing miss-specific about the implementation).
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

		groups[suffix] = &langGroup{fromLang, toLang, existing, existingRows, newWords, seenPath}
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
		testDictFile(*testDict, cfg.MaxSenses, cfg.MaxDepth, cfg.MaxChars, cfg.TranslationTag, cfg.DictExamples, cfg.CardFormat)
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
				if parsed, err := parseKoboDict(path, maxSenses, maxDepth, maxChars, translationTag, dictExamples, cardFormat); err != nil {
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
		var newSeen []string

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
			t := Translation{front, def, g.fromLang, g.toLang, entry.Source}
			newTranslations = append(newTranslations, t)
			newSeen = append(newSeen, strings.ToLower(w.Text))
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

		// Save seen (raw Kobo words that got a real translation) - see the
		// comment in computeGroups on why this can't just be the CSV's
		// front column anymore.
		if len(newSeen) > 0 {
			appendMisses(g.seenPath, newSeen)
		}
	}
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
	case "mymemory":
		return NewCircuitBreaker(NewMyMemoryProvider(cfg.CACertPath), networkFailureThreshold)
	case "lexicala":
		p, err := NewLexicalaProvider(cfg.LexicalaConf, compileCardFormatOrDefault(cfg.CardFormat), cfg.CACertPath)
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

var wordBlockRe = regexp.MustCompile(`(?s)<w>(.*?)</w>`)
var nameAttrRe = regexp.MustCompile(`<a\s+name="([^"]*)"`)
var liRe = regexp.MustCompile(`(?s)<li>(.*?)</li>`)
var divRe = regexp.MustCompile(`(?s)<div>(.*?)</div>`)
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
// whether a dictionary is supported before deploying it to the Kobo.
func testDictFile(path string, maxSenses, maxDepth, maxChars int, translationTag string, dictExamples bool, cardFormat string) {
	dict, err := parseKoboDict(path, maxSenses, maxDepth, maxChars, translationTag, dictExamples, compileCardFormatOrDefault(cardFormat))
	if err != nil {
		log.Fatalf("Failed to parse %s: %v", path, err)
	}
	fmt.Printf("%s: %d entries\n", path, len(dict.Entries))
	if len(dict.Entries) == 0 {
		fmt.Println("No entries parsed - dictionary format is likely unsupported.")
		return
	}
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

func parseKoboDict(zipPath string, maxSenses, maxDepth, maxChars int, translationTag string, dictExamples bool, cardFormat cardTemplate) (*KoboDict, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	// Compiled once per dict load, not per entry - dicts run into the tens
	// of thousands of entries and this regex would otherwise be rebuilt
	// that many times.
	var tagRe *regexp.Regexp
	if translationTag != "" {
		// The tag name must be followed by '>' or whitespace, not just any
		// non-'>' character - `<b[^>]*>` would also match `<br/>` (and any
		// other tag with "b" as a prefix), silently swallowing everything
		// up to the next real </b> as if it were bold content.
		t := regexp.QuoteMeta(translationTag)
		tagRe = regexp.MustCompile(`(?is)<` + t + `(?:\s[^>]*)?>(.*?)</` + t + `\s*>`)
	}

	dict := &KoboDict{Entries: make(map[string]string), Fronts: make(map[string]string)}

	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}

		var data []byte
		if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
			gr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				continue
			}
			data, err = io.ReadAll(gr)
			gr.Close()
			if err != nil {
				continue
			}
		} else {
			data = raw
		}

		for _, block := range wordBlockRe.FindAllString(string(data), -1) {
			word, definition, front, ok := extractEntry(block, maxSenses, maxDepth, maxChars, tagRe, dictExamples, cardFormat)
			if !ok {
				continue
			}
			// Homographs - the same headword spelled/pronounced differently
			// across parts of speech (e.g. "ruim" as adverb, nautical term,
			// adjective, and noun) - get their own separate <w> block in
			// this dict rather than being combined into one entry. Verified
			// against the real dict: 4.76% of headwords have more than one
			// block, and for 67% of those, keeping whichever block merely
			// happens to be parsed last (the previous behavior here) throws
			// away a richer block for a thinner one - e.g. "ruim" kept a
			// 2-sense nautical noun over an 8-sense adjective entry that's
			// ruim's actually-common meaning; "aan" kept a 21-sense block
			// over one with 45. Keeping whichever block's extracted
			// definition is longest is a simple, order-independent proxy
			// for "the more complete entry" that measurably beats
			// last-wins on this data, without trying to guess which sense
			// is grammatically "primary".
			if existing, exists := dict.Entries[word]; exists && len(existing) >= len(definition) {
				continue
			}
			dict.Entries[word] = definition
			delete(dict.Fronts, word) // clear a stale front from a now-discarded block
			if front != "" {
				dict.Fronts[word] = front
			}
		}
	}
	return dict, nil
}

// extractEntry pulls the headword and definition out of a single <w>...</w>
// block, handling both the flat and nested dict layouts described above.
// tagRe, if non-nil, restricts each sense in the nested-format branch to
// only the spans matched by that tag (see translation_tag in the config) -
// e.g. keeping only <b>...</b> content drops the source-language gloss and
// grammar annotations a rich dict wraps in <i>, leaving just the bare
// target-language translation. It has no effect on the flat-format branch:
// verified against a real flat dict (dicthtml-nl-en.zip) that <b> there
// wraps the repeated headword, not the translation, so applying the same
// filter there would extract nothing.
//
// wantExamples (see dict_examples in the config), if true and tagRe is also
// set, tries extractCardEntry first for a ChatGPT/Lexicala-style card
// (headword + alternatives + one example, matching formatCard's shape) built
// from the dict's own example phrases - see extractCardEntry for how that's
// found. maxSenses/maxDepth do not apply to that path: it only ever looks at
// the entry's first sense, since "one example" doesn't compose with a
// multi-sense list. Falls back to the plain multi-sense behavior below
// whenever extractCardEntry finds nothing usable (e.g. no bold match in the
// first sense), so a word without a matching translation doesn't silently
// go blank.
func extractEntry(block string, maxSenses, maxDepth, maxChars int, tagRe *regexp.Regexp, wantExamples bool, cardFormat cardTemplate) (word, definition, front string, ok bool) {
	nameMatch := nameAttrRe.FindStringSubmatch(block)
	if nameMatch == nil {
		return "", "", "", false
	}
	word = strings.ToLower(nameMatch[1])

	if topSenses := topLevelLis(block); len(topSenses) > 0 {
		if wantExamples && tagRe != nil {
			if def, fr, cardOK := extractCardEntry(word, topSenses, tagRe, cardFormat); cardOK {
				def = truncateMarkdownVisible(def, maxChars)
				fr = truncateMarkdownVisible(fr, maxChars)
				return word, def, fr, true
			}
		}

		// Nested format: join up to maxSenses top-level <li> senses. Within
		// each sense, keep only its first maxDepth direct-child <div>s -
		// dictgen always emits the core gloss as the first child div and,
		// only if present, an examples div (class="ex") as the second and
		// last child, so this reliably drops examples without depending on
		// their class name or wording.
		n := len(topSenses)
		if maxSenses > 0 && n > maxSenses {
			n = maxSenses
		}
		var senses []string
		for _, m := range topSenses[:n] {
			divs := topLevelDivs(m)
			if len(divs) == 0 {
				divs = []string{m}
			}
			if maxDepth > 0 && len(divs) > maxDepth {
				divs = divs[:maxDepth]
			}
			joined := strings.Join(divs, " ")
			sense := ""
			if tagRe != nil {
				sense = extractTagText(joined, tagRe)
			}
			if sense == "" {
				sense = cleanText(joined)
			}
			if sense != "" {
				senses = append(senses, sense)
			}
		}
		if len(senses) > 1 {
			definition = "- " + strings.Join(senses, "\n- ")
		} else if len(senses) == 1 {
			definition = senses[0]
		}
	} else if divMatch := divRe.FindStringSubmatch(block); divMatch != nil {
		// Flat format: word/pos/gloss separated by <br>, gloss is the last segment.
		body := divMatch[1]
		parts := strings.Split(body, "<br")
		var last string
		if len(parts) >= 2 {
			last = parts[len(parts)-1]
			if idx := strings.Index(last, ">"); idx >= 0 {
				last = last[idx+1:]
			}
		} else {
			last = body
		}
		// Some dicts pack "word [phonetic]\nactual gloss" into that final
		// segment as plain text with no further tag boundary (e.g.
		// "<b>invloed</b><br/>invloed [invlut]\n     Einfluß") - split on
		// the last literal newline too, mirroring the <br> heuristic one
		// level deeper, so the phonetic-annotated word-repeat doesn't leak
		// into the definition. Must happen before cleanText, which
		// collapses newlines into spaces.
		if idx := strings.LastIndex(last, "\n"); idx >= 0 {
			if tail := strings.TrimSpace(last[idx+1:]); tail != "" {
				last = tail
			}
		}
		definition = cleanText(last)
		if definition == "" {
			definition = cleanText(body)
		}
	} else {
		definition = cleanText(block)
	}

	definition = truncateMarkdownVisible(definition, maxChars)
	return word, definition, "", definition != ""
}

// exDivOpenRe matches the opening tag of a sense's examples wrapper -
// generated by this project's own dict source pipeline (extractor.py,
// ~/Hacking/dict/) as an explicit, unambiguous marker: `<div class="ex">`,
// always exactly that, immediately followed by a "♦ voorbeelden" label div
// and a <ul> of one <li>Dutch phrase<br/>German translation</li> per
// example. Verified present, in exactly this shape, in the currently
// deployed dicthtml-nl-de.zip.
var exDivOpenRe = regexp.MustCompile(`<div\s+class="ex"[^>]*>`)

// extractCardEntry tries to build a ChatGPT/Lexicala-style card - bold
// headword + alternatives + one example, via the shared formatCard - purely
// from the dict's own content, for the entry's first sense only (senses[0]
// from topLevelLis - a correctly-bounded top-level sense, not one that may
// have been truncated mid-example by a naive non-nesting-aware <li> match).
//
// The example, if any, comes from that sense's own class="ex" wrapper (see
// exDivOpenRe) - found by locating the marker, then re-running
// topLevelDivs from that position so its full, correctly-nested content
// (including the further-nested "♦ voorbeelden" label div) comes back as
// one balanced unit, then taking the first <li> within it (safe to match
// with a plain, non-nesting-aware regex here - a <ul> of example <li>s has
// no further nesting inside).  Searching only within senses[0], not the
// whole entry, matters: a later sense may have its own, unrelated
// class="ex" - verified against "boodschap", which has four senses, each
// with its own separate example list.
//
// Returns ok=false if the first sense has no tagRe match at all (e.g. a
// sense that's genuinely just usage notes) - the caller falls back to the
// plain multi-sense extraction in that case, same as when tagRe finds
// nothing in extractTagText.
func extractCardEntry(word string, senses []string, tagRe *regexp.Regexp, cardFormat cardTemplate) (definition, front string, ok bool) {
	senseDivs := topLevelDivs(senses[0])
	if len(senseDivs) == 0 {
		senseDivs = []string{senses[0]}
	}
	translations := extractTagList(senseDivs[0], tagRe)
	if len(translations) == 0 {
		return "", "", false
	}

	var sourceExample, targetExample string
	if loc := exDivOpenRe.FindStringIndex(senses[0]); loc != nil {
		if exDivs := topLevelDivs(senses[0][loc[0]:]); len(exDivs) > 0 {
			if ex := liRe.FindStringSubmatch(exDivs[0]); ex != nil {
				sourceExample, targetExample = splitExample(ex[1])
			}
		}
	}

	definition = formatCard(cardFormat, translations[0], translations[1:], targetExample)
	front = formatCard(cardFormat, word, nil, sourceExample)
	return definition, front, true
}

// splitExample splits a bare example <li>'s raw content - "Dutch
// phrase<br/>German translation", no wrapping tags - into its two halves on
// the first <br> (mirroring the flat-format branch's own "<br" split, same
// case-insensitive/self-closing tolerance). Returns ("", "") if there's no
// <br> to split on, or either half cleans down to nothing - a malformed or
// unrecognized example is worth skipping over, not worth showing broken.
func splitExample(raw string) (source, target string) {
	idx := strings.Index(strings.ToLower(raw), "<br")
	if idx < 0 {
		return "", ""
	}
	head, rest := raw[:idx], raw[idx:]
	if gt := strings.Index(rest, ">"); gt >= 0 {
		rest = rest[gt+1:]
	}
	source, target = cleanPlainText(head), cleanPlainText(rest)
	if source == "" || target == "" {
		return "", ""
	}
	return source, target
}

// extractTagText pulls the text content of every span matched by tagRe out
// of htmlSrc, strips any tags/entities within each span, and joins the
// results with ", " - e.g. turning "<i>inwerking</i><b>Einfluss</b><i>
// (der</i>), <b>Einwirkung</b>..." into "Einfluss, Einwirkung" when tagRe
// matches <b>. Returns "" if the tag has no matches (caller falls back to
// cleanText in that case, rather than silently emitting a blank card).
//
// Known accepted limitation: this assumes the tag is used exclusively for
// the translation, which is true for the vast majority of entries but not
// guaranteed - verified against dicthtml-nl-de.zip's "instorten" (second
// sense), where the source dict itself also bolds case-government notes
// ("<b> (in</b><i> (+ 4e naamval)</i><b>)</b>"), so those leak through
// too. Not fixable by pattern matching alone (would need to distinguish
// "bold = translation" from "bold = grammar note" semantically); rare
// enough in practice not to be worth chasing further.
func extractTagText(htmlSrc string, tagRe *regexp.Regexp) string {
	return strings.Join(extractTagList(htmlSrc, tagRe), ", ")
}

// extractTagList is extractTagText's list form - same extraction, unjoined -
// so extractCardEntry can treat the first match as the headword and the rest
// as alternatives (formatCard's shape) instead of one flat string.
func extractTagList(htmlSrc string, tagRe *regexp.Regexp) []string {
	var parts []string
	for _, m := range tagRe.FindAllStringSubmatch(htmlSrc, -1) {
		inner := htmlTagRe.ReplaceAllString(m[1], " ")
		inner = html.UnescapeString(inner)
		inner = strings.ReplaceAll(inner, " ", " ")
		inner = whitespaceRe.ReplaceAllString(inner, " ")
		inner = strings.TrimSpace(inner)
		if inner != "" {
			parts = append(parts, inner)
		}
	}
	return parts
}

// cleanPlainText strips all tags to bare text with no Markdown conversion,
// unlike cleanText - for extractCardEntry's example halves, which get their
// own outer *italic* wrapping from formatCard. Converting an inner
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

var divTagRe = regexp.MustCompile(`(?i)<div[^>]*>|</div>`)
var liTagRe = regexp.MustCompile(`(?i)<li[^>]*>|</li>`)

// topLevelTags returns the inner HTML of each element matched by tagRe that
// is a direct child (depth 0) of the given fragment, skipping over any
// further nested occurrences of the same tag - e.g. an examples <div> that
// itself contains a label <div>, or a sense <li> whose own example list is
// itself a further-nested <ul><li>...</li></ul> (verified against
// dicthtml-nl-de.zip: a naive non-depth-aware <li>...</li> match, as used
// here previously, matches the FIRST </li> it finds regardless of nesting -
// which for a sense followed by "<div class=\"ex\"><ul><li>example</li>...",
// is the closing tag of the first example bullet, not the sense's own
// closing tag. That silently truncated every "sense" to end mid-example,
// and flattened every example bullet in a dict_examples-relevant word's
// example list into what looked like separate top-level senses (e.g.
// "boodschap" appeared to have 19 senses; it has 4, each with its own
// example list of varying length. Only ever exposed at max_senses>1 - the
// default max_senses=1 always uses just the first, correctly-bounded sense
// anyway, coincidentally masking the bug in what was already deployed).
func topLevelTags(html string, tagRe *regexp.Regexp) []string {
	var result []string
	depth := 0
	start := -1
	for _, m := range tagRe.FindAllStringIndex(html, -1) {
		tag := html[m[0]:m[1]]
		if strings.HasPrefix(tag, "</") {
			depth--
			if depth == 0 && start >= 0 {
				result = append(result, html[start:m[0]])
				start = -1
			}
		} else {
			if depth == 0 {
				start = m[1]
			}
			depth++
		}
	}
	return result
}

func topLevelDivs(html string) []string { return topLevelTags(html, divTagRe) }
func topLevelLis(html string) []string  { return topLevelTags(html, liTagRe) }

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

func exportCSV(path string, existingRows [][]string, newTranslations []Translation) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w, err := core.NewSpreadsheetCSVWriter(f)
	if err != nil {
		return err
	}
	defer w.Flush()
	w.Write([]string{"front", "back", "due", "stability", "difficulty",
		"elapsed_days", "scheduled_days", "reps", "lapses", "state", "last_review"})

	// Preserve existing rows (already normalized to 11-col by readExistingCSV)
	for _, row := range existingRows {
		w.Write(row)
	}

	// Append new translations as New cards (state=0)
	for _, t := range newTranslations {
		w.Write([]string{t.Word, t.Translation, "", "0", "0", "0", "0", "0", "0", "0", ""})
	}

	return w.Error()
}

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
		if i == 0 || len(row) < 2 {
			continue
		}
		// Normalize old 6-col (SM-2) rows to 11-col (FSRS)
		if len(row) < 11 {
			normalized := make([]string, 11)
			normalized[0] = row[0] // front
			normalized[1] = row[1] // back
			// due, stability, difficulty, elapsed_days, scheduled_days, reps, lapses = zero
			normalized[9] = "0" // state = New
			row = normalized
		}
		byWord[strings.ToLower(row[0])] = row
		rows = append(rows, row)
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
