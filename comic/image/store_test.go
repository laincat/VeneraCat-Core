package image

import (
	"bytes"
	"errors"
	"math"
	"sync"
	"testing"
)

// px encodes pixel number n as a distinct RGBA value.
func px(n int) [4]byte { return [4]byte{byte(n), byte(n + 100), byte(n + 200), 255} }

// pattern returns a w*h bitmap where pixel (x,y) is px(y*w+x+1).
func pattern(w, h int) *Bitmap {
	b := &Bitmap{W: w, H: h, Pix: make([]byte, w*h*4)}
	for i := 0; i < w*h; i++ {
		p := px(i + 1)
		copy(b.Pix[i*4:], p[:])
	}
	return b
}

func at(t *testing.T, b *Bitmap, x, y int) [4]byte {
	t.Helper()
	if x < 0 || y < 0 || x >= b.W || y >= b.H {
		t.Fatalf("at(%d,%d) outside %dx%d", x, y, b.W, b.H)
	}
	var p [4]byte
	copy(p[:], b.Pix[(y*b.W+x)*4:])
	return p
}

// grid renders the bitmap as pixel numbers (0 = transparent black).
func grid(t *testing.T, b *Bitmap) [][]int {
	t.Helper()
	out := make([][]int, b.H)
	for y := range out {
		out[y] = make([]int, b.W)
		for x := range out[y] {
			p := at(t, b, x, y)
			if p == [4]byte{} {
				continue
			}
			if p[3] != 255 || p[1] != p[0]+100 || p[2] != p[0]+200 {
				t.Fatalf("pixel (%d,%d) = %v is not from the pattern", x, y, p)
			}
			out[y][x] = int(p[0])
		}
	}
	return out
}

func eq(a, b [][]int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

func mustPut(t *testing.T, s *Store, b *Bitmap) int {
	t.Helper()
	k, err := s.Put(b)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustKey(t *testing.T, s *Store, m Message) int {
	t.Helper()
	v, err := s.Do(m)
	if err != nil {
		t.Fatalf("%s: %v", m.Function, err)
	}
	k, ok := v.(int)
	if !ok {
		t.Fatalf("%s returned %#v, want int key", m.Function, v)
	}
	return k
}

func mustGet(t *testing.T, s *Store, key int) *Bitmap {
	t.Helper()
	b, err := s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func wantGrid(t *testing.T, s *Store, key int, want [][]int) {
	t.Helper()
	if got := grid(t, mustGet(t, s, key)); !eq(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func newS() *Store { return NewStore(Limits{}) }

// The hand-checked pattern used below is 4x3:
//
//	 1  2  3  4
//	 5  6  7  8
//	 9 10 11 12

func TestKeysStartAtOneAndIncrease(t *testing.T) {
	s := newS()
	a := mustPut(t, s, pattern(4, 3))
	b := mustKey(t, s, Message{Function: "emptyImage", Width: 1, Height: 1})
	c := mustKey(t, s, Message{Function: "copyAndRotate90", Key: a})
	if a != 1 || b != 2 || c != 3 {
		t.Fatalf("keys = %d %d %d, want 1 2 3", a, b, c)
	}
}

func TestGetWidthHeight(t *testing.T) {
	s := newS()
	k := mustPut(t, s, pattern(4, 3))
	w, err := s.Do(Message{Function: "getWidth", Key: k})
	if err != nil || w != 4 {
		t.Fatalf("getWidth = %v, %v", w, err)
	}
	h, err := s.Do(Message{Function: "getHeight", Key: k})
	if err != nil || h != 3 {
		t.Fatalf("getHeight = %v, %v", h, err)
	}
}

func TestEmptyImage(t *testing.T) {
	s := newS()
	k := mustKey(t, s, Message{Function: "emptyImage", Width: 4, Height: 3})
	b := mustGet(t, s, k)
	if b.W != 4 || b.H != 3 || !bytes.Equal(b.Pix, make([]byte, 48)) {
		t.Fatalf("bad empty image: %dx%d len %d", b.W, b.H, len(b.Pix))
	}
	for _, c := range [][2]int{{0, 1}, {1, 0}, {-1, 5}, {5, -1}, {0, 0}} {
		if _, err := s.Do(Message{Function: "emptyImage", Width: c[0], Height: c[1]}); !errors.Is(err, ErrInvalid) {
			t.Errorf("emptyImage(%d,%d) err = %v, want ErrInvalid", c[0], c[1], err)
		}
	}
}

func TestCopyRange(t *testing.T) {
	s := newS()
	src := mustPut(t, s, pattern(4, 3))
	k := mustKey(t, s, Message{Function: "copyRange", Key: src, X: 1, Y: 1, Width: 2, Height: 2})
	wantGrid(t, s, k, [][]int{{6, 7}, {10, 11}})
	// The copy is independent of the source.
	mustGet(t, s, k).Pix[0] = 0xEE
	if mustGet(t, s, src).Pix[(1*4+1)*4] != 6 {
		t.Fatal("copy aliases source")
	}
	k = mustKey(t, s, Message{Function: "copyRange", Key: src, Width: 4, Height: 3})
	wantGrid(t, s, k, [][]int{{1, 2, 3, 4}, {5, 6, 7, 8}, {9, 10, 11, 12}})
}

func TestCopyRangeClipping(t *testing.T) {
	s := newS()
	src := mustPut(t, s, pattern(4, 3))

	// Partly outside on the right/bottom: missing area is transparent black.
	k := mustKey(t, s, Message{Function: "copyRange", Key: src, X: 3, Y: 2, Width: 2, Height: 2})
	wantGrid(t, s, k, [][]int{{12, 0}, {0, 0}})

	// Partly outside on the left/top.
	k = mustKey(t, s, Message{Function: "copyRange", Key: src, X: -1, Y: -1, Width: 3, Height: 3})
	wantGrid(t, s, k, [][]int{{0, 0, 0}, {0, 1, 2}, {0, 5, 6}})

	// Rectangle larger than the source on both sides.
	k = mustKey(t, s, Message{Function: "copyRange", Key: src, X: -1, Y: 0, Width: 6, Height: 3})
	wantGrid(t, s, k, [][]int{{0, 1, 2, 3, 4, 0}, {0, 5, 6, 7, 8, 0}, {0, 9, 10, 11, 12, 0}})

	nilCases := []Message{
		{X: 4, Y: 0, Width: 1, Height: 1},  // right of source
		{X: 0, Y: 3, Width: 1, Height: 1},  // below source
		{X: -2, Y: 0, Width: 2, Height: 1}, // ends exactly at x=0
		{X: 0, Y: -2, Width: 1, Height: 2}, // ends exactly at y=0
		{X: 0, Y: 0, Width: 0, Height: 1},
		{X: 0, Y: 0, Width: 1, Height: 0},
		{X: 0, Y: 0, Width: -1, Height: 1},
		{X: 0, Y: 0, Width: 1, Height: -1},
		{X: math.MaxInt32, Y: math.MaxInt32, Width: 1, Height: 1},
		{X: math.MinInt32, Y: math.MinInt32, Width: 1, Height: 1},
		{X: math.MaxInt, Y: 0, Width: math.MaxInt, Height: 1},
		{X: math.MinInt, Y: 0, Width: math.MaxInt, Height: 1},
	}
	before := len(s.bitmaps)
	for i, m := range nilCases {
		m.Function, m.Key = "copyRange", src
		v, err := s.Do(m)
		if v != nil || err != nil {
			t.Errorf("case %d %+v: got %#v, %v; want nil, nil", i, m, v, err)
		}
	}
	if len(s.bitmaps) != before {
		t.Fatal("nil copyRange created bitmaps")
	}
}

func TestCopyAndRotate90(t *testing.T) {
	s := newS()
	src := mustPut(t, s, pattern(4, 3))
	k := mustKey(t, s, Message{Function: "copyAndRotate90", Key: src})
	if b := mustGet(t, s, k); b.W != 3 || b.H != 4 {
		t.Fatalf("rotated size %dx%d, want 3x4", b.W, b.H)
	}
	wantGrid(t, s, k, [][]int{{9, 5, 1}, {10, 6, 2}, {11, 7, 3}, {12, 8, 4}})
	wantGrid(t, s, src, [][]int{{1, 2, 3, 4}, {5, 6, 7, 8}, {9, 10, 11, 12}}) // source untouched
}

func TestRotateTwiceIs180AndFourTimesIsIdentity(t *testing.T) {
	s := newS()
	src := mustPut(t, s, pattern(4, 3))
	r1 := mustKey(t, s, Message{Function: "copyAndRotate90", Key: src})
	r2 := mustKey(t, s, Message{Function: "copyAndRotate90", Key: r1})
	wantGrid(t, s, r2, [][]int{{12, 11, 10, 9}, {8, 7, 6, 5}, {4, 3, 2, 1}})
	r3 := mustKey(t, s, Message{Function: "copyAndRotate90", Key: r2})
	r4 := mustKey(t, s, Message{Function: "copyAndRotate90", Key: r3})
	got, want := mustGet(t, s, r4), mustGet(t, s, src)
	if got.W != want.W || got.H != want.H || !bytes.Equal(got.Pix, want.Pix) {
		t.Fatal("four rotations are not the identity")
	}
}

func TestFillImageAtNoBlending(t *testing.T) {
	s := newS()
	dst := mustPut(t, s, pattern(4, 3))
	small := mustKey(t, s, Message{Function: "emptyImage", Width: 2, Height: 2})
	if _, err := s.Do(Message{Function: "fillImageAt", Key: dst, Image: small, X: 1, Y: 1}); err != nil {
		t.Fatal(err)
	}
	// Transparent black replaces the pattern: copy, not blend.
	wantGrid(t, s, dst, [][]int{{1, 2, 3, 4}, {5, 0, 0, 8}, {9, 0, 0, 12}})
}

func TestFillImageAtPlaceAndClip(t *testing.T) {
	s := newS()
	src := mustPut(t, s, pattern(4, 3))
	dst := mustKey(t, s, Message{Function: "emptyImage", Width: 5, Height: 4})
	do := func(x, y int) {
		t.Helper()
		v, err := s.Do(Message{Function: "fillImageAt", Key: dst, Image: src, X: x, Y: y})
		if v != nil || err != nil {
			t.Fatalf("fillImageAt = %#v, %v", v, err)
		}
	}
	do(1, 1)
	wantGrid(t, s, dst, [][]int{
		{0, 0, 0, 0, 0},
		{0, 1, 2, 3, 4},
		{0, 5, 6, 7, 8},
		{0, 9, 10, 11, 12},
	})
	// Clipped at the bottom-right; overwrites previous content.
	do(3, 3)
	wantGrid(t, s, dst, [][]int{
		{0, 0, 0, 0, 0},
		{0, 1, 2, 3, 4},
		{0, 5, 6, 7, 8},
		{0, 9, 10, 1, 2},
	})
	// Clipped at the top-left: source (2,1)..(3,2) lands at (0,0).
	do(-2, -1)
	want := [][]int{
		{7, 8, 0, 0, 0},
		{11, 12, 2, 3, 4},
		{0, 5, 6, 7, 8},
		{0, 9, 10, 1, 2},
	}
	wantGrid(t, s, dst, want)
	// Entirely outside or absurd offsets: no change, no panic.
	for _, c := range [][2]int{{5, 0}, {0, 4}, {-4, 0}, {0, -3}, {math.MaxInt, math.MaxInt}, {math.MinInt, math.MinInt}, {math.MaxInt32, 0}} {
		do(c[0], c[1])
	}
	wantGrid(t, s, dst, want)
}

func TestFillImageRangeAt(t *testing.T) {
	s := newS()
	src := mustPut(t, s, pattern(4, 3))
	dst := mustKey(t, s, Message{Function: "emptyImage", Width: 4, Height: 3})
	fill := func(m Message) {
		t.Helper()
		m.Function, m.Key, m.Image = "fillImageRangeAt", dst, src
		if v, err := s.Do(m); v != nil || err != nil {
			t.Fatalf("%+v -> %#v, %v", m, v, err)
		}
	}
	fill(Message{X: 0, Y: 0, SrcX: 1, SrcY: 1, Width: 2, Height: 2})
	wantGrid(t, s, dst, [][]int{{6, 7, 0, 0}, {10, 11, 0, 0}, {0, 0, 0, 0}})

	// Source rectangle partly outside the source: only the inside part is drawn.
	fill(Message{X: 2, Y: 1, SrcX: 3, SrcY: 2, Width: 3, Height: 3})
	wantGrid(t, s, dst, [][]int{{6, 7, 0, 0}, {10, 11, 12, 0}, {0, 0, 0, 0}})

	// A negative source origin keeps the offset: source x=0 lands at X+1.
	fill(Message{X: 2, Y: 0, SrcX: -1, SrcY: 0, Width: 3, Height: 1})
	wantGrid(t, s, dst, [][]int{{6, 7, 0, 1}, {10, 11, 12, 0}, {0, 0, 0, 0}})

	// Empty or negative ranges, and absurd values, change nothing.
	for _, m := range []Message{
		{Width: 0, Height: 1}, {Width: 1, Height: 0}, {Width: -3, Height: -3},
		{SrcX: 4, Width: 1, Height: 1}, {SrcY: 3, Width: 1, Height: 1},
		{SrcX: math.MaxInt, SrcY: math.MaxInt, Width: math.MaxInt, Height: math.MaxInt},
		{SrcX: math.MinInt, Width: math.MaxInt, Height: 1},
		{X: math.MaxInt, Width: math.MaxInt32, Height: math.MaxInt32},
		{X: math.MinInt, Y: math.MinInt, Width: math.MaxInt, Height: math.MaxInt},
	} {
		fill(m)
	}
	wantGrid(t, s, dst, [][]int{{6, 7, 0, 1}, {10, 11, 12, 0}, {0, 0, 0, 0}})
}

func TestFillImageSelfOverlap(t *testing.T) {
	// Every shift of the 4x3 pattern onto itself must equal drawing a snapshot.
	for dy := -3; dy <= 3; dy++ {
		for dx := -4; dx <= 4; dx++ {
			s := newS()
			k := mustPut(t, s, pattern(4, 3))
			snap := mustPut(t, s, pattern(4, 3))
			want := mustKey(t, s, Message{Function: "copyRange", Key: k, Width: 4, Height: 3})
			if _, err := s.Do(Message{Function: "fillImageAt", Key: want, Image: snap, X: dx, Y: dy}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Do(Message{Function: "fillImageAt", Key: k, Image: k, X: dx, Y: dy}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(mustGet(t, s, k).Pix, mustGet(t, s, want).Pix) {
				t.Errorf("self fillImageAt(%d,%d) differs from snapshot draw:\n got %v\nwant %v",
					dx, dy, grid(t, mustGet(t, s, k)), grid(t, mustGet(t, s, want)))
			}
		}
	}
	// Range variant: X, Y, SrcX, SrcY, Width, Height.
	for _, c := range [][6]int{{1, 1, 0, 0, 3, 2}, {0, 0, 1, 1, 3, 2}, {1, 0, 0, 0, 3, 3}, {0, 1, 0, 0, 4, 2}} {
		s := newS()
		k := mustPut(t, s, pattern(4, 3))
		snap := mustPut(t, s, pattern(4, 3))
		want := mustKey(t, s, Message{Function: "copyRange", Key: k, Width: 4, Height: 3})
		m := Message{Function: "fillImageRangeAt", Key: want, Image: snap, X: c[0], Y: c[1], SrcX: c[2], SrcY: c[3], Width: c[4], Height: c[5]}
		if _, err := s.Do(m); err != nil {
			t.Fatal(err)
		}
		m.Key, m.Image = k, k
		if _, err := s.Do(m); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(mustGet(t, s, k).Pix, mustGet(t, s, want).Pix) {
			t.Errorf("self fillImageRangeAt %v differs from snapshot draw", c)
		}
	}
}

func TestUnknownKeyAndFunction(t *testing.T) {
	s := newS()
	k := mustPut(t, s, pattern(2, 2))
	for _, m := range []Message{
		{Function: "copyRange", Key: 99, Width: 1, Height: 1},
		{Function: "copyAndRotate90", Key: 0},
		{Function: "copyAndRotate90", Key: -1},
		{Function: "fillImageAt", Key: 99, Image: k},
		{Function: "fillImageAt", Key: k, Image: 99},
		{Function: "fillImageRangeAt", Key: k, Image: 99, Width: 1, Height: 1},
		{Function: "fillImageRangeAt", Key: 99, Image: k, Width: 1, Height: 1},
		{Function: "getWidth", Key: 99},
		{Function: "getHeight", Key: 99},
	} {
		if _, err := s.Do(m); !errors.Is(err, ErrUnknownKey) {
			t.Errorf("%+v: err = %v, want ErrUnknownKey", m, err)
		}
	}
	if _, err := s.Get(99); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("Get(99) err = %v", err)
	}
	if _, err := s.Do(Message{Function: "nope"}); !errors.Is(err, ErrUnknownFunction) {
		t.Errorf("unknown function err = %v", err)
	}
	if _, err := s.Do(Message{}); !errors.Is(err, ErrUnknownFunction) {
		t.Errorf("empty function err = %v", err)
	}
}

func TestPutValidation(t *testing.T) {
	s := newS()
	bad := []*Bitmap{
		nil,
		{W: 0, H: 1},
		{W: 1, H: 0},
		{W: -1, H: -1, Pix: make([]byte, 4)},
		{W: 2, H: 2, Pix: make([]byte, 15)},
		{W: 2, H: 2, Pix: make([]byte, 17)},
		{W: math.MaxInt, H: math.MaxInt, Pix: make([]byte, 4)},
		{W: math.MaxInt32, H: math.MaxInt32, Pix: nil},
	}
	for i, b := range bad {
		if _, err := s.Put(b); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: err = %v, want ErrInvalid", i, err)
		}
	}
	// A failed Put must not consume a key.
	if k := mustPut(t, s, pattern(1, 1)); k != 1 {
		t.Fatalf("first key = %d, want 1", k)
	}
}

func TestLimitMaxPixels(t *testing.T) {
	s := NewStore(Limits{MaxPixels: 100, MaxBitmaps: 10})
	if _, err := s.Put(pattern(10, 11)); !errors.Is(err, ErrLimit) {
		t.Errorf("Put over limit err = %v", err)
	}
	mustPut(t, s, pattern(10, 10)) // exactly at the limit
	mustKey(t, s, Message{Function: "emptyImage", Width: 100, Height: 1})
	for _, c := range [][2]int{{101, 1}, {1, 101}, {11, 10}, {math.MaxInt32, math.MaxInt32}, {math.MaxInt, math.MaxInt}, {math.MaxInt32, 1}} {
		if _, err := s.Do(Message{Function: "emptyImage", Width: c[0], Height: c[1]}); !errors.Is(err, ErrLimit) {
			t.Errorf("emptyImage(%d,%d) err = %v, want ErrLimit", c[0], c[1], err)
		}
	}
	// copyRange asking for a huge output over a small overlap is an error, not an allocation.
	src := mustPut(t, s, pattern(4, 3))
	if _, err := s.Do(Message{Function: "copyRange", Key: src, Width: math.MaxInt32, Height: math.MaxInt32}); !errors.Is(err, ErrLimit) {
		t.Errorf("huge copyRange err = %v, want ErrLimit", err)
	}
	if _, err := s.Do(Message{Function: "copyRange", Key: src, X: -5, Y: -5, Width: math.MaxInt, Height: math.MaxInt}); !errors.Is(err, ErrLimit) {
		t.Errorf("max copyRange err = %v, want ErrLimit", err)
	}
	// Rotation keeps the pixel count, so it stays within the limit.
	big := mustKey(t, s, Message{Function: "emptyImage", Width: 100, Height: 1})
	mustKey(t, s, Message{Function: "copyAndRotate90", Key: big})
}

func TestLimitMaxBitmaps(t *testing.T) {
	s := NewStore(Limits{MaxBitmaps: 3})
	a := mustPut(t, s, pattern(2, 2))
	mustKey(t, s, Message{Function: "emptyImage", Width: 1, Height: 1})
	mustKey(t, s, Message{Function: "copyAndRotate90", Key: a})
	if _, err := s.Put(pattern(1, 1)); !errors.Is(err, ErrLimit) {
		t.Errorf("Put err = %v", err)
	}
	for _, m := range []Message{
		{Function: "emptyImage", Width: 1, Height: 1},
		{Function: "copyAndRotate90", Key: a},
		{Function: "copyRange", Key: a, Width: 1, Height: 1},
	} {
		if _, err := s.Do(m); !errors.Is(err, ErrLimit) {
			t.Errorf("%s err = %v, want ErrLimit", m.Function, err)
		}
	}
	// Drawing still works when the store is full.
	if _, err := s.Do(Message{Function: "fillImageAt", Key: a, Image: a}); err != nil {
		t.Error(err)
	}
	// Failed creations did not consume keys.
	if _, err := s.Get(4); !errors.Is(err, ErrUnknownKey) {
		t.Error("key 4 exists")
	}
}

func TestDefaultLimits(t *testing.T) {
	s := NewStore(Limits{})
	if s.limits.MaxPixels != 64<<20 || s.limits.MaxBitmaps != 64 {
		t.Fatalf("defaults = %+v", s.limits)
	}
	if _, err := s.Do(Message{Function: "emptyImage", Width: 64<<20 + 1, Height: 1}); !errors.Is(err, ErrLimit) {
		t.Errorf("err = %v", err)
	}
	// Even with an absurd MaxPixels, a MaxInt32 x MaxInt32 request is refused, not attempted.
	s = NewStore(Limits{MaxPixels: math.MaxInt, MaxBitmaps: 1})
	if _, err := s.Do(Message{Function: "emptyImage", Width: math.MaxInt32, Height: math.MaxInt32}); !errors.Is(err, ErrLimit) {
		t.Errorf("err = %v, want ErrLimit", err)
	}
}

func TestConcurrency(t *testing.T) {
	s := NewStore(Limits{MaxBitmaps: 100000})
	src := mustPut(t, s, pattern(4, 3))
	const workers, iters = 8, 100
	keys := make(chan int, workers*iters*2)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				k, err := s.Do(Message{Function: "copyAndRotate90", Key: src})
				if err != nil {
					t.Error(err)
					return
				}
				e, err := s.Do(Message{Function: "emptyImage", Width: 4, Height: 3})
				if err != nil {
					t.Error(err)
					return
				}
				keys <- k.(int)
				keys <- e.(int)
				if _, err := s.Do(Message{Function: "fillImageAt", Key: e.(int), Image: src, X: i % 3, Y: i % 2}); err != nil {
					t.Error(err)
					return
				}
				if _, err := s.Do(Message{Function: "getWidth", Key: k.(int)}); err != nil {
					t.Error(err)
					return
				}
				if _, err := s.Get(src); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(keys)
	seen := map[int]bool{}
	for k := range keys {
		if seen[k] || k <= src {
			t.Fatalf("key %d reused or out of order", k)
		}
		seen[k] = true
	}
	if len(seen) != workers*iters*2 {
		t.Fatalf("got %d distinct keys", len(seen))
	}
}

// Typical unscramble: split an 8x8 image into four 8x2 strips and rebuild it
// with the strip order reversed.
func TestUnscrambleReverseStrips(t *testing.T) {
	const n, strips = 8, 4
	const sh = n / strips
	s := newS()
	src := mustPut(t, s, pattern(n, n))
	out := mustKey(t, s, Message{Function: "emptyImage", Width: n, Height: n})
	for i := 0; i < strips; i++ {
		strip := mustKey(t, s, Message{Function: "copyRange", Key: src, X: 0, Y: i * sh, Width: n, Height: sh})
		if _, err := s.Do(Message{Function: "fillImageAt", Key: out, Image: strip, X: 0, Y: (strips - 1 - i) * sh}); err != nil {
			t.Fatal(err)
		}
	}
	got := mustGet(t, s, out)
	want := pattern(n, n)
	for y := 0; y < n; y++ {
		sy := (strips-1-y/sh)*sh + y%sh // source row that ends up at row y
		for x := 0; x < n; x++ {
			if at(t, got, x, y) != at(t, want, x, sy) {
				t.Fatalf("pixel (%d,%d) = %v, want source (%d,%d) = %v", x, y, at(t, got, x, y), x, sy, at(t, want, x, sy))
			}
		}
	}
	// Spot check: output (0,0) is source (0,6) = pixel 6*8+1; output (7,7) is source (7,1) = pixel 1*8+8.
	if at(t, got, 0, 0) != px(6*n+1) || at(t, got, 7, 7) != px(1*n+8) {
		t.Fatal("spot check failed")
	}
	// Same result via fillImageRangeAt directly from the source.
	out2 := mustKey(t, s, Message{Function: "emptyImage", Width: n, Height: n})
	for i := 0; i < strips; i++ {
		if _, err := s.Do(Message{Function: "fillImageRangeAt", Key: out2, Image: src, X: 0, Y: (strips - 1 - i) * sh, SrcX: 0, SrcY: i * sh, Width: n, Height: sh}); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(mustGet(t, s, out2).Pix, got.Pix) {
		t.Fatal("fillImageRangeAt result differs")
	}
}
