package danmaku

import (
	"math"
	"sort"
	"unicode"
)

const (
	DefaultScrollDurationMs int64   = 8000
	DefaultFixedDurationMs  int64   = 4000
	DefaultViewportWidth    float64 = 1280
	DefaultViewportHeight   float64 = 720
	DefaultMinGapPx         float64 = 16
	DefaultCharWidthFactor  float64 = 0.6
)

// QueryWindow returns comments whose TimeMs falls inside [startMs, endMs].
// When limit > 0, at most limit comments are returned.
func (t *Timeline) QueryWindow(startMs, endMs int64, limit int) []Comment {
	if t == nil || len(t.Comments) == 0 || endMs < startMs {
		return []Comment{}
	}
	lo := sort.Search(len(t.Comments), func(i int) bool {
		return t.Comments[i].TimeMs >= startMs
	})
	if lo >= len(t.Comments) {
		return []Comment{}
	}
	hi := sort.Search(len(t.Comments), func(i int) bool {
		return t.Comments[i].TimeMs > endMs
	})
	if hi <= lo {
		return []Comment{}
	}
	if limit > 0 && hi-lo > limit {
		hi = lo + limit
	}
	out := make([]Comment, hi-lo)
	copy(out, t.Comments[lo:hi])
	return out
}

// ActiveAt returns comments visible at playback positionMs, using each
// comment's own DurationMs or the supplied defaults when unset.
func (t *Timeline) ActiveAt(positionMs, scrollDurationMs, fixedDurationMs int64) []Comment {
	if t == nil || len(t.Comments) == 0 || positionMs < 0 {
		return []Comment{}
	}
	if scrollDurationMs <= 0 {
		scrollDurationMs = DefaultScrollDurationMs
	}
	if fixedDurationMs <= 0 {
		fixedDurationMs = DefaultFixedDurationMs
	}
	maxDur := max(scrollDurationMs, fixedDurationMs)
	// Also scan slightly earlier if comments may declare a longer explicit duration,
	// capped at 60s so active queries stay bounded.
	lookback := max(maxDur, int64(60000))
	start := max(int64(0), positionMs-lookback)
	candidates := t.QueryWindow(start, positionMs, 0)
	out := make([]Comment, 0, len(candidates))
	for _, c := range candidates {
		dur := commentDurationMs(c, scrollDurationMs, fixedDurationMs)
		if c.TimeMs <= positionMs && positionMs < c.TimeMs+dur {
			out = append(out, c)
		}
	}
	return out
}

// LayoutOptions configures basic anti-overlap track allocation.
type LayoutOptions struct {
	ViewportWidth        float64 `json:"viewportWidth"`
	ViewportHeight       float64 `json:"viewportHeight"`
	LineHeight           float64 `json:"lineHeight,omitempty"`
	MaxTracks            int     `json:"maxTracks,omitempty"`
	ScrollDurationMs     int64   `json:"scrollDurationMs,omitempty"`
	FixedDurationMs      int64   `json:"fixedDurationMs,omitempty"`
	CharWidthFactor      float64 `json:"charWidthFactor,omitempty"`
	MinGapPx             float64 `json:"minGapPx,omitempty"`
	AllowOverlapFallback bool    `json:"allowOverlapFallback,omitempty"`
}

// PlacedComment is a comment assigned to a concrete vertical track and time span.
type PlacedComment struct {
	Comment    Comment `json:"comment"`
	Track      int     `json:"track"`
	StartMs    int64   `json:"startMs"`
	EndMs      int64   `json:"endMs"`
	WidthPx    float64 `json:"widthPx"`
	Y          float64 `json:"y"`
	Overlapped bool    `json:"overlapped,omitempty"`
}

// LayoutResult is the output of track collision avoidance.
type LayoutResult struct {
	Placed  []PlacedComment `json:"placed"`
	Dropped int             `json:"dropped"`
	Tracks  int             `json:"tracks"`
}

type normalizedLayout struct {
	width           float64
	height          float64
	lineHeight      float64
	tracks          int
	scrollDurMs     int64
	fixedDurMs      int64
	charWidthFactor float64
	minGapPx        float64
	allowOverlap    bool
}

func normalizeLayoutOptions(opts LayoutOptions) normalizedLayout {
	w := opts.ViewportWidth
	if w <= 0 {
		w = DefaultViewportWidth
	}
	h := opts.ViewportHeight
	if h <= 0 {
		h = DefaultViewportHeight
	}
	lh := opts.LineHeight
	if lh <= 0 {
		lh = DefaultFontSize * 1.3
	}
	tracks := opts.MaxTracks
	if tracks <= 0 {
		tracks = int(math.Floor(h / lh))
	}
	if tracks < 1 {
		tracks = 1
	}
	scrollDur := opts.ScrollDurationMs
	if scrollDur <= 0 {
		scrollDur = DefaultScrollDurationMs
	}
	fixedDur := opts.FixedDurationMs
	if fixedDur <= 0 {
		fixedDur = DefaultFixedDurationMs
	}
	cwf := opts.CharWidthFactor
	if cwf <= 0 {
		cwf = DefaultCharWidthFactor
	}
	gap := opts.MinGapPx
	if gap < 0 {
		gap = 0
	} else if opts.MinGapPx == 0 {
		gap = DefaultMinGapPx
	}
	return normalizedLayout{
		width:           w,
		height:          h,
		lineHeight:      lh,
		tracks:          tracks,
		scrollDurMs:     scrollDur,
		fixedDurMs:      fixedDur,
		charWidthFactor: cwf,
		minGapPx:        gap,
		allowOverlap:    opts.AllowOverlapFallback,
	}
}

type scrollSlot struct {
	occupied bool
	startMs  int64
	endMs    int64
	widthPx  float64
	speedPx  float64 // px per ms
}

type fixedSlot struct {
	freeAtMs int64
}

// Layout computes non-overlapping track assignments for every comment in the
// timeline. Scroll, reverse, top and bottom lanes are tracked independently.
func (t *Timeline) Layout(opts LayoutOptions) LayoutResult {
	if t == nil {
		return LayoutResult{Placed: []PlacedComment{}, Tracks: normalizeLayoutOptions(opts).tracks}
	}
	return layoutComments(t.Comments, opts)
}

// LayoutWindow computes track assignments for comments active within
// [startMs, endMs], warming up track occupancy from preceding comments so
// boundary items avoid overlapping comments that entered slightly earlier.
func (t *Timeline) LayoutWindow(startMs, endMs int64, opts LayoutOptions) LayoutResult {
	cfg := normalizeLayoutOptions(opts)
	if t == nil || len(t.Comments) == 0 || endMs < startMs {
		return LayoutResult{Placed: []PlacedComment{}, Tracks: cfg.tracks}
	}
	warmup := max(cfg.scrollDurMs, cfg.fixedDurMs)
	scanStart := max(int64(0), startMs-warmup)
	slice := t.QueryWindow(scanStart, endMs, 0)
	full := layoutComments(slice, opts)
	filtered := make([]PlacedComment, 0, len(full.Placed))
	for _, p := range full.Placed {
		if p.EndMs > startMs && p.StartMs <= endMs {
			filtered = append(filtered, p)
		}
	}
	return LayoutResult{
		Placed:  filtered,
		Dropped: full.Dropped,
		Tracks:  full.Tracks,
	}
}

func layoutComments(comments []Comment, opts LayoutOptions) LayoutResult {
	cfg := normalizeLayoutOptions(opts)
	scrollTracks := make([]scrollSlot, cfg.tracks)
	reverseTracks := make([]scrollSlot, cfg.tracks)
	topTracks := make([]fixedSlot, cfg.tracks)
	bottomTracks := make([]fixedSlot, cfg.tracks)

	placed := make([]PlacedComment, 0, len(comments))
	dropped := 0

	for _, c := range comments {
		durMs := commentDurationMs(c, cfg.scrollDurMs, cfg.fixedDurMs)
		widthPx := estimateTextWidth(c.Text, c.FontSize, cfg.charWidthFactor)
		startMs := c.TimeMs
		endMs := startMs + durMs

		var (
			track      = -1
			overlapped = false
			y          float64
		)

		switch c.Mode {
		case ModeTop:
			track, overlapped = placeFixed(topTracks, startMs, endMs, cfg.allowOverlap)
			if track >= 0 {
				y = float64(track) * cfg.lineHeight
			}
		case ModeBottom:
			track, overlapped = placeFixed(bottomTracks, startMs, endMs, cfg.allowOverlap)
			if track >= 0 {
				y = cfg.height - float64(track+1)*cfg.lineHeight
				if y < 0 {
					y = 0
				}
			}
		case ModeReverse:
			speed := (cfg.width + widthPx) / float64(durMs)
			track, overlapped = placeScroll(reverseTracks, startMs, endMs, widthPx, speed, cfg.width, cfg.minGapPx, cfg.allowOverlap)
			if track >= 0 {
				y = float64(track) * cfg.lineHeight
			}
		default:
			speed := (cfg.width + widthPx) / float64(durMs)
			track, overlapped = placeScroll(scrollTracks, startMs, endMs, widthPx, speed, cfg.width, cfg.minGapPx, cfg.allowOverlap)
			if track >= 0 {
				y = float64(track) * cfg.lineHeight
			}
		}

		if track < 0 {
			dropped++
			continue
		}
		placed = append(placed, PlacedComment{
			Comment:    c,
			Track:      track,
			StartMs:    startMs,
			EndMs:      endMs,
			WidthPx:    widthPx,
			Y:          y,
			Overlapped: overlapped,
		})
	}

	return LayoutResult{
		Placed:  placed,
		Dropped: dropped,
		Tracks:  cfg.tracks,
	}
}

func placeFixed(tracks []fixedSlot, startMs, endMs int64, allowOverlap bool) (int, bool) {
	bestIdx := 0
	bestFree := tracks[0].freeAtMs
	for i := range tracks {
		if startMs >= tracks[i].freeAtMs {
			tracks[i].freeAtMs = endMs
			return i, false
		}
		if tracks[i].freeAtMs < bestFree {
			bestFree = tracks[i].freeAtMs
			bestIdx = i
		}
	}
	if !allowOverlap {
		return -1, false
	}
	if endMs > tracks[bestIdx].freeAtMs {
		tracks[bestIdx].freeAtMs = endMs
	}
	return bestIdx, true
}

func placeScroll(tracks []scrollSlot, startMs, endMs int64, widthPx, speedPx, viewportW, minGapPx float64, allowOverlap bool) (int, bool) {
	bestIdx := 0
	bestEnd := tracks[0].endMs
	for i := range tracks {
		slot := tracks[i]
		if canPlaceScroll(slot, startMs, speedPx, viewportW, minGapPx) {
			tracks[i] = scrollSlot{
				occupied: true,
				startMs:  startMs,
				endMs:    endMs,
				widthPx:  widthPx,
				speedPx:  speedPx,
			}
			return i, false
		}
		if slot.endMs < bestEnd {
			bestEnd = slot.endMs
			bestIdx = i
		}
	}
	if !allowOverlap {
		return -1, false
	}
	tracks[bestIdx] = scrollSlot{
		occupied: true,
		startMs:  startMs,
		endMs:    endMs,
		widthPx:  widthPx,
		speedPx:  speedPx,
	}
	return bestIdx, true
}

// canPlaceScroll checks both entry clearance (previous comment's tail has
// entered the viewport plus minGapPx) and catch-up clearance (the new comment's
// head will not catch the previous comment's tail before the previous exits).
func canPlaceScroll(prev scrollSlot, startMs int64, newSpeed, viewportW, minGapPx float64) bool {
	if !prev.occupied || startMs >= prev.endMs {
		return true
	}
	dt := float64(startMs - prev.startMs)
	if dt < 0 {
		return false
	}
	// Distance the previous comment's tail has travelled past the entry edge.
	prevTailTravel := prev.speedPx*dt - prev.widthPx
	if prevTailTravel < minGapPx {
		return false
	}
	// When the previous comment finishes exiting at prev.endMs, ensure the new
	// comment's leading edge still leaves at least minGapPx behind it.
	remainMs := float64(prev.endMs - startMs)
	newHeadPos := newSpeed * remainMs
	return newHeadPos <= viewportW-minGapPx
}

func commentDurationMs(c Comment, scrollDurMs, fixedDurMs int64) int64 {
	if c.DurationMs > 0 {
		return c.DurationMs
	}
	switch c.Mode {
	case ModeTop, ModeBottom:
		return fixedDurMs
	default:
		return scrollDurMs
	}
}

func estimateTextWidth(text string, fontSize, narrowFactor float64) float64 {
	if fontSize <= 0 {
		fontSize = DefaultFontSize
	}
	var maxLine float64
	var cur float64
	for _, r := range text {
		if r == '\n' {
			if cur > maxLine {
				maxLine = cur
			}
			cur = 0
			continue
		}
		if isWideRune(r) {
			cur += fontSize
		} else {
			cur += fontSize * narrowFactor
		}
	}
	if cur > maxLine {
		maxLine = cur
	}
	return maxLine
}

func isWideRune(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) ||
		(r >= 0xFF01 && r <= 0xFF60) || (r >= 0x3000 && r <= 0x303F)
}
