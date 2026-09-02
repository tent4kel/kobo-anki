package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LexicalaProvider looks up words via the Lexicala API (RapidAPI proxy).
// A single Lookup chains up to four calls:
//
//  1. /lemmatize   - resolve an inflected form ("liepen") to its headword
//     ("lopen"). Best-effort: on failure the original word is used as-is.
//  2. /translate-to - the actual translations for the headword. If this
//     yields nothing, the word is treated as not-found (no fallback to the
//     inflected form - it's the headword that's looked up).
//  3. /examples    - a curated example sentence in the source language for
//     the headword.
//  4. /translate-example - that sentence translated into the target
//     language, so it's directly usable on the card back.
//
// Steps 3-4 are best-effort too: any failure just means Entry.Example is
// left blank, not that the whole lookup fails - a missing example is far
// less bad than a missing translation.
//
// Note the RapidAPI proxy uses inconsistent query parameter names across
// these otherwise-related endpoints (verified by trial and error against
// the real API): "language" vs "source_language" for the source language,
// "targetLang" on /translate-to vs "target_language" everywhere else. This
// is not a typo here - each endpoint really does want its own name.
type LexicalaProvider struct {
	baseURL         string
	host            string
	token           string
	client          *http.Client
	quota           *lexicalaQuota
	maxTranslations int
	cardFormat      cardTemplate
}

type lexicalaCreds struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

// NewLexicalaProvider loads credentials from confPath (see APIs/lexicala.json)
// and a sibling quota-tracking state file (confPath with ".state.json"
// instead of ".json"). Returns an error if the file is missing or
// incomplete, so callers can log a warning and skip this provider rather
// than crash the whole run.
func NewLexicalaProvider(confPath string, cardFormat cardTemplate, caCertPath string) (*LexicalaProvider, error) {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return nil, err
	}
	var creds lexicalaCreds
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("%s: %w", confPath, err)
	}
	if creds.BaseURL == "" || creds.Token == "" {
		return nil, fmt.Errorf("%s: base_url and token must both be set", confPath)
	}

	base := strings.TrimRight(creds.BaseURL, "/") + "/"
	host := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	host = strings.TrimRight(host, "/")

	statePath := strings.TrimSuffix(confPath, filepath.Ext(confPath)) + ".state.json"

	return &LexicalaProvider{
		baseURL: base,
		host:    host,
		token:   creds.Token,
		client:  newHTTPClient(10*time.Second, caCertPath),
		// The real limit is 50 calls/day (verified against the live API);
		// 45 leaves a little headroom for manual -test-dict-style checks
		// against the same key without a real run tipping it over.
		quota:           loadLexicalaQuota(statePath, 45),
		maxTranslations: 3, // "most common translations" - top few, not the whole list
		cardFormat:      cardFormat,
	}, nil
}

func (p *LexicalaProvider) Name() string { return "lexicala" }

func (p *LexicalaProvider) Lookup(word, fromLang, toLang string) (Entry, error) {
	lemma := word
	var lem lexLemmatizeResp
	if err := p.get("lemmatize", url.Values{"word": {word}, "language": {fromLang}}, &lem); err == nil {
		if len(lem.Results) > 0 && len(lem.Results[0].Headwords) > 0 {
			lemma = lem.Results[0].Headwords[0].Text
		}
	}

	var trans lexTranslateToResp
	if err := p.get("translate-to", url.Values{"text": {lemma}, "language": {fromLang}, "targetLang": {toLang}}, &trans); err != nil {
		return Entry{}, err
	}
	if len(trans.Results) == 0 {
		return Entry{}, ErrNotFound
	}
	words := trans.Results[0].Translations[toLang]
	if len(words) == 0 {
		return Entry{}, ErrNotFound
	}
	if len(words) > p.maxTranslations {
		words = words[:p.maxTranslations]
	}

	sourceExample, targetExample := p.fetchExample(lemma, fromLang, toLang)

	// Same card theme as ChatGPTProvider: bold headword, plain alternatives,
	// italic example - words[0] is the primary translation (bold headword
	// on the back), the rest are alternatives, mirroring how ChatGPT's own
	// "headword + alternatives" split works. Front gets the source-language
	// lemma + the untranslated example; Lexicala has no cheap source-language
	// "alternative meanings" endpoint (that's what /search's monolingual
	// definitions would be, at the cost of a 5th API call per word), so
	// unlike ChatGPT's front, there's no alternatives segment here.
	return Entry{
		Front:      formatCard(p.cardFormat, lemma, nil, sourceExample),
		Definition: formatCard(p.cardFormat, words[0], words[1:], targetExample),
		Source:     "lexicala",
	}, nil
}

// fetchExample returns the untranslated (source-language) example sentence
// alongside its target-language translation - best-effort, both empty on
// any failure rather than failing the whole lookup (a missing example is
// far less bad than a missing translation).
func (p *LexicalaProvider) fetchExample(lemma, fromLang, toLang string) (source, target string) {
	var ex lexExamplesResp
	if err := p.get("examples", url.Values{"text": {lemma}, "language": {fromLang}}, &ex); err != nil {
		return "", ""
	}
	if len(ex.Results) == 0 || len(ex.Results[0].Examples) == 0 {
		return "", ""
	}
	source = ex.Results[0].Examples[0].Text

	var tr lexTranslateExampleResp
	if err := p.get("translate-example", url.Values{"text": {source}, "language": {fromLang}, "target_language": {toLang}}, &tr); err != nil {
		return source, ""
	}
	if len(tr.Results) == 0 || len(tr.Results[0].Translation) == 0 {
		return source, ""
	}
	return source, tr.Results[0].Translation[0].Text
}

type lexLemmatizeResp struct {
	Results []struct {
		Headwords []struct {
			Text string `json:"text"`
		} `json:"headwords"`
	} `json:"results"`
}

type lexTranslateToResp struct {
	Results []struct {
		Translations map[string][]string `json:"translations"`
	} `json:"results"`
}

type lexExamplesResp struct {
	Results []struct {
		Examples []struct {
			Text string `json:"text"`
		} `json:"examples"`
	} `json:"results"`
}

type lexTranslateExampleResp struct {
	Results []struct {
		Translation []struct {
			Text string `json:"text"`
		} `json:"translation"`
	} `json:"results"`
}

// get issues one GET request against the Lexicala API and decodes the JSON
// body into out. Returns ErrNotFound without making a request if the daily
// quota is already spent, so callers fall through to the next provider
// exactly as if this one had no entry - not a hard failure.
func (p *LexicalaProvider) get(path string, params url.Values, out interface{}) error {
	if !p.quota.allow() {
		return ErrNotFound
	}

	headers := map[string]string{
		"X-RapidAPI-Key":  p.token,
		"X-RapidAPI-Host": p.host,
	}
	status, body, err := doRequest(p.client, "GET", p.baseURL+path+"?"+params.Encode(), nil, headers)
	if err != nil {
		return err
	}
	p.quota.record()

	if status != http.StatusOK {
		return fmt.Errorf("lexicala %s: HTTP %d: %s", path, status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// lexicalaQuota tracks a simple daily call budget in a small JSON state
// file, so repeated runs across a day (or across multiple language groups
// in one run) share one counter instead of each assuming the full quota.
type lexicalaQuota struct {
	path  string
	limit int
	date  string
	used  int
}

func loadLexicalaQuota(path string, limit int) *lexicalaQuota {
	q := &lexicalaQuota{path: path, limit: limit, date: time.Now().Format("2006-01-02")}

	if data, err := os.ReadFile(path); err == nil {
		var saved struct {
			Date string `json:"date"`
			Used int    `json:"used"`
		}
		if json.Unmarshal(data, &saved) == nil && saved.Date == q.date {
			q.used = saved.Used
		}
	}
	return q
}

func (q *lexicalaQuota) allow() bool {
	return q.used < q.limit
}

func (q *lexicalaQuota) record() {
	q.used++
	data, err := json.Marshal(struct {
		Date string `json:"date"`
		Used int    `json:"used"`
	}{q.date, q.used})
	if err != nil {
		return
	}
	os.WriteFile(q.path, data, 0644)
}
