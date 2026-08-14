// Package ankipkg opens a real Anki .apkg (schema-11 collection.anki2/anki21,
// the format every current Anki version and every exporter still in wide use
// writes) and resolves each card's Question/Answer HTML through a minimal
// reimplementation of Anki's card template engine. It has no rendering
// dependency - cmd/apkgimport uses it to produce CSV rows, htmlrender is a
// separate, later step.
//
// Deliberately NOT implemented, matching the prototype this was ported from
// (~/Hacking/GoHtml/cmd/ankirender): {{type:}} fields, TTS, MathJax, nested
// conditionals, and the newer schema-18 notetypes/templates tables some very
// recent Anki versions can write instead of col.models JSON. Unsupported
// {{}} tokens fall back to the bare field value rather than being guessed
// at.
package ankipkg

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// Model is one Anki note type: its fields (in note.flds order) and the
// card templates generated from a note of this type.
type Model struct {
	ID    int64
	Name  string
	CSS   string
	Flds  []Field
	Tmpls []Template
	// Type is 0 for a standard model, 1 for Cloze. A Cloze model has
	// exactly one template regardless of how many distinct {{cN::}} blanks
	// its notes use - cards.ord counts cloze deletions there, not a
	// position into Tmpls, unlike every other model type. See Resolve.
	Type int `json:"type"`
}

// UnmarshalJSON accepts a model id encoded as either a JSON number (the
// common case) or a JSON string (some exporters do this - large Anki ids are
// 64-bit millisecond timestamps, past JavaScript's safe integer range, so a
// JS-based exporter can end up string-encoding them to avoid precision
// loss). Every other field unmarshals normally.
func (m *Model) UnmarshalJSON(data []byte) error {
	type alias Model
	aux := struct {
		ID json.RawMessage `json:"id"`
		*alias
	}{alias: (*alias)(m)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	id, err := flexInt64(aux.ID)
	if err != nil {
		return fmt.Errorf("model id: %w", err)
	}
	m.ID = id
	return nil
}

// flexInt64 parses a JSON number or a JSON string of digits into an int64.
func flexInt64(raw json.RawMessage) (int64, error) {
	var asInt int64
	if err := json.Unmarshal(raw, &asInt); err == nil {
		return asInt, nil
	}
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		return strconv.ParseInt(asStr, 10, 64)
	}
	return 0, fmt.Errorf("neither a number nor a numeric string: %s", raw)
}

type Field struct {
	Name string `json:"name"`
	Ord  int    `json:"ord"`
}

type Template struct {
	Name string `json:"name"`
	Ord  int    `json:"ord"`
	Qfmt string `json:"qfmt"`
	Afmt string `json:"afmt"`
}

type note struct {
	id      int64
	modelID int64
	flds    string
	tags    string // space-separated, Anki's own on-disk format
}

type card struct {
	id     int64
	noteID int64
	ord    int
}

// Package is one opened .apkg's parsed collection.
type Package struct {
	Models map[int64]Model
	Media  map[string][]byte // filename -> bytes, e.g. "cat.png"

	notes map[int64]note
	cards []card
}

// Open unzips path, loads its sqlite collection, and parses models/notes/
// cards. The collection is read into a temp file (modernc.org/sqlite needs a
// real path) which is removed before Open returns - everything needed is
// copied into the returned Package.
func Open(path string) (*Package, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer zr.Close()

	// A modern export commonly ships both: collection.anki21 is the real
	// collection, and collection.anki2 is a legacy-compat stub Anki
	// includes for pre-2.1 importers - deliberately near-empty (one note
	// whose card just says to update Anki), not a smaller copy of the same
	// content. Prefer .anki21, exactly like Anki's own importer does;
	// picking whichever the zip happened to list last silently imported
	// that one-note stub instead of the deck for 5 of 8 real test decks.
	var anki2Data, anki21Data []byte
	mediaIndex := map[string]string{} // "0" -> "cat.png"
	mediaBlobs := map[string][]byte{} // "0" -> bytes

	for _, f := range zr.File {
		switch f.Name {
		case "collection.anki21":
			anki21Data, err = readZipFile(f)
		case "collection.anki2":
			anki2Data, err = readZipFile(f)
		case "media":
			var raw []byte
			if raw, err = readZipFile(f); err == nil {
				var idx map[string]string
				if err = json.Unmarshal(raw, &idx); err == nil {
					mediaIndex = idx
				}
			}
		default:
			mediaBlobs[f.Name], err = readZipFile(f)
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s from %s: %w", f.Name, path, err)
		}
	}
	collectionData := anki21Data
	if collectionData == nil {
		collectionData = anki2Data
	}
	if collectionData == nil {
		return nil, fmt.Errorf("%s: no collection.anki2/anki21 (not a valid .apkg, or a schema-18 export)", path)
	}

	p, err := parseCollection(collectionData)
	if err != nil {
		return nil, err
	}

	p.Media = make(map[string][]byte, len(mediaIndex))
	for idx, name := range mediaIndex {
		if blob, ok := mediaBlobs[idx]; ok {
			p.Media[name] = blob
		}
	}
	return p, nil
}

func parseCollection(data []byte) (*Package, error) {
	tmp, err := os.CreateTemp("", "ankicol-*.sqlite")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", tmp.Name())
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var modelsJSON string
	if err := db.QueryRow("SELECT models FROM col WHERE id = 1").Scan(&modelsJSON); err != nil {
		return nil, fmt.Errorf("reading col.models (schema-18 export? only schema-11 is supported): %w", err)
	}
	var rawModels map[string]json.RawMessage
	if err := json.Unmarshal([]byte(modelsJSON), &rawModels); err != nil {
		return nil, fmt.Errorf("parsing col.models: %w", err)
	}
	models := map[int64]Model{}
	for _, raw := range rawModels {
		var m Model
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("parsing a model in col.models: %w", err)
		}
		models[m.ID] = m
	}

	notes := map[int64]note{}
	rows, err := db.Query("SELECT id, mid, flds, tags FROM notes")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n note
		if err := rows.Scan(&n.id, &n.modelID, &n.flds, &n.tags); err != nil {
			rows.Close()
			return nil, err
		}
		notes[n.id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	var cards []card
	cardRows, err := db.Query("SELECT id, nid, ord FROM cards ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer cardRows.Close()
	for cardRows.Next() {
		var c card
		if err := cardRows.Scan(&c.id, &c.noteID, &c.ord); err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	if err := cardRows.Err(); err != nil {
		return nil, err
	}

	return &Package{Models: models, notes: notes, cards: cards}, nil
}

// Resolved is one card's Question/Answer HTML, ready to store as a
// core.Card's Front/Back (with core.FormatHTML) - the model's CSS still
// needs wrapping around it, which is a rendering concern this package leaves
// to the caller (see core.WrapCardHTML in the fbink/apkgimport integration).
type Resolved struct {
	CardID       int64
	NoteID       int64
	ModelName    string
	ModelCSS     string
	TemplateName string
	Question     string
	Answer       string
	// Tags is the owning note's Anki tags, for a caller that wants to split
	// an import by tag (see cmd/apkgimport's -split-tags). A note with no
	// tags at all yields an empty (not nil-vs-empty-distinguished) slice.
	Tags []string
}

// Resolve runs every card in the collection through the template engine.
// Cards whose note or model went missing (shouldn't happen in a well-formed
// apkg, but a hand-edited one is possible) are skipped rather than failing
// the whole import.
func (p *Package) Resolve() []Resolved {
	var out []Resolved
	for _, c := range p.cards {
		n, ok := p.notes[c.noteID]
		if !ok {
			continue
		}
		m, ok := p.Models[n.modelID]
		if !ok || len(m.Tmpls) == 0 {
			continue
		}
		// A Cloze model always has exactly one template; card.ord counts
		// which {{cN::}} blank is active, not a position into Tmpls. Every
		// other model type generates one card per template, in order, so
		// ord does index Tmpls there.
		tmplIdx := c.ord
		if m.Type == 1 {
			tmplIdx = 0
		}
		if tmplIdx >= len(m.Tmpls) {
			continue
		}
		tmpl := m.Tmpls[tmplIdx]

		fieldVals := strings.Split(n.flds, "\x1f")
		fields := map[string]string{}
		for _, f := range m.Flds {
			if f.Ord < len(fieldVals) {
				fields[f.Name] = fieldVals[f.Ord]
			}
		}

		activeCloze := c.ord + 1 // Anki: card ord 0 -> {{c1::}}, ord 1 -> {{c2::}}, ...
		question := renderTemplate(tmpl.Qfmt, fields, "", activeCloze, false)
		answer := renderTemplate(tmpl.Afmt, fields, question, activeCloze, true)

		out = append(out, Resolved{
			CardID: c.id, NoteID: c.noteID,
			ModelName: m.Name, ModelCSS: m.CSS, TemplateName: tmpl.Name,
			Question: question, Answer: answer,
			Tags: strings.Fields(n.tags),
		})
	}
	return out
}

var (
	// Go's regexp (RE2) has no backreferences, so the closing {{/Field}}'s
	// name is captured but not required to match the opener — fine for the
	// non-nested, single-conditional stock templates this is scoped to.
	condRe  = regexp.MustCompile(`(?s)\{\{[#^]([^}]+)\}\}(.*?)\{\{/([^}]+)\}\}`)
	tokenRe = regexp.MustCompile(`\{\{([^}]+)\}\}`)
	clozeRe = regexp.MustCompile(`\{\{c(\d+)::(.*?)(?:::(.*?))?\}\}`)
)

// renderTemplate implements the small slice of Anki's template language that
// the Basic and Cloze stock models actually use: plain {{Field}}
// substitution, {{FrontSide}}, {{#Field}}/{{^Field}} conditionals, and
// {{cloze:Field}}. Anything else (filters like {{type:Field}}, TTS tags,
// nested conditionals) is left as literal text.
func renderTemplate(tmpl string, fields map[string]string, frontSide string, activeCloze int, reveal bool) string {
	for i := 0; i < 5; i++ { // bounded passes instead of recursion; stock templates need at most one
		newTmpl := condRe.ReplaceAllStringFunc(tmpl, func(m string) string {
			g := condRe.FindStringSubmatch(m)
			field, body := g[1], g[2]
			negate := strings.HasPrefix(m, "{{^")
			present := strings.TrimSpace(fields[field]) != ""
			if present != negate {
				return body
			}
			return ""
		})
		if newTmpl == tmpl {
			break
		}
		tmpl = newTmpl
	}

	return tokenRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		token := strings.TrimSpace(m[2 : len(m)-2])
		switch {
		case token == "FrontSide":
			return frontSide
		case strings.HasPrefix(token, "cloze:"):
			field := strings.TrimPrefix(token, "cloze:")
			return renderCloze(fields[field], activeCloze, reveal)
		case strings.Contains(token, ":"):
			// A filter we don't implement (e.g. {{type:Field}}, {{furigana:Field}}).
			// Fall back to the bare field so the card still renders instead of
			// silently dropping content.
			parts := strings.SplitN(token, ":", 2)
			return fields[parts[len(parts)-1]]
		default:
			return fields[token]
		}
	})
}

func renderCloze(text string, activeCloze int, reveal bool) string {
	return clozeRe.ReplaceAllStringFunc(text, func(m string) string {
		g := clozeRe.FindStringSubmatch(m)
		n, _ := strconv.Atoi(g[1])
		content, hint := g[2], g[3]

		if n != activeCloze {
			return content // other clozes: always shown, never highlighted
		}
		if reveal {
			return `<span class="cloze">` + content + `</span>`
		}
		label := "..."
		if hint != "" {
			label = hint
		}
		return `<span class="cloze">[` + label + `]</span>`
	})
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// SanitizeFilename strips path separators from an Anki media filename before
// it's used as an on-disk path component - apkg media filenames are
// arbitrary user/exporter-controlled strings.
func SanitizeFilename(name string) string {
	return filepath.Base(filepath.Clean("/" + name))
}
