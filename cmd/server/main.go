package main

import (
	"bytes"
	"html/template"
	"kobo-anki/core"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/open-spaced-repetition/go-fsrs/v3"
	"github.com/yuin/goldmark"
)

var (
	cards   []core.Card
	cardsMu sync.RWMutex
	tmpl    *template.Template
	csvFile string
	dataDir = "."

	// The running order for the deck currently being studied. This server is
	// a single-user local thing - one browser, one deck at a time - so one
	// session, guarded by cardsMu like everything else, is enough.
	session     *core.Session
	sessionDeck string
	sessionCfg  core.SessionConfig
)

type studyData struct {
	Card      *core.Card
	FrontHTML template.HTML
	// FrontHeadwordHTML is just the leading **bold** headword out of
	// FrontHTML's source, for the small gray "front repeated" field on the
	// back template - with dict_examples/card_format, FrontHTML can carry
	// alternatives and an example too, which is too much to restate there;
	// this is a reminder of which word the card is about, not a second
	// copy of the front page (which still uses the full FrontHTML).
	FrontHeadwordHTML template.HTML
	BackHTML          template.HTML
	Deck              string
	Key               string // card.Key() - stable id when present, else the front text
	Reverse           bool
}

// renderMD converts a card field (which may be Markdown emitted by
// kobo-vocab, or a plain headword) to HTML. Converting plain text through
// goldmark is a safe no-op, so every field is treated as "may contain
// Markdown" uniformly rather than special-cased by Front/Back - reverse
// mode can put either physical CSV column in either display slot.
func renderMD(s string) template.HTML {
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(s), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(s))
	}
	return template.HTML(buf.String())
}

func newStudyData(display *core.Card, deck, key string, reverse bool) studyData {
	return studyData{
		Card:              display,
		FrontHTML:         renderMD(display.Front),
		FrontHeadwordHTML: renderMD(core.HeadwordOnly(display.Front)),
		BackHTML:          renderMD(display.Back),
		Deck:              deck,
		Key:               key,
		Reverse:           reverse,
	}
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	type DeckInfo struct {
		Name string
		Due  int
	}

	cardsMu.Lock()
	decks := core.ListDecks(dataDir)
	var deckInfos []DeckInfo
	for _, d := range decks {
		c, err := core.LoadCards(core.DeckCSVPath(dataDir, d))
		if err != nil {
			continue
		}
		// The real backlog, deliberately - not what a sitting would open
		// with. The learning pool decides how cards arrive; it does not get
		// to rewrite the number that says where the deck actually stands.
		deckInfos = append(deckInfos, DeckInfo{Name: d, Due: core.CountDueCards(c)})
	}
	cardsMu.Unlock()

	tmpl.ExecuteTemplate(w, "index", deckInfos)
}

func studyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	deck := r.URL.Query().Get("deck")
	if deck == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	reverse := r.URL.Query().Get("reverse") == "1"

	cardsMu.Lock()
	csvFile = core.DeckCSVPath(dataDir, deck)
	var err error
	cards, err = core.LoadCards(csvFile)
	// ahead is only ever set by the link on the done page. Once the sitting
	// exists it carries on under sessionDeck like any other, so nothing else -
	// the rating links, the reverse toggle - has to thread the flag through.
	ahead := r.URL.Query().Get("ahead") == "1"
	if err == nil && (ahead || sessionDeck != deck) {
		// Otherwise only on a deck change: rateHandler redirects back here
		// after every rating, so rebuilding unconditionally would throw the
		// running order away between every pair of cards - re-queued ones too.
		if ahead {
			session = core.NewStudyAheadSession(cards, sessionCfg)
		} else {
			session = core.NewSession(cards, sessionCfg)
		}
		sessionDeck = deck
	}
	card := session.Current(cards)
	aheadCount := 0
	if card == nil {
		// Let the next visit start a fresh sitting.
		sessionDeck = ""
		// "Nothing due" is the scheduler's verdict on right now, and the
		// learning pool is part of the scheduler - so this page reads the same
		// whether the deck is empty or the pool is holding new words back. The
		// deck list is where the real backlog lives.
		aheadCount = core.CountStudyAhead(cards)
	}
	cardsMu.Unlock()
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if card == nil {
		tmpl.ExecuteTemplate(w, "done", struct {
			Deck  string
			Ahead int
		}{deck, aheadCount})
		return
	}

	display := *card
	if reverse {
		display.Front, display.Back = display.Back, display.Front
	}
	tmpl.ExecuteTemplate(w, "front", newStudyData(&display, deck, card.Key(), reverse))
}

func backHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	front := r.URL.Query().Get("front") // card.Key(), named "front" for URL compatibility
	deck := r.URL.Query().Get("deck")
	reverse := r.URL.Query().Get("reverse") == "1"

	cardsMu.Lock()
	csvFile = core.DeckCSVPath(dataDir, deck)
	cards, _ = core.LoadCards(csvFile)
	cardsMu.Unlock()

	cardsMu.RLock()
	card := core.FindCard(cards, front)
	cardsMu.RUnlock()
	if card == nil {
		http.Redirect(w, r, "/study?deck="+deck, http.StatusSeeOther)
		return
	}

	display := *card
	if reverse {
		display.Front, display.Back = display.Back, display.Front
	}
	tmpl.ExecuteTemplate(w, "back", newStudyData(&display, deck, card.Key(), reverse))
}

func rateHandler(w http.ResponseWriter, r *http.Request) {
	front := r.URL.Query().Get("front")
	deck := r.URL.Query().Get("deck")
	q, _ := strconv.Atoi(r.URL.Query().Get("q"))
	reverse := r.URL.Query().Get("reverse")

	rating := fsrs.Rating(q)
	if rating < fsrs.Again || rating > fsrs.Easy {
		rating = fsrs.Good
	}

	cardsMu.Lock()
	csvFile = core.DeckCSVPath(dataDir, deck)
	cards, _ = core.LoadCards(csvFile)
	if cur := session.Current(cards); sessionDeck == deck && cur != nil && cur.Key() == front {
		session.Rate(cards, rating)
		core.SaveCards(csvFile, cards)
	} else if card := core.FindCard(cards, front); card != nil {
		// A stale URL or the back button: rate the card the browser actually
		// asked about, but leave the running order alone rather than advancing
		// it past a card that was never shown.
		core.Review(card, rating)
		core.SaveCards(csvFile, cards)
	}
	cardsMu.Unlock()

	redirect := "/study?deck=" + deck
	if reverse == "1" {
		redirect += "&reverse=1"
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	deck := r.URL.Query().Get("deck")

	cardsMu.Lock()
	csvFile = core.DeckCSVPath(dataDir, deck)
	cards, _ = core.LoadCards(csvFile)
	cardsMu.Unlock()

	cardsMu.RLock()
	// Both numbers, because with a learning pool in force they differ and
	// the difference is the interesting part: "due" is what FSRS says, the
	// other is what a sitting would actually offer.
	due := core.CountDueCards(cards)
	studyable := core.CountStudyable(cards, sessionCfg)
	total := len(cards)
	cardsMu.RUnlock()

	data := struct {
		Deck      string
		Total     int
		Due       int
		Studyable int
	}{deck, total, due, studyable}
	tmpl.ExecuteTemplate(w, "stats", data)
}

func main() {
	coreCfg := core.LoadCoreConfig("anki-core.conf")
	dataDir = coreCfg.DataDir
	sessionCfg = coreCfg.Session()
	core.InitScheduler(coreCfg.RequestRetention, coreCfg.MaximumInterval, coreCfg.EnableShortTerm)

	if len(os.Args) > 1 {
		dataDir = os.Args[1]
	}

	log.Printf("Data dir: %s", dataDir)

	var err error
	tmpl, err = template.ParseGlob(filepath.Join(filepath.Dir(os.Args[0]), "templates", "*.html"))
	if err != nil {
		tmpl, err = template.ParseGlob("templates/*.html")
		if err != nil {
			log.Fatalf("Failed to parse templates: %v", err)
		}
	}

	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/study", studyHandler)
	http.HandleFunc("/back", backHandler)
	http.HandleFunc("/rate", rateHandler)
	http.HandleFunc("/stats", statsHandler)
	http.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html><body bgcolor='#FFFFFF'><center><br><br><br><font size='6'><b>Server stopped.</b></font></center></body></html>"))
		go func() {
			time.Sleep(500 * time.Millisecond)
			os.Exit(0)
		}()
	})

	log.Println("Listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
