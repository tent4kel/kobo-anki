package main

import (
	"encoding/binary"
	"math/rand"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
)

func testTable(t *testing.T) *ligatureTable {
	t.Helper()
	return &ligatureTable{
		subs: map[string]rune{
			"ff":  'ﬀ',
			"fi":  'ﬁ',
			"fl":  'ﬂ',
			"ffi": 'ﬃ',
		},
		starts: map[rune]bool{'f': true},
		max:    3,
	}
}

func TestLigatureApply(t *testing.T) {
	tbl := testTable(t)
	tests := []struct {
		in, want string
	}{
		{"fijn", "ﬁjn"},
		{"vlieg", "vlieg"},        // no trigger at all
		{"Auflage", "Auﬂage"},     // fl mid-word
		{"stoffig", "stoﬃg"},      // ffi must beat ff followed by i
		{"schaffen", "schaﬀen"},   // ff with no i after
		{"fluffig", "ﬂuﬃg"},       // two separate substitutions in one word
		{"offensief", "oﬀensief"}, // trailing f has nothing to ligate with
		{"", ""},
		{"f", "f"}, // a lone trigger rune must not index past the end
		{"ff", "ﬀ"},
		{"ffi", "ﬃ"},
	}
	for _, tc := range tests {
		if got := tbl.apply(tc.in); got != tc.want {
			t.Errorf("apply(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The longest match has to win, or "ffi" renders as an ff ligature awkwardly
// followed by a bare i - which is exactly the artifact ligatures exist to
// avoid.
func TestLigatureApplyPrefersLongestMatch(t *testing.T) {
	tbl := testTable(t)
	if got := tbl.apply("ffi"); got != "ﬃ" {
		t.Errorf("apply(\"ffi\") = %q, want the three-glyph ligature %q", got, "ﬃ")
	}
	// Without an ffi entry the same input must fall back to ff + i.
	short := &ligatureTable{
		subs:   map[string]rune{"ff": 'ﬀ'},
		starts: map[rune]bool{'f': true},
		max:    2,
	}
	if got := short.apply("ffi"); got != "ﬀi" {
		t.Errorf("apply(\"ffi\") = %q, want %q", got, "ﬀi")
	}
}

// A font with no usable ligatures yields a nil table, and every call site
// passes text through it unconditionally - so nil must be the identity.
func TestNilLigatureTableIsIdentity(t *testing.T) {
	var tbl *ligatureTable
	const s = "affige fluff"
	if got := tbl.apply(s); got != s {
		t.Errorf("nil table changed %q to %q", s, got)
	}
	empty := &ligatureTable{subs: map[string]rune{}, starts: map[rune]bool{}}
	if got := empty.apply(s); got != s {
		t.Errorf("empty table changed %q to %q", s, got)
	}
}

// Go Regular carries glyphs for U+FB01 and U+FB02 but declares no "liga"
// feature at all. If this returned substitutions, the implementation would
// be inventing them from cmap coverage instead of reading what the font
// actually asks for - and would ligate in fonts whose designer chose not to.
func TestBuildLigaturesRespectsAFontThatDeclaresNone(t *testing.T) {
	f, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tbl := buildLigatures(goregular.TTF, f); tbl != nil {
		t.Errorf("Go Regular declares no liga feature but got %d substitutions: %v",
			len(tbl.subs), tbl.subs)
	}
}

// The GSUB walk is the part most likely to be wrong in a way no font on
// this machine would reveal, so it gets a hand-built table with a known
// answer: f+i and f+f+i, reachable through a latn/liga feature.
func TestLigatureSubstsWalksGSUB(t *testing.T) {
	const fGID, iGID, fiGID, ffiGID = 10, 11, 20, 21

	got := ligatureSubsts(synthGSUBFont(fGID, iGID, fiGID, ffiGID))
	if len(got) != 2 {
		t.Fatalf("got %d substitutions, want 2: %+v", len(got), got)
	}

	byResult := map[int][]int{}
	for _, s := range got {
		var comps []int
		for _, c := range s.components {
			comps = append(comps, int(c))
		}
		byResult[int(s.result)] = comps
	}
	if c := byResult[fiGID]; len(c) != 2 || c[0] != fGID || c[1] != iGID {
		t.Errorf("fi ligature components = %v, want [%d %d]", c, fGID, iGID)
	}
	if c := byResult[ffiGID]; len(c) != 3 || c[0] != fGID || c[1] != fGID || c[2] != iGID {
		t.Errorf("ffi ligature components = %v, want [%d %d %d]", c, fGID, fGID, iGID)
	}
}

// Font files come off the user's device and are not validated anywhere.
// This runs where there is no way to surface an error, so a malformed table
// must yield no ligatures rather than a panic mid-review.
func TestLigatureSubstsSurvivesGarbage(t *testing.T) {
	full := synthGSUBFont(10, 11, 20, 21)

	// Every truncation of a valid font.
	for n := 0; n <= len(full); n++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on %d-byte prefix: %v", n, r)
				}
			}()
			ligatureSubsts(full[:n])
		}()
	}

	// And random corruption of it, which reaches offset combinations no
	// truncation produces.
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		b := append([]byte(nil), full...)
		for j := 0; j < 6; j++ {
			b[rng.Intn(len(b))] = byte(rng.Intn(256))
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on corrupted font (seed iteration %d): %v", i, r)
				}
			}()
			ligatureSubsts(b)
		}()
	}
}

func TestSafeBytesReadsOutOfRangeAsZero(t *testing.T) {
	d := safeBytes{0x00, 0x01, 0x02}
	if v := d.u16(2); v != 0 {
		t.Errorf("u16 straddling the end = %d, want 0", v)
	}
	if v := d.u16(-1); v != 0 {
		t.Errorf("u16 at a negative offset = %d, want 0", v)
	}
	if v := d.u32(0); v != 0 {
		t.Errorf("u32 past the end = %d, want 0", v)
	}
	if s := d.tag(1); s != "" {
		t.Errorf("tag past the end = %q, want empty", s)
	}
	if off := d.tableOffset("GSUB"); off != 0 {
		t.Errorf("tableOffset on a stub = %d, want 0", off)
	}
}

// synthGSUBFont builds the smallest font file that ligatureSubsts will walk:
// a table directory holding one GSUB table, whose latn script points at a
// "liga" feature, pointing at one LigatureSubst lookup with two entries.
func synthGSUBFont(fGID, iGID, fiGID, ffiGID int) []byte {
	u16 := func(v int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(v)) }

	// Offsets below are relative to the start of GSUB; see the OpenType
	// GSUB spec for the shape of each of these tables.
	const (
		scriptList = 10
		script     = 18
		langSys    = 22
		featList   = 30
		feature    = 38
		lookupList = 44
		lookup     = 48
		ligSubst   = 56
		ligSet     = 64
		lig2       = 70 // f + i
		lig3       = 76 // f + f + i
		coverage   = 84
		gsubLen    = 90
	)

	var g []byte
	add := func(bs ...[]byte) {
		for _, b := range bs {
			g = append(g, b...)
		}
	}
	add(u16(1), u16(0), u16(scriptList), u16(featList), u16(lookupList)) // header
	add(u16(1), []byte("latn"), u16(script-scriptList))                  // ScriptList
	add(u16(langSys-script), u16(0))                                     // Script
	add(u16(0), u16(0xFFFF), u16(1), u16(0))                             // LangSys
	add(u16(1), []byte("liga"), u16(feature-featList))                   // FeatureList
	add(u16(0), u16(1), u16(0))                                          // Feature
	add(u16(1), u16(lookup-lookupList))                                  // LookupList
	add(u16(4), u16(0), u16(1), u16(ligSubst-lookup))                    // Lookup (type 4)
	add(u16(1), u16(coverage-ligSubst), u16(1), u16(ligSet-ligSubst))    // LigatureSubst
	add(u16(2), u16(lig2-ligSet), u16(lig3-ligSet))                      // LigatureSet
	add(u16(fiGID), u16(2), u16(iGID))                                   // Ligature: f+i
	add(u16(ffiGID), u16(3), u16(fGID), u16(iGID))                       // Ligature: f+f+i
	add(u16(1), u16(1), u16(fGID))                                       // Coverage
	if len(g) != gsubLen {
		panic("synthGSUBFont: layout constants disagree with what was written")
	}

	const gsubStart = 12 + 16 // one record in the table directory
	var font []byte
	font = append(font, 0x00, 0x01, 0x00, 0x00) // sfntVersion
	font = append(font, u16(1)...)              // numTables
	font = append(font, u16(0)...)              // searchRange
	font = append(font, u16(0)...)              // entrySelector
	font = append(font, u16(0)...)              // rangeShift
	font = append(font, []byte("GSUB")...)
	font = binary.BigEndian.AppendUint32(font, 0) // checksum
	font = binary.BigEndian.AppendUint32(font, gsubStart)
	font = binary.BigEndian.AppendUint32(font, gsubLen)
	return append(font, g...)
}
