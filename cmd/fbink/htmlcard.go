package main

import (
	"fmt"
	"image"
	"image/draw"
	"os"
	"path/filepath"

	"kobo-anki/core"
	"kobo-anki/htmlrender"
)

// drawHTMLLabel is drawMarkdownLabel's sibling for card.Format == html
// faces: renders html (a self-contained document, model CSS baked in by
// cmd/apkgimport at import time) through litehtml at width r.W, then
// composites the result onto the shared canvas - draw.Draw converts the
// renderer's *image.RGBA to the canvas's *image.Gray automatically, so
// Canvas/pgm.go/flushScreen need no changes at all.
//
// Unlike drawMarkdownLabel, font/size/color aren't parameters here: an html
// face carries its own styling in the CSS cmd/apkgimport already wrapped
// around it (or, for the small headword reminder label built by
// core.HeadwordOnlyHTML, the container's own small default size - no
// wrapping <style> means no explicit font-size, so it renders smaller than a
// full card face without needing one threaded through here).
//
// page/pageCount mirror drawMarkdownLabel: litehtml already lays the whole
// card out to its natural height before this ever crops anything, so
// pagination here is just slicing that one raster into bands instead of
// discarding everything past the first one. centerV only applies when the
// content fits on a single page, for the same reason drawMarkdownLabel
// disables it once paginated - centering would make the content jump
// vertically page to page instead of reading as one continuous document.
//
// indicatorH is the height a "more"/"back to top" bar would need if this
// content turns out to need more than one page - reserved from r only
// when it actually does (checked against the unreserved r.H first), so a
// card that already fits doesn't lose any of its height to a bar it will
// never draw.
func drawHTMLLabel(r Rect, html string, centerV bool, page int, indicatorH int) (pageCount int) {
	// screenDPI is the real, detected panel density (see detectScreen);
	// real Anki card CSS assumes a normal ~96-160 DPI phone/desktop screen,
	// so litehtml's literal font-size:20px needs inflating by roughly
	// screenDPI/96 to occupy its intended physical size here - htmlFontScale
	// (html_font_scale in anki-core.conf) is an additional user taste
	// multiplier on top of that automatic correction, default 1.0.
	scale := float32(screenDPI) / 96.0 * float32(htmlFontScale)

	// cmd/apkgimport now references a card's model stylesheet by name
	// ("<!--css:slug-->" + bare content) instead of baking a full
	// <html><head><style>...</style></head><body> into every single face -
	// resolve that here, so a card imported before this existed (still
	// carrying its own full baked-in document) keeps rendering exactly as
	// before: ExtractCardStyleRef's ok is false for it, and it's used
	// as-is.
	if cssFile, body, ok := core.ExtractCardStyleRef(html); ok {
		css, _ := resolveDeckStyle(cssFile) // "" (unstyled, not missing) if the file's gone
		html = "<html><head><style>" + css + "</style></head><body>" + body + "</body></html>"
	}
	html = core.InjectIntoHead(html, globalCardCSSTag)
	// core.BaseCardCSS (in globalCardCSSTag) already caps a raw image's
	// *width* to the card; it can't also cap height there, because that
	// needs a concrete pixel budget only known here, per call, from r.H (a
	// front face and a back face - which reserves space for the rating
	// buttons - have different budgets). 70%, not 100%: a single image
	// filling the entire available height left no room for any
	// accompanying text on the same page, forcing a page turn even for a
	// card that would otherwise fit; width stays uncapped by this rule
	// (core.BaseCardCSS's max-width:100% already covers it) and aspect
	// ratio is preserved automatically - only max-height is set here,
	// height:auto already came from core.BaseCardCSS. Injected last, after
	// globalCardCSSTag, so it wins the cascade for this one property.
	maxImgHeight := int(float32(r.H) * 0.7 / scale)
	if maxImgHeight < 1 {
		maxImgHeight = 1
	}
	html = core.InjectIntoHead(html, fmt.Sprintf("<style>img{max-height:%dpx}</style>", maxImgHeight))
	rendered, err := htmlrender.RenderCard(html, r.W, deckMediaResolver(), scale)
	if err != nil {
		if debug {
			fmt.Printf("drawHTMLLabel: %v\n", err)
		}
		return 1
	}

	// Only reserve indicatorH if the content actually needs more than one
	// page at the full r.H - see the doc comment above.
	total := rendered.Bounds().Dy()
	availH := r.H
	if total > availH {
		availH -= indicatorH
	}
	if availH < 1 {
		availH = 1
	}
	pageCount = (total + availH - 1) / availH
	if pageCount < 1 {
		pageCount = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= pageCount {
		page = pageCount - 1
	}
	sliceTop := page * availH
	sliceBottom := sliceTop + availH
	if sliceBottom > total {
		sliceBottom = total
	}
	// SubImage shares the underlying pixels and keeps rendered's own
	// coordinate space (its Bounds().Min is (0, sliceTop), not (0, 0)) -
	// the sp passed to draw.Draw below has to account for that, not just
	// for target/bound clipping.
	sliceRect := image.Rect(0, sliceTop, rendered.Bounds().Dx(), sliceBottom)
	slice := rendered.SubImage(sliceRect).(*image.RGBA)

	target := r
	if pageCount == 1 && centerV {
		target = centerRectV(r, sliceRect.Dy())
	}
	dst := image.Rect(target.X, target.Y, target.X+sliceRect.Dx(), target.Y+sliceRect.Dy())
	// Clip to r: draw.Draw only clips against the canvas's own bounds (the
	// whole screen), not against this label's allotted rect. Pagination
	// already keeps each slice within availH, so this is now a pure safety
	// net for rounding rather than the primary overflow answer it used to
	// be - but cheap to keep regardless.
	bound := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
	clipped := dst.Intersect(bound)
	if clipped.Empty() {
		return pageCount
	}
	sp := image.Point{
		X: sliceRect.Min.X + (clipped.Min.X - dst.Min.X),
		Y: sliceRect.Min.Y + (clipped.Min.Y - dst.Min.Y),
	}
	draw.Draw(canvas.Img, clipped, slice, sp, draw.Over)
	return pageCount
}

// styleCache holds deck stylesheets already read this run, keyed by
// "<deck>/<file>" - a card's model stylesheet is the same for every card
// sharing that model, so re-reading it from disk on every single card shown
// would be pure waste (and this project has already found one instance of
// exactly that kind of waste costing real snappiness - see
// ensureDeckLoaded in cmd/server/main.go).
var styleCache = map[string]string{}

// resolveDeckStyle loads and caches the current deck's style-<slug>.css
// (see core.ExtractCardStyleRef), through the same MediaResolver <img>
// already uses - a stylesheet is just another deck media file. ok is false
// only if the file can't be read at all (e.g. hand-deleted to reset a
// deck's styling before a re-import that hasn't happened yet); the caller
// still renders, just unstyled, rather than failing the whole card.
func resolveDeckStyle(cssFile string) (css string, ok bool) {
	key := currentDeck + "/" + cssFile
	if css, ok := styleCache[key]; ok {
		return css, true
	}
	data, ok := deckMediaResolver()(cssFile, "")
	if !ok {
		return "", false
	}
	css = string(data)
	styleCache[key] = css
	return css, true
}

// deckMediaResolver resolves an html card's <img> references against the
// current deck's media zip (core.DeckMediaResolver) - the same archive
// cmd/apkgimport wrote into and archived the source .apkg alongside.
func deckMediaResolver() htmlrender.MediaResolver {
	lookup := core.DeckMediaResolver(dataDir, currentDeck)
	return func(src, _ string) ([]byte, bool) {
		// An absolute path - e.g. a Kobo system font referenced from
		// cards.css as url('/mnt/onboard/fonts/extra-kobo/KF_Literata-
		// Regular.ttf') - is read directly, not sandboxed to the deck's
		// own media. This isn't a new privilege: cards.css is a file you
		// already have full filesystem access to edit, same as this
		// binary itself, so referencing another file you can already read
		// grants nothing you didn't already have.
		if filepath.IsAbs(src) {
			b, err := os.ReadFile(src)
			return b, err == nil
		}
		return lookup(src)
	}
}
