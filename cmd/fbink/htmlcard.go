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
func drawHTMLLabel(r Rect, html string, centerV bool) {
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
	// buttons - have different budgets). Injected last, after
	// globalCardCSSTag, so it wins the cascade for this one property
	// without touching max-width/height:auto already set there - the
	// browser/litehtml replaced-element sizing algorithm applies both
	// constraints together, same as object-fit:contain would.
	maxImgHeight := int(float32(r.H) / scale)
	if maxImgHeight < 1 {
		maxImgHeight = 1
	}
	html = core.InjectIntoHead(html, fmt.Sprintf("<style>img{max-height:%dpx}</style>", maxImgHeight))
	rendered, err := htmlrender.RenderCard(html, r.W, deckMediaResolver(), scale)
	if err != nil {
		if debug {
			fmt.Printf("drawHTMLLabel: %v\n", err)
		}
		return
	}

	target := r
	if centerV {
		target = centerRectV(r, rendered.Bounds().Dy())
	}
	dst := image.Rect(target.X, target.Y, target.X+rendered.Bounds().Dx(), target.Y+rendered.Bounds().Dy())
	// Clip to r: draw.Draw only clips against the canvas's own bounds (the
	// whole screen), not against this label's allotted rect - without this,
	// content still too tall after the max-height cap above (long text, or
	// a max-height edge case) would spill into whatever's drawn below, e.g.
	// the rating buttons on the back screen, instead of being cleanly
	// cropped at the card's own boundary.
	bound := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
	clipped := dst.Intersect(bound)
	if clipped.Empty() {
		return
	}
	sp := image.Point{X: clipped.Min.X - dst.Min.X, Y: clipped.Min.Y - dst.Min.Y}
	draw.Draw(canvas.Img, clipped, rendered, sp, draw.Over)
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
// current deck's media directory (core.DeckMediaDir) - the same directory
// cmd/apkgimport wrote into and archived the source .apkg alongside.
func deckMediaResolver() htmlrender.MediaResolver {
	mediaDir := core.DeckMediaDir(dataDir, currentDeck)
	return func(src, _ string) ([]byte, bool) {
		// An absolute path - e.g. a Kobo system font referenced from
		// cards.css as url('/mnt/onboard/fonts/extra-kobo/KF_Literata-
		// Regular.ttf') - is read directly, not sandboxed to the deck's
		// own media dir. This isn't a new privilege: cards.css is a file
		// you already have full filesystem access to edit, same as this
		// binary itself, so referencing another file you can already read
		// grants nothing you didn't already have.
		if filepath.IsAbs(src) {
			b, err := os.ReadFile(src)
			return b, err == nil
		}
		b, err := os.ReadFile(filepath.Join(mediaDir, filepath.Base(src)))
		return b, err == nil
	}
}
