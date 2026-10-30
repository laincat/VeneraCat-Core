// Package image implements the in-memory pixel-buffer protocol used by comic
// modifyImage scripts (docs/SENDMESSAGE-SPEC.md, section 5).
//
// Scripts address bitmaps by integer key. The client decodes the source image,
// registers it with Store.Put, runs the script (which calls Store.Do) and reads
// the result back with Store.Get. Decoding and encoding are the client's job;
// this package only moves RGBA bytes.
package image

import (
	"errors"
	"fmt"
)

// Bitmap is a straight (non-premultiplied) RGBA image, 4 bytes per pixel,
// row-major, top row first. len(Pix) must equal W*H*4.
type Bitmap struct {
	W, H int
	Pix  []byte
}

// Errors returned by Store. Use errors.Is to test for them.
var (
	ErrUnknownKey      = errors.New("image: unknown key")
	ErrUnknownFunction = errors.New("image: unknown function")
	ErrInvalid         = errors.New("image: invalid bitmap or size")
	ErrLimit           = errors.New("image: limit exceeded")
)

// coordLimit bounds script-supplied coordinates so that sums of a few of them
// cannot overflow int64. Real bitmaps are far smaller, so clamping does not
// change the result of any operation that could succeed.
const coordLimit = int64(1) << 40

func clamp(v int) int64 {
	return max(-coordLimit, min(coordLimit, int64(v)))
}

// newBitmap allocates a zeroed (transparent black) bitmap. Callers must have
// validated the size against the limits first.
func newBitmap(w, h int) *Bitmap {
	return &Bitmap{W: w, H: h, Pix: make([]byte, w*h*4)}
}

// fits reports whether a w*h bitmap is positive-sized and within maxPixels,
// without overflowing.
func fits(w, h, maxPixels int64) bool {
	return w > 0 && h > 0 && w <= maxPixels/h
}

func (b *Bitmap) check() error {
	if b == nil {
		return fmt.Errorf("%w: nil bitmap", ErrInvalid)
	}
	if b.W <= 0 || b.H <= 0 {
		return fmt.Errorf("%w: size %dx%d", ErrInvalid, b.W, b.H)
	}
	// W*H*4 == len(Pix), computed without overflow.
	if int64(b.W) > int64(len(b.Pix))/4/int64(b.H) || len(b.Pix) != b.W*b.H*4 {
		return fmt.Errorf("%w: %dx%d needs %d bytes, have %d", ErrInvalid, b.W, b.H, int64(b.W)*int64(b.H)*4, len(b.Pix))
	}
	return nil
}

// blit copies the w*h rectangle at (sx,sy) in src to (dx,dy) in dst. There is
// no blending: destination pixels are replaced, alpha included. The rectangle
// is clipped to both bitmaps; pixels whose source or destination lies outside
// are skipped. If dst and src are the same bitmap the result is as if the
// source had been copied first.
func blit(dst *Bitmap, dx, dy int64, src *Bitmap, sx, sy, w, h int64) {
	if w <= 0 || h <= 0 {
		return
	}
	ox, oy := dx-sx, dy-sy
	x0 := max(sx, 0, -ox)
	x1 := min(sx+w, int64(src.W), int64(dst.W)-ox)
	y0 := max(sy, 0, -oy)
	y1 := min(sy+h, int64(src.H), int64(dst.H)-oy)
	if x0 >= x1 || y0 >= y1 {
		return
	}
	n := int(x1-x0) * 4
	row := func(y int64) {
		s := (int(y)*src.W + int(x0)) * 4
		d := (int(y+oy)*dst.W + int(x0+ox)) * 4
		copy(dst.Pix[d:d+n], src.Pix[s:s+n]) // copy is memmove-safe within a row
	}
	if dst == src && oy > 0 {
		// Destination rows lie below the source rows: go bottom-up so no
		// source row is overwritten before it is read.
		for y := y1 - 1; y >= y0; y-- {
			row(y)
		}
		return
	}
	for y := y0; y < y1; y++ {
		row(y)
	}
}

// rotate90 returns a copy of b rotated 90 degrees clockwise (W and H swap).
func rotate90(b *Bitmap) *Bitmap {
	out := newBitmap(b.H, b.W)
	for y := 0; y < b.H; y++ {
		for x := 0; x < b.W; x++ {
			s := (y*b.W + x) * 4
			d := (x*out.W + (b.H - 1 - y)) * 4
			copy(out.Pix[d:d+4], b.Pix[s:s+4])
		}
	}
	return out
}
