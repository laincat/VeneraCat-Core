package mobile

import (
	"encoding/json"
	"fmt"

	"github.com/laincat/veneracat-core/video/danmaku"
)

// ParseDanmaku parses a local danmaku document (Bilibili XML, ASS or JSON;
// format may be "", "auto", "xml", "ass" or "json") and returns a normalized,
// time-sorted danmaku.Timeline JSON document.
func (c *Core) ParseDanmaku(format, content string) (_ string, err error) {
	defer guard(&err)
	tl, err := danmaku.Parse(danmaku.Format(format), []byte(content))
	if err != nil {
		return "", err
	}
	return encode(tl)
}

// DanmakuQueryWindow parses a local danmaku document (or a previously parsed
// Timeline JSON string) and returns comments in [startMs, endMs] as JSON.
func (c *Core) DanmakuQueryWindow(format, content string, startMs, endMs int64, limit int) (_ string, err error) {
	defer guard(&err)
	tl, err := danmaku.Parse(danmaku.Format(format), []byte(content))
	if err != nil {
		return "", err
	}
	return encode(tl.QueryWindow(startMs, endMs, limit))
}

// DanmakuLayout computes collision-avoiding track layout for a local danmaku
// document and returns danmaku.LayoutResult JSON. optionsJSON mirrors
// danmaku.LayoutOptions and may be empty for defaults.
func (c *Core) DanmakuLayout(format, content, optionsJSON string) (_ string, err error) {
	defer guard(&err)
	var opts danmaku.LayoutOptions
	if optionsJSON != "" {
		if err := json.Unmarshal([]byte(optionsJSON), &opts); err != nil {
			return "", fmt.Errorf("mobile: invalid danmaku layout options JSON: %w", err)
		}
	}
	tl, err := danmaku.Parse(danmaku.Format(format), []byte(content))
	if err != nil {
		return "", err
	}
	return encode(tl.Layout(opts))
}

// DanmakuLayoutWindow computes collision-avoiding track layout for comments
// active in [startMs, endMs] and returns danmaku.LayoutResult JSON.
func (c *Core) DanmakuLayoutWindow(format, content string, startMs, endMs int64, optionsJSON string) (_ string, err error) {
	defer guard(&err)
	var opts danmaku.LayoutOptions
	if optionsJSON != "" {
		if err := json.Unmarshal([]byte(optionsJSON), &opts); err != nil {
			return "", fmt.Errorf("mobile: invalid danmaku layout options JSON: %w", err)
		}
	}
	tl, err := danmaku.Parse(danmaku.Format(format), []byte(content))
	if err != nil {
		return "", err
	}
	return encode(tl.LayoutWindow(startMs, endMs, opts))
}
