package main

import (
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
)

var (
	cards   []core.Card
	cardsMu sync.RWMutex
	tmpl    *template.Template
	csvFile string
	dataDir = "."
)

type studyData struct {
	Card    *core.Card
	Deck    string
	Key     string // original card.Front for URL lookups
	Reverse bool
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
	cardsMu.Unlock()
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	cardsMu.RLock()
	card := core.RandomDueCard(cards)
	cardsMu.RUnlock()
	if card == nil {
		tmpl.ExecuteTemplate(w, "done", deck)
		return
	}

	display := *card
	if reverse {
		display.Front, display.Back = display.Back, display.Front
	}
	tmpl.ExecuteTemplate(w, "front", studyData{&display, deck, card.Front, reverse})
}

func backHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	front := r.URL.Query().Get("front") // always the original card.Front
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
	tmpl.ExecuteTemplate(w, "back", studyData{&display, deck, card.Front, reverse})
}

func rateHandler(w http.ResponseWriter, r *http.Request) {
	front := r.URL.Query().Get("front")
	deck := r.URL.Query().Get("deck")
	q, _ := strconv.Atoi(r.URL.Query().Get("q"))
	reverse := r.URL.Query().Get("reverse")

	cardsMu.Lock()
	csvFile = core.DeckCSVPath(dataDir, deck)
	cards, _ = core.LoadCards(csvFile)
	card := core.FindCard(cards, front)
	if card != nil {
		rating := fsrs.Rating(q)
		if rating < fsrs.Again || rating > fsrs.Easy {
			rating = fsrs.Good
		}
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
	due := core.CountDueCards(cards)
	total := len(cards)
	cardsMu.RUnlock()

	data := struct {
		Deck  string
		Total int
		Due   int
	}{deck, total, due}
	tmpl.ExecuteTemplate(w, "stats", data)
}

func main() {
	coreCfg := core.LoadCoreConfig("anki-core.conf")
	dataDir = coreCfg.DataDir
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
