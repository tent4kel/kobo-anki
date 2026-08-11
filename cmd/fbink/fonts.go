package main

import (
	"fmt"
	"os"
	"sync"

	// Aliased: this file lives alongside functions taking a "font FontType"
	// parameter (drawButton, drawLabel, fbinkTextRect, ...) - an
	// unaliased import named "font" would be shadowed by those parameters.
	xfont "golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

// FontStyle distinguishes which face to resolve for a styled word.
type FontStyle int

const (
	StyleRegular FontStyle = iota
	StyleBold
	StyleItalic
	StyleBoldItalic
)

// FontCache parses each font file at most once and caches the derived
// xfont.Face per (path, size) pair, since faces get requested repeatedly
// across draw calls within and across screens. The font's ligature table
// is derived once alongside the parse - it depends on the file, not on the
// point size, and reading GSUB is not something to redo per draw call.
type FontCache struct {
	mu     sync.Mutex
	parsed map[string]*opentype.Font
	ligs   map[string]*ligatureTable
	faces  map[string]xfont.Face
}

var fontCache = &FontCache{
	parsed: make(map[string]*opentype.Font),
	ligs:   make(map[string]*ligatureTable),
	faces:  make(map[string]xfont.Face),
}

// Face returns a xfont.Face for the TTF/OTF at path, at the given point
// size, parsing and caching the underlying font the first time it's seen.
// This uses golang.org/x/image/font/opentype - a genuine pure-Go TrueType
// and OpenType rasterizer (vector outlines, not bitmaps), not a downgrade
// from FBInk's FreeType-backed rendering. The font's ligature table comes
// back with it (nil if the font declares none this package can reach); see
// ligature.go for why that is read here rather than shaped at draw time.
func (fc *FontCache) Face(path string, size float64) (xfont.Face, *ligatureTable, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	key := fmt.Sprintf("%s@%.2f@%d", path, size, screenDPI)
	if f, ok := fc.faces[key]; ok {
		return f, fc.ligs[path], nil
	}

	parsed, ok := fc.parsed[path]
	if !ok {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		parsed, err = opentype.Parse(data)
		if err != nil {
			return nil, nil, err
		}
		fc.parsed[path] = parsed
		fc.ligs[path] = buildLigatures(data, parsed)
	}

	// DPI must match the panel's real density, or Size (in points) rasterizes
	// at the wrong pixel size - confirmed on real hardware: a hardcoded 96
	// here produced fonts ~3x too small on a 300 DPI Clara BW panel.
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{
		Size:    size,
		DPI:     float64(screenDPI),
		Hinting: xfont.HintingFull,
	})
	if err != nil {
		return nil, nil, err
	}
	fc.faces[key] = face
	return face, fc.ligs[path], nil
}

// makeFaceResolver builds a faceResolver for the given FontType/size,
// resolving each requested style via resolveFontStyle (explicit config
// override -> same-directory naming convention -> synthetic bold ->
// plain regular for italic with no real file). Shared by fbinkTextRect
// (always requests StyleRegular) and drawMarkdownLabel (requests whatever
// styles the Markdown actually uses).
func makeFaceResolver(ft FontType, size int) faceResolver {
	return func(style FontStyle) (resolvedFace, error) {
		path, synthBold := resolveFontStyle(ft, style)
		if path == "" {
			return resolvedFace{}, fmt.Errorf("no font configured for font type %v", ft)
		}
		face, ligs, err := fontCache.Face(path, float64(size))
		if err != nil {
			return resolvedFace{}, err
		}
		if !activeStyle.Ligatures {
			ligs = nil
		}
		return resolvedFace{Face: face, SynthBold: synthBold, Ligatures: ligs}, nil
	}
}

// scaledLineHeight applies activeStyle.LineHeight (a multiplier, e.g. 1.2
// for extra spacing) on top of the font's natural line height. Guards
// against a non-positive multiplier (shouldn't happen via normal config
// parsing, but would otherwise collapse every line on top of the last).
func scaledLineHeight(face xfont.Face) int {
	natural := face.Metrics().Height.Round()
	mult := activeStyle.LineHeight
	if mult <= 0 {
		mult = 1.0
	}
	return int(float64(natural) * mult)
}

// faceAscent returns the font's natural ascent (distance from the top of a
// line to its baseline) - not scaled by LineHeight, which only spaces
// lines apart from each other, not the first line's position.
func faceAscent(face xfont.Face) int {
	return face.Metrics().Ascent.Round()
}

// scaledBlockSpace is the extra gap drawn after a paragraph or list item,
// as activeStyle.BlockSpacing line heights. It scales with the font rather
// than being a pixel constant, so it stays proportionate across the size
// range a deck override can ask for. A negative multiplier would pull the
// next block upward into this one, so it is clamped away.
func scaledBlockSpace(face xfont.Face) int {
	mult := activeStyle.BlockSpacing
	if mult <= 0 {
		return 0
	}
	return int(float64(scaledLineHeight(face)) * mult)
}
