package main

import "image"

// Canvas is the in-memory, whole-screen buffer everything gets drawn into.
// One canvas is composed per screen, then flushed to the e-ink panel with a
// single fbink blit + refresh (flushScreen in pgm.go), replacing the old
// pattern of one fbink CLI subprocess per drawn element.
type Canvas struct {
	Img *image.Gray // e-ink is grayscale; Gray8 matches the panel natively
}

func NewCanvas(w, h int) *Canvas {
	return &Canvas{Img: image.NewGray(image.Rect(0, 0, w, h))}
}

func (c *Canvas) Clear(gray uint8) {
	c.FillRect(Rect{0, 0, c.Img.Bounds().Dx(), c.Img.Bounds().Dy()}, gray)
}

func (c *Canvas) FillRect(r Rect, gray uint8) {
	bw, bh := c.Img.Bounds().Dx(), c.Img.Bounds().Dy()
	x0, y0 := clampCoord(r.X, bw), clampCoord(r.Y, bh)
	x1, y1 := clampCoord(r.X+r.W, bw), clampCoord(r.Y+r.H, bh)
	if x1 <= x0 || y1 <= y0 {
		return
	}
	for y := y0; y < y1; y++ {
		row := c.Img.Pix[c.Img.PixOffset(x0, y):c.Img.PixOffset(x1, y)]
		for i := range row {
			row[i] = gray
		}
	}
}

func clampCoord(v, max int) int {
	if v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

// colorToGray maps the FBInk color-token strings already used throughout
// this file ("GRAYD", "WHITE", "", ...) to an 8-bit gray value, so existing
// call sites don't need to change their color argument now that drawing
// happens on an in-memory canvas instead of shelling out to fbink per call.
//
// GRAY<hex nibble> is assumed to follow FBInk's usual convention of 16
// grayscale steps from 0 (black) to F (white) - unverified against real
// fbink output in this sandbox (no device/binary available); confirm on
// real hardware that GRAYD/GRAYE/GRAY8/GRAYB look right relative to each
// other before relying on exact shades.
//
// "" defaults to black (0x00): the only place this function ever receives
// an empty string in practice is as a text color meaning "default text
// color", never as a fill color (fbinkFillRect is always called with an
// explicit color in this codebase).
func colorToGray(name string) uint8 {
	switch name {
	case "":
		return 0x00
	case "WHITE":
		return 0xFF
	case "BLACK":
		return 0x00
	}
	if len(name) == 5 && name[:4] == "GRAY" {
		if n := hexNibble(name[4]); n >= 0 {
			return uint8(n) * 0x11 // scale 0-15 to 0-255 (0xF*0x11 == 0xFF exactly)
		}
	}
	return 0x00
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	}
	return -1
}
