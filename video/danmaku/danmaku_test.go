package danmaku

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseBilibiliXML(t *testing.T) {
	raw := "\xef\xbb\xbf" + `<?xml version="1.0" encoding="UTF-8"?>
<i>
  <chatid>12345</chatid>
  <d p="12.500,5,30,16711680,1700000000,0,userA,9007199254740993">Top red &amp; bold</d>
  <d p="3.2,1,25,16777215,1700000001,0,userB,42">Hello world</d>
  <d p="8.0,4,25,65280,1700000002,0,userC,43">Bottom green</d>
  <d p="15.0,6,25,255,1700000003,0,userD,44">Reverse blue</d>
  <d p="1.0,1,25,16777215">   </d>
</i>`

	tl, err := Parse(FormatAuto, []byte(raw))
	if err != nil {
		t.Fatalf("Parse XML: %v", err)
	}
	if tl.Format != FormatXML {
		t.Fatalf("format = %q, want %q", tl.Format, FormatXML)
	}
	if len(tl.Comments) != 4 {
		t.Fatalf("len = %d, want 4", len(tl.Comments))
	}
	// Sorted by TimeMs ascending.
	if tl.Comments[0].TimeMs != 3200 || tl.Comments[0].Mode != ModeScroll || tl.Comments[0].Text != "Hello world" {
		t.Fatalf("first comment mismatch: %+v", tl.Comments[0])
	}
	if tl.Comments[1].TimeMs != 8000 || tl.Comments[1].Mode != ModeBottom || tl.Comments[1].ColorHex != "#00FF00" {
		t.Fatalf("second comment mismatch: %+v", tl.Comments[1])
	}
	if tl.Comments[2].TimeMs != 12500 || tl.Comments[2].Mode != ModeTop || tl.Comments[2].Color != 0xFF0000 || tl.Comments[2].ID != "9007199254740993" || tl.Comments[2].Text != "Top red & bold" {
		t.Fatalf("third comment mismatch: %+v", tl.Comments[2])
	}
	if tl.Comments[3].Mode != ModeReverse || tl.Comments[3].Color != 0x0000FF {
		t.Fatalf("fourth comment mismatch: %+v", tl.Comments[3])
	}
}

func TestParseASS(t *testing.T) {
	raw := `[Script Info]
ScriptType: v4.00+
PlayResX: 1920
PlayResY: 1080

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, Alignment
Style: R2L, Sans, 36, &H00FFFFFF, 2
Style: Top, Sans, 30, &H0000FFFF, 8

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:05.50,0:00:13.50,R2L,alice,0,0,0,,{\move(1920,100,-200,100)\c&HFF0000&\fs40}Blue scroll, with comma
Dialogue: 0,0:00:02.00,0:00:06.00,Top,bob,0,0,0,,{\an8}Yellow top\NLine 2
Dialogue: 0,0:00:10.00,0:00:14.00,R2L,carol,0,0,0,,{\pos(960,900)\1c&H00FF00&}Bottom positioned
`
	tl, err := Parse(FormatAuto, []byte(raw))
	if err != nil {
		t.Fatalf("Parse ASS: %v", err)
	}
	if tl.Format != FormatASS {
		t.Fatalf("format = %q, want %q", tl.Format, FormatASS)
	}
	if len(tl.Comments) != 3 {
		t.Fatalf("len = %d, want 3", len(tl.Comments))
	}
	// First at 2.00s: Top, PrimaryColour &H0000FFFF (BGR) -> #FFFF00 (yellow).
	c0 := tl.Comments[0]
	if c0.TimeMs != 2000 || c0.DurationMs != 4000 || c0.Mode != ModeTop || c0.Color != 0xFFFF00 || c0.Text != "Yellow top\nLine 2" {
		t.Fatalf("c0 = %+v", c0)
	}
	// Second at 5.50s: Scroll, \c&HFF0000& (BGR blue) -> #0000FF, \fs40, comma preserved.
	c1 := tl.Comments[1]
	if c1.TimeMs != 5500 || c1.DurationMs != 8000 || c1.Mode != ModeScroll || c1.Color != 0x0000FF || c1.FontSize != 40 || c1.Text != "Blue scroll, with comma" {
		t.Fatalf("c1 = %+v", c1)
	}
	// Third at 10.00s: Bottom from \pos(960,900) >= 60% of 1080, green.
	c2 := tl.Comments[2]
	if c2.TimeMs != 10000 || c2.Mode != ModeBottom || c2.Color != 0x00FF00 {
		t.Fatalf("c2 = %+v", c2)
	}
}

func TestParseJSONVariantsAndRoundTrip(t *testing.T) {
	raw := `{
		"comments": [
			{"id": 9007199254740995, "time": 1.5, "mode": "scroll", "color": "#ff8800", "fontSize": 28, "text": "obj comment"},
			{"cid": 9007199254740996, "p": "4.25,5,16711935,u1", "m": "dandan comment"},
			[2.0, 4, "#00f", "tupleUser", "tuple comment"]
		]
	}`
	tl, err := Parse(FormatAuto, []byte(raw))
	if err != nil {
		t.Fatalf("Parse JSON: %v", err)
	}
	if len(tl.Comments) != 3 {
		t.Fatalf("len = %d, want 3", len(tl.Comments))
	}
	if tl.Comments[0].ID != "9007199254740995" || tl.Comments[0].Color != 0xFF8800 || tl.Comments[0].TimeMs != 1500 {
		t.Fatalf("c0 = %+v", tl.Comments[0])
	}
	if tl.Comments[1].Mode != ModeBottom || tl.Comments[1].Color != 0x0000FF || tl.Comments[1].Author != "tupleUser" {
		t.Fatalf("c1 = %+v", tl.Comments[1])
	}
	if tl.Comments[2].ID != "9007199254740996" || tl.Comments[2].Mode != ModeTop || tl.Comments[2].Color != 0xFF00FF || tl.Comments[2].TimeMs != 4250 {
		t.Fatalf("c2 = %+v", tl.Comments[2])
	}

	// Round-trip serialized Timeline JSON.
	blob, err := json.Marshal(tl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rt, err := Parse(FormatJSON, blob)
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if len(rt.Comments) != len(tl.Comments) {
		t.Fatalf("round-trip count = %d, want %d", len(rt.Comments), len(tl.Comments))
	}
	for i := range tl.Comments {
		if rt.Comments[i] != tl.Comments[i] {
			t.Fatalf("round-trip[%d] = %+v, want %+v", i, rt.Comments[i], tl.Comments[i])
		}
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse(FormatAuto, nil); !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("empty err = %v, want ErrEmptyInput", err)
	}
	if _, err := Parse("srt", []byte("1\n00:00:01,000 --> 00:00:02,000\nhi")); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("unsupported err = %v, want ErrUnsupportedFormat", err)
	}
	if _, err := Parse(FormatJSON, []byte(`{"unrelated":1}`)); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("bad json object err = %v, want ErrInvalidDocument", err)
	}
	if _, err := Parse(FormatXML, []byte(`not xml`)); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("bad xml err = %v, want ErrInvalidDocument", err)
	}
}

func TestTimelineQueryAndActiveAt(t *testing.T) {
	tl := buildTimeline(FormatJSON, []Comment{
		normalizeComment(Comment{ID: "1", TimeMs: 1000, Mode: ModeScroll, Text: "a"}),
		normalizeComment(Comment{ID: "2", TimeMs: 3000, Mode: ModeTop, Text: "b"}),
		normalizeComment(Comment{ID: "3", TimeMs: 5000, Mode: ModeScroll, Text: "c"}),
		normalizeComment(Comment{ID: "4", TimeMs: 12000, Mode: ModeBottom, Text: "d"}),
	})
	win := tl.QueryWindow(2000, 6000, 0)
	if len(win) != 2 || win[0].ID != "2" || win[1].ID != "3" {
		t.Fatalf("QueryWindow = %+v", win)
	}
	limited := tl.QueryWindow(0, 20000, 2)
	if len(limited) != 2 || limited[1].ID != "2" {
		t.Fatalf("QueryWindow limited = %+v", limited)
	}
	// At 6500ms: "1" (1000..9000) active, "2" (3000..7000) active, "3" (5000..13000) active, "4" not yet.
	active := tl.ActiveAt(6500, 8000, 4000)
	if len(active) != 3 {
		t.Fatalf("ActiveAt(6500) = %d, want 3", len(active))
	}
	// At 7500ms: "2" (3000..7000) ended, "1" and "3" still active.
	active2 := tl.ActiveAt(7500, 8000, 4000)
	if len(active2) != 2 || active2[0].ID != "1" || active2[1].ID != "3" {
		t.Fatalf("ActiveAt(7500) = %+v", active2)
	}
}

func TestLayoutAntiOverlapAndOverflow(t *testing.T) {
	// Two scroll comments at the exact same timestamp must get different tracks.
	// A third scroll comment at the same timestamp with MaxTracks=2 must drop when AllowOverlapFallback=false.
	tl := buildTimeline(FormatJSON, []Comment{
		normalizeComment(Comment{ID: "1", TimeMs: 1000, Mode: ModeScroll, Text: "短弹幕"}),
		normalizeComment(Comment{ID: "2", TimeMs: 1000, Mode: ModeScroll, Text: "这是一条非常非常长而且速度很快的弹幕用来测试追尾防重叠"}),
		normalizeComment(Comment{ID: "3", TimeMs: 1000, Mode: ModeScroll, Text: "第三条溢出"}),
		// Top comments use an independent lane from scroll comments.
		normalizeComment(Comment{ID: "4", TimeMs: 1000, Mode: ModeTop, Text: "顶部弹幕"}),
		// At t=3000ms on track 0, a very long (faster) comment would catch up to "1" (ending at 9000ms)
		// if placed on track 0, so check catch-up avoidance!
		normalizeComment(Comment{ID: "5", TimeMs: 3000, Mode: ModeScroll, Text: "非常非常长非常非常长非常非常长非常非常长非常非常长非常非常长"}),
	})

	res := tl.Layout(LayoutOptions{
		ViewportWidth:    800,
		ViewportHeight:   200,
		LineHeight:       32,
		MaxTracks:        2,
		ScrollDurationMs: 8000,
		FixedDurationMs:  4000,
		MinGapPx:         16,
	})
	if res.Dropped != 2 {
		// "3" drops (both scroll tracks occupied at t=1000), and "5" drops because at t=3000 track 0
		// would catch up to "1" and track 1's long comment still hasn't cleared safely.
		t.Fatalf("Dropped = %d, want 2 (placed=%+v)", res.Dropped, res.Placed)
	}
	if len(res.Placed) != 3 {
		t.Fatalf("Placed = %d, want 3", len(res.Placed))
	}
	if res.Placed[0].Track != 0 || res.Placed[1].Track != 1 || res.Placed[2].Track != 0 {
		t.Fatalf("tracks = [%d,%d,%d], want [0,1,0]", res.Placed[0].Track, res.Placed[1].Track, res.Placed[2].Track)
	}

	// With AllowOverlapFallback=true, nothing is dropped and fallback items are marked Overlapped.
	resFallback := tl.Layout(LayoutOptions{
		ViewportWidth:        800,
		ViewportHeight:       200,
		LineHeight:           32,
		MaxTracks:            2,
		ScrollDurationMs:     8000,
		FixedDurationMs:      4000,
		MinGapPx:             16,
		AllowOverlapFallback: true,
	})
	if resFallback.Dropped != 0 || len(resFallback.Placed) != 5 {
		t.Fatalf("fallback dropped=%d placed=%d", resFallback.Dropped, len(resFallback.Placed))
	}
	if !resFallback.Placed[2].Overlapped {
		t.Fatalf("expected 3rd comment to be marked overlapped: %+v", resFallback.Placed[2])
	}

	// LayoutWindow warms up occupancy from comments before startMs.
	winRes := tl.LayoutWindow(2500, 3500, LayoutOptions{
		ViewportWidth:    800,
		ViewportHeight:   200,
		LineHeight:       32,
		MaxTracks:        2,
		ScrollDurationMs: 8000,
		FixedDurationMs:  4000,
		MinGapPx:         16,
	})
	// Comments 1, 2, 4 from t=1000 are still active at 2500..3500.
	if len(winRes.Placed) != 3 {
		t.Fatalf("LayoutWindow placed = %d, want 3", len(winRes.Placed))
	}
}
