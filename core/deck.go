package core

import "log"

// The deck CSV schema - the actual interface between kobo-vocab (which
// produces decks) and the review UIs (which consume them). It lives here so
// there is exactly one definition: it used to be spelled out independently
// in core.SaveCards and in kobo-vocab's exportCSV/readExistingCSV, with
// nothing keeping the two in sync.
//
// Columns are append-only. Readers tolerate rows with fewer columns (see
// NormalizeRow) so older and hand-written decks keep working, which means a
// new column must never be inserted in the middle - only added at the end.
var DeckHeader = []string{
	"front", "back", "due", "stability", "difficulty",
	"elapsed_days", "scheduled_days", "reps", "lapses", "state", "last_review",
	"id",
}

// DeckColumns is the canonical row width (len(DeckHeader)).
const DeckColumns = 12

// Column indices, for readers that pick fields out of a raw row.
const (
	ColFront = iota
	ColBack
	ColDue
	ColStability
	ColDifficulty
	ColElapsedDays
	ColScheduledDays
	ColReps
	ColLapses
	ColState
	ColLastReview

	// ColID is a stable identifier for the card, independent of how its
	// faces happen to be rendered - kobo-vocab fills it with the source word
	// it looked up. It is deliberately the LAST column: a leading id would
	// make a hand-written two-column "front,back" file parse as "id,front",
	// which is precisely how the bundled french.csv came to show its own row
	// numbers as card fronts.
	//
	// May be empty - decks written before this column existed, and
	// hand-written ones, simply don't have it. Card.Key falls back to the
	// front text in that case, which is the behaviour those decks already
	// had.
	ColID
)

// NewCardRow builds a row for a never-reviewed card: id/front/back filled
// in, every scheduling field at its zero value, state = New.
func NewCardRow(id, front, back string) []string {
	row := make([]string, DeckColumns)
	row[ColFront] = front
	row[ColBack] = back
	row[ColID] = id
	row[ColState] = "0" // fsrs.New
	for _, i := range []int{ColStability, ColDifficulty, ColElapsedDays, ColScheduledDays, ColReps, ColLapses} {
		row[i] = "0"
	}
	return row
}

// NormalizeRow widens a short row to DeckColumns, keeping whatever columns
// it does have and filling the rest with their zero values. This is what
// makes a hand-written two-column "front,back" deck work, and what lets a
// deck written before the id column gain one without losing its review
// history. Returns ok=false for rows too short to be a card at all.
//
// Rows already at or beyond full width pass through untouched, so columns
// added by a future version survive a read/write cycle rather than being
// truncated by an older binary.
func NormalizeRow(row []string) ([]string, bool) {
	if len(row) < 2 {
		return nil, false
	}
	if len(row) >= DeckColumns {
		return row, true
	}
	out := make([]string, DeckColumns)
	copy(out, row)
	for i := len(row); i < DeckColumns; i++ {
		out[i] = zeroForColumn(i)
	}
	return out, true
}

// zeroForColumn is the value a missing column takes when a short row is
// widened: "0" for the numeric scheduling fields, empty for the timestamps
// and the id (both of which treat "" as "unset").
func zeroForColumn(i int) string {
	switch i {
	case ColStability, ColDifficulty, ColElapsedDays, ColScheduledDays, ColReps, ColLapses, ColState:
		return "0"
	default:
		return ""
	}
}

// LooksLikeDeckHeader reports whether a row is this schema's header line.
// Compared case-insensitively on the first two columns only, so a deck saved
// by a spreadsheet (which may quote or re-case things) is still recognised.
func LooksLikeDeckHeader(row []string) bool {
	return len(row) >= 2 &&
		equalFold(row[ColFront], "front") &&
		equalFold(row[ColBack], "back")
}

// warnIfNotHeader flags a first row that isn't the expected header. A deck
// whose first line is data means either a missing header (its first card is
// being silently dropped, since readers skip row 0) or a foreign schema
// being mis-sliced column-for-column - which is exactly how the bundled
// french.csv came to read its own id column as the card front.
func warnIfNotHeader(path string, row []string) {
	if LooksLikeDeckHeader(row) {
		return
	}
	log.Printf("%s: first line is not the expected %q,%q header - if this deck came from "+
		"elsewhere its columns may not line up; see the CSV format section of the README",
		path, DeckHeader[ColFront], DeckHeader[ColBack])
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
