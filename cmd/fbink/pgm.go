package main

import (
	"fmt"
	"image"
	"os"
	"os/exec"
)

// encodePGM writes img as an uncompressed binary PGM (P5): a tiny text
// header followed by raw grayscale bytes, no deflate/zlib cost - FBInk
// accepts PNM as an input format, and this is essentially a memory copy.
func encodePGM(img *image.Gray) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	header := fmt.Sprintf("P5\n%d %d\n255\n", w, h)

	out := make([]byte, 0, len(header)+w*h)
	out = append(out, header...)
	if img.Stride == w {
		out = append(out, img.Pix[:w*h]...)
	} else {
		for y := 0; y < h; y++ {
			off := img.PixOffset(0, y)
			out = append(out, img.Pix[off:off+w]...)
		}
	}
	return out
}

// flushScreen encodes the canvas, blits it with a single fbink call, and
// triggers a single refresh - replacing fbinkRefresh's old role at the end
// of every draw*Screen function, where each screen used to already be
// composed of many separate fbink CLI subprocess calls.
func flushScreen() {
	data := encodePGM(canvas.Img)

	tmp, err := os.CreateTemp("", "kobo-anki-*.pgm")
	if err != nil {
		if debug {
			fmt.Printf("flushScreen: temp file: %v\n", err)
		}
		return
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		if debug {
			fmt.Printf("flushScreen: write temp file: %v\n", err)
		}
		return
	}
	tmp.Close()

	args := []string{"-g", "file=" + tmp.Name()}
	if cfg.DarkMode {
		args = append(args, "-H")
	}
	if out, err := exec.Command(fbinkPath, args...).CombinedOutput(); err != nil && debug {
		fmt.Printf("fbink blit error: %v, output: %s\n", err, string(out))
	}
	exec.Command(fbinkPath, "-s").Run()
}
