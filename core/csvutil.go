package core

import (
	"bufio"
	"encoding/csv"
	"io"
)

// utf8BOM is the UTF-8 byte order mark. Excel (notably on Windows) does not
// assume UTF-8 for .csv files: without this marker it guesses the local
// legacy codepage, so a deck full of German/Dutch text renders "Größe" as
// "GrÃ¶ÃŸe". Numbers, LibreOffice, and Go's own csv package are all happy
// either way, so writing it costs nothing and fixes the one tool that
// needs it.
const utf8BOM = "\xef\xbb\xbf"

// NewSpreadsheetCSVWriter returns a csv.Writer whose output opens cleanly by
// double-click in Excel and Numbers: a leading UTF-8 BOM (see above) and
// CRLF row terminators, as RFC 4180 specifies. CRLF matters more here than
// for a typical CSV because card text legitimately contains newlines inside
// quoted fields - with LF-only terminators some importers can't tell a row
// break from a line break within a value.
//
// The BOM is written immediately, so this must be called before anything
// else writes to w.
func NewSpreadsheetCSVWriter(w io.Writer) (*csv.Writer, error) {
	if _, err := io.WriteString(w, utf8BOM); err != nil {
		return nil, err
	}
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	return cw, nil
}

// NewSpreadsheetCSVReader is the counterpart to NewSpreadsheetCSVWriter: it
// transparently skips a leading UTF-8 BOM if present, so files we wrote
// round-trip, files written before BOMs were added still read, and a deck
// re-saved by Excel (which adds its own BOM) doesn't end up with the marker
// glued onto the first header cell - which would silently break every
// column-name-sensitive path.
func NewSpreadsheetCSVReader(r io.Reader) *csv.Reader {
	return csv.NewReader(skipBOM(r))
}

// skipBOM returns a reader positioned past a leading UTF-8 BOM, or the
// original content untouched if there isn't one.
func skipBOM(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	if prefix, err := br.Peek(len(utf8BOM)); err == nil && string(prefix) == utf8BOM {
		br.Discard(len(utf8BOM))
	}
	return br
}
