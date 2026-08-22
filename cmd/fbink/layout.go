package main

import (
	"image"
	"image/color"
	"strings"

	// Aliased for the same reason as in fonts.go - "font FontType"
	// parameters elsewhere in this package would shadow an unaliased import.
	xfont "golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// styledWord is one whitespace-delimited token with an associated style.
// Phase 3a only ever produces StyleRegular words (via splitPlainWords);
// Phase 3c's Markdown walker feeds this same structure real Bold/Italic
// runs from a goldmark AST, reusing wrapWords/drawLines unchanged.
// isListItem marks a word as belonging to a Markdown list item, so its
// line always renders left-aligned regardless of the block's alignment -
// a centered or right-aligned bullet list reads badly, so this overrides
// whatever Align the caller requested for lines that came from a list.
// isBullet marks the list marker itself, which is what wrapWords measures
// to size the item's hanging indent.
type styledWord struct {
	text       string
	style      FontStyle
	isListItem bool
	isBullet   bool
	brk        breakKind
}

// breakKind distinguishes the two reasons a line can end early, because
// they get different amounts of space after them.
type breakKind int

const (
	breakNone breakKind = iota
	// breakLine is a newline inside one block of text - the soft breaks
	// that separate a card's headword, alternatives and example. Those
	// belong tight together, so nothing extra is added.
	breakLine
	// breakBlock ends a paragraph or a list item, and gets block_spacing
	// added after it so consecutive senses don't read as one run-on.
	breakBlock
)

// layoutLine is one wrapped, drawable line. An empty Words slice renders
// as a blank line (used for paragraph breaks). isListItem is set if any
// word on the line came from a list item (see styledWord). indent shifts
// the whole line right - nonzero only on the continuation lines of a list
// item, so a wrapped item's text stays in one column under itself instead
// of running back under its own bullet.
// endsBlock marks the last line of a paragraph or list item, which is
// where drawLines inserts the extra block spacing.
type layoutLine struct {
	words      []styledWord
	isListItem bool
	indent     fixed.Int26_6
	endsBlock  bool
}

// resolvedFace is what a faceResolver hands back for a given style: the
// face to measure/draw with, whether the caller should apply a synthetic
// double-strike bold (true only when no real bold font file was found -
// see resolveFontStyle in main.go), and the font's ligature substitutions
// (nil when the font has none, or when they're switched off in config).
type resolvedFace struct {
	Face      xfont.Face
	SynthBold bool
	Ligatures *ligatureTable
}

// shape returns the text as it must be both measured and drawn. Every
// measurement and every draw goes through here, so the two cannot drift
// apart: a ligature that shortens a word during drawing but not during
// wrapping would silently push lines past the right margin.
func (rf resolvedFace) shape(s string) string {
	return rf.Ligatures.apply(s)
}

// faceResolver returns the face to use for a given style.
type faceResolver func(style FontStyle) (resolvedFace, error)

// splitPlainWords turns plain text into an all-regular-style word list,
// splitting on whitespace. Explicit newlines in the source force a line
// break in the output rather than being treated as ordinary whitespace.
func splitPlainWords(text string) []styledWord {
	var words []styledWord
	rawLines := strings.Split(text, "\n")
	for i, line := range rawLines {
		for _, w := range strings.Fields(line) {
			words = append(words, styledWord{text: w, style: StyleRegular})
		}
		if i < len(rawLines)-1 {
			// Plain text has no block structure to speak of, so every
			// newline in it is just a newline.
			words = append(words, styledWord{text: "\n", brk: breakLine})
		}
	}
	return words
}

// wrapWords greedily wraps words into lines no wider than maxWidth,
// measuring real glyph advances via the resolved xfont.Face per word.
// A word whose text is exactly "\n" forces a line break (a hard break
// from the source, not a wrap point).
//
// List items get a hanging indent: the bullet sits at the left margin and
// every continuation line of that item is shifted right by the bullet's
// own width plus the space after it, so the item's text forms a single
// column. The indent is measured, not a constant, because the bullet is
// drawn in the same face as everything else and its width varies with the
// font and point size in play.
func wrapWords(words []styledWord, resolve faceResolver, maxWidth fixed.Int26_6) ([]layoutLine, error) {
	var lines []layoutLine
	var cur []styledWord
	var curWidth fixed.Int26_6
	// indent applies to the line currently being filled; hang is what the
	// *next* line inherits, which is how the first line of an item sits
	// flush left while its wrapped remainder hangs.
	var indent, hang fixed.Int26_6

	flush := func(endsBlock bool) {
		isList := false
		for _, w := range cur {
			if w.isListItem {
				isList = true
				break
			}
		}
		lines = append(lines, layoutLine{
			words: cur, isListItem: isList, indent: indent, endsBlock: endsBlock,
		})
		cur = nil
		curWidth = 0
		indent = hang
	}

	for _, w := range words {
		if w.brk != breakNone {
			// A break ends the list item too: whatever follows starts its
			// own item (with its own bullet) or leaves the list entirely.
			flush(w.brk == breakBlock)
			hang, indent = 0, 0
			continue
		}

		rf, err := resolve(w.style)
		if err != nil {
			return nil, err
		}
		wordWidth := xfont.MeasureString(rf.Face, rf.shape(w.text))
		space, _ := rf.Face.GlyphAdvance(' ')

		if w.isBullet {
			// An item always starts a line of its own, and sets the indent
			// its continuation lines will hang at.
			if len(cur) > 0 {
				flush(false)
			}
			hang = wordWidth + space
			indent = 0
		}

		addWidth := wordWidth
		if len(cur) > 0 {
			addWidth += space
		}

		if len(cur) > 0 && curWidth+addWidth > maxWidth-indent {
			flush(false)
			cur = []styledWord{w}
			curWidth = wordWidth
			continue
		}
		cur = append(cur, w)
		curWidth += addWidth
	}
	if len(cur) > 0 {
		// The final line closes whatever block it belongs to, but nothing
		// follows it, so the spacing is never actually drawn (see drawLines).
		flush(true)
	}
	return lines, nil
}

func lineWidth(line layoutLine, resolve faceResolver) (fixed.Int26_6, error) {
	var w fixed.Int26_6
	for i, word := range line.words {
		rf, err := resolve(word.style)
		if err != nil {
			return 0, err
		}
		if i > 0 {
			sp, _ := rf.Face.GlyphAdvance(' ')
			w += sp
		}
		w += xfont.MeasureString(rf.Face, rf.shape(word.text))
	}
	return w, nil
}

// drawLines renders wrapped lines into rect r, top to bottom, honoring the
// requested horizontal alignment per line. gray is the text color. ascent
// positions the first line's baseline correctly below r.Y - a baseline
// sits ascent pixels below the top of a line, not a full lineHeight down
// (that overshoots by roughly the font's descent, producing text that
// sits visibly too low - found via on-device testing). Lines whose
// baseline would fall past r's bottom edge are skipped rather than
// overflowing into whatever comes after r.
//
// blockSpace is extra vertical space inserted after a line that ends a
// paragraph or list item, so senses read as separate entries. It is never
// added after the last line: trailing space below the final block would
// push vertically centered content off centre for no visible gain.
func drawLines(c *Canvas, lines []layoutLine, r Rect, resolve faceResolver, gray uint8, lineHeight, ascent, blockSpace int, align Align) {
	if lineHeight <= 0 {
		lineHeight = 1
	}
	if blockSpace < 0 {
		blockSpace = 0
	}
	uniform := image.NewUniform(color.Gray{Y: gray})
	y := r.Y + ascent
	bottom := r.Y + r.H

	for i, line := range lines {
		if y > bottom {
			break
		}

		lineAlign := align
		if line.isListItem {
			lineAlign = AlignLeft // bullet lists always read left to right, never centered/right
		}

		// The indent eats into the line's usable width, so alignment is
		// computed against what's left of r rather than all of it.
		left := fixed.I(r.X) + line.indent
		width := fixed.I(r.W) - line.indent

		// x stays in 26.6 fixed point for the whole line. Rounding each
		// word's advance to a whole pixel instead would drift the line's
		// right end away from what lineWidth just measured (visible as
		// centered text sitting slightly off-center), and would throw away
		// the sub-pixel glyph placement the rasterizer already supports.
		x := left
		if lw, err := lineWidth(line, resolve); err == nil {
			switch lineAlign {
			case AlignCenter:
				x = left + (width-lw)/2
			case AlignRight:
				x = left + width - lw
			}
		}

		for i, w := range line.words {
			rf, err := resolve(w.style)
			if err != nil {
				continue
			}
			if i > 0 {
				sp, _ := rf.Face.GlyphAdvance(' ')
				x += sp
			}
			text := rf.shape(w.text)
			dot := fixed.Point26_6{X: x, Y: fixed.I(y)}
			d := &xfont.Drawer{Dst: c.Img, Src: uniform, Face: rf.Face, Dot: dot}
			if rf.SynthBold {
				// Faux-bold: draw twice, offset by one device pixel - a
				// common, low-risk technique when no real bold file exists.
				offsetDot := fixed.Point26_6{X: dot.X + fixed.I(1), Y: dot.Y}
				d2 := &xfont.Drawer{Dst: c.Img, Src: uniform, Face: rf.Face, Dot: offsetDot}
				d2.DrawString(text)
			}
			d.DrawString(text)
			x += xfont.MeasureString(rf.Face, text)
		}
		y += lineHeight
		if line.endsBlock && i < len(lines)-1 {
			y += blockSpace
		}
	}
}

// blockHeight is the vertical space lines occupy once block spacing is
// counted - the measurement drawLines will actually lay out to, and so what
// vertical centring has to use.
func blockHeight(lines []layoutLine, lineHeight, blockSpace int) int {
	h := len(lines) * lineHeight
	for i, line := range lines {
		if line.endsBlock && i < len(lines)-1 {
			h += blockSpace
		}
	}
	return h
}

// fitCount returns how many of lines, starting from the first, drawLines
// would actually draw into a box availH tall - the same "if y > bottom:
// break" criterion drawLines itself uses, so a page built from this count
// is guaranteed to render entirely within availH; no line ever lands half
// cut at a page boundary the way the old single-page hard clip could.
// Always at least 1 (given len(lines) > 0), so pagination can't get stuck
// producing empty pages for a box too short for even one line.
func fitCount(lines []layoutLine, lineHeight, blockSpace, ascent, availH int) int {
	y := ascent
	n := 0
	for i, line := range lines {
		if y > availH {
			break
		}
		n++
		y += lineHeight
		if line.endsBlock && i < len(lines)-1 {
			y += blockSpace
		}
	}
	if n == 0 && len(lines) > 0 {
		n = 1
	}
	return n
}

// paginate splits lines into successive groups, each sized by fitCount, so
// concatenating the groups' drawn output reproduces exactly what drawLines
// would draw unpaginated - pagination changes nothing about how content
// looks, only how much of it a single screen shows at once.
func paginate(lines []layoutLine, lineHeight, blockSpace, ascent, availH int) [][]layoutLine {
	if len(lines) == 0 {
		return [][]layoutLine{nil}
	}
	var pages [][]layoutLine
	rest := lines
	for len(rest) > 0 {
		n := fitCount(rest, lineHeight, blockSpace, ascent, availH)
		if n > len(rest) {
			n = len(rest)
		}
		pages = append(pages, rest[:n])
		rest = rest[n:]
	}
	return pages
}
