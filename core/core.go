package core

import (
	"bufio"
	"encoding/csv"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/open-spaced-repetition/go-fsrs/v3"
)

type CoreConfig struct {
	DataDir            string
	Reverse            bool
	RequestRetention   float64
	MaximumInterval    float64
	EnableShortTerm    bool
}

func LoadCoreConfig(path string) CoreConfig {
	cfg := CoreConfig{
		DataDir:          "words",
		RequestRetention: 0.9,
		MaximumInterval:  36500,
		EnableShortTerm:  false,
	}

	f, err := os.Open(path)
	if err != nil {
		return cfg
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
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
			cfg.Reverse = val == "true" || val == "1"
		case "request_retention":
			if v, err := strconv.ParseFloat(val, 64); err == nil {
				cfg.RequestRetention = v
			}
		case "maximum_interval":
			if v, err := strconv.ParseFloat(val, 64); err == nil {
				cfg.MaximumInterval = v
			}
		case "enable_short_term":
			cfg.EnableShortTerm = val == "true" || val == "1"
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
	Front         string
	Back          string
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

func LoadCards(csvFile string) ([]Card, error) {
	file, err := os.Open(csvFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, err
	}

	var cards []Card
	for i, row := range rows {
		if i == 0 || len(row) < 2 {
			continue
		}

		c := Card{
			Front: row[0],
			Back:  row[1],
			State: fsrs.New,
		}

		if len(row) >= 11 {
			// FSRS format: front,back,due,stability,difficulty,elapsed_days,scheduled_days,reps,lapses,state,last_review
			c.Due = parseTime(row[2])
			c.Stability, _ = strconv.ParseFloat(row[3], 64)
			c.Difficulty, _ = strconv.ParseFloat(row[4], 64)
			c.ElapsedDays, _ = strconv.ParseUint(row[5], 10, 64)
			c.ScheduledDays, _ = strconv.ParseUint(row[6], 10, 64)
			c.Reps, _ = strconv.ParseUint(row[7], 10, 64)
			c.Lapses, _ = strconv.ParseUint(row[8], 10, 64)
			state, _ := strconv.Atoi(row[9])
			c.State = fsrs.State(state)
			c.LastReview = parseTime(row[10])
		}

		cards = append(cards, c)
	}
	return cards, nil
}

func SaveCards(csvFile string, cards []Card) error {
	file, err := os.Create(csvFile)
	if err != nil {
		return err
	}
	defer file.Close()

	w := csv.NewWriter(file)
	defer w.Flush()

	w.Write([]string{"front", "back", "due", "stability", "difficulty",
		"elapsed_days", "scheduled_days", "reps", "lapses", "state", "last_review"})
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
		})
	}
	return nil
}

func FindCard(cards []Card, front string) *Card {
	for i := range cards {
		if cards[i].Front == front {
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

// RandomDueCard returns a pointer to a random due card in the slice,
// or nil if no cards are due. The caller decides the fallback behavior.
func RandomDueCard(cards []Card) *Card {
	var due []int
	for i, c := range cards {
		if IsDue(c) {
			due = append(due, i)
		}
	}
	if len(due) > 0 {
		return &cards[due[rand.Intn(len(due))]]
	}
	return nil
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
