package js

import (
	"context"
	"errors"
	"testing"

	"github.com/laincat/veneracat-core/comic/image"
)

// A 2x2 bitmap with distinct pixels, so a rotate or a copy is visible.
func testBitmap() image.Bitmap {
	return image.Bitmap{W: 2, H: 2, Pix: []byte{
		1, 2, 3, 4, 5, 6, 7, 8,
		9, 10, 11, 12, 13, 14, 15, 16,
	}}
}

func TestModifyImageRunsTheScript(t *testing.T) {
	r := newRT(t, Options{}, nil)
	script := "let modifyImage = (image) => image.copyAndRotate90()"
	got, err := r.ModifyImage(context.Background(), script, testBitmap())
	if err != nil {
		t.Fatal(err)
	}
	if got.W != 2 || got.H != 2 || len(got.Pix) != 16 {
		t.Fatalf("size = %dx%d, %d bytes", got.W, got.H, len(got.Pix))
	}
	// A clockwise quarter turn moves the top-left pixel to the top-right,
	// which is the second pixel of a two-pixel row.
	if got.Pix[4] != 1 || got.Pix[5] != 2 || got.Pix[0] != 9 {
		t.Errorf("rotation is not a clockwise quarter turn: %v", got.Pix)
	}
}

// jm's shape: read the height, cut it into blocks, build a new image and copy
// the blocks in reverse. The point is that the store and the geometry the
// script reads are the ones Core handed over.
func TestModifyImageRebuildsFromBlocks(t *testing.T) {
	r := newRT(t, Options{}, nil)
	script := `
		let modifyImage = (image) => {
			const half = Math.floor(image.height / 2)
			let res = Image.empty(image.width, image.height)
			for (let i = 0; i < 2; i++) {
				res.fillImageRangeAt(0, i * half, image, 0, (1 - i) * half, image.width, half)
			}
			return res
		}`
	got, err := r.ModifyImage(context.Background(), script, testBitmap())
	if err != nil {
		t.Fatal(err)
	}
	// Row 0 and row 1 swapped: the first pixel is what was at (0,1).
	if got.Pix[0] != 9 || got.Pix[8] != 1 {
		t.Errorf("blocks were not swapped: %v", got.Pix)
	}
}

func TestModifyImageRejectsWhatItCannotUse(t *testing.T) {
	ctx := context.Background()
	r := newRT(t, Options{}, nil)
	if _, err := r.ModifyImage(ctx, "", testBitmap()); !errors.Is(err, ErrModifyImage) {
		t.Errorf("an empty script answered %v", err)
	}
	if _, err := r.ModifyImage(ctx, "let x = 1", testBitmap()); !errors.Is(err, ErrModifyImage) {
		t.Errorf("a script without modifyImage answered %v", err)
	}
	if _, err := r.ModifyImage(ctx, "let modifyImage = () => 'not an image'", testBitmap()); !errors.Is(err, ErrModifyImage) {
		t.Errorf("a non-image result answered %v", err)
	}
	// A bitmap whose length disagrees with its size is refused before the
	// script can read past it.
	broken := image.Bitmap{W: 2, H: 2, Pix: []byte{1, 2, 3}}
	if _, err := r.ModifyImage(ctx, "let modifyImage = (i) => i", broken); !errors.Is(err, image.ErrInvalid) {
		t.Errorf("a short bitmap answered %v", err)
	}
}

// A script that runs more than once must not keep state between calls: each
// call gets its own runtime.
func TestModifyImageIsIndependentPerCall(t *testing.T) {
	ctx := context.Background()
	r := newRT(t, Options{}, nil)
	script := "let n = (globalThis.__n = (globalThis.__n || 0) + 1); let modifyImage = (image) => image"
	if _, err := r.ModifyImage(ctx, script, testBitmap()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ModifyImage(ctx, script, testBitmap()); err != nil {
		t.Fatal(err)
	}
}
