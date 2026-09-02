package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	_ "modernc.org/sqlite"
)

// --- Main ---

func main() {
	confPath := flag.String("conf", "config/anki-mywords.conf", "path to config file")
	dbOverride := flag.String("db", "", "override db path from config")
	createTest := flag.Bool("create-test-db", false, "create a test KoboReader.sqlite and exit")
	testDict := flag.String("test-dict", "", "parse a single dicthtml-*.zip and print entry count + samples, then exit")
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
