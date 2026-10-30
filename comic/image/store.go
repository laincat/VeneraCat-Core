package image

import (
	"fmt"
	"math"
	"sync"
)

// Default limits used when a Limits field is zero or negative.
const (
	DefaultMaxPixels  = 64 << 20
	DefaultMaxBitmaps = 64
)

// Limits bounds a Store. Zero or negative fields select the defaults.
type Limits struct {
	// MaxPixels is the largest allowed width*height of any single bitmap.
	MaxPixels int
	// MaxBitmaps is the largest number of bitmaps a Store will ever hold.
	// Bitmaps are never freed (keys are never reused), so this also bounds
	// the number of keys handed out.
	MaxBitmaps int
}

// Message is one image call from a modifyImage script. Only the fields that
// the named Function uses are read.
type Message struct {
	Function                        string
	Key, Image                      int
	X, Y, Width, Height, SrcX, SrcY int
}

// Store holds bitmaps by integer key. Keys start at 1, increase by one and are
// never reused. A Store is safe for concurrent use; the bitmaps themselves are
// not (see Get).
type Store struct {
	mu      sync.Mutex
	limits  Limits
	next    int
	bitmaps map[int]*Bitmap
}

// NewStore returns an empty Store using limits (defaults for unset fields).
func NewStore(limits Limits) *Store {
	if limits.MaxPixels <= 0 {
		limits.MaxPixels = DefaultMaxPixels
	}
	// Keep pixels*4 representable as an int.
	limits.MaxPixels = min(limits.MaxPixels, math.MaxInt/4)
	if limits.MaxBitmaps <= 0 {
		limits.MaxBitmaps = DefaultMaxBitmaps
	}
	return &Store{limits: limits, next: 1, bitmaps: make(map[int]*Bitmap)}
}

// Put registers b and returns its key. The Store takes ownership of b and its
// Pix slice without copying; the caller must not touch b afterwards except
// through Get after scripts have finished.
func (s *Store) Put(b *Bitmap) (int, error) {
	if err := b.check(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !fits(int64(b.W), int64(b.H), int64(s.limits.MaxPixels)) {
		return 0, fmt.Errorf("%w: %dx%d exceeds %d pixels", ErrLimit, b.W, b.H, s.limits.MaxPixels)
	}
	return s.addLocked(b)
}

// Get returns the bitmap stored under key. The returned pointer is the live
// bitmap, not a copy: read it only when no Do call can run concurrently.
func (s *Store) Get(key int) (*Bitmap, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(key)
}

func (s *Store) getLocked(key int) (*Bitmap, error) {
	b, ok := s.bitmaps[key]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownKey, key)
	}
	return b, nil
}

func (s *Store) addLocked(b *Bitmap) (int, error) {
	if len(s.bitmaps) >= s.limits.MaxBitmaps {
		return 0, fmt.Errorf("%w: more than %d bitmaps", ErrLimit, s.limits.MaxBitmaps)
	}
	key := s.next
	s.next++
	s.bitmaps[key] = b
	return key, nil
}

// reserveLocked checks every limit for a new w*h bitmap before any memory is
// allocated.
func (s *Store) reserveLocked(w, h int64) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("%w: size %dx%d", ErrInvalid, w, h)
	}
	if !fits(w, h, int64(s.limits.MaxPixels)) {
		return fmt.Errorf("%w: %dx%d exceeds %d pixels", ErrLimit, w, h, s.limits.MaxPixels)
	}
	if len(s.bitmaps) >= s.limits.MaxBitmaps {
		return fmt.Errorf("%w: more than %d bitmaps", ErrLimit, s.limits.MaxBitmaps)
	}
	return nil
}

// Do executes one image call. Results by function:
//
//	copyRange         new key (int), or nil if nothing of the source is covered
//	copyAndRotate90   new key (int)
//	fillImageAt       nil
//	fillImageRangeAt  nil
//	getWidth          int
//	getHeight         int
//	emptyImage        new key (int)
//
// Behaviour choices:
//
//   - copyRange returns a Width x Height bitmap. The part of the rectangle that
//     lies inside the source is copied; the part outside is transparent black
//     (0,0,0,0). If Width or Height <= 0, or the rectangle does not intersect
//     the source at all, the result is nil and no bitmap is created. A
//     rectangle larger than the limits is an error.
//   - copyAndRotate90 rotates clockwise: the top-left pixel moves to the
//     top-right, new W = old H, new H = old W.
//   - fillImageAt and fillImageRangeAt copy pixels (no blending, alpha
//     included) and clip to both the destination and the source bitmap.
//     Anything outside either bitmap is skipped, never an error. Width or
//     Height <= 0 in fillImageRangeAt draws nothing. Drawing a bitmap onto
//     itself behaves as if the source were copied first.
//   - emptyImage returns an error for Width or Height <= 0 or over the limits.
//   - Unknown keys and unknown functions are errors.
func (s *Store) Do(m Message) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch m.Function {
	case "copyRange":
		src, err := s.getLocked(m.Key)
		if err != nil {
			return nil, err
		}
		x, y, w, h := clamp(m.X), clamp(m.Y), clamp(m.Width), clamp(m.Height)
		if w <= 0 || h <= 0 || x+w <= 0 || y+h <= 0 || x >= int64(src.W) || y >= int64(src.H) {
			return nil, nil
		}
		if err := s.reserveLocked(w, h); err != nil {
			return nil, err
		}
		out := newBitmap(int(w), int(h))
		blit(out, 0, 0, src, x, y, w, h)
		return s.addLocked(out)

	case "copyAndRotate90":
		src, err := s.getLocked(m.Key)
		if err != nil {
			return nil, err
		}
		if err := s.reserveLocked(int64(src.H), int64(src.W)); err != nil {
			return nil, err
		}
		return s.addLocked(rotate90(src))

	case "fillImageAt":
		dst, err := s.getLocked(m.Key)
		if err != nil {
			return nil, err
		}
		src, err := s.getLocked(m.Image)
		if err != nil {
			return nil, err
		}
		blit(dst, clamp(m.X), clamp(m.Y), src, 0, 0, int64(src.W), int64(src.H))
		return nil, nil

	case "fillImageRangeAt":
		dst, err := s.getLocked(m.Key)
		if err != nil {
			return nil, err
		}
		src, err := s.getLocked(m.Image)
		if err != nil {
			return nil, err
		}
		blit(dst, clamp(m.X), clamp(m.Y), src, clamp(m.SrcX), clamp(m.SrcY), clamp(m.Width), clamp(m.Height))
		return nil, nil

	case "getWidth":
		b, err := s.getLocked(m.Key)
		if err != nil {
			return nil, err
		}
		return b.W, nil

	case "getHeight":
		b, err := s.getLocked(m.Key)
		if err != nil {
			return nil, err
		}
		return b.H, nil

	case "emptyImage":
		w, h := clamp(m.Width), clamp(m.Height)
		if err := s.reserveLocked(w, h); err != nil {
			return nil, err
		}
		return s.addLocked(newBitmap(int(w), int(h)))
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownFunction, m.Function)
}
