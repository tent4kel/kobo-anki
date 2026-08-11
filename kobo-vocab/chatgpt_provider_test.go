package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestChatGPTProvider(t *testing.T, handler http.HandlerFunc) *ChatGPTProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	confPath := filepath.Join(dir, "chatgpt.json")
	creds := fmt.Sprintf(`{"base_url": %q, "token": "test-token"}`, server.URL+"/")
	if err := os.WriteFile(confPath, []byte(creds), 0644); err != nil {
		t.Fatalf("write test creds: %v", err)
	}

	p, err := NewChatGPTProvider(confPath, "test-model", 0.3, 400, 3, defaultCardFormat(), "")
	if err != nil {
		t.Fatalf("NewChatGPTProvider: %v", err)
	}
	return p
}

func chatCompletionResponse(content string) string {
	body, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"role": "assistant", "content": content}},
		},
	})
	return string(body)
}

func TestChatGPTProviderFullLookup(t *testing.T) {
	const cardJSON = `{
		"source_headword": "lopen",
		"source_alternatives": ["stappen", "wandelen"],
		"source_example": "Zij liepen naar school.",
		"target_headword": "laufen",
		"target_alternatives": ["gehen", "rennen"],
		"target_example": "Sie liefen zur Schule."
	}`

	var gotAuth, gotModel string
	var gotMessages []chatGPTChatMsg
	p := newTestChatGPTProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		var req chatGPTChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		gotMessages = req.Messages
		fmt.Fprint(w, chatCompletionResponse(cardJSON))
	})

	e, err := p.Lookup("liepen", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", gotAuth)
	}
	if gotModel != "test-model" {
		t.Errorf("model sent = %q, want test-model", gotModel)
	}
	if len(gotMessages) != 2 || gotMessages[1].Content != `Word: "liepen"` {
		t.Errorf("user message = %+v, want the looked-up word quoted", gotMessages)
	}
	if !strings.Contains(gotMessages[0].Content, "Dutch") || !strings.Contains(gotMessages[0].Content, "German") {
		t.Errorf("system prompt should name the languages, got: %s", gotMessages[0].Content)
	}

	wantFront := "**lopen**\n\nstappen, wandelen\n\n*Zij liepen naar school.*"
	if e.Front != wantFront {
		t.Errorf("Front = %q, want %q", e.Front, wantFront)
	}
	wantBack := "**laufen**\n\ngehen, rennen\n\n*Sie liefen zur Schule.*"
	if e.Definition != wantBack {
		t.Errorf("Definition = %q, want %q", e.Definition, wantBack)
	}
	if e.Source != "chatgpt" {
		t.Errorf("Source = %q, want chatgpt", e.Source)
	}
	if e.Example != "" {
		t.Errorf("Example = %q, want empty (embedded into Definition/Front directly, not the generic append)", e.Example)
	}
}

func TestChatGPTProviderStripsMarkdownFence(t *testing.T) {
	const cardJSON = `{"source_headword":"huis","source_alternatives":[],"source_example":"","target_headword":"Haus","target_alternatives":[],"target_example":""}`
	p := newTestChatGPTProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, chatCompletionResponse("```json\n"+cardJSON+"\n```"))
	})

	e, err := p.Lookup("huis", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error (fenced JSON should be tolerated), got %v", err)
	}
	if e.Definition != "**Haus**" {
		t.Errorf("Definition = %q, want **Haus**", e.Definition)
	}
}

func TestChatGPTProviderEmptyTargetIsNotFound(t *testing.T) {
	const cardJSON = `{"source_headword":"","source_alternatives":[],"source_example":"","target_headword":"","target_alternatives":[],"target_example":""}`
	p := newTestChatGPTProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, chatCompletionResponse(cardJSON))
	})

	if _, err := p.Lookup("xyzzy", "nl", "de"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestChatGPTProviderCapsAlternatives(t *testing.T) {
	const cardJSON = `{
		"source_headword": "gaan",
		"source_alternatives": ["a", "b", "c", "d", "e"],
		"source_example": "",
		"target_headword": "gehen",
		"target_alternatives": ["1", "2", "3", "4", "5"],
		"target_example": ""
	}`
	p := newTestChatGPTProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, chatCompletionResponse(cardJSON))
	})

	e, err := p.Lookup("ga", "nl", "de")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if e.Front != "**gaan**\n\na, b, c" {
		t.Errorf("Front = %q, want alternatives capped at 3 (maxAlternatives passed to constructor)", e.Front)
	}
	if e.Definition != "**gehen**\n\n1, 2, 3" {
		t.Errorf("Definition = %q, want alternatives capped at 3", e.Definition)
	}
}

func TestChatGPTProviderAPIErrorPropagates(t *testing.T) {
	p := newTestChatGPTProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error": {"message": "invalid api key"}}`)
	})

	_, err := p.Lookup("huis", "nl", "de")
	if err == nil || err == ErrNotFound {
		t.Fatalf("err = %v, want a hard error surfacing the API's message", err)
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("err = %v, want it to include the API's error message", err)
	}
}

func TestNewChatGPTProviderMissingCredsFile(t *testing.T) {
	if _, err := NewChatGPTProvider(filepath.Join(t.TempDir(), "nonexistent.json"), "m", 0.3, 400, 3, defaultCardFormat(), ""); err == nil {
		t.Error("expected an error for a missing credentials file")
	}
}

func TestNewChatGPTProviderEmptyToken(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "chatgpt.json")
	os.WriteFile(confPath, []byte(`{"base_url": "https://api.openai.com/v1/", "token": ""}`), 0644)

	if _, err := NewChatGPTProvider(confPath, "m", 0.3, 400, 3, defaultCardFormat(), ""); err == nil {
		t.Error("expected an error for an empty token - this is the expected pre-key-added state")
	}
}

func TestFormatCard(t *testing.T) {
	cases := []struct {
		name         string
		headword     string
		alternatives []string
		example      string
		want         string
	}{
		{"all present", "lopen", []string{"stappen", "wandelen"}, "Zij liepen.", "**lopen**\n\nstappen, wandelen\n\n*Zij liepen.*"},
		{"headword only", "lopen", nil, "", "**lopen**"},
		{"headword and example, no alternatives", "lopen", nil, "Zij liepen.", "**lopen**\n\n*Zij liepen.*"},
		{"headword and alternatives, no example", "lopen", []string{"stappen"}, "", "**lopen**\n\nstappen"},
		{"everything empty", "", nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatCard(defaultCardFormat(), c.headword, c.alternatives, c.example)
			if got != c.want {
				t.Errorf("formatCard(%q, %v, %q) = %q, want %q", c.headword, c.alternatives, c.example, got, c.want)
			}
		})
	}
}

func TestLanguageNameFallback(t *testing.T) {
	if got := languageName("nl"); got != "Dutch" {
		t.Errorf("languageName(nl) = %q, want Dutch", got)
	}
	if got := languageName("xx-unknown"); got != "xx-unknown" {
		t.Errorf("languageName(xx-unknown) = %q, want the raw code as fallback", got)
	}
}
