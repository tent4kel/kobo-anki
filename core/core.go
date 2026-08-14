package core

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/open-spaced-repetition/go-fsrs/v3"
)

type CoreConfig struct {
	DataDir          string
	Reverse          bool
	RequestRetention float64
	MaximumInterval  float64
	EnableShortTerm  bool

	// Learning mode - see SessionConfig, whose fields these are.
	LearningPool   int
	MatureInterval int
	StepGap        int

	// HTMLFontScale is a multiplier layered on top of each binary's own
	// baseline correction for an html/apkg card's literal CSS font sizes -
	// real Anki templates are authored assuming a normal ~96-160 DPI
	// phone/desktop screen, so "20px" is tiny both on the Kobo panel's
	// ~300 DPI and, in practice, in every browser cmd/server has been
	// tested in (including modern ones - a browser's own DPI handling
	// turned out not to be a substitute for this). 1.0 means "accept that
	// binary's baseline as-is", not "no scaling at all": cmd/fbink's
	// baseline is the real detected screenDPI/96; cmd/server has no
	// per-viewer DPI to detect, so its baseline (serverBaselineScale) is a
	// fixed assumption instead.
	HTMLFontScale float64
}

// Session returns the learning-mode policy these settings describe.
func (c CoreConfig) Session() SessionConfig {
	return SessionConfig{
		LearningPool:   c.LearningPool,
		MatureInterval: c.MatureInterval,
		StepGap:        c.StepGap,
	}
}

func LoadCoreConfig(path string) CoreConfig {
	cfg := CoreConfig{
		DataDir:          "words",
		RequestRetention: 0.9,
		MaximumInterval:  36500,
		EnableShortTerm:  true,
		LearningPool:     10,
		MatureInterval:   7,
		StepGap:          3,
		HTMLFontScale:    1.0,
	}

	f, err := os.Open(path)
	if err != nil {
		return cfg
	}
	defer f.Close()

	warn := NewConfigWarnings(path)
	defer warn.Summary()

	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch key {
		case "data_dir":
			cfg.DataDir = val
		case "reverse":
			if v, ok := warn.Bool(lineNo, key, val); ok {
				cfg.Reverse = v
			}
		case "request_retention":
			if v, ok := warn.Float(lineNo, key, val); ok {
				cfg.RequestRetention = v
			}
		case "maximum_interval":
			if v, ok := warn.Float(lineNo, key, val); ok {
				cfg.MaximumInterval = v
			}
		case "enable_short_term":
			if v, ok := warn.Bool(lineNo, key, val); ok {
				cfg.EnableShortTerm = v
			}
		case "learning_pool":
			if v, ok := warn.Int(lineNo, key, val); ok {
				cfg.LearningPool = v
			}
		case "mature_interval":
			if v, ok := warn.Int(lineNo, key, val); ok {
				cfg.MatureInterval = v
			}
		case "step_gap":
			if v, ok := warn.Int(lineNo, key, val); ok {
				cfg.StepGap = v
			}
		case "html_font_scale":
			if v, ok := warn.Float(lineNo, key, val); ok {
				cfg.HTMLFontScale = v
			}
		default:
			warn.UnknownKey(lineNo, key)
		}
	}
	return cfg
}

func InitScheduler(retention float64, maxInterval float64, shortTerm bool) {
	p := fsrs.DefaultParam()
	p.RequestRetention = retention
	p.MaximumInterval = maxInterval
	p.EnableShortTerm = shortTerm
	scheduler = fsrs.NewFSRS(p)
}

type Card struct {
	Front string
	Back  string

	// ID identifies the card independently of how its faces are rendered.
	// kobo-vocab sets it to the word it looked up; hand-written decks and
	// decks predating the column leave it empty. Use Key, not this field
	// directly, to look a card up.
	ID string

	// Format says how Front/Back should be drawn: "" means markdown (every
	// card before this field existed, and everything kobo-vocab produces
	// today), FormatHTML means already-resolved HTML/CSS. Use IsHTML rather
	// than comparing this directly, so a future non-markdown, non-html value
	// still falls back to markdown instead of failing to render at all.
	Format string

	Due           time.Time
	Stability     float64
	Difficulty    float64
	ElapsedDays   uint64
	ScheduledDays uint64
	Reps          uint64
	Lapses        uint64
	State         fsrs.State
	LastReview    time.Time
}

// Key is the card's lookup identity: its ID when it has one, otherwise its
// front text.
//
// The front text alone used to be the identity everywhere, which quietly
// broke once providers started rendering fronts as full cards - the same
// word would produce a different front after a card_format change or a
// different provider answering, so its review history was orphaned and the
// word re-added as a duplicate. Falling back to Front keeps hand-written
// two-column decks working exactly as before.
func (c *Card) Key() string {
	if c.ID != "" {
		return c.ID
	}
	return c.Front
}

// IsHTML reports whether a card's Front/Back are already-resolved HTML/CSS
// rather than markdown. Both review UIs branch on this, not on comparing
// Format directly, so an unrecognised future value falls back to markdown
// (today's only behaviour) instead of failing to render.
func IsHTML(c Card) bool {
	return c.Format == FormatHTML
}

var scheduler = fsrs.NewFSRS(fsrs.DefaultParam())

func (c *Card) fsrsCard() fsrs.Card {
	return fsrs.Card{
		Due:           c.Due,
		Stability:     c.Stability,
		Difficulty:    c.Difficulty,
		ElapsedDays:   c.ElapsedDays,
		ScheduledDays: c.ScheduledDays,
		Reps:          c.Reps,
		Lapses:        c.Lapses,
		State:         c.State,
		LastReview:    c.LastReview,
	}
}

func (c *Card) applyFSRS(fc fsrs.Card) {
	c.Due = fc.Due
	c.Stability = fc.Stability
	c.Difficulty = fc.Difficulty
	c.ElapsedDays = fc.ElapsedDays
	c.ScheduledDays = fc.ScheduledDays
	c.Reps = fc.Reps
	c.Lapses = fc.Lapses
	c.State = fc.State
	c.LastReview = fc.LastReview
}

// Review applies the FSRS algorithm to schedule the next review.
// rating: fsrs.Again (1), fsrs.Hard (2), fsrs.Good (3), fsrs.Easy (4)
func Review(card *Card, rating fsrs.Rating) {
	fc := card.fsrsCard()
	result := scheduler.Next(fc, time.Now(), rating)
	card.applyFSRS(result.Card)
}

func IsDue(c Card) bool {
	return !c.Due.After(time.Now())
}

func ListDecks(dataDir string) []string {
	files, _ := filepath.Glob(filepath.Join(dataDir, "*.csv"))
	var result []string
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasPrefix(base, ".") {
			continue
		}
		result = append(result, strings.TrimSuffix(base, ".csv"))
	}
	return result
}

func DeckCSVPath(dataDir, deckName string) string {
	return filepath.Join(dataDir, deckName+".csv")
}

// DeckMediaDir is where a deck's referenced media (images, and for an
// apkg-imported deck the archived source .apkg itself) lives on disk.
// cmd/apkgimport writes into it, htmlrender.Container's MediaResolver and
// cmd/server's static file route both read from it - one definition so the
// three agree, the same reason DeckCSVPath exists as a shared helper rather
// than three ad hoc filepath.Join calls.
func DeckMediaDir(dataDir, deckName string) string {
	return filepath.Join(dataDir, deckName+".media")
}

// CardFromRow builds a Card from one raw CSV row (widening it via
// NormalizeRow first), so anything that reads deck rows - LoadCards, and any
// content provider that wants to merge into an existing deck the way
// kobo-vocab and cmd/apkgimport both do - parses them identically. ok is
// false for a row NormalizeRow rejects as too short to be a card at all.
func CardFromRow(row []string) (c Card, ok bool) {
	normalized, ok := NormalizeRow(row)
	if !ok {
		return Card{}, false
	}

	c = Card{
		Front:      normalized[ColFront],
		Back:       normalized[ColBack],
		ID:         normalized[ColID],
		Format:     normalized[ColFormat],
		Due:        parseTime(normalized[ColDue]),
		LastReview: parseTime(normalized[ColLastReview]),
	}
	c.Stability, _ = strconv.ParseFloat(normalized[ColStability], 64)
	c.Difficulty, _ = strconv.ParseFloat(normalized[ColDifficulty], 64)
	c.ElapsedDays, _ = strconv.ParseUint(normalized[ColElapsedDays], 10, 64)
	c.ScheduledDays, _ = strconv.ParseUint(normalized[ColScheduledDays], 10, 64)
	c.Reps, _ = strconv.ParseUint(normalized[ColReps], 10, 64)
	c.Lapses, _ = strconv.ParseUint(normalized[ColLapses], 10, 64)
	state, _ := strconv.Atoi(normalized[ColState])
	c.State = fsrs.State(state)

	return c, true
}

func LoadCards(csvFile string) ([]Card, error) {
	file, err := os.Open(csvFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	rows, err := NewSpreadsheetCSVReader(file).ReadAll()
	if err != nil {
		return nil, err
	}

	var cards []Card
	for i, row := range rows {
		if i == 0 {
			warnIfNotHeader(csvFile, row)
			continue
		}
		c, ok := CardFromRow(row)
		if !ok {
			continue
		}
		cards = append(cards, c)
	}
	return cards, nil
}

// SaveCards writes the whole deck atomically (see WriteFileAtomic) - an
// interrupted save leaves the previous deck intact rather than a truncated
// one, which matters because this runs on every card rating.
func SaveCards(csvFile string, cards []Card) error {
	return WriteFileAtomic(csvFile, 0644, func(f io.Writer) error {
		w, err := NewSpreadsheetCSVWriter(f)
		if err != nil {
			return err
		}

		w.Write(DeckHeader)
		for _, c := range cards {
			w.Write([]string{
				c.Front, c.Back,
				formatTime(c.Due),
				strconv.FormatFloat(c.Stability, 'f', 4, 64),
				strconv.FormatFloat(c.Difficulty, 'f', 4, 64),
				strconv.FormatUint(c.ElapsedDays, 10),
				strconv.FormatUint(c.ScheduledDays, 10),
				strconv.FormatUint(c.Reps, 10),
				strconv.FormatUint(c.Lapses, 10),
				strconv.Itoa(int(c.State)),
				formatTime(c.LastReview),
				c.ID,
				c.Format,
			})
		}
		w.Flush()
		return w.Error()
	})
}

// FindCard looks a card up by its Key (see Card.Key) - its id when it has
// one, otherwise its front text.
func FindCard(cards []Card, key string) *Card {
	for i := range cards {
		if cards[i].Key() == key {
			return &cards[i]
		}
	}
	return nil
}

func CountDueCards(cards []Card) int {
	n := 0
	for _, c := range cards {
		if IsDue(c) {
			n++
		}
	}
	return n
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Try date-only format for backward compat
		t, _ = time.Parse("2006-01-02", s)
	}
	return t
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
