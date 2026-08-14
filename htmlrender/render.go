package htmlrender

import (
	"fmt"
	"image"
	"math"

	litehtml "github.com/andresbott/litehtml-go"
)

// maxHeight bounds the logical canvas litehtml lays out into before the
// real content height is known. Generous on purpose: a card that overflows
// this gets silently capped rather than failing, since a card that's too
// tall to fit on an e-ink screen isn't fully usable either way.
const maxHeight = 4000

// RenderCard lays out and paints html (a full document, or a fragment -
// both work) at the given physical width, returning an image cropped to
// the actual content height. resolver may be nil for content with no
// images.
//
// scale corrects for a target screen much denser than the ~96-160 DPI real
// card CSS assumes (a Kobo panel is ~300 DPI): litehtml is given the
// narrower logical width (width/scale) such a normal-DPI screen would see,
// where every length in the deck's own CSS - line-height, padding,
// border-width - stays mutually consistent exactly as its author intended,
// and Container paints each element directly onto the full physical-width
// canvas, text rasterized fresh at its true physical size rather than
// upscaled from a smaller render - see Container.New's doc comment for why
// that distinction matters. scale <= 0 is treated as 1 (no scaling).
func RenderCard(html string, width int, resolver MediaResolver, scale float32) (*image.RGBA, error) {
	if scale <= 0 {
		scale = 1
	}
	logicalWidth := width
	if scale != 1 {
		logicalWidth = int(math.Round(float64(width) / float64(scale)))
		if logicalWidth < 1 {
			logicalWidth = 1
		}
	}

	container := New(logicalWidth, maxHeight, resolver, scale)
	container.RegisterFontFaces(html)

	doc, err := litehtml.NewDocument(html, container, "", "")
	if err != nil {
		return nil, fmt.Errorf("parsing document: %w", err)
	}
	defer doc.Close()

	doc.Render(float32(logicalWidth))

	clip := litehtml.Position{X: 0, Y: 0, Width: float32(logicalWidth), Height: float32(maxHeight)}
	doc.Draw(0, 0, 0, &clip)

	contentHeight := int(math.Ceil(float64(doc.Height())))
	if contentHeight <= 0 || contentHeight > maxHeight {
		contentHeight = maxHeight
	}
	physW := int(math.Round(float64(logicalWidth) * float64(scale)))
	physH := int(math.Round(float64(contentHeight) * float64(scale)))
	sub := container.Img.SubImage(image.Rect(0, 0, physW, physH)).(*image.RGBA)
	return sub, nil
}
