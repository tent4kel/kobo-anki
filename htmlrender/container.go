// Package htmlrender implements litehtml's DocumentContainer against a plain
// Go *image.RGBA, and a RenderCard entry point on top of it. It is not
// Kobo-specific: cmd/fbink composites its output onto the framebuffer canvas,
// cmd/apkgimport uses it only for an optional preview.
//
// Ported from the prototype validated in ~/Hacking/GoHtml
// (internal/imgrender/container.go) against real kobo-vocab dictionary
// entries and a real .apkg export - this is the same code, not a rewrite.
package htmlrender

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"regexp"
	"strings"
	"sync"

	litehtml "github.com/andresbott/litehtml-go"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gobolditalic"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// fontEntry keeps two faces of the same font deliberately separate, the way
// a browser keeps text layout and text painting separate under a device
// pixel ratio: metricsFace (built at the font's literal CSS size) is what
// TextWidth/CreateFont's returned FontMetrics use, so litehtml's own layout
// - line boxes, wrapping - stays entirely in the small logical coordinate
// space doc.Render was called with. paintFace (built at size*scale) is used
// only by DrawText, to rasterize glyphs fresh at their real physical pixel
// size - not by shrinking layout then blowing up a finished raster (which
// was tried and produced blurry, non-pixel-perfect text), but by hinting
// and rendering the actual outline at the resolution it will be shown at,
// same as a real text rasterizer does for a HiDPI screen.
type fontEntry struct {
	metricsFace font.Face
	paintFace   font.Face
	paintAscent float32
}

// MediaResolver returns the raw bytes for an <img> src (or CSS
// background-image url), as looked up by whatever medium the document came
// from — an Anki .apkg's media/ files, a dictzip, etc. A nil resolver (or
// one that always returns ok=false) makes images silently absent, which is
// fine for text-only content.
type MediaResolver func(src, baseURL string) ([]byte, bool)

// Container implements litehtml.DocumentContainer, painting onto an
// *image.RGBA.
type Container struct {
	Img *image.RGBA
	w   float32 // logical (litehtml layout-unit) width/height - see New
	h   float32
	// scale is physical raster pixels per logical layout unit. Every
	// Position litehtml hands to a Draw* callback is in logical units;
	// each callback multiplies by scale itself, at the point of painting,
	// rather than litehtml or GetViewport/GetMediaFeatures ever seeing
	// scale - those stay entirely in logical units, matching what
	// doc.Render was actually called with.
	scale float32

	resolveMedia MediaResolver

	mu         sync.Mutex
	fonts      map[uintptr]*fontEntry
	nextFontID uintptr
	images     map[string]image.Image // decoded, keyed by src

	regular    *opentype.Font
	bold       *opentype.Font
	italic     *opentype.Font
	boldItalic *opentype.Font

	// customFonts holds @font-face fonts registered by RegisterFontFaces,
	// keyed by lowercased family name. litehtml itself never sees
	// @font-face at all (this vendored build only recognizes @charset,
	// @import, @media - any other at-rule, including @font-face, is
	// silently discarded during CSS parsing), so this container has to
	// find and load them itself before litehtml starts requesting fonts.
	customFonts map[string]*opentype.Font
}

// New allocates a container whose litehtml-facing layout space is w x h
// logical units, backed by a physical raster canvas of w*scale x h*scale
// pixels - real card CSS is authored assuming a normal ~96-160 DPI
// phone/desktop screen, so on a much denser physical target (a Kobo panel
// is ~300 DPI) litehtml is given the smaller logical viewport it expects
// (where a deck's own absolute CSS lengths - line-height, padding,
// border-width - stay mutually consistent, exactly as authored), while
// every Draw* callback paints directly onto the full physical-resolution
// canvas by scaling the Position it receives at the point of drawing. Text
// specifically is rasterized fresh at its true physical size (see
// fontEntry), not shrunk-then-upscaled, so it stays as crisp as native
// rendering. scale <= 0 is treated as 1 (no scaling; w/h become literal
// pixels, matching every caller before this parameter existed).
//
// Height should be generous — the real content height is only known after
// Document.Render — and the caller should crop Img (in physical pixels,
// i.e. logical height * scale) to the actual layout height before
// saving/using it.
func New(w, h int, resolver MediaResolver, scale float32) *Container {
	if scale <= 0 {
		scale = 1
	}
	physW := int(math.Round(float64(w) * float64(scale)))
	physH := int(math.Round(float64(h) * float64(scale)))
	img := image.NewRGBA(image.Rect(0, 0, physW, physH))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	reg, _ := opentype.Parse(goregular.TTF)
	bld, _ := opentype.Parse(gobold.TTF)
	ita, _ := opentype.Parse(goitalic.TTF)
	bi, _ := opentype.Parse(gobolditalic.TTF)

	return &Container{
		Img:          img,
		w:            float32(w),
		h:            float32(h),
		scale:        scale,
		resolveMedia: resolver,
		fonts:        make(map[uintptr]*fontEntry),
		images:       make(map[string]image.Image),
		regular:      reg,
		bold:         bld,
		italic:       ita,
		boldItalic:   bi,
	}
}

var (
	// A small, deliberate subset - one @font-face block, one font-family,
	// one src url - matching how this project scopes every other CSS/HTML
	// parsing bit it hand-rolls (see kobo-vocab/selector.go). Multiple
	// @font-face blocks for the same family at different weights (regular
	// vs. bold as separate files) all collapse onto whichever this project
	// sees last for that family name; a deck relying on that distinction
	// gets one face used for both, not a parse failure.
	fontFaceBlockRe = regexp.MustCompile(`(?is)@font-face\s*\{([^}]*)\}`)
	fontFamilyRe    = regexp.MustCompile(`(?is)font-family\s*:\s*['"]?([^;'"]+?)['"]?\s*;`)
	fontSrcURLRe    = regexp.MustCompile(`(?is)url\(\s*['"]?([^'")]+?)['"]?\s*\)`)
)

// RegisterFontFaces scans html's stylesheets for @font-face rules and loads
// each referenced font file through the container's MediaResolver (the
// same mechanism <img> uses), so CreateFont can serve them by family name
// instead of always falling back to the four bundled Go fonts. Call this
// once, before litehtml.NewDocument, on the exact html string that will be
// rendered. A nil resolver, or a document with no @font-face rules, is a
// silent no-op.
func (c *Container) RegisterFontFaces(html string) {
	if c.resolveMedia == nil {
		return
	}
	for _, block := range fontFaceBlockRe.FindAllStringSubmatch(html, -1) {
		body := block[1]
		fm := fontFamilyRe.FindStringSubmatch(body)
		sm := fontSrcURLRe.FindStringSubmatch(body)
		if fm == nil || sm == nil {
			continue
		}
		family := strings.ToLower(strings.TrimSpace(fm[1]))
		url := strings.TrimSpace(sm[1])

		data, ok := c.resolveMedia(url, "")
		if !ok {
			continue
		}
		f, err := opentype.Parse(data)
		if err != nil {
			continue
		}
		if c.customFonts == nil {
			c.customFonts = make(map[string]*opentype.Font)
		}
		c.customFonts[family] = f
	}
}

// resolveCustomFont checks familyList (litehtml's comma-separated
// font-family value, e.g. `"MyFont", Arial, sans-serif` - litehtml passes
// the raw CSS list through unresolved) against fonts RegisterFontFaces
// found, first match wins, same fallback order the CSS itself specifies.
func (c *Container) resolveCustomFont(familyList string) *opentype.Font {
	if len(c.customFonts) == 0 {
		return nil
	}
	for _, name := range strings.Split(familyList, ",") {
		name = strings.ToLower(strings.Trim(strings.TrimSpace(name), `"'`))
		if f, ok := c.customFonts[name]; ok {
			return f
		}
	}
	return nil
}

func (c *Container) pickFont(weight int, style litehtml.FontStyle) *opentype.Font {
	isBold := weight >= 700
	isItalic := style == litehtml.FontStyleItalic
	switch {
	case isBold && isItalic:
		return c.boldItalic
	case isBold:
		return c.bold
	case isItalic:
		return c.italic
	default:
		return c.regular
	}
}

func (c *Container) CreateFont(descr litehtml.FontDescription) (uintptr, litehtml.FontMetrics) {
	otFont := c.resolveCustomFont(descr.Family)
	if otFont == nil {
		otFont = c.pickFont(descr.Weight, descr.Style)
	}
	size := float64(descr.Size)
	if size < 1 {
		size = 16
	}
	metricsFace, err := opentype.NewFace(otFont, &opentype.FaceOptions{
		Size:    size,
		DPI:     96,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return 0, litehtml.FontMetrics{FontSize: descr.Size, Height: descr.Size * 1.2, Ascent: descr.Size * 0.8, Descent: descr.Size * 0.2, XHeight: descr.Size * 0.5, ChWidth: descr.Size * 0.6, DrawSpaces: true}
	}

	// The paint face is rasterized fresh at its real physical size, not
	// derived from metricsFace by scaling pixels - this is what keeps text
	// crisp instead of blurry (see fontEntry). A failure here (should only
	// happen if metricsFace itself somehow can't be re-created at a
	// different size) falls back to painting at the logical size rather
	// than losing the font entirely.
	paintFace, err := opentype.NewFace(otFont, &opentype.FaceOptions{
		Size:    size * float64(c.scale),
		DPI:     96,
		Hinting: font.HintingFull,
	})
	if err != nil {
		paintFace = metricsFace
	}

	met := metricsFace.Metrics()
	ascent := fixedToFloat(met.Ascent)
	descent := fixedToFloat(met.Descent)
	height := ascent + descent

	xBounds, _, _ := metricsFace.GlyphBounds('x')
	xHeight := fixedToFloat(-xBounds.Min.Y)
	chAdv, _ := metricsFace.GlyphAdvance('0')
	chWidth := fixedToFloat(chAdv)

	paintAscent := fixedToFloat(paintFace.Metrics().Ascent)

	c.mu.Lock()
	c.nextFontID++
	id := c.nextFontID
	c.fonts[id] = &fontEntry{metricsFace: metricsFace, paintFace: paintFace, paintAscent: paintAscent}
	c.mu.Unlock()

	return id, litehtml.FontMetrics{
		FontSize:   descr.Size,
		Height:     height,
		Ascent:     ascent,
		Descent:    descent,
		XHeight:    xHeight,
		ChWidth:    chWidth,
		DrawSpaces: true,
	}
}

func (c *Container) DeleteFont(hFont uintptr) {
	c.mu.Lock()
	if fe, ok := c.fonts[hFont]; ok {
		_ = fe.metricsFace.Close()
		if fe.paintFace != fe.metricsFace {
			_ = fe.paintFace.Close()
		}
		delete(c.fonts, hFont)
	}
	c.mu.Unlock()
}

func (c *Container) getFont(hFont uintptr) *fontEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fonts[hFont]
}

func (c *Container) TextWidth(text string, hFont uintptr) float32 {
	fe := c.getFont(hFont)
	if fe == nil {
		return float32(len(text)) * 8
	}
	return fixedToFloat(font.MeasureString(fe.metricsFace, text))
}

func (c *Container) DrawText(hdc uintptr, text string, hFont uintptr, clr litehtml.WebColor, pos litehtml.Position) {
	fe := c.getFont(hFont)
	if fe == nil {
		return
	}
	col := color.NRGBA{R: clr.Red, G: clr.Green, B: clr.Blue, A: clr.Alpha}
	d := &font.Drawer{
		Dst:  c.Img,
		Src:  &image.Uniform{col},
		Face: fe.paintFace,
		Dot: fixed.P(
			int(math.Round(float64(pos.X)*float64(c.scale))),
			int(math.Round(float64(pos.Y)*float64(c.scale)))+int(math.Round(float64(fe.paintAscent))),
		),
	}
	d.DrawString(text)
}

func (c *Container) DrawSolidFill(hdc uintptr, layer litehtml.BackgroundLayer, clr litehtml.WebColor) {
	col := color.NRGBA{R: clr.Red, G: clr.Green, B: clr.Blue, A: clr.Alpha}
	draw.Draw(c.Img, c.rectFromPos(layer.BorderBox), &image.Uniform{col}, image.Point{}, draw.Over)
}

func (c *Container) DrawBorders(hdc uintptr, borders litehtml.Borders, drawPos litehtml.Position, root bool) {
	s := float64(c.scale)
	x0 := int(math.Round(float64(drawPos.X) * s))
	y0 := int(math.Round(float64(drawPos.Y) * s))
	x1 := int(math.Round(float64(drawPos.X+drawPos.Width) * s))
	y1 := int(math.Round(float64(drawPos.Y+drawPos.Height) * s))

	if borders.Top.Width > 0 && borders.Top.Style != litehtml.BorderStyleNone {
		bw := int(math.Round(float64(borders.Top.Width) * s))
		col := color.NRGBA{R: borders.Top.Color.Red, G: borders.Top.Color.Green, B: borders.Top.Color.Blue, A: borders.Top.Color.Alpha}
		draw.Draw(c.Img, image.Rect(x0, y0, x1, y0+bw), &image.Uniform{col}, image.Point{}, draw.Over)
	}
	if borders.Bottom.Width > 0 && borders.Bottom.Style != litehtml.BorderStyleNone {
		bw := int(math.Round(float64(borders.Bottom.Width) * s))
		col := color.NRGBA{R: borders.Bottom.Color.Red, G: borders.Bottom.Color.Green, B: borders.Bottom.Color.Blue, A: borders.Bottom.Color.Alpha}
		draw.Draw(c.Img, image.Rect(x0, y1-bw, x1, y1), &image.Uniform{col}, image.Point{}, draw.Over)
	}
	if borders.Left.Width > 0 && borders.Left.Style != litehtml.BorderStyleNone {
		bw := int(math.Round(float64(borders.Left.Width) * s))
		col := color.NRGBA{R: borders.Left.Color.Red, G: borders.Left.Color.Green, B: borders.Left.Color.Blue, A: borders.Left.Color.Alpha}
		draw.Draw(c.Img, image.Rect(x0, y0, x0+bw, y1), &image.Uniform{col}, image.Point{}, draw.Over)
	}
	if borders.Right.Width > 0 && borders.Right.Style != litehtml.BorderStyleNone {
		bw := int(math.Round(float64(borders.Right.Width) * s))
		col := color.NRGBA{R: borders.Right.Color.Red, G: borders.Right.Color.Green, B: borders.Right.Color.Blue, A: borders.Right.Color.Alpha}
		draw.Draw(c.Img, image.Rect(x1-bw, y0, x1, y1), &image.Uniform{col}, image.Point{}, draw.Over)
	}
}

func (c *Container) DrawListMarker(hdc uintptr, marker litehtml.ListMarker) {
	col := color.NRGBA{R: marker.Color.Red, G: marker.Color.Green, B: marker.Color.Blue, A: marker.Color.Alpha}
	s := float64(c.scale)
	cx := int(math.Round((float64(marker.Pos.X) + float64(marker.Pos.Width)/2) * s))
	cy := int(math.Round((float64(marker.Pos.Y) + float64(marker.Pos.Height)/2) * s))
	radius := int(math.Round(3 * s))
	if radius < 1 {
		radius = 1
	}
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if dx*dx+dy*dy <= radius*radius {
				c.Img.Set(cx+dx, cy+dy, col)
			}
		}
	}
}

func (c *Container) PtToPx(pt float32) float32   { return pt * 96.0 / 72.0 }
func (c *Container) GetDefaultFontSize() float32 { return 16 }
func (c *Container) GetDefaultFontName() string  { return "arial" }

func (c *Container) decodeImage(src, baseurl string) image.Image {
	c.mu.Lock()
	if img, ok := c.images[src]; ok {
		c.mu.Unlock()
		return img
	}
	c.mu.Unlock()

	if c.resolveMedia == nil {
		return nil
	}
	data, ok := c.resolveMedia(src, baseurl)
	if !ok {
		return nil
	}
	var img image.Image
	var err error
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG")):
		img, err = png.Decode(bytes.NewReader(data))
	case bytes.HasPrefix(data, []byte("\xff\xd8")):
		img, err = jpeg.Decode(bytes.NewReader(data))
	case bytes.HasPrefix(data, []byte("GIF8")):
		img, err = gif.Decode(bytes.NewReader(data))
	default:
		// Try PNG as a default guess before giving up.
		img, err = png.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil
	}
	c.mu.Lock()
	c.images[src] = img
	c.mu.Unlock()
	return img
}

func (c *Container) LoadImage(src, baseurl string, redrawOnReady bool) {
	c.decodeImage(src, baseurl)
}

func (c *Container) GetImageSize(src, baseurl string) litehtml.Size {
	img := c.decodeImage(src, baseurl)
	if img == nil {
		return litehtml.Size{}
	}
	b := img.Bounds()
	return litehtml.Size{Width: float32(b.Dx()), Height: float32(b.Dy())}
}

func (c *Container) DrawImage(hdc uintptr, layer litehtml.BackgroundLayer, url, baseURL string) {
	img := c.decodeImage(url, baseURL)
	if img == nil {
		return
	}
	// Unlike text, a raster <img> genuinely has no higher-resolution source
	// to re-rasterize from, so scaling it up here does resample - the same
	// limitation a browser has scaling a bitmap image.
	dst := c.rectFromPos(layer.BorderBox)
	xdraw.CatmullRom.Scale(c.Img, dst, img, img.Bounds(), xdraw.Over, nil)
}

func (c *Container) DrawLinearGradient(uintptr, litehtml.BackgroundLayer, litehtml.LinearGradient) {}
func (c *Container) DrawRadialGradient(uintptr, litehtml.BackgroundLayer, litehtml.RadialGradient) {}
func (c *Container) DrawConicGradient(uintptr, litehtml.BackgroundLayer, litehtml.ConicGradient)   {}
func (c *Container) SetCaption(string)                                                             {}
func (c *Container) SetBaseURL(string)                                                             {}
func (c *Container) Link(string, string, string)                                                   {}
func (c *Container) OnAnchorClick(string)                                                          {}
func (c *Container) OnMouseEvent(litehtml.MouseEvent)                                              {}
func (c *Container) SetCursor(string)                                                              {}
func (c *Container) ImportCSS(_, baseurl string) (string, string)                                  { return "", baseurl }
func (c *Container) SetClip(litehtml.Position, litehtml.BorderRadiuses)                            {}
func (c *Container) DelClip()                                                                      {}
func (c *Container) GetViewport() litehtml.Position {
	return litehtml.Position{X: 0, Y: 0, Width: c.w, Height: c.h}
}
func (c *Container) CreateElement(string, map[string]string) uintptr { return 0 }
func (c *Container) GetMediaFeatures() litehtml.MediaFeatures {
	return litehtml.MediaFeatures{
		Type: litehtml.MediaTypeScreen, Width: c.w, Height: c.h,
		DeviceWidth: c.w, DeviceHeight: c.h, Color: 8, Resolution: 96,
	}
}
func (c *Container) GetLanguage() (string, string) { return "en", "en-US" }
func (c *Container) TransformText(text string, tt litehtml.TextTransform) string {
	switch tt {
	case litehtml.TextTransformUppercase:
		return strings.ToUpper(text)
	case litehtml.TextTransformLowercase:
		return strings.ToLower(text)
	default:
		return text
	}
}

func fixedToFloat(v fixed.Int26_6) float32 { return float32(v) / 64.0 }

// rectFromPos scales a logical litehtml Position to a physical pixel rect.
// Each edge (not origin+size separately) is rounded independently, so
// adjacent elements sharing an edge in logical space don't drift apart by a
// rounding pixel in physical space.
func (c *Container) rectFromPos(p litehtml.Position) image.Rectangle {
	s := float64(c.scale)
	x0 := int(math.Round(float64(p.X) * s))
	y0 := int(math.Round(float64(p.Y) * s))
	x1 := int(math.Round(float64(p.X+p.Width) * s))
	y1 := int(math.Round(float64(p.Y+p.Height) * s))
	return image.Rect(x0, y0, x1, y1)
}
