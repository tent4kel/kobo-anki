package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/image/math/fixed"
)

// markdownToWords parses src as Markdown and walks the resulting AST into a
// flat []styledWord, reusing the same wrapWords/drawLines engine that
// fbinkTextRect uses for plain text - only the input side differs (parsed
// Markdown structure instead of a naive whitespace split). Only the
// Markdown kobo-vocab actually emits is handled: **bold**/*italic* (nested
// arbitrarily), "- " tight lists, and line breaks. List items are wrapped
// with a hanging indent - see bulletMarker and wrapWords.
func markdownToWords(src []byte) []styledWord {
	doc := goldmark.DefaultParser().Parse(text.NewReader(src))
	w := &mdWalker{source: src}
	w.walkBlock(doc)
	return trimBreaks(w.words)
}

type mdWalker struct {
	source      []byte
	words       []styledWord
	boldDepth   int
	italicDepth int
}

func (w *mdWalker) style() FontStyle {
	switch {
	case w.boldDepth > 0 && w.italicDepth > 0:
		return StyleBoldItalic
	case w.boldDepth > 0:
		return StyleBold
	case w.italicDepth > 0:
		return StyleItalic
	default:
		return StyleRegular
	}
}

func (w *mdWalker) walkBlock(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			// Both cases end a block: a Paragraph is one, and a TextBlock is
			// the body of a tight list item. Newlines *within* either are
			// emitted by walkInline as plain line breaks instead.
			w.walkInline(c)
			w.words = append(w.words, styledWord{text: "\n", brk: breakBlock})
		case *ast.List:
			w.walkList(node)
		default:
			w.walkBlock(c)
		}
	}
}

// bulletMarker is the list marker drawn in place of Markdown's "- ". It is
// emitted as a word of its own rather than glued onto the item's first
// word, so wrapWords can measure it and hang the item's wrapped lines at
// its width (see wrapWords); a baked-in prefix is unmeasurable once it has
// been concatenated, which is why wrapped items used to run back under
// their own bullet.
const bulletMarker = "-"

func (w *mdWalker) walkList(list *ast.List) {
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		before := len(w.words)
		w.walkBlock(item)
		if len(w.words) == before {
			continue
		}
		// Mark every word from this item as list content, which forces its
		// line to render left-aligned (see drawLines).
		for i := before; i < len(w.words); i++ {
			w.words[i].isListItem = true
		}
		w.words = slices.Insert(w.words, before, styledWord{
			text:       bulletMarker,
			style:      StyleRegular,
			isListItem: true,
			isBullet:   true,
		})
	}
}

func (w *mdWalker) walkInline(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {
		case *ast.Text:
			segText := string(node.Segment.Value(w.source))
			for _, word := range strings.Fields(segText) {
				w.words = append(w.words, styledWord{text: word, style: w.style()})
			}
			if node.SoftLineBreak() || node.HardLineBreak() {
				// A newline inside a paragraph - what separates a card's
				// headword from its alternatives and example. Those stay
				// tight; only block ends get extra space.
				w.words = append(w.words, styledWord{text: "\n", brk: breakLine})
			}
		case *ast.Emphasis:
			if node.Level >= 2 {
				w.boldDepth++
			} else {
				w.italicDepth++
			}
			w.walkInline(c)
			if node.Level >= 2 {
				w.boldDepth--
			} else {
				w.italicDepth--
			}
		default:
			w.walkInline(c)
		}
	}
}

// trimBreaks drops leading/trailing/duplicate line-break markers so a
// document's outer paragraph break doesn't produce a stray blank line.
// Where breaks collapse, the strongest one wins: a list item ends with an
// inline break immediately followed by its block break, and it is the block
// break that has to survive or the item loses its spacing.
func trimBreaks(words []styledWord) []styledWord {
	var out []styledWord
	for _, w := range words {
		if w.brk != breakNone {
			if len(out) == 0 {
				continue
			}
			if prev := &out[len(out)-1]; prev.brk != breakNone {
				prev.brk = max(prev.brk, w.brk)
				continue
			}
		}
		out = append(out, w)
	}
	for len(out) > 0 && out[len(out)-1].brk != breakNone {
		out = out[:len(out)-1]
	}
	return out
}

// wrappedBlock is the result of wrapping Markdown text for a given
// font/size/width: the wrapped lines plus the metrics needed to position
// and draw them. Splitting this "measure" step from actually drawing lets
// a caller compute a block's real rendered height before deciding where to
// place it - e.g. to center two stacked blocks as one combined unit (see
// drawBackScreen's front-label + answer-text pairing), rather than each
// block claiming a fixed, content-oblivious fraction of the available
// space regardless of how little (or much) it actually needs.
type wrappedBlock struct {
	lines      []layoutLine
	lineHeight int
	ascent     int
	blockSpace int
	resolve    faceResolver
}

func (b wrappedBlock) height() int {
	return blockHeight(b.lines, b.lineHeight, b.blockSpace)
}

// pages splits the block into successive availH-tall pages via paginate -
// see there for why a page boundary never lands mid-line. A block that
// fits without splitting still returns a single-element slice, so callers
// don't need a separate "did it paginate" branch.
func (b wrappedBlock) pages(availH int) [][]layoutLine {
	return paginate(b.lines, b.lineHeight, b.blockSpace, b.ascent, availH)
}

// wrapMarkdown parses and word-wraps Markdown text for the given
// font/size within maxWidth, without drawing anything yet.
func wrapMarkdown(md string, ft FontType, size, maxWidth int) (wrappedBlock, error) {
	resolve := makeFaceResolver(ft, size)
	regular, err := resolve(StyleRegular)
	if err != nil {
		return wrappedBlock{}, err
	}
	words := markdownToWords([]byte(md))
	lines, err := wrapWords(words, resolve, fixed.I(maxWidth))
	if err != nil {
		return wrappedBlock{}, err
	}
	return wrappedBlock{
		lines:      lines,
		lineHeight: scaledLineHeight(regular.Face),
		ascent:     faceAscent(regular.Face),
		blockSpace: scaledBlockSpace(regular.Face),
		resolve:    resolve,
	}, nil
}

// draw renders the wrapped block into rect r. If centerV, the block is
// vertically centered within r using its real rendered height (b.height())
// rather than r's own size.
func (b wrappedBlock) draw(r Rect, gray uint8, align Align, centerV bool) {
	target := r
	if centerV {
		target = centerRectV(r, b.height())
	}
	drawLines(canvas, b.lines, target, b.resolve, gray, b.lineHeight, b.ascent, b.blockSpace, align)
}

// drawMarkdownLabel wraps and draws Markdown-formatted card content
// (bold/italic/lists/line breaks) in one call - the common case of a
// single independent block. If centerV, it's vertically centered within r
// using its real post-wrap height - but only when the content fits on one
// page (page 0 of a single-page result); a card spread across pages stays
// top-aligned on every page, so content doesn't visually jump up and down
// as pageCount changes what "centered" would mean. page selects which
// availH-tall slice to draw (0-based, clamped into range); the return
// value is the total page count, so a caller can tell whether more
// content exists beyond what's shown and needs a "more" affordance -
// callers that never paginate (e.g. the small fixed-height headword
// reminder label) can simply pass page 0, 0 for indicatorH, and ignore the
// return value. Used at the card content call sites in
// drawFrontScreen/drawBackScreen; every other label (buttons, deck names,
// "Done!" messages) never carries card content and stays on the plain
// fbinkTextRect path.
//
// indicatorH is the height a "more"/"back to top" bar would need if this
// content turns out to need more than one page - reserved from r only
// when it actually does (checked against the unreserved r.H first, via
// b.height()), so a card that already fits doesn't lose any of its height
// to a bar it will never draw.
func drawMarkdownLabel(r Rect, md string, ft FontType, size int, color string, align Align, centerV bool, page int, indicatorH int) (pageCount int) {
	b, err := wrapMarkdown(md, ft, size, r.W)
	if err != nil {
		if debug {
			fmt.Printf("drawMarkdownLabel: %v\n", err)
		}
		return 1
	}
	availH := r.H
	if b.height() > availH {
		availH -= indicatorH
	}
	if availH < 1 {
		availH = 1
	}
	pages := b.pages(availH)
	if page < 0 {
		page = 0
	}
	if page >= len(pages) {
		page = len(pages) - 1
	}
	pageBlock := wrappedBlock{lines: pages[page], lineHeight: b.lineHeight, ascent: b.ascent, blockSpace: b.blockSpace, resolve: b.resolve}
	drawRect := Rect{r.X, r.Y, r.W, availH}
	pageBlock.draw(drawRect, colorToGray(color), align, centerV && len(pages) == 1)
	return len(pages)
}
