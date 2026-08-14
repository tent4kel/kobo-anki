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
	"strings"
	"sync"
	"time"

	"github.com/open-spaced-repetition/go-fsrs/v3"
	"github.com/yuin/goldmark"
)

// serverBaselineScale is the default zoom applied to every html/apkg card
// before htmlFontScale - see htmlCardHTML's doc comment.
const serverBaselineScale = 3.0

var (
	cards         []core.Card
	cardsMu       sync.RWMutex
	tmpl          *template.Template
	csvFile       string
	dataDir       = "."
	htmlFontScale = 1.0 // see newStudyData; set from coreCfg.HTMLFontScale in main()
	// globalCardCSS is cards.css's raw content read once at startup ("" if
	// the file doesn't exist), threaded into every studyData so
	// front.html/back.html can put it in a real <style> tag.
	globalCardCSS string

	// The running order for the deck currently being studied. This server is
	// a single-user local thing - one browser, one deck at a time - so one
	// session, guarded by cardsMu like everything else, is enough.
	session     *core.Session
	sessionDeck string
	sessionCfg  core.SessionConfig
)

type studyData struct {
	Card *core.Card
	// FrontHTML/BackHTML are pre-wrapped in <div class="card-face">...</div>
	// (see wrapCardFace) - the one CSS hook both markdown and html/apkg
	// cards render through, so .card-face { zoom: ... } in front.html/
	// back.html's own <style> applies the same way to either, and neither
	// template needs its own per-format markup.
	FrontHTML template.HTML
	// FrontHeadwordHTML is just the leading **bold** headword out of
	// FrontHTML's source, for the small gray "front repeated" field on the
	// back template - with dict_examples/card_format, FrontHTML can carry
	// alternatives and an example too, which is too much to restate there;
	// this is a reminder of which word the card is about, not a second
	// copy of the front page (which still uses the full FrontHTML).
	//
	// back.html only shows it when IsHTML is false: an html/apkg card's own
	// BackHTML already opens with the resolved front content (real Anki's
	// stock Afmt is "{{FrontSide}}<hr id=answer>{{Back}}", baked in by
	// cmd/apkgimport), so showing this too would repeat the queried word
	// twice in a row - once here, once from the card's own {{FrontSide}}.
	FrontHeadwordHTML template.HTML
	BackHTML          template.HTML
	// FrontCardCSS/BackCardCSS are the deck's own CSS for an html/apkg
	// card (cmd/apkgimport's baked-in model CSS), extracted out of the
	// wrapped document cmd/apkgimport stored so it can go in the page's own
	// real <head> instead of a second, nested one - see
	// core.ExtractCardDocument. Empty for a markdown card; there's nothing
	// to extract.
	FrontCardCSS template.CSS
	BackCardCSS  template.CSS
	// GlobalCardCSS is cards.css's raw content, same for every card and
	// every format - see main()'s startup load.
	GlobalCardCSS template.CSS
	// CardScale is applied to .card-face uniformly for markdown and
	// html/apkg cards alike (see front.html/back.html) - the two used to
	// size completely differently (a fixed legacy <font size="7"> for
	// markdown, an ad hoc zoom only for html), which is what made them
	// look inconsistent with each other.
	CardScale float64
	IsHTML    bool
	Deck      string
	Key       string // card.Key() - stable id when present, else the front text
	Reverse   bool
}

// wrapCardFace wraps a card face's body content (already-trusted HTML, from
// either goldmark or cmd/apkgimport's resolved output) in the one element
// front.html/back.html's .card-face CSS rule targets.
func wrapCardFace(innerHTML string) template.HTML {
	return template.HTML(`<div class="card-face">` + innerHTML + `</div>`)
}

// styleCache holds deck stylesheets already read this run, keyed by
// "<deck>/<file>" - a card's model stylesheet is the same for every card
// sharing that model, so re-reading it from disk on every single card shown
// would be exactly the kind of waste ensureDeckLoaded was just added to
// eliminate for the CSV itself.
var styleCache = map[string]string{}

// resolveCardCSS splits a card face into its CSS and body content,
// regardless of which of the two shapes cmd/apkgimport has ever produced:
// the current one, a "<!--css:slug-->" reference to a deduped
// style-slug.css in the deck's media dir (core.ExtractCardStyleRef), or the
// older one, a full baked-in <html><head><style>...</style></head><body>
// document (core.ExtractCardDocument) from a deck imported before dedup
// existed and not re-imported since.
func resolveCardCSS(deck, cardFace string) (css, body string) {
	cssFile, body, ok := core.ExtractCardStyleRef(cardFace)
	if !ok {
		return core.ExtractCardDocument(cardFace)
	}
	key := deck + "/" + cssFile
	if cached, ok := styleCache[key]; ok {
		return cached, body
	}
	data, err := os.ReadFile(filepath.Join(core.DeckMediaDir(dataDir, deck), cssFile))
	if err != nil {
		return "", body // unstyled, not a failed render
	}
	css = string(data)
	styleCache[key] = css
	return css, body
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

// newStudyData resolves a card's faces for a template. Streamlined so
// markdown and html/apkg cards go through exactly one shared rendering
// concern - wrapCardFace + the .card-face CSS rule in front.html/back.html -
// instead of the html branch carrying its own bespoke sizing/injection
// logic: that used to inject a per-card <base>/zoom/<style> into a second,
// nested <head> embedded mid-page, which broke in two different ways (a
// viewport meta tag there is never honoured; a body{} selector there
// matches the page's one real <body>, not just the card, so it zoomed the
// surrounding chrome too). <base> now lives as a plain, static tag in each
// template's real <head> instead - see front.html/back.html.
func newStudyData(display *core.Card, deck, key string, reverse bool) studyData {
	data := studyData{
		Card:          display,
		Deck:          deck,
		Key:           key,
		Reverse:       reverse,
		GlobalCardCSS: template.CSS(globalCardCSS),
		// cmd/fbink auto-computes its correction from the real screen it
		// draws to (screenDPI/96); cmd/server has no such detection - it
		// serves whatever browser/device asks - so serverBaselineScale is a
		// fixed assumption rather than a measurement, roughly matching a
		// Kobo panel since that's this project's primary target.
		// htmlFontScale layers on top the same way it does for fbink: 1.0
		// means "accept this baseline as-is", not "no scaling at all".
		CardScale: serverBaselineScale * htmlFontScale,
	}

	if core.IsHTML(*display) {
		frontCSS, frontBody := resolveCardCSS(deck, display.Front)
		backCSS, backBody := resolveCardCSS(deck, display.Back)
		data.FrontHTML = wrapCardFace(frontBody)
		data.FrontCardCSS = template.CSS(frontCSS)
		data.FrontHeadwordHTML = template.HTML(core.HeadwordOnlyHTML(display.Front))
		data.BackHTML = wrapCardFace(backBody)
		data.BackCardCSS = template.CSS(backCSS)
		data.IsHTML = true
		return data
	}

	data.FrontHTML = wrapCardFace(string(renderMD(display.Front)))
	data.FrontHeadwordHTML = renderMD(core.HeadwordOnly(display.Front))
	data.BackHTML = wrapCardFace(string(renderMD(display.Back)))
	return data
}

// ensureDeckLoaded loads deck's CSV into the shared cards/csvFile globals
// only if it isn't already the deck cached there - every request handler
// used to call core.LoadCards unconditionally, so a single "flip, then
// rate" cycle did three full CSV parses (/study, /back, /rate) plus a full
// rewrite (SaveCards in rateHandler), every time, regardless of whether
// anything on disk had actually changed since the last request. For a
// large apkg-imported deck - thousands of cards, each face carrying its
// own copy of the deck's CSS (cmd/apkgimport bakes it in per face; see the
// CSS-dedup discussion this was raised alongside) - that's several
// megabytes parsed and rewritten per tap. cmd/fbink never had this
// problem: it loads a deck once per sitting and keeps it in memory:
// openDeck in cmd/fbink/main.go.
//
// This makes the same assumption cmd/fbink already does - this process is
// the only writer that matters for the deck it's actively reviewing - so a
// change made by another process to the same CSV while this deck is open
// here won't be picked up until the deck is switched away and back (or the
// server restarts). That's not a new risk: TODO.md already documents "no
// locking between server and fbink binaries... running both concurrently
// is last-writer-wins" as an accepted, existing tradeoff.
//
// Caller must hold cardsMu.
func ensureDeckLoaded(deck string) error {
	path := core.DeckCSVPath(dataDir, deck)
	if csvFile == path {
		return nil
	}
	loaded, err := core.LoadCards(path)
	if err != nil {
		return err
	}
	csvFile = path
	cards = loaded
	return nil
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
	err := ensureDeckLoaded(deck)
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
	_ = ensureDeckLoaded(deck)
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
	_ = ensureDeckLoaded(deck)
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
	_ = ensureDeckLoaded(deck)
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

// mediaHandler serves an apkg-imported deck's media (images, referenced by
// core.DeckMediaDir(dataDir, deck)) at /media/<deck>/<file>. filepath.Base
// strips any directory components from the requested file, so a request
// can't escape the deck's media directory.
func mediaHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/media/")
	deck, file, ok := strings.Cut(rest, "/")
	if !ok || deck == "" || file == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(core.DeckMediaDir(dataDir, deck), filepath.Base(file)))
}

// koboFontsHandler serves the Kobo's own bundled fonts directly from their
// real on-device path, so a @font-face rule in cards.css (or a deck's own
// stylesheet) can reference one with its literal absolute path -
// url('/mnt/onboard/fonts/extra-kobo/KF_Literata-Regular.ttf') - and have
// it work unchanged in cmd/fbink (which reads that path straight off the
// filesystem, see deckMediaResolver in cmd/fbink/htmlcard.go) and here,
// where the browser instead requests that same absolute path as a URL.
//
// http.ServeFile itself rejects any request path containing ".." before
// this ever runs, so this can't be used to walk outside /mnt/onboard/fonts;
// registering it at that one fixed prefix (see main, below) is what keeps
// it from serving anything else on the device at all.
func koboFontsHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, r.URL.Path)
}

func main() {
	coreCfg := core.LoadCoreConfig("anki-core.conf")
	dataDir = coreCfg.DataDir
	htmlFontScale = coreCfg.HTMLFontScale
	if css, err := os.ReadFile("cards.css"); err == nil {
		globalCardCSS = string(css)
	}
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
	http.HandleFunc("/media/", mediaHandler)
	http.HandleFunc("/mnt/onboard/fonts/", koboFontsHandler)
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
