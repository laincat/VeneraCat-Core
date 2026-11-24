package gohost

import "github.com/laincat/veneracat-core/video/danmaku"

// ParseDanmaku parses a local danmaku document (Bilibili XML, ASS or JSON)
// into a normalized, time-sorted Timeline. Core never fetches danmaku online.
func (c *Core) ParseDanmaku(format danmaku.Format, data []byte) (*danmaku.Timeline, error) {
	return danmaku.Parse(format, data)
}

// QueryDanmakuWindow parses local danmaku data and returns comments whose
// timestamps lie within [startMs, endMs].
func (c *Core) QueryDanmakuWindow(format danmaku.Format, data []byte, startMs, endMs int64, limit int) ([]danmaku.Comment, error) {
	tl, err := danmaku.Parse(format, data)
	if err != nil {
		return nil, err
	}
	return tl.QueryWindow(startMs, endMs, limit), nil
}

// LayoutDanmaku parses local danmaku data and computes collision-avoiding
// vertical track placements.
func (c *Core) LayoutDanmaku(format danmaku.Format, data []byte, opts danmaku.LayoutOptions) (danmaku.LayoutResult, error) {
	tl, err := danmaku.Parse(format, data)
	if err != nil {
		return danmaku.LayoutResult{}, err
	}
	return tl.Layout(opts), nil
}

// LayoutDanmakuWindow computes collision-avoiding track placements for comments
// active within [startMs, endMs].
func (c *Core) LayoutDanmakuWindow(format danmaku.Format, data []byte, startMs, endMs int64, opts danmaku.LayoutOptions) (danmaku.LayoutResult, error) {
	tl, err := danmaku.Parse(format, data)
	if err != nil {
		return danmaku.LayoutResult{}, err
	}
	return tl.LayoutWindow(startMs, endMs, opts), nil
}
