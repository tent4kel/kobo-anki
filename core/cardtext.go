package core

import (
	"regexp"
	"strings"
)

// leadingBoldRe matches a **bold** run anchored at the very start of a card
// face. kobo-vocab's card_format always opens with the headword in bold when
// there is one at all (see DefaultCardFormat), so the first such run is the
// headword and nothing else.
var leadingBoldRe = regexp.MustCompile(`(?s)^\*\*(.+?)\*\*`)

// HeadwordOnly reduces a formatted card face to just its bold headword,
// dropping the alternatives and example that follow.
//
// Used for the small grey "which word is this?" line on the back of a card,
// which should not restate the entire front now that fronts can carry
// alternatives and an example sentence. Both review UIs call this so the
// e-ink screen and the browser agree: they previously had separate
// implementations that disagreed, with "**een** **twee**, drie" yielding
// "**een twee**" on one and "**een**" on the other.
//
// A face that does not begin with a bold run - a custom card_format that
// styles the headword differently, or a plain hand-written card - is
// returned unchanged, so the label shows something rather than going blank.
func HeadwordOnly(cardFace string) string {
	m := leadingBoldRe.FindStringSubmatch(cardFace)
	if m == nil {
		return cardFace
	}
	return "**" + m[1] + "**"
}

// firstBoldHTMLRe finds the first <b>/<strong> run in a resolved Anki card
// face - unlike leadingBoldRe it isn't anchored at the start, since apkg
// templates rarely open a face with nothing but the headword the way
// kobo-vocab's own card_format always does (a face commonly leads with a
// wrapping <div> or an <img>).
var firstBoldHTMLRe = regexp.MustCompile(`(?is)<(?:b|strong)\b[^>]*>(.*?)</(?:b|strong)>`)

// htmlTagRe strips markup from inside an already-isolated headword run, so
// e.g. <b>de <i>kat</i></b> yields "de kat" rather than carrying the <i>
// through into a plain-text label.
var htmlTagRe = regexp.MustCompile(`(?s)<[^>]*>`)

// HeadwordOnlyHTML is HeadwordOnly's sibling for card.Format == FormatHTML
// faces: same "which word is this?" label, same "no bold run means show the
// face unchanged rather than going blank" fallback, but finding the first
// <b>/<strong> anywhere in the face and re-wrapping it as HTML instead of
// markdown, so callers can feed the result straight back into the html
// render path.
func HeadwordOnlyHTML(cardFace string) string {
	m := firstBoldHTMLRe.FindStringSubmatch(cardFace)
	if m == nil {
		return cardFace
	}
	inner := strings.TrimSpace(htmlTagRe.ReplaceAllString(m[1], ""))
	return "<b>" + inner + "</b>"
}

// styleBlockRe matches every <style>...</style> block in a card document,
// concatenated by ExtractCardDocument in case cmd/apkgimport ever emits more
// than one (it doesn't today, but nothing should silently drop a second
// block if that changes).
var styleBlockRe = regexp.MustCompile(`(?is)<style[^>]*>(.*?)</style>`)

// ExtractCardDocument pulls the <style> content and the <body> inner
// content out of a card face cmd/apkgimport wrapped as a full document
// (<html><head><style>...</style></head><body>...</body></html>).
//
// Embedding that whole wrapped document as a second, nested
// <html><head>...<body> inside a real page's own <body> - which is what
// cmd/server's review templates used to do - doesn't produce two DOM body
// elements: per the HTML parsing spec, a second <body> tag encountered
// mid-document is merged into the one that already exists, so a selector
// like "body { zoom: 3 }" meant to scale just the card ends up matching the
// page's one and only real <body> and scaling the surrounding chrome
// (buttons, headers) right along with it. Extracting the pieces here lets a
// caller place the CSS in the page's own real <head> and the content in an
// ordinary, distinctly-classed wrapper div instead, so scoping actually
// works.
//
// A face with no <body> tag (a bare fragment, or the small
// HeadwordOnlyHTML reminder label) is returned as the full input string
// with an empty css, so callers can treat every face uniformly.
func ExtractCardDocument(cardFace string) (css, body string) {
	for _, m := range styleBlockRe.FindAllStringSubmatch(cardFace, -1) {
		css += m[1]
	}
	bi := strings.Index(cardFace, "<body")
	if bi < 0 {
		return css, cardFace
	}
	open := strings.Index(cardFace[bi:], ">")
	if open < 0 {
		return css, cardFace
	}
	start := bi + open + 1
	end := strings.LastIndex(cardFace, "</body>")
	if end < start {
		return css, cardFace
	}
	return css, cardFace[start:end]
}

// styleRefRe matches the "<!--css:slug-->" marker cmd/apkgimport now writes
// at the start of an html/apkg card face, in place of baking a full
// <style>...</style> block into every single face (see
// ExtractCardStyleRef).
var styleRefRe = regexp.MustCompile(`^<!--css:([A-Za-z0-9_-]+)-->`)

// ExtractCardStyleRef splits a card face into a stylesheet filename and the
// remaining body, if the face starts with the "<!--css:slug-->" marker
// cmd/apkgimport writes for a deduped stylesheet - "style-slug.css",
// resolved the same way an <img> reference already is, via a deck's own
// media directory. ok is false for a face with no marker (a card imported
// before this existed, still carrying its own full baked-in
// <html><head><style>...; see ExtractCardDocument for that shape, or the
// small HeadwordOnlyHTML reminder label, which has no styling of its own at
// all) - callers fall back to ExtractCardDocument in that case, so an
// already-imported deck keeps working without needing to be re-imported.
func ExtractCardStyleRef(cardFace string) (cssFile, body string, ok bool) {
	m := styleRefRe.FindStringSubmatch(cardFace)
	if m == nil {
		return "", cardFace, false
	}
	return "style-" + m[1] + ".css", cardFace[len(m[0]):], true
}

// InjectIntoHead inserts extra HTML/CSS immediately before an html/apkg
// card's closing </head>, so it cascades after - and can override - the
// model CSS cmd/apkgimport already baked into the card at import time.
// Used by both review UIs to layer a shared cards.css on top of every
// card's own styling. A face with no <head> (a bare fragment, or the small
// HeadwordOnlyHTML reminder label) is returned unchanged - there is nowhere
// to inject into, and one is not worth manufacturing for a label with no
// styling of its own to override.
// BaseCardCSS approximates the handful of default rules real Anki's
// reviewer webview applies on top of every note type's own CSS. Most
// importantly: a note type's CSS rarely constrains <img> sizing itself,
// because Anki's own webview already caps oversized source images (a
// full-resolution photo dropped straight into a note, common in
// image-heavy decks) to fit the available space. litehtml has no such
// built-in opinion - without this, an image renders at its native pixel
// size, which reads as "images are huge" on decks that never needed their
// own sizing rule because real Anki was doing it for them.
const BaseCardCSS = `img { max-width: 100%; height: auto; }`

func InjectIntoHead(cardFace, extra string) string {
	if extra == "" {
		return cardFace
	}
	i := strings.LastIndex(cardFace, "</head>")
	if i < 0 {
		return cardFace
	}
	return cardFace[:i] + extra + cardFace[i:]
}
