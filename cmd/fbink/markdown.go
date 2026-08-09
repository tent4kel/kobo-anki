package main

import (
	"fmt"
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
// arbitrarily), "- " tight lists, and line breaks. List items get a
// leading "- " baked onto their first word rather than a true hanging
// indent on wrapped continuation lines - simpler to implement correctly
// and still visually distinguishes list items; a real hanging indent would
// need wrapWords/drawLines to carry per-line indent state.
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
			w.walkInline(c)
			w.words = append(w.words, styledWord{text: "\n"})
		case *ast.List:
			w.walkList(node)
		default:
			w.walkBlock(c)
		}
	}
}

func (w *mdWalker) walkList(list *ast.List) {
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		before := len(w.words)
		w.walkBlock(item)
		// Mark every word from this item as list content (forces its line
		// to render left-aligned, see drawLines) and bake a leading "- "
		// onto just the first non-break word.
		prefixed := false
		for i := before; i < len(w.words); i++ {
			w.words[i].isListItem = true
			if !prefixed && w.words[i].text != "\n" {
				w.words[i].text = "- " + w.words[i].text
				prefixed = true
			}
		}
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
				w.words = append(w.words, styledWord{text: "\n"})
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
func trimBreaks(words []styledWord) []styledWord {
	var out []styledWord
	for _, w := range words {
		if w.text == "\n" && (len(out) == 0 || out[len(out)-1].text == "\n") {
			continue
		}
		out = append(out, w)
	}
	for len(out) > 0 && out[len(out)-1].text == "\n" {
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
	resolve    faceResolver
}

func (b wrappedBlock) height() int {
	return len(b.lines) * b.lineHeight
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
	drawLines(canvas, b.lines, target, b.resolve, gray, b.lineHeight, b.ascent, align)
}

// drawMarkdownLabel wraps and draws Markdown-formatted card content
// (bold/italic/lists/line breaks) in one call - the common case of a
// single independent block. If centerV, it's vertically centered within r
// using its real post-wrap height. Used at the card-content call sites in
// drawFrontScreen and (for the combined front+answer centering)
// indirectly via wrapMarkdown/wrappedBlock in drawBackScreen. Every other
// label (buttons, deck names, "Done!" messages) never carries card content
// and stays on the plain fbinkTextRect path.
func drawMarkdownLabel(r Rect, md string, ft FontType, size int, color string, align Align, centerV bool) {
	b, err := wrapMarkdown(md, ft, size, r.W)
	if err != nil {
		if debug {
			fmt.Printf("drawMarkdownLabel: %v\n", err)
		}
		return
	}
	b.draw(r, colorToGray(color), align, centerV)
}
