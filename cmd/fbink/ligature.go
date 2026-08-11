package main

import (
	"encoding/binary"
	"strings"

	"golang.org/x/image/font/sfnt"
)

// Ligatures, without a shaping engine.
//
// golang.org/x/image rasterizes outlines but does no GSUB shaping, so "fi"
// is drawn as an f followed by an i and the font's ligature glyph is never
// reached. A full fix means moving the whole measure/draw path onto a
// shaper (go-text/typesetting); this gets the standard Latin ligatures
// without that, by exploiting a property of the Unicode presentation-forms
// block: for text fonts, every ligature the "liga" feature declares also
// has its own codepoint in U+FB00..U+FB06, and so is reachable through the
// rune-keyed font.Face API the rest of this package already uses.
//
// The substitutions are read out of the font's own GSUB table rather than
// hardcoded, so a font that declares no ligatures gets none, and a font
// whose ligature glyph has no codepoint is simply left alone instead of
// being sent to a wrong glyph. What this does not do is general shaping:
// no contextual alternates, no mark positioning, no complex scripts, and
// no ligature whose result glyph exists only behind a glyph index.
//
// One typographic caveat this cannot decide on its own: German suppresses
// ligatures across morpheme boundaries ("Auflage" is auf+lage, and should
// not carry an fl ligature), and Turkish must not ligate fi at all.
// Deciding that needs a hyphenation dictionary, so the escape hatch is the
// "ligatures" config key - see anki-fbink.conf.example.

// presentationForms are the codepoints a Latin ligature glyph can be
// reached by. Reverse-mapping a glyph index to a rune has no API, so
// instead each of these is mapped forward once and the result indexed.
// The list is the Latin block of Unicode's Alphabetic Presentation Forms,
// which is exactly what a text font's "liga" feature covers.
var presentationForms = []rune{
	'ﬀ', // ff
	'ﬁ', // fi
	'ﬂ', // fl
	'ﬃ', // ffi
	'ﬄ', // ffl
	'ﬅ', // long s + t
	'ﬆ', // st
}

// ligatureTable maps a rune sequence to the single rune that renders it as
// one glyph. A nil table substitutes nothing, which is what every caller
// gets for a font with no usable ligatures.
type ligatureTable struct {
	subs   map[string]rune
	starts map[rune]bool // first rune of every source sequence
	max    int           // longest source sequence, in runes
}

// apply rewrites s with the font's ligature substitutions, preferring the
// longest match at each position so "ffi" wins over "ff" followed by "i".
func (t *ligatureTable) apply(s string) string {
	if t == nil || len(t.subs) == 0 {
		return s
	}
	rs := []rune(s)
	// Nothing to do for the overwhelmingly common case of a word with no
	// ligature trigger in it; checking first keeps this off the hot path
	// of every measured word on every screen.
	if !t.mayContain(rs) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(rs); {
		matched := false
		for n := min(t.max, len(rs)-i); n >= 2; n-- {
			if r, ok := t.subs[string(rs[i:i+n])]; ok {
				b.WriteRune(r)
				i += n
				matched = true
				break
			}
		}
		if !matched {
			b.WriteRune(rs[i])
			i++
		}
	}
	return b.String()
}

// mayContain reports whether any substitution could possibly fire, by
// testing only the first rune of each source sequence.
func (t *ligatureTable) mayContain(rs []rune) bool {
	for i := 0; i < len(rs)-1; i++ {
		if t.starts[rs[i]] {
			return true
		}
	}
	return false
}

// buildLigatures reads the font's GSUB "liga" feature and keeps only those
// substitutions whose result glyph is reachable as a rune. data is the raw
// font file; f is the same font already parsed, used for cmap lookups.
func buildLigatures(data []byte, f *sfnt.Font) *ligatureTable {
	subs := ligatureSubsts(data)
	if len(subs) == 0 {
		return nil
	}

	var buf sfnt.Buffer
	// Which glyph does each presentation form resolve to, and which rune
	// does each component glyph come from? Both directions are needed: the
	// GSUB table speaks in glyph indices, the font.Face API speaks in runes.
	byGlyph := map[sfnt.GlyphIndex]rune{}
	for _, r := range presentationForms {
		if g, err := f.GlyphIndex(&buf, r); err == nil && g != 0 {
			byGlyph[g] = r
		}
	}
	if len(byGlyph) == 0 {
		return nil
	}
	// Components are always plain letters for the Latin liga set; map the
	// ASCII letters once and reject any substitution mentioning a glyph
	// outside that set rather than guessing at what rune produced it.
	runeOf := map[sfnt.GlyphIndex]rune{}
	for r := rune('A'); r <= 'z'; r++ {
		if g, err := f.GlyphIndex(&buf, r); err == nil && g != 0 {
			if _, taken := runeOf[g]; !taken {
				runeOf[g] = r
			}
		}
	}

	t := &ligatureTable{subs: map[string]rune{}, starts: map[rune]bool{}}
	for _, s := range subs {
		result, ok := byGlyph[s.result]
		if !ok {
			continue // ligature glyph has no codepoint; unreachable here
		}
		var src []rune
		for _, c := range s.components {
			r, ok := runeOf[c]
			if !ok {
				src = nil
				break
			}
			src = append(src, r)
		}
		if len(src) < 2 {
			continue
		}
		t.subs[string(src)] = result
		t.starts[src[0]] = true
		if len(src) > t.max {
			t.max = len(src)
		}
	}
	if len(t.subs) == 0 {
		return nil
	}
	return t
}

// ligatureSubst is one LigatureSubst entry: a run of component glyphs that
// the font replaces with a single result glyph.
type ligatureSubst struct {
	components []sfnt.GlyphIndex
	result     sfnt.GlyphIndex
}

// ligatureSubsts pulls every GSUB lookup-type-4 substitution reachable from
// a "liga" feature. Everything here is bounds-checked and gives up quietly:
// this runs on a device with no way to report a malformed font, and text
// without ligatures is a far better outcome than a crash mid-review.
func ligatureSubsts(data []byte) []ligatureSubst {
	d := safeBytes(data)
	base := d.tableOffset("GSUB")
	if base == 0 {
		return nil
	}

	// Every count in GSUB is a uint16, so nested loops over corrupted counts
	// can describe billions of iterations. Bounding each loop separately
	// invites missing one; instead all of them spend from a single budget,
	// generous next to any real font's liga feature (a few dozen entries
	// behind a handful of tables) and small next to a hang on the device.
	budget := 1 << 18
	spend := func() bool {
		budget--
		return budget > 0
	}
	scriptList := base + d.u16(base+4)
	featureList := base + d.u16(base+6)
	lookupList := base + d.u16(base+8)

	// Lookup indices reachable from a "liga" feature, under any script:
	// unlike kerning, the Latin ligature set is not script-sensitive here.
	wanted := map[int]bool{}
	for i := 0; i < d.u16(scriptList) && spend(); i++ {
		script := scriptList + d.u16(scriptList+2+i*6+4)
		def := d.u16(script)
		if def == 0 {
			continue
		}
		langSys := script + def
		for j := 0; j < d.u16(langSys+4) && spend(); j++ {
			fi := d.u16(langSys + 6 + j*2)
			rec := featureList + 2 + fi*6
			if d.tag(rec) != "liga" {
				continue
			}
			feature := featureList + d.u16(rec+4)
			for k := 0; k < d.u16(feature+2) && spend(); k++ {
				wanted[d.u16(feature+4+k*2)] = true
			}
		}
	}

	var out []ligatureSubst
	for li := range wanted {
		lookup := lookupList + d.u16(lookupList+2+li*2)
		if d.u16(lookup) != 4 { // LigatureSubst
			continue
		}
		for s := 0; s < d.u16(lookup+4) && spend(); s++ {
			sub := lookup + d.u16(lookup+6+s*2)
			if d.u16(sub) != 1 {
				continue
			}
			cov := d.coverage(sub + d.u16(sub+2))
			for si := 0; si < d.u16(sub+4) && si < len(cov) && spend(); si++ {
				set := sub + d.u16(sub+6+si*2)
				for k := 0; k < d.u16(set) && spend(); k++ {
					lt := set + d.u16(set+2+k*2)
					count := d.u16(lt + 2)
					if count < 2 || count > 8 {
						continue
					}
					comps := []sfnt.GlyphIndex{sfnt.GlyphIndex(cov[si])}
					for c := 1; c < count; c++ {
						comps = append(comps, sfnt.GlyphIndex(d.u16(lt+2+c*2)))
					}
					out = append(out, ligatureSubst{
						components: comps,
						result:     sfnt.GlyphIndex(d.u16(lt)),
					})
				}
			}
		}
	}
	return out
}

// safeBytes is a font file with out-of-range reads defined to return zero,
// so a truncated or hostile table walks off into empty offsets and yields
// nothing instead of panicking.
type safeBytes []byte

func (d safeBytes) u16(off int) int {
	if off < 0 || off+2 > len(d) {
		return 0
	}
	return int(binary.BigEndian.Uint16(d[off:]))
}

func (d safeBytes) u32(off int) int {
	if off < 0 || off+4 > len(d) {
		return 0
	}
	return int(binary.BigEndian.Uint32(d[off:]))
}

func (d safeBytes) tag(off int) string {
	if off < 0 || off+4 > len(d) {
		return ""
	}
	return string(d[off : off+4])
}

// tableOffset finds a top-level table in the font's table directory.
func (d safeBytes) tableOffset(tag string) int {
	if len(d) < 12 || d.tag(0) == "ttcf" {
		return 0 // collections are not something this app ever loads
	}
	for i := 0; i < d.u16(4); i++ {
		rec := 12 + i*16
		if d.tag(rec) == tag {
			return d.u32(rec + 8)
		}
	}
	return 0
}

// maxGlyphs bounds how much a Coverage table may claim to cover. A font
// cannot have more than 65536 glyphs, so anything past this is corruption -
// and without the bound, a garbled range-format table asks for billions of
// entries and hangs the reader instead of failing.
const maxGlyphs = 1 << 16

// coverage returns the glyph indices a Coverage table lists, in the order
// the table defines - that order is what indexes the parallel arrays.
func (d safeBytes) coverage(off int) []int {
	var out []int
	switch d.u16(off) {
	case 1:
		for i := 0; i < d.u16(off+2) && len(out) < maxGlyphs; i++ {
			out = append(out, d.u16(off+4+i*2))
		}
	case 2:
		for i := 0; i < d.u16(off+2) && len(out) < maxGlyphs; i++ {
			rec := off + 4 + i*6
			start, end := d.u16(rec), d.u16(rec+2)
			if end < start {
				continue
			}
			for g := start; g <= end && len(out) < maxGlyphs; g++ {
				out = append(out, g)
			}
		}
	}
	return out
}
