package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MyMemoryProvider queries the free MyMemory translation-memory API. Unlike
// Lexicala, MyMemory has no separate "top translations" endpoint - a single
// query returns one best-pick translation (responseData.translatedText) plus
// a "matches" array of alternative translation-memory entries for the same
// segment, each carrying two numeric fields worth distinguishing:
//
//   - "match" (0-1): how closely the *query text* matches the segment stored
//     in memory - fuzzy string similarity, not translation correctness. For
//     our single-word queries this is ~1 for nearly every result, since the
//     stored segment is usually just the word itself.
//   - "quality" (0-100): a per-entry reliability rating from MyMemory's own
//     contributors/sources. This is NOT reliably a "how good is this
//     translation" signal in the way it sounds: verified against the live
//     API (query "huis"/nl->de) that a perfectly correct, extremely common
//     entry ("Haus") came back with quality=0 right alongside a "quality=74"
//     entry for the same word - 0 reads as "unrated by this particular
//     source", not "this translation is bad". Filtering out low-quality
//     entries (e.g. dropping everything below some threshold) would
//     therefore silently drop good, common translations - not done here.
//     quality is only used as a tiebreaker among otherwise-equal matches.
type MyMemoryProvider struct {
	maxTranslations int
	client          *http.Client
}

func NewMyMemoryProvider(caCertPath string) MyMemoryProvider {
	return MyMemoryProvider{
		maxTranslations: 3,
		// Without an explicit timeout, http.Get's default client will hang
		// indefinitely on a connection that's open but stalled (e.g. flaky
		// Kobo WiFi) rather than failing fast - matches LexicalaProvider's
		// timeout so both network providers fail at the same bound.
		client: newHTTPClient(10*time.Second, caCertPath),
	}
}

// myMemoryAPIURL is a var (not a const) so tests can redirect it at a fake
// server instead of hitting the real API.
var myMemoryAPIURL = "https://api.mymemory.translated.net/get"

// flexibleQuote decodes a JSON field that MyMemory sends inconsistently -
// verified live that "quality" comes back as a quoted string ("74") for
// some matches and as a bare JSON number (0) for others, for the exact same
// query. A plain `string` field errors out entirely on the bare-number
// form, so this accepts either and stores the text either way.
type flexibleQuote string

func (s *flexibleQuote) UnmarshalJSON(data []byte) error {
	if len(data) >= 2 && data[0] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		*s = flexibleQuote(str)
		return nil
	}
	*s = flexibleQuote(data)
	return nil
}

func (MyMemoryProvider) Name() string { return "mymemory" }

func (p MyMemoryProvider) Lookup(word, fromLang, toLang string) (Entry, error) {
	// Free public API - stay polite regardless of where this provider ends
	// up in the chain (previously a fixed sleep after every API call).
	defer time.Sleep(300 * time.Millisecond)

	apiURL := fmt.Sprintf("%s?q=%s&langpair=%s",
		myMemoryAPIURL, url.QueryEscape(word), url.QueryEscape(fromLang+"|"+toLang))

	resp, err := p.client.Get(apiURL)
	if err != nil {
		return Entry{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Entry{}, err
	}

	var result struct {
		ResponseData struct {
			TranslatedText string `json:"translatedText"`
		} `json:"responseData"`
		ResponseStatus int `json:"responseStatus"`
		Matches        []struct {
			Translation string        `json:"translation"`
			Quality     flexibleQuote `json:"quality"` // sometimes a quoted "74", sometimes a bare 0 - see flexibleQuote
			Match       float64       `json:"match"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return Entry{}, err
	}
	if result.ResponseStatus != 200 {
		return Entry{}, fmt.Errorf("mymemory: API status %d", result.ResponseStatus)
	}
	if result.ResponseData.TranslatedText == "" {
		return Entry{}, ErrNotFound
	}

	// Rank alternates by (match desc, quality desc) - match first since it's
	// the one field MyMemory documents as meaningful (segment relevance),
	// quality only to break ties among equally relevant matches (see the
	// type doc comment on why quality alone isn't trustworthy as a filter).
	sort.SliceStable(result.Matches, func(i, j int) bool {
		if result.Matches[i].Match != result.Matches[j].Match {
			return result.Matches[i].Match > result.Matches[j].Match
		}
		qi, _ := strconv.Atoi(string(result.Matches[i].Quality))
		qj, _ := strconv.Atoi(string(result.Matches[j].Quality))
		return qi > qj
	})

	seen := map[string]bool{}
	words := []string{result.ResponseData.TranslatedText}
	seen[strings.ToLower(result.ResponseData.TranslatedText)] = true

	for _, m := range result.Matches {
		if len(words) >= p.maxTranslations {
			break
		}
		key := strings.ToLower(strings.TrimSpace(m.Translation))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		words = append(words, m.Translation)
	}

	return Entry{Definition: strings.Join(words, ", "), Source: "mymemory"}, nil
}
