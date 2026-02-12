package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// --- Types ---

type Config struct {
	DictDir     string
	DBPath      string
	OutDir      string
	APIFallback bool
	Langs       map[string]*LangRules
}

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
	Word, Translation, FromLang, ToLang, Source string
}

type KoboDict struct {
	Entries map[string]string
}

// --- Config parsing ---

func loadConfig(path string) Config {
	cfg := Config{
		DictDir:     "dict",
		DBPath:      "KoboReader.sqlite",
		OutDir:      ".",
		APIFallback: false,
		Langs:       make(map[string]*LangRules),
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
			}
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

	return cfg
}

// --- Main ---

func main() {
	confPath := flag.String("conf", "anki-mywords.conf", "path to config file")
	dbOverride := flag.String("db", "", "override db path from config")
	createTest := flag.Bool("create-test-db", false, "create a test KoboReader.sqlite and exit")
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

	log.Printf("Config: dict_dir=%s db=%s api=%v langs=%v",
		cfg.DictDir, cfg.DBPath, cfg.APIFallback, langList(cfg.Langs))

	// Open Kobo database read-only
	koboDB, err := sql.Open("sqlite", cfg.DBPath+"?mode=ro")
	if err != nil {
		log.Fatalf("Cannot open Kobo DB: %v", err)
	}
	defer koboDB.Close()

	// Read words
	words, err := readWordList(koboDB)
	if err != nil {
		log.Fatalf("Cannot read WordList: %v", err)
	}
	for i := range words {
		words[i].Text = stripPunctuation(words[i].Text)
	}
	log.Printf("Found %d words in vocabulary", len(words))

	// Group by language pair
	byLang := make(map[string][]VocabWord)
	for _, w := range words {
		byLang[w.DictSuffix] = append(byLang[w.DictSuffix], w)
	}

	// Check which words are new (not in existing CSVs or misses files)
	type langGroup struct {
		fromLang, toLang string
		existing         map[string][]string // existing CSV rows keyed by word
		existingRows     [][]string          // existing CSV rows in order
		newWords         []VocabWord
	}
	groups := make(map[string]*langGroup)
	needsDict := false

	for suffix, langWords := range byLang {
		fromLang, toLang := parseLangPair(suffix)
		if fromLang == "" || toLang == "" {
			log.Printf("Skipping unknown dict suffix: %q", suffix)
			continue
		}

		csvPath := filepath.Join(cfg.OutDir, fmt.Sprintf("vocab-%s-%s.csv", fromLang, toLang))
		missesPath := filepath.Join(cfg.OutDir, fmt.Sprintf("vocab-misses-%s-%s.txt", fromLang, toLang))

		existing, existingRows := readExistingCSV(csvPath)
		misses := readMisses(missesPath)

		var newWords []VocabWord
		for _, w := range langWords {
			key := strings.ToLower(w.Text)
			if _, ok := existing[key]; ok {
				continue
			}
			if misses[key] {
				continue
			}
			newWords = append(newWords, w)
		}

		groups[suffix] = &langGroup{fromLang, toLang, existing, existingRows, newWords}
		if len(newWords) > 0 {
			needsDict = true
			log.Printf("%s→%s: %d new words to translate", fromLang, toLang, len(newWords))
		}
	}

	if !needsDict {
		log.Printf("No new words, done")
		return
	}

	// Load dictionaries only when needed
	dicts := loadKoboDicts(cfg.DictDir)

	for suffix, g := range groups {
		if len(g.newWords) == 0 {
			continue
		}

		log.Printf("Processing %s → %s", g.fromLang, g.toLang)

		dict := dicts[suffix]
		if dict != nil {
			log.Printf("Using dictionary: dicthtml%s.zip (%d entries)", suffix, len(dict.Entries))
		}
		rules := cfg.Langs[g.fromLang]

		var newTranslations []Translation
		var newMisses []string

		for _, w := range g.newWords {
			// 1. Dict exact match
			if dict != nil {
				if def, ok := dict.Entries[strings.ToLower(w.Text)]; ok {
					t := Translation{w.Text, def, g.fromLang, g.toLang, "dict"}
					newTranslations = append(newTranslations, t)
					fmt.Printf("  [dict]       %s → %s\n", t.Word, t.Translation)
					continue
				}

				// 2. Stemmed lookup
				if rules != nil {
					if def, stem := stemLookup(dict, w.Text, rules); def != "" {
						label := fmt.Sprintf("%s (→%s)", def, stem)
						t := Translation{w.Text, label, g.fromLang, g.toLang, "dict-stem"}
						newTranslations = append(newTranslations, t)
						fmt.Printf("  [stem:%-6s] %s → %s\n", stem, w.Text, def)
						continue
					}
				}
			}

			// 3. API fallback
			if !cfg.APIFallback {
				newMisses = append(newMisses, strings.ToLower(w.Text))
				fmt.Printf("  [miss]       %s\n", w.Text)
				continue
			}
			trans, err := translateAPI(w.Text, g.fromLang, g.toLang)
			if err != nil {
				log.Printf("  [error]      %s: %v", w.Text, err)
				continue
			}
			t := Translation{w.Text, trans, g.fromLang, g.toLang, "api"}
			newTranslations = append(newTranslations, t)
			fmt.Printf("  [api]        %s → %s\n", t.Word, t.Translation)
			time.Sleep(300 * time.Millisecond)
		}

		// Export: existing rows + new translations
		csvPath := filepath.Join(cfg.OutDir, fmt.Sprintf("vocab-%s-%s.csv", g.fromLang, g.toLang))
		if err := exportCSV(csvPath, g.existingRows, newTranslations); err != nil {
			log.Printf("Failed to write %s: %v", csvPath, err)
		} else {
			log.Printf("Wrote %s (%d existing + %d new)", csvPath, len(g.existingRows), len(newTranslations))
		}

		// Save misses
		if len(newMisses) > 0 {
			missesPath := filepath.Join(cfg.OutDir, fmt.Sprintf("vocab-misses-%s-%s.txt", g.fromLang, g.toLang))
			appendMisses(missesPath, newMisses)
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

var wordEntryRe = regexp.MustCompile(`(?s)<w><a name="([^"]*)"/><div>(.*?)</div></w>`)
var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

func loadKoboDicts(dir string) map[string]*KoboDict {
	dicts := make(map[string]*KoboDict)
	matches, err := filepath.Glob(filepath.Join(dir, "dicthtml-*.zip"))
	if err != nil || len(matches) == 0 {
		log.Printf("No dictionaries found in %s", dir)
		return dicts
	}

	for _, path := range matches {
		base := filepath.Base(path)
		suffix := strings.TrimSuffix(strings.TrimPrefix(base, "dicthtml"), ".zip")

		dict, err := parseKoboDict(path)
		if err != nil {
			log.Printf("Failed to parse %s: %v", base, err)
			continue
		}
		dicts[suffix] = dict
		log.Printf("Loaded %s: %d entries", base, len(dict.Entries))
	}
	return dicts
}

func parseKoboDict(zipPath string) (*KoboDict, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	dict := &KoboDict{Entries: make(map[string]string)}

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

		text := string(data)
		for _, m := range wordEntryRe.FindAllStringSubmatch(text, -1) {
			word := strings.ToLower(m[1])
			body := m[2]

			parts := strings.Split(body, "<br")
			var definition string
			if len(parts) >= 2 {
				last := parts[len(parts)-1]
				if idx := strings.Index(last, ">"); idx >= 0 {
					last = last[idx+1:]
				}
				definition = strings.TrimSpace(htmlTagRe.ReplaceAllString(last, ""))
			}
			if definition == "" {
				definition = strings.TrimSpace(htmlTagRe.ReplaceAllString(body, " "))
			}

			if len(definition) > 100 {
				definition = definition[:100]
			}

			dict.Entries[word] = definition
		}
	}
	return dict, nil
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

// --- Misses tracking ---

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

// --- MyMemory API ---

func translateAPI(word, from, to string) (string, error) {
	apiURL := fmt.Sprintf("https://api.mymemory.translated.net/get?q=%s&langpair=%s",
		url.QueryEscape(word), url.QueryEscape(from+"|"+to))

	resp, err := http.Get(apiURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result struct {
		ResponseData struct {
			TranslatedText string `json:"translatedText"`
		} `json:"responseData"`
		ResponseStatus int `json:"responseStatus"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if result.ResponseStatus != 200 {
		return "", fmt.Errorf("API status %d", result.ResponseStatus)
	}
	return result.ResponseData.TranslatedText, nil
}

// --- CSV export ---

func exportCSV(path string, existingRows [][]string, newTranslations []Translation) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
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

	allRows, err := csv.NewReader(f).ReadAll()
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
