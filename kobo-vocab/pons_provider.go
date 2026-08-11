package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// PonsProvider looks words up in the PONS dictionary API
// (https://en.pons.com/p/online-dictionary/developers/api). One lookup is
// one GET; unlike Lexicala there is no separate lemmatize/examples call,
// because a PONS entry already carries both the translations and the
// example collocations for a headword.
//
// Two things about this API drive the shape of everything below.
//
// The dictionary is named by both languages sorted alphabetically and run
// together - "denl", "deen" - and that name does NOT imply a direction:
// querying "denl" alone yields nl->de and de->nl results in one response.
// The direction comes from the "in" parameter, and the response is an array
// with one object per direction tagged by "lang". Both are used here, since
// a card wants one direction only.
//
// The payload is HTML, not plain text: each translation's source and target
// are marked-up fragments carrying grammatical metadata in <span> elements
// (genus, topic, style, complement, info...). Those are stripped, using the
// same selector engine the local dict profiles use - see ponsPlainText.
type PonsProvider struct {
	endpoint        string
	secret          string
	client          *http.Client
	cooldown        *ponsCooldown
	maxTranslations int
	cardFormat      cardTemplate
	now             func() time.Time // swappable so tests can cross midnight
}

type ponsCreds struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

// NewPonsProvider loads credentials from confPath (see APIs/pons.json) and
// a sibling cooldown state file (confPath with ".state.json" instead of
// ".json"). Returns an error if the file is missing or incomplete, so
// callers can log a warning and drop this provider rather than abort.
func NewPonsProvider(confPath string, cardFormat cardTemplate, caCertPath string) (*PonsProvider, error) {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return nil, err
	}
	var creds ponsCreds
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("%s: %w", confPath, err)
	}
	if creds.BaseURL == "" || creds.Token == "" {
		return nil, fmt.Errorf("%s: base_url and token must both be set", confPath)
	}

	statePath := strings.TrimSuffix(confPath, filepath.Ext(confPath)) + ".state.json"

	return &PonsProvider{
		endpoint: ponsEndpoint(creds.BaseURL),
		secret:   creds.Token,
		client:   newHTTPClient(10*time.Second, caCertPath),
		cooldown: loadPonsCooldown(statePath),
		now:      time.Now,
		// Same "top few translations, not the whole list" rule as the other
		// providers - a PONS entry can carry a dozen senses, which is a
		// reference work's job, not a flashcard's.
		maxTranslations: 3,
		cardFormat:      cardFormat,
	}, nil
}

// ponsEndpoint normalizes whatever base_url points at into the query
// endpoint. PONS documents two endpoints one after the other - /dictionaries
// lists the available dictionaries, /dictionary performs a lookup - and
// configuring the first when you meant the second is the obvious mistake to
// make. Accepting the API root or either endpoint costs one line and saves
// a failure that would otherwise look like "every word returns HTTP 404".
func ponsEndpoint(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	switch {
	case strings.HasSuffix(base, "/dictionary"):
		return base
	case strings.HasSuffix(base, "/dictionaries"):
		return strings.TrimSuffix(base, "/dictionaries") + "/dictionary"
	default:
		return base + "/dictionary"
	}
}

// ponsDictKey builds the dictionary name for a language pair: both codes
// lowercased, sorted alphabetically, concatenated ("nl"+"de" -> "denl").
// That ordering is the API's own convention, not a guess - confirmed
// against the live /dictionaries listing.
func ponsDictKey(a, b string) string {
	pair := []string{strings.ToLower(a), strings.ToLower(b)}
	sort.Strings(pair)
	return pair[0] + pair[1]
}

func (p *PonsProvider) Name() string { return "pons" }

func (p *PonsProvider) Lookup(word, fromLang, toLang string) (Entry, error) {
	if fromLang == "" || toLang == "" || strings.EqualFold(fromLang, toLang) {
		return Entry{}, ErrNotFound
	}
	today := p.now().Format("2006-01-02")
	if p.cooldown.blocking(today) {
		// Already told today that the allowance is gone. Skip silently -
		// the chain moves to the next provider exactly as for a word PONS
		// does not have.
		return Entry{}, ErrNotFound
	}

	params := url.Values{
		"q":  {word},
		"l":  {ponsDictKey(fromLang, toLang)},
		"in": {strings.ToLower(fromLang)},
		// References pull an inflected or cross-referenced headword's real
		// entry into the same response. Note this does NOT lemmatize: a
		// Dutch "liepen" is simply not found. Fuzzy matching (fm=1) is not
		// used to paper over that, because it ranks by string similarity
		// rather than by meaning - it answers "liepen" with "liegen",
		// "lieden", "piepen", none of which is the lemma "lopen". A missed
		// word falls through to the next provider; a wrong translation on a
		// flashcard would be memorized.
		"ref": {"true"},
	}

	req, err := http.NewRequest("GET", p.endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return Entry{}, err
	}
	req.Header.Set("X-Secret", p.secret)

	resp, err := p.client.Do(req)
	if err != nil {
		return Entry{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent:
		// The API's documented "no results" case, and by far the most
		// common one for a vocabulary list full of inflected forms.
		return Entry{}, ErrNotFound
	case http.StatusNotFound:
		return Entry{}, fmt.Errorf("pons: no dictionary %q (PONS has no such language pair)", params.Get("l"))
	case http.StatusForbidden:
		return Entry{}, fmt.Errorf("pons: not authorized - check the token in APIs/pons.json, or the account's access to dictionary %q", params.Get("l"))
	case http.StatusServiceUnavailable:
		// The API's own "you are out of allowance" answer. There is no
		// usage header to read and no way to learn when PONS's monthly
		// window rolls over, so rather than guess a budget up front, take
		// this as the signal and stand down until tomorrow. If it was the
		// monthly allowance rather than the daily one, tomorrow's first
		// lookup costs one more 503 and stands down again - self-correcting
		// at one wasted call per day.
		p.cooldown.trip(today)
		return Entry{}, fmt.Errorf("pons: out of allowance (HTTP 503) - disabled until tomorrow")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Entry{}, fmt.Errorf("pons: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var results []ponsResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return Entry{}, fmt.Errorf("pons: %w", err)
	}

	card := p.collect(results, fromLang)
	if len(card.translations) == 0 {
		return Entry{}, ErrNotFound
	}

	headword := card.headword
	if headword == "" {
		headword = word
	}
	translations := card.translations
	if len(translations) > p.maxTranslations {
		translations = translations[:p.maxTranslations]
	}

	return Entry{
		Front:      formatCard(p.cardFormat, headword, nil, card.exampleSource),
		Definition: formatCard(p.cardFormat, translations[0], translations[1:], card.exampleTarget),
		Source:     "pons",
	}, nil
}

// ponsCard is what one response boils down to: the headword as PONS spells
// it, its translations in order, and at most one example pair.
type ponsCard struct {
	headword      string
	translations  []string
	exampleSource string
	exampleTarget string
}

// collect walks the response for the requested direction and picks out the
// card material. Rows are self-labelling: a translation whose source is
// marked class="example" is a collocation ("open huis" -> "Tag der offenen
// Tür"), anything else is a translation of the headword itself. That
// distinction is what separates the card's alternatives from its example,
// and it comes from the markup rather than from guessing at the text.
func (p *PonsProvider) collect(results []ponsResult, fromLang string) ponsCard {
	var card ponsCard
	seen := map[string]bool{}

	for _, res := range results {
		// One object per direction; take only the one we asked for. An
		// empty lang is tolerated rather than skipped, so a future response
		// that omits the field still yields a card.
		if res.Lang != "" && !strings.EqualFold(res.Lang, fromLang) {
			continue
		}
		for _, hit := range flattenPonsHits(res.Hits) {
			for _, rom := range hit.Roms {
				if card.headword == "" {
					card.headword = ponsPlainText(rom.Headword)
				}
				for _, arab := range rom.Arabs {
					for _, tr := range arab.Translations {
						if phrase := ponsExamplePhrase(tr.Source); phrase != "" {
							if card.exampleSource == "" {
								if target := ponsPlainText(tr.Target); target != "" {
									card.exampleSource, card.exampleTarget = phrase, target
								}
							}
							continue
						}
						addPonsTranslation(&card, seen, tr.Target)
					}
				}
			}
			// A hit with no entries at all: PONS falls back to bare
			// source/target translation pairs, which are still usable.
			if len(hit.Roms) == 0 && hit.Target != "" {
				if card.headword == "" {
					card.headword = ponsPlainText(hit.Source)
				}
				addPonsTranslation(&card, seen, hit.Target)
			}
		}
	}
	return card
}

func addPonsTranslation(card *ponsCard, seen map[string]bool, raw string) {
	text := ponsPlainText(raw)
	if text == "" {
		return
	}
	key := strings.ToLower(text)
	if seen[key] {
		return
	}
	seen[key] = true
	card.translations = append(card.translations, text)
}

// flattenPonsHits expands entry_with_secondary_entries (what ref=true
// produces) into plain entries, primary first: the referenced entry is
// where the translations actually live when the queried form is only a
// cross-reference.
func flattenPonsHits(hits []ponsHit) []ponsHit {
	var out []ponsHit
	for _, h := range hits {
		if h.PrimaryEntry != nil {
			out = append(out, *h.PrimaryEntry)
			out = append(out, h.SecondaryEntries...)
			continue
		}
		out = append(out, h)
	}
	return out
}

type ponsResult struct {
	Lang string    `json:"lang"`
	Hits []ponsHit `json:"hits"`
}

type ponsHit struct {
	Type string    `json:"type"`
	Roms []ponsRom `json:"roms"`

	// Set on type="translation" hits, which carry a pair directly instead
	// of nesting it under roms/arabs.
	Source string `json:"source"`
	Target string `json:"target"`

	// Set on type="entry_with_secondary_entries" (ref=true only).
	PrimaryEntry     *ponsHit  `json:"primary_entry"`
	SecondaryEntries []ponsHit `json:"secondary_entries"`
}

type ponsRom struct {
	Headword     string     `json:"headword"`
	HeadwordFull string     `json:"headword_full"`
	Wordclass    string     `json:"wordclass"`
	Arabs        []ponsArab `json:"arabs"`
}

type ponsArab struct {
	Header       string            `json:"header"`
	Translations []ponsTranslation `json:"translations"`
}

type ponsTranslation struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

var (
	ponsSpanSel    = parseSelector("span")
	ponsExampleSel = parseSelector("span.example")
)

// ponsPlainText reduces a PONS markup fragment to the words that belong on
// a card. Everything PONS wraps in a <span> is grammatical apparatus -
// gender, register, subject area, complements, usage notes - so removing
// span subtrees and keeping the rest leaves exactly the translation, even
// when the apparatus is interleaved: "Tag <span>m</span> der offenen Tür
// <span>fig</span>" comes back as "Tag der offenen Tür".
func ponsPlainText(fragment string) string {
	root := parsePonsFragment(fragment)
	if root == nil {
		return ""
	}
	removeMatching(root, ponsSpanSel)
	return nodeText(root)
}

// ponsExamplePhrase returns the example collocation a source fragment
// carries, or "" if the fragment is a plain headword rather than an
// example. The phrase lives inside span.example; sibling spans next to it
// are the usual apparatus and are not part of it.
func ponsExamplePhrase(fragment string) string {
	root := parsePonsFragment(fragment)
	if root == nil {
		return ""
	}
	ex := queryFirst(root, ponsExampleSel)
	if ex == nil {
		return ""
	}
	// Nested spans inside the example are apparatus too; the phrase itself
	// is text and <strong> (PONS renders the repeated headword as a tilde).
	removeMatching(ex, ponsSpanSel)
	return nodeText(ex)
}

func parsePonsFragment(fragment string) *html.Node {
	if strings.TrimSpace(fragment) == "" {
		return nil
	}
	doc, err := html.Parse(strings.NewReader(fragment))
	if err != nil {
		return nil
	}
	return doc
}

// ponsCooldown stands the provider down for the rest of the day once the
// API reports the allowance is spent.
//
// There is deliberately no counter here. PONS publishes a monthly figure
// (1000 queries) but sends no usage header, and its billing month starts on
// an account-specific date this code cannot know - so any budget kept here
// would be counting against the wrong window, and a month is far too long a
// window to be wrong about. The API already knows the answer and says so
// with HTTP 503; this just remembers that it did.
//
// State is one date, persisted so the standdown survives between runs:
// without that, every run would re-discover the limit at the cost of
// another call. "Until tomorrow" is a calendar date change in local time,
// which matches how a daily limit resets - though not necessarily in PONS's
// timezone, so the first lookup after midnight may cost one more 503.
type ponsCooldown struct {
	path      string
	exhausted string // date the API last reported the allowance gone
}

type ponsCooldownState struct {
	Exhausted string `json:"exhausted_on"`
}

// loadPonsCooldown reads the saved date, treating a missing, unreadable or
// unrecognized file as "not blocked" - including the counter-shaped state
// this provider used to write. Failing open is right: losing a standdown
// costs one 503, while failing closed would silently disable the provider.
func loadPonsCooldown(path string) *ponsCooldown {
	c := &ponsCooldown{path: path}
	if data, err := os.ReadFile(path); err == nil {
		var saved ponsCooldownState
		if json.Unmarshal(data, &saved) == nil {
			c.exhausted = saved.Exhausted
		}
	}
	return c
}

func (c *ponsCooldown) blocking(today string) bool {
	return c.exhausted != "" && c.exhausted == today
}

func (c *ponsCooldown) trip(today string) {
	c.exhausted = today
	if data, err := json.Marshal(ponsCooldownState{Exhausted: today}); err == nil {
		os.WriteFile(c.path, data, 0644)
	}
}
