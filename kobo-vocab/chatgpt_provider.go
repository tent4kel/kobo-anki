package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ChatGPTProvider queries an OpenAI-compatible Chat Completions endpoint
// (POST {base_url}chat/completions, Authorization: Bearer {token}). That
// API shape isn't OpenAI-exclusive - it's the de facto standard many
// providers speak, so pointing APIs/chatgpt.json's base_url/model at a
// different one is enough to switch backends with no code change: Groq,
// DeepSeek, Together, a local vLLM/Ollama OpenAI-compat server, or
// OpenRouter (which alone proxies OpenAI/Anthropic/Google/Meta/etc. models
// behind one such endpoint). This is what "open for other AI APIs" means
// here - not a multi-adapter abstraction, just not hardcoding one vendor's
// request shape unnecessarily.
//
// Unlike every other provider, ChatGPTProvider also sets Entry.Front: per
// the card-format spec, both faces share one structure - a headword
// (dictionary/citation form, not necessarily the inflected form looked up),
// a few alternative meanings, and one example sentence - just in different
// languages (front: source language; back: target language, using a
// translation of that same example sentence, not an independently
// generated one, so the two faces actually correspond to each other as a
// flashcard).
type ChatGPTProvider struct {
	baseURL         string
	token           string
	model           string
	temperature     float64
	maxTokens       int
	maxAlternatives int
	cardFormat      cardTemplate
	client          *http.Client
}

type chatGPTCreds struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

// NewChatGPTProvider loads credentials from confPath (see APIs/chatgpt.json)
// and combines them with the request-shaping settings from Config (model,
// temperature, max_tokens, max_alternatives - see chatgpt_* keys in
// anki-mywords.conf). Returns an error if the file is missing or the token
// is empty, so callers can log a warning and skip this provider rather than
// send doomed requests - this is the expected state until a key is added.
func NewChatGPTProvider(confPath string, model string, temperature float64, maxTokens, maxAlternatives int, cardFormat cardTemplate, caCertPath string) (*ChatGPTProvider, error) {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return nil, err
	}
	var creds chatGPTCreds
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("%s: %w", confPath, err)
	}
	if creds.BaseURL == "" || creds.Token == "" {
		return nil, fmt.Errorf("%s: base_url and token must both be set", confPath)
	}

	return &ChatGPTProvider{
		baseURL:         strings.TrimRight(creds.BaseURL, "/") + "/",
		token:           creds.Token,
		model:           model,
		temperature:     temperature,
		maxTokens:       maxTokens,
		maxAlternatives: maxAlternatives,
		cardFormat:      cardFormat,
		client:          newHTTPClient(30*time.Second, caCertPath), // generous timeout - LLM completions are slower than a dictionary API lookup
	}, nil
}

func (p *ChatGPTProvider) Name() string { return "chatgpt" }

type chatGPTChatRequest struct {
	Model               string           `json:"model"`
	Temperature         float64          `json:"temperature"`
	MaxCompletionTokens int              `json:"max_completion_tokens"`
	Messages            []chatGPTChatMsg `json:"messages"`
}

type chatGPTChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatGPTChatResponse struct {
	Choices []struct {
		Message chatGPTChatMsg `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// chatGPTCard is the exact JSON shape the model is instructed to reply
// with - see the system prompt in Lookup.
type chatGPTCard struct {
	SourceHeadword     string   `json:"source_headword"`
	SourceAlternatives []string `json:"source_alternatives"`
	SourceExample      string   `json:"source_example"`
	TargetHeadword     string   `json:"target_headword"`
	TargetAlternatives []string `json:"target_alternatives"`
	TargetExample      string   `json:"target_example"`
}

func (p *ChatGPTProvider) Lookup(word, fromLang, toLang string) (Entry, error) {
	system := fmt.Sprintf(`You are a bilingual dictionary assistant building flashcards. Given a word (possibly inflected/conjugated) in %s, respond with ONLY a single JSON object - no markdown, no code fences, no commentary before or after - matching exactly this shape:
{
  "source_headword": string,   // the dictionary/citation form (lemma) of the word in %s - e.g. infinitive for verbs, singular for nouns - NOT necessarily the form given
  "source_alternatives": [string, ...],  // up to %d other common meanings or close synonyms of the headword, in %s. Empty array if the word isn't meaningfully ambiguous
  "source_example": string,    // one short, natural example sentence in %s using the headword
  "target_headword": string,   // the headword translated into %s (its dictionary/citation form in %s)
  "target_alternatives": [string, ...],  // up to %d common translations of the headword into %s
  "target_example": string     // source_example translated into %s - the SAME sentence, not a new one
}
Headword and example must reflect one single sense - the one a native speaker would assume by default in everyday use, not a literal decomposition of the word's parts. Never mix senses between headword and example. Ordinary alternatives are plain synonym/translation words in the target language ONLY - never annotate them, never repeat the headword itself among them.
Exception: if the word ALSO has another distinct, genuinely common everyday sense - a materially different meaning from the one used for headword/example, not just a synonym of it (e.g. a lexicalized plural or an idiom) - append exactly ONE extra entry as the LAST alternative, and only that one, formatted as "translation (=brief English gloss of that other sense)" - e.g. for Dutch "boodschappen" meaning "message", target_alternatives' last entry could be "Einkäufe (=groceries)". Skip this entirely for words with only one real sense - most words.
Before answering, double-check that both example sentences are grammatically correct in their language, including noun genders/articles, verb agreement, and case - a native speaker should find nothing off about them.
Never pad the alternatives arrays to reach the maximum count. Only include a word there if it is a genuinely common synonym or translation in natural everyday use - an empty array is the correct and expected answer for most words, which usually don't have several equally common synonyms.
If you cannot confidently identify the word, return {"source_headword": "", "source_alternatives": [], "source_example": "", "target_headword": "", "target_alternatives": [], "target_example": ""}.`,
		languageName(fromLang), languageName(fromLang), p.maxAlternatives, languageName(fromLang), languageName(fromLang),
		languageName(toLang), languageName(toLang), p.maxAlternatives, languageName(toLang), languageName(toLang))

	reqBody := chatGPTChatRequest{
		Model:               p.model,
		Temperature:         p.temperature,
		MaxCompletionTokens: p.maxTokens,
		Messages: []chatGPTChatMsg{
			{Role: "system", Content: system},
			{Role: "user", Content: fmt.Sprintf("Word: %q", word)},
		},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return Entry{}, err
	}

	req, err := http.NewRequest("POST", p.baseURL+"chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Entry{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)

	resp, err := p.client.Do(req)
	if err != nil {
		return Entry{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Entry{}, err
	}

	var result chatGPTChatResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return Entry{}, fmt.Errorf("chatgpt: %w (body: %s)", err, truncateForLog(body))
	}
	if result.Error != nil {
		return Entry{}, fmt.Errorf("chatgpt: API error: %s", result.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return Entry{}, fmt.Errorf("chatgpt: HTTP %d: %s", resp.StatusCode, truncateForLog(body))
	}
	if len(result.Choices) == 0 {
		return Entry{}, fmt.Errorf("chatgpt: no choices in response")
	}

	var card chatGPTCard
	content := stripJSONFence(result.Choices[0].Message.Content)
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		return Entry{}, fmt.Errorf("chatgpt: could not parse model output as JSON: %w (content: %s)", err, truncateForLog([]byte(content)))
	}
	if card.TargetHeadword == "" {
		return Entry{}, ErrNotFound
	}

	if len(card.SourceAlternatives) > p.maxAlternatives {
		card.SourceAlternatives = card.SourceAlternatives[:p.maxAlternatives]
	}
	if len(card.TargetAlternatives) > p.maxAlternatives {
		card.TargetAlternatives = card.TargetAlternatives[:p.maxAlternatives]
	}

	return Entry{
		Front:      formatCard(p.cardFormat, card.SourceHeadword, card.SourceAlternatives, card.SourceExample),
		Definition: formatCard(p.cardFormat, card.TargetHeadword, card.TargetAlternatives, card.TargetExample),
		Source:     "chatgpt",
	}, nil
}

// formatCard renders one card face - by default "**headword**, alt1, alt2
// *example*", degrading gracefully (no dangling commas/spaces) when
// alternatives or the example are empty - via t, a card_format compiled by
// parseCardTemplate (see cardtemplate.go). Used for both Entry.Front and
// Entry.Definition, which share the same structure per the card-format
// spec, and shared across the chatgpt/lexicala/dict_examples providers so
// card_format applies uniformly across all three.
func formatCard(t cardTemplate, headword string, alternatives []string, example string) string {
	return t.render(headword, alternatives, example)
}

// stripJSONFence removes a ```json ... ``` (or bare ``` ... ```) wrapper if
// the model added one despite being told not to - common enough real-world
// LLM behavior that it's worth tolerating rather than failing the lookup.
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimPrefix(s, "json")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func truncateForLog(b []byte) string {
	const max = 300
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}

// languageName maps an ISO 639-1 code to an English name for the prompt -
// LLMs follow "Translate this Dutch word" far more reliably than raw
// "nl"/"de" codes, especially for less common languages. Falls back to the
// raw code for anything unlisted, so an unmapped language degrades to a
// slightly worse prompt rather than breaking.
func languageName(code string) string {
	if name, ok := languageNames[code]; ok {
		return name
	}
	return code
}

var languageNames = map[string]string{
	"af": "Afrikaans", "ar": "Arabic", "az": "Azeri", "bg": "Bulgarian",
	"br": "Brazilian Portuguese", "ca": "Catalan", "cs": "Czech", "da": "Danish",
	"de": "German", "el": "Greek", "en": "English", "es": "Spanish",
	"et": "Estonian", "fa": "Farsi", "fi": "Finnish", "fr": "French",
	"fr-ca": "Canadian French", "fy": "Frisian", "he": "Hebrew", "hi": "Hindi",
	"hr": "Croatian", "hu": "Hungarian", "id": "Indonesian", "is": "Icelandic",
	"it": "Italian", "ja": "Japanese", "ko": "Korean", "la": "Latin",
	"lt": "Lithuanian", "lv": "Latvian", "ml": "Malay", "nl": "Dutch",
	"no": "Norwegian", "pl": "Polish", "prs": "Dari", "ps": "Pashto",
	"pt": "Portuguese", "ro": "Romanian", "ru": "Russian", "sk": "Slovak",
	"sl": "Slovenian", "sr": "Serbian", "sv": "Swedish", "th": "Thai",
	"tr": "Turkish", "tw": "Traditional Chinese", "uk": "Ukrainian",
	"ur": "Urdu", "vi": "Vietnamese", "zh": "Simplified Chinese",
}
