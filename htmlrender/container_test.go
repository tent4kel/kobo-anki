package htmlrender

import (
	"os"
	"path/filepath"
	"testing"

	litehtml "github.com/andresbott/litehtml-go"
)

func testFontResolver(t *testing.T) MediaResolver {
	t.Helper()
	dir := "../fonts"
	files := map[string]string{
		"regular.ttf":    "NV_Newsreader-Regular.ttf",
		"bold.ttf":       "NV_Newsreader-Bold.ttf",
		"italic.ttf":     "NV_Newsreader-Italic.ttf",
		"bolditalic.ttf": "NV_Newsreader-BoldItalic.ttf",
	}
	data := make(map[string][]byte, len(files))
	for src, real := range files {
		b, err := os.ReadFile(filepath.Join(dir, real))
		if err != nil {
			t.Fatalf("reading fixture font %s: %v", real, err)
		}
		data[src] = b
	}
	return func(src, _ string) ([]byte, bool) {
		b, ok := data[src]
		return b, ok
	}
}

const fourWeightFontFaceCSS = `
@font-face { font-family: "NV Newsreader"; src: url('regular.ttf'); }
@font-face { font-family: "NV Newsreader"; font-weight: bold; src: url('bold.ttf'); }
@font-face { font-family: "NV Newsreader"; font-style: italic; src: url('italic.ttf'); }
@font-face { font-family: "NV Newsreader"; font-weight: bold; font-style: italic; src: url('bolditalic.ttf'); }
`

func TestResolveCustomFontPicksBoldVariant(t *testing.T) {
	c := &Container{resolveMedia: testFontResolver(t)}
	c.RegisterFontFaces("<style>" + fourWeightFontFaceCSS + "</style>")

	regular := c.resolveCustomFont(`"NV Newsreader"`, 400, litehtml.FontStyleNormal)
	bold := c.resolveCustomFont(`"NV Newsreader"`, 700, litehtml.FontStyleNormal)

	if regular == nil || bold == nil {
		t.Fatalf("resolveCustomFont returned nil: regular=%v bold=%v", regular, bold)
	}
	if regular == bold {
		t.Fatalf("bold weight resolved to the same font as regular - bold variant not selected")
	}
}

func TestResolveCustomFontPicksItalicVariant(t *testing.T) {
	c := &Container{resolveMedia: testFontResolver(t)}
	c.RegisterFontFaces("<style>" + fourWeightFontFaceCSS + "</style>")

	regular := c.resolveCustomFont(`"NV Newsreader"`, 400, litehtml.FontStyleNormal)
	italic := c.resolveCustomFont(`"NV Newsreader"`, 400, litehtml.FontStyleItalic)

	if regular == nil || italic == nil {
		t.Fatalf("resolveCustomFont returned nil: regular=%v italic=%v", regular, italic)
	}
	if regular == italic {
		t.Fatalf("italic style resolved to the same font as regular - italic variant not selected")
	}
}

func TestResolveCustomFontPicksBoldItalicVariant(t *testing.T) {
	c := &Container{resolveMedia: testFontResolver(t)}
	c.RegisterFontFaces("<style>" + fourWeightFontFaceCSS + "</style>")

	bold := c.resolveCustomFont(`"NV Newsreader"`, 700, litehtml.FontStyleNormal)
	boldItalic := c.resolveCustomFont(`"NV Newsreader"`, 700, litehtml.FontStyleItalic)

	if bold == nil || boldItalic == nil {
		t.Fatalf("resolveCustomFont returned nil: bold=%v boldItalic=%v", bold, boldItalic)
	}
	if bold == boldItalic {
		t.Fatalf("bold+italic resolved to the same font as bold alone - bold-italic variant not selected")
	}
}

// Only Regular was ever registered for this family (the common case: a deck
// declares one @font-face). Bold text must still render in that family
// rather than silently reverting to nil (which CreateFont would then hand
// to pickFont - the bundled Go font, a visibly different typeface) just
// because no dedicated bold file exists.
func TestResolveCustomFontFallsBackToRegularWhenVariantMissing(t *testing.T) {
	c := &Container{resolveMedia: testFontResolver(t)}
	c.RegisterFontFaces(`<style>@font-face { font-family: "NV Newsreader"; src: url('regular.ttf'); }</style>`)

	regular := c.resolveCustomFont(`"NV Newsreader"`, 400, litehtml.FontStyleNormal)
	bold := c.resolveCustomFont(`"NV Newsreader"`, 700, litehtml.FontStyleNormal)

	if regular == nil {
		t.Fatalf("resolveCustomFont(regular) = nil, want the registered font")
	}
	if bold != regular {
		t.Fatalf("resolveCustomFont(bold), no bold variant registered = %v, want the family's regular font %v as a degrade-gracefully fallback", bold, regular)
	}
}

// A CSS-commented-out @font-face block (a real author writing "disable
// this for now" the normal CSS way) must not register - RegisterFontFaces
// scans raw text with a regex, not a real CSS parser, so without stripping
// comments first it would still find and load a face the stylesheet itself
// says is off.
func TestRegisterFontFacesIgnoresCommentedOutBlock(t *testing.T) {
	c := &Container{resolveMedia: testFontResolver(t)}
	c.RegisterFontFaces(`<style>/* @font-face { font-family: "NV Newsreader"; src: url('regular.ttf'); } */</style>`)

	got := c.resolveCustomFont(`"NV Newsreader"`, 400, litehtml.FontStyleNormal)

	if got != nil {
		t.Fatalf("resolveCustomFont after a commented-out @font-face = %v, want nil - the block is disabled", got)
	}
}

func TestResolveCustomFontReturnsNilForUnregisteredFamily(t *testing.T) {
	c := &Container{resolveMedia: testFontResolver(t)}
	c.RegisterFontFaces(`<style>@font-face { font-family: "NV Newsreader"; src: url('regular.ttf'); }</style>`)

	got := c.resolveCustomFont(`"Some Other Family"`, 400, litehtml.FontStyleNormal)

	if got != nil {
		t.Fatalf("resolveCustomFont(unregistered family) = %v, want nil so CreateFont falls through to the bundled fonts", got)
	}
}
