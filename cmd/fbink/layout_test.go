package main

import (
	"image"
	"strings"
	"testing"

	xfont "golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// testResolve hands every style the same fixed-width bitmap face, so line
// breaks and indents come out as exact multiples of testAdvance instead of
// depending on whatever fonts happen to be installed on the build machine.
const testAdvance = 7 // basicfont.Face7x13 advances 7px for every glyph

func testResolve(FontStyle) (resolvedFace, error) {
	return resolvedFace{Face: basicfont.Face7x13}, nil
}

// px converts a count of glyphs to the width they occupy.
func px(glyphs int) fixed.Int26_6 { return fixed.I(glyphs * testAdvance) }

// lineText renders a layoutLine back to a string, so assertions can talk
// about what a line says rather than about slices of styledWord.
func lineText(l layoutLine) string {
	var parts []string
	for _, w := range l.words {
		parts = append(parts, w.text)
	}
	return strings.Join(parts, " ")
}

func lineTexts(lines []layoutLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = lineText(l)
	}
	return out
}

func indents(lines []layoutLine) []fixed.Int26_6 {
	out := make([]fixed.Int26_6, len(lines))
	for i, l := range lines {
		out[i] = l.indent
	}
	return out
}

func wrap(t *testing.T, md string, glyphs int) []layoutLine {
	t.Helper()
	lines, err := wrapWords(markdownToWords([]byte(md)), testResolve, px(glyphs))
	if err != nil {
		t.Fatalf("wrapWords: %v", err)
	}
	return lines
}

// The bullet has to survive as a word of its own: the hanging indent is
// measured from it, and a bullet concatenated onto the first word (the old
// behaviour) cannot be measured separately from that word.
func TestListBulletIsItsOwnWord(t *testing.T) {
	words := markdownToWords([]byte("- alpha beta\n- gamma"))

	var bullets int
	for _, w := range words {
		if w.isBullet {
			bullets++
			if w.text != bulletMarker {
				t.Errorf("bullet word text = %q, want %q", w.text, bulletMarker)
			}
			if !w.isListItem {
				t.Error("bullet word is not marked as list content")
			}
		}
		if strings.HasPrefix(w.text, bulletMarker+" ") {
			t.Errorf("word %q still has the marker baked into it", w.text)
		}
	}
	if bullets != 2 {
		t.Errorf("got %d bullet words for a 2-item list, want 2", bullets)
	}
}

// The whole point of the change: an item's first line starts at the margin
// and every wrapped continuation of that item hangs at the bullet's width.
func TestListItemWrapsWithHangingIndent(t *testing.T) {
	// "- alpha beta gamma" in a 60px column: "- alpha" fits (49px), "beta"
	// does not (84px), so the item wraps twice.
	lines := wrap(t, "- alpha beta gamma", 60/testAdvance)

	wantText := []string{"- alpha", "beta", "gamma"}
	if got := lineTexts(lines); !equalStrings(got, wantText) {
		t.Fatalf("lines = %q, want %q", got, wantText)
	}

	hang := px(2) // width of "-" plus the space after it
	want := []fixed.Int26_6{0, hang, hang}
	if got := indents(lines); !equalIndents(got, want) {
		t.Errorf("indents = %v, want %v", got, want)
	}
}

// The indent has to come out of the line's usable width, or a hanging line
// would be allowed to run past the right edge by exactly the indent.
func TestHangingIndentNarrowsTheWrapWidth(t *testing.T) {
	// Column fits 9 glyphs. Unindented that is "ab cdef" (7); the same
	// words on a hanging line only have 7 glyphs of room, so "cdef" must
	// move down rather than sit beside "ab".
	lines := wrap(t, "- xxxxxx ab cdef", 9)

	for _, l := range lines {
		width, err := lineWidth(l, testResolve)
		if err != nil {
			t.Fatalf("lineWidth: %v", err)
		}
		if width+l.indent > px(9) {
			t.Errorf("line %q occupies %v+%v, past the %v column",
				lineText(l), l.indent, width, px(9))
		}
	}
}

// A hard break ends the item, so the next item's bullet starts flush left
// again instead of inheriting the previous item's hang.
func TestEachListItemStartsFlushLeft(t *testing.T) {
	lines := wrap(t, "- alpha\n- beta", 40)

	wantText := []string{"- alpha", "- beta"}
	if got := lineTexts(lines); !equalStrings(got, wantText) {
		t.Fatalf("lines = %q, want %q", got, wantText)
	}
	for i, l := range lines {
		if l.indent != 0 {
			t.Errorf("line %d (%q) indent = %v, want 0", i, lineText(l), l.indent)
		}
	}
}

// Text that follows a list must not keep hanging.
func TestIndentResetsAfterTheList(t *testing.T) {
	lines := wrap(t, "- alpha beta gamma delta\n\nplain tail text", 10)

	tail := -1
	for i, l := range lines {
		if strings.Contains(lineText(l), "plain") {
			tail = i
			break
		}
	}
	if tail < 0 {
		t.Fatalf("trailing paragraph missing from %q", lineTexts(lines))
	}
	for _, l := range lines[tail:] {
		if l.indent != 0 {
			t.Errorf("trailing line %q indent = %v, want 0", lineText(l), l.indent)
		}
		if l.isListItem {
			t.Errorf("trailing line %q is marked as list content", lineText(l))
		}
	}
}

// Ordinary prose is untouched by any of this.
func TestPlainTextHasNoIndent(t *testing.T) {
	lines := wrap(t, "one two three four five six", 10)
	if len(lines) < 2 {
		t.Fatalf("expected the text to wrap, got %q", lineTexts(lines))
	}
	for i, l := range lines {
		if l.indent != 0 {
			t.Errorf("line %d (%q) indent = %v, want 0", i, lineText(l), l.indent)
		}
	}
}

// drawLines is where the indent turns into pixels, and it is easy to
// compute the right indent and then draw at the wrong x. Comparing against
// the same lines drawn with their indents zeroed isolates the shift from
// each glyph's own left side bearing, which would otherwise show up in any
// absolute pixel expectation.
func TestDrawLinesHangsContinuationInk(t *testing.T) {
	lines := wrap(t, "- alpha beta gamma", 60/testAdvance)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %q", lineTexts(lines))
	}

	flat := make([]layoutLine, len(lines))
	for i, l := range lines {
		l.indent = 0
		flat[i] = l
	}

	hung := renderInkColumns(t, lines)
	base := renderInkColumns(t, flat)

	for i := range lines {
		want := lines[i].indent.Round()
		if got := hung[i] - base[i]; got != want {
			t.Errorf("line %d (%q) drawn %dpx right of the margin, want %d",
				i, lineText(lines[i]), got, want)
		}
	}
	if lines[1].indent == 0 {
		t.Fatal("continuation line has no indent, so this test proved nothing")
	}
}

// renderInkColumns draws lines onto a fresh canvas and returns the leftmost
// inked pixel column of each.
func renderInkColumns(t *testing.T, lines []layoutLine) []int {
	t.Helper()
	h := basicfont.Face7x13.Height
	c := NewCanvas(400, len(lines)*h+h)
	c.Clear(0xFF)
	drawLines(c, lines, Rect{X: 10, Y: 0, W: 380, H: c.Img.Bounds().Dy()}, testResolve,
		0x00, h, basicfont.Face7x13.Ascent, 0, AlignLeft)

	cols := make([]int, len(lines))
	for i := range lines {
		cols[i] = firstInkColumn(c, i*h, (i+1)*h)
		if cols[i] < 0 {
			t.Fatalf("line %d (%q) drew nothing", i, lineText(lines[i]))
		}
	}
	return cols
}

// firstInkColumn returns the leftmost x with a non-white pixel in the
// horizontal band [yTop, yBottom), or -1 if the band is blank.
func firstInkColumn(c *Canvas, yTop, yBottom int) int {
	b := c.Img.Bounds()
	for x := b.Min.X; x < b.Max.X; x++ {
		for y := yTop; y < yBottom && y < b.Max.Y; y++ {
			if c.Img.GrayAt(x, y).Y != 0xFF {
				return x
			}
		}
	}
	return -1
}

// fracFace gives every glyph a half-pixel-wider advance, which is what real
// scaled outline fonts do and what the fixed-width test face does not. It
// exists so a test can tell whether drawLines accumulates x in 26.6 fixed
// point or rounds each word to a whole pixel as it goes.
type fracFace struct {
	xfont.Face
	extra fixed.Int26_6
}

func (f fracFace) GlyphAdvance(r rune) (fixed.Int26_6, bool) {
	a, ok := f.Face.GlyphAdvance(r)
	return a + f.extra, ok
}

func (f fracFace) Glyph(dot fixed.Point26_6, r rune) (image.Rectangle, image.Image, image.Point, fixed.Int26_6, bool) {
	dr, mask, maskp, a, ok := f.Face.Glyph(dot, r)
	return dr, mask, maskp, a + f.extra, ok
}

// A line has to end where lineWidth said it would. Rounding each word's
// advance up to a whole pixel while drawing accumulates a fraction of a
// pixel per word, so a long line drifts past its own measured width -
// which is what positions centered and right-aligned text.
func TestDrawnLineEndsWhereItWasMeasured(t *testing.T) {
	face := fracFace{Face: basicfont.Face7x13, extra: 32} // +0.5px per glyph
	resolve := func(FontStyle) (resolvedFace, error) { return resolvedFace{Face: face}, nil }

	words := splitPlainWords("aa bb cc dd ee ff gg hh ii jj kk ll mm nn oo pp")
	lines, err := wrapWords(words, resolve, fixed.I(10000)) // one long line
	if err != nil {
		t.Fatalf("wrapWords: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected a single line, got %d", len(lines))
	}

	lw, err := lineWidth(lines[0], resolve)
	if err != nil {
		t.Fatalf("lineWidth: %v", err)
	}

	const originX = 10
	h := basicfont.Face7x13.Height
	draw := func(ls []layoutLine) *Canvas {
		c := NewCanvas(600, 2*h)
		c.Clear(0xFF)
		drawLines(c, ls, Rect{X: originX, Y: 0, W: 580, H: 2 * h}, resolve,
			0x00, h, basicfont.Face7x13.Ascent, 0, AlignLeft)
		return c
	}

	got := lastInkColumn(draw(lines), 0, h)

	// Reference: the final word alone, placed at the x the measurement
	// implies. Its right side bearing is then identical to the full line's,
	// so any difference is accumulated drift and nothing else.
	last := lines[0].words[len(lines[0].words)-1]
	lastWidth := xfont.MeasureString(face, last.text)
	ref := []layoutLine{{words: []styledWord{last}, indent: fixed.I(originX) + lw - lastWidth - fixed.I(originX)}}
	want := lastInkColumn(draw(ref), 0, h)

	if got != want {
		t.Errorf("line ends at x=%d but its measured width puts the last word's end at x=%d (drift %dpx)",
			got, want, got-want)
	}
}

// lastInkColumn returns the rightmost x with a non-white pixel in the
// horizontal band [yTop, yBottom), or -1 if the band is blank.
func lastInkColumn(c *Canvas, yTop, yBottom int) int {
	b := c.Img.Bounds()
	for x := b.Max.X - 1; x >= b.Min.X; x-- {
		for y := yTop; y < yBottom && y < b.Max.Y; y++ {
			if c.Img.GrayAt(x, y).Y != 0xFF {
				return x
			}
		}
	}
	return -1
}

// Kerning is not something this package implements, but it is something it
// silently depends on: wrapWords measures with MeasureString and drawLines
// draws with a Drawer, and both apply Face.Kern. If a future face swap ever
// made the two disagree, centered and right-aligned lines would drift.
func TestMeasuredWidthMatchesDrawnWidth(t *testing.T) {
	face := basicfont.Face7x13
	const s = "AVATar Wave"

	measured := xfont.MeasureString(face, s)

	var sum fixed.Int26_6
	rs := []rune(s)
	for i, r := range rs {
		a, ok := face.GlyphAdvance(r)
		if !ok {
			t.Fatalf("no advance for %q", r)
		}
		sum += a
		if i > 0 {
			sum += face.Kern(rs[i-1], r)
		}
	}
	if measured != sum {
		t.Errorf("MeasureString = %v but advances+kern = %v", measured, sum)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalIndents(a, b []fixed.Int26_6) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Ligature substitution changes a word's width, so it has to reach the
// wrapping measurement and the drawing identically. If only drawing shaped
// the text, lines would be measured too wide and wrap early; if only
// measuring did, they would overrun the right margin.
func TestLigaturesReachMeasuringAndDrawingAlike(t *testing.T) {
	// One substitution collapsing two glyphs into one, on a face whose
	// every glyph is the same width - so the expected widths are exact.
	tbl := &ligatureTable{
		subs:   map[string]rune{"fi": 'ﬁ'},
		starts: map[rune]bool{'f': true},
		max:    2,
	}
	ligate := func(FontStyle) (resolvedFace, error) {
		return resolvedFace{Face: basicfont.Face7x13, Ligatures: tbl}, nil
	}

	lines, err := wrapWords(splitPlainWords("fijn"), ligate, fixed.I(1000))
	if err != nil {
		t.Fatalf("wrapWords: %v", err)
	}
	got, err := lineWidth(lines[0], ligate)
	if err != nil {
		t.Fatalf("lineWidth: %v", err)
	}
	// "fijn" is 4 glyphs unshaped, 3 once f+i becomes one glyph.
	if want := px(3); got != want {
		t.Errorf("measured width of %q = %v, want %v (the ligature was not measured)",
			"fijn", got, want)
	}

	// And the drawn result must occupy that same width, not the unshaped one.
	h := basicfont.Face7x13.Height
	c := NewCanvas(200, 2*h)
	c.Clear(0xFF)
	drawLines(c, lines, Rect{X: 0, Y: 0, W: 200, H: 2 * h}, ligate,
		0x00, h, basicfont.Face7x13.Ascent, 0, AlignLeft)
	if end := lastInkColumn(c, 0, h); end >= px(4).Round() {
		t.Errorf("drawn text reaches x=%d, past the %dpx the ligature should have saved",
			end, px(3).Round())
	}
}

// blocks returns which wrapped lines close a paragraph or list item.
func blockEnds(lines []layoutLine) []bool {
	out := make([]bool, len(lines))
	for i, l := range lines {
		out[i] = l.endsBlock
	}
	return out
}

// Markdown convention: a blank line starts a new paragraph and that is what
// earns the extra space. A single newline is a soft break and must not.
func TestOnlyBlockEndsEarnSpacing(t *testing.T) {
	// The card shape kobo-vocab emits: three paragraphs, and inside the
	// first one a soft break that has to stay tight.
	lines := wrap(t, "**Einfluss**\nzweite Zeile\n\nEinwirkung\n\n*Beispiel*", 40)

	got := lineTexts(lines)
	want := []string{"Einfluss", "zweite Zeile", "Einwirkung", "Beispiel"}
	if !equalStrings(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	// Only the paragraph ends: not the soft break after "Einfluss", and not
	// the final line (nothing follows it to be spaced from).
	wantEnds := []bool{false, true, true, true}
	for i := range lines {
		if lines[i].endsBlock != wantEnds[i] {
			t.Errorf("line %d (%q) endsBlock = %v, want %v",
				i, lineText(lines[i]), lines[i].endsBlock, wantEnds[i])
		}
	}
}

// Each list item is its own block, so consecutive senses get separated.
// A wrapped continuation inside one item must not.
func TestListItemsEndBlocksButWrapsDoNot(t *testing.T) {
	lines := wrap(t, "- alpha beta gamma\n- delta", 60/testAdvance)

	if got, want := lineTexts(lines), []string{"- alpha", "beta", "gamma", "- delta"}; !equalStrings(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	wantEnds := []bool{false, false, true, true}
	for i := range lines {
		if lines[i].endsBlock != wantEnds[i] {
			t.Errorf("line %d (%q) endsBlock = %v, want %v",
				i, lineText(lines[i]), lines[i].endsBlock, wantEnds[i])
		}
	}
}

// The spacing has to reach the pixels, and blockHeight has to predict
// exactly what drawLines lays out - vertical centring depends on the two
// agreeing.
func TestBlockSpacingMovesLinesAndIsMeasured(t *testing.T) {
	lines := wrap(t, "one\n\ntwo\n\nthree", 40)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %q", lineTexts(lines))
	}
	if got, want := blockEnds(lines), []bool{true, true, true}; len(got) != len(want) {
		t.Fatalf("blockEnds = %v", got)
	}

	h := basicfont.Face7x13.Height
	const space = 6

	render := func(blockSpace int) []int {
		c := NewCanvas(200, 8*h)
		c.Clear(0xFF)
		drawLines(c, lines, Rect{X: 0, Y: 0, W: 200, H: 8 * h}, testResolve,
			0x00, h, basicfont.Face7x13.Ascent, blockSpace, AlignLeft)
		var rows []int
		for y := 0; y < 8*h; y++ {
			for x := 0; x < 200; x++ {
				if c.Img.GrayAt(x, y).Y != 0xFF {
					rows = append(rows, y)
					break
				}
			}
		}
		return rows
	}

	tight, spaced := render(0), render(space)
	if len(tight) == 0 || len(spaced) == 0 {
		t.Fatal("nothing drew")
	}
	// Each of the two gaps pushes everything below it down by `space`, so
	// the last inked row moves by exactly twice that.
	if got, want := spaced[len(spaced)-1]-tight[len(tight)-1], 2*space; got != want {
		t.Errorf("last line moved down %dpx, want %d", got, want)
	}
	// Trailing spacing after the final block would show up as extra height.
	if got, want := blockHeight(lines, h, space), 3*h+2*space; got != want {
		t.Errorf("blockHeight = %d, want %d (no spacing after the last block)", got, want)
	}
}
