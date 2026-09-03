package main

import (
	"image"
	"testing"
)

func TestInvertGrayFlipsEveryPixel(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 2, 2))
	src.Pix = []uint8{0x00, 0xFF, 0x40, 0xC0}

	got := invertGray(src)

	want := []uint8{0xFF, 0x00, 0xBF, 0x3F}
	for i, w := range want {
		if got.Pix[i] != w {
			t.Errorf("pixel %d = 0x%02X, want 0x%02X", i, got.Pix[i], w)
		}
	}
}

func TestInvertGrayDoesNotMutateSource(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 1, 1))
	src.Pix = []uint8{0x10}

	invertGray(src)

	if src.Pix[0] != 0x10 {
		t.Fatalf("invertGray mutated its input: got 0x%02X, want 0x10", src.Pix[0])
	}
}
