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
type styledWord struct {
	text       string
	style      FontStyle
	isListItem bool
}

// layoutLine is one wrapped, drawable line. An empty Words slice renders
// as a blank line (used for paragraph breaks). isListItem is set if any
// word on the line came from a list item (see styledWord).
type layoutLine struct {
	words      []styledWord
	isListItem bool
}

// resolvedFace is what a faceResolver hands back for a given style: the
// face to measure/draw with, and whether the caller should apply a
// synthetic double-strike bold (true only when no real bold font file was
// found - see resolveFontStyle in main.go).
type resolvedFace struct {
	Face      xfont.Face
	SynthBold bool
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
			words = append(words, styledWord{text: "\n"})
		}
	}
	return words
}

// wrapWords greedily wraps words into lines no wider than maxWidth,
// measuring real glyph advances via the resolved xfont.Face per word.
// A word whose text is exactly "\n" forces a line break (a hard break
// from the source, not a wrap point).
func wrapWords(words []styledWord, resolve faceResolver, maxWidth fixed.Int26_6) ([]layoutLine, error) {
	var lines []layoutLine
	var cur []styledWord
	var curWidth fixed.Int26_6

	flush := func() {
		isList := false
		for _, w := range cur {
			if w.isListItem {
				isList = true
				break
			}
		}
		lines = append(lines, layoutLine{words: cur, isListItem: isList})
		cur = nil
		curWidth = 0
	}

	for _, w := range words {
		if w.text == "\n" {
			flush()
			continue
		}

		rf, err := resolve(w.style)
		if err != nil {
			return nil, err
		}
		wordWidth := xfont.MeasureString(rf.Face, w.text)

		addWidth := wordWidth
		if len(cur) > 0 {
			sp, _ := rf.Face.GlyphAdvance(' ')
			addWidth += sp
		}

		if len(cur) > 0 && curWidth+addWidth > maxWidth {
			flush()
			cur = []styledWord{w}
			curWidth = wordWidth
			continue
		}
		cur = append(cur, w)
		curWidth += addWidth
	}
	if len(cur) > 0 {
		flush()
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
		w += xfont.MeasureString(rf.Face, word.text)
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
func drawLines(c *Canvas, lines []layoutLine, r Rect, resolve faceResolver, gray uint8, lineHeight, ascent int, align Align) {
	if lineHeight <= 0 {
		lineHeight = 1
	}
	uniform := image.NewUniform(color.Gray{Y: gray})
	y := r.Y + ascent
	bottom := r.Y + r.H

	for _, line := range lines {
		if y > bottom {
			break
		}

		lineAlign := align
		if line.isListItem {
			lineAlign = AlignLeft // bullet lists always read left to right, never centered/right
		}

		x := r.X
		if lw, err := lineWidth(line, resolve); err == nil {
			switch lineAlign {
			case AlignCenter:
				x = r.X + (r.W-lw.Round())/2
			case AlignRight:
				x = r.X + r.W - lw.Round()
			}
		}

		for i, w := range line.words {
			rf, err := resolve(w.style)
			if err != nil {
				continue
			}
			if i > 0 {
				sp, _ := rf.Face.GlyphAdvance(' ')
				x += sp.Round()
			}
			dot := fixed.P(x, y)
			d := &xfont.Drawer{Dst: c.Img, Src: uniform, Face: rf.Face, Dot: dot}
			if rf.SynthBold {
				// Faux-bold: draw twice, offset by one device pixel - a
				// common, low-risk technique when no real bold file exists.
				offsetDot := fixed.Point26_6{X: dot.X + fixed.I(1), Y: dot.Y}
				d2 := &xfont.Drawer{Dst: c.Img, Src: uniform, Face: rf.Face, Dot: offsetDot}
				d2.DrawString(w.text)
			}
			d.DrawString(w.text)
			x += xfont.MeasureString(rf.Face, w.text).Round()
		}
		y += lineHeight
	}
}
