// Package danmaku provides a pure local parser, timeline index and basic
// anti-overlap track layout engine for timed video comments (danmaku).
//
// Online danmaku fetching is intentionally out of scope for Core and belongs
// to plugins or clients. This package operates solely on local bytes.
package danmaku

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Format identifies a supported local danmaku document format.
type Format string

const (
	FormatAuto Format = "auto"
	FormatXML  Format = "xml"
	FormatASS  Format = "ass"
	FormatJSON Format = "json"
)

// Mode identifies how a comment moves across the video viewport.
type Mode string

const (
	ModeScroll  Mode = "scroll"
	ModeTop     Mode = "top"
	ModeBottom  Mode = "bottom"
	ModeReverse Mode = "reverse"
)

const (
	// DefaultColor is white (0xFFFFFF).
	DefaultColor uint32 = 0xFFFFFF
	// DefaultFontSize matches the conventional 25px danmaku baseline.
	DefaultFontSize float64 = 25
	// MaxInputBytes bounds input size to protect memory on mobile devices.
	MaxInputBytes = 32 << 20
	// MaxComments caps parsed comments per document.
	MaxComments = 500000
)

var (
	ErrEmptyInput        = errors.New("danmaku: input is empty")
	ErrInputTooLarge     = errors.New("danmaku: input exceeds size limit")
	ErrTooManyComments   = errors.New("danmaku: comment count exceeds limit")
	ErrUnsupportedFormat = errors.New("danmaku: unsupported format")
	ErrInvalidDocument   = errors.New("danmaku: invalid document")
)

// Comment is one normalized timed comment.
type Comment struct {
	ID         string  `json:"id,omitempty"`
	TimeMs     int64   `json:"timeMs"`
	DurationMs int64   `json:"durationMs,omitempty"`
	Mode       Mode    `json:"mode"`
	Color      uint32  `json:"color"`
	ColorHex   string  `json:"colorHex,omitempty"`
	FontSize   float64 `json:"fontSize"`
	Text       string  `json:"text"`
	Author     string  `json:"author,omitempty"`
	Timestamp  int64   `json:"timestamp,omitempty"`
}

// Timeline holds normalized comments sorted by ascending TimeMs.
type Timeline struct {
	Format   Format    `json:"format"`
	Comments []Comment `json:"comments"`
}

// FormatColorHex formats a 24-bit RGB integer as "#RRGGBB".
func FormatColorHex(rgb uint32) string {
	return fmt.Sprintf("#%06X", rgb&0xFFFFFF)
}

// DetectFormat inspects the leading content of data to guess its format.
func DetectFormat(data []byte) Format {
	trimmed := bytes.TrimSpace(stripBOM(data))
	if len(trimmed) == 0 {
		return ""
	}
	sample := trimmed
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	lower := strings.ToLower(string(sample))
	if strings.Contains(lower, "[script info]") || strings.Contains(lower, "[v4+ styles]") || strings.Contains(lower, "[v4 styles]") || strings.Contains(lower, "[events]") || strings.Contains(lower, "dialogue:") {
		return FormatASS
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return FormatJSON
	}
	if trimmed[0] == '<' {
		return FormatXML
	}
	return ""
}

// Parse decodes data in the requested format (or auto-detects when format is
// empty or FormatAuto) and returns a normalized, sorted Timeline.
func Parse(format Format, data []byte) (*Timeline, error) {
	if len(data) == 0 {
		return nil, ErrEmptyInput
	}
	if len(data) > MaxInputBytes {
		return nil, ErrInputTooLarge
	}
	f := Format(strings.ToLower(strings.TrimSpace(string(format))))
	if f == "" || f == FormatAuto {
		f = DetectFormat(data)
		if f == "" {
			return nil, ErrUnsupportedFormat
		}
	}
	switch f {
	case FormatXML:
		return ParseXML(data)
	case FormatASS:
		return ParseASS(data)
	case FormatJSON:
		return ParseJSON(data)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}
}

// ParseXML parses Bilibili-compatible XML danmaku documents:
// <d p="timeSec,mode,fontSize,color,timestamp,pool,userHash,dmid">text</d>.
func ParseXML(data []byte) (*Timeline, error) {
	clean := bytes.TrimSpace(stripBOM(data))
	if len(clean) == 0 {
		return nil, ErrEmptyInput
	}
	if len(clean) > MaxInputBytes {
		return nil, ErrInputTooLarge
	}
	if clean[0] != '<' {
		return nil, fmt.Errorf("%w: expected XML root element", ErrInvalidDocument)
	}
	dec := xml.NewDecoder(bytes.NewReader(clean))
	dec.Strict = false
	var (
		sawRoot  bool
		comments []Comment
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidDocument, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		sawRoot = true
		if !strings.EqualFold(se.Name.Local, "d") {
			continue
		}
		var pAttr string
		for _, attr := range se.Attr {
			if strings.EqualFold(attr.Name.Local, "p") {
				pAttr = attr.Value
				break
			}
		}
		var body string
		if err := dec.DecodeElement(&body, &se); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidDocument, err)
		}
		text := normalizeText(body)
		if text == "" {
			continue
		}
		c, ok := parseBiliAttr(pAttr, text)
		if !ok {
			continue
		}
		comments = append(comments, c)
		if len(comments) > MaxComments {
			return nil, ErrTooManyComments
		}
	}
	if !sawRoot {
		return nil, fmt.Errorf("%w: no XML elements found", ErrInvalidDocument)
	}
	return buildTimeline(FormatXML, comments), nil
}

func parseBiliAttr(pAttr, text string) (Comment, bool) {
	parts := strings.Split(strings.TrimSpace(pAttr), ",")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return Comment{}, false
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return Comment{}, false
	}
	c := Comment{
		TimeMs:   secondsToMs(sec),
		Mode:     ModeScroll,
		Color:    DefaultColor,
		FontSize: DefaultFontSize,
		Text:     text,
	}
	if len(parts) > 1 {
		if m, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
			c.Mode = modeFromBili(m)
		}
	}
	if len(parts) > 2 {
		if fs, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64); err == nil && fs > 0 && !math.IsNaN(fs) && !math.IsInf(fs, 0) {
			c.FontSize = fs
		}
	}
	if len(parts) > 3 {
		if col, err := parseColorString(parts[3]); err == nil {
			c.Color = col
		}
	}
	if len(parts) > 4 {
		if ts, err := strconv.ParseInt(strings.TrimSpace(parts[4]), 10, 64); err == nil && ts > 0 {
			c.Timestamp = ts
		}
	}
	if len(parts) > 6 {
		c.Author = strings.TrimSpace(parts[6])
	}
	if len(parts) > 7 {
		c.ID = strings.TrimSpace(parts[7])
	}
	return normalizeComment(c), true
}

// ParseASS parses ASS/SSA danmaku subtitle scripts.
func ParseASS(data []byte) (*Timeline, error) {
	clean := stripBOM(data)
	if len(bytes.TrimSpace(clean)) == 0 {
		return nil, ErrEmptyInput
	}
	if len(clean) > MaxInputBytes {
		return nil, ErrInputTooLarge
	}

	type assStyle struct {
		fontSize float64
		color    uint32
		align    int
	}

	var (
		section     string
		playResY    float64 = 1080
		styles              = map[string]assStyle{}
		styleCols   []string
		eventCols   []string
		sawSection  bool
		sawDialogue bool
		comments    []Comment
		dialogueIdx int
	)

	lines := strings.Split(strings.ReplaceAll(string(clean), "\r\n", "\n"), "\n")
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "!:") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			sawSection = true
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)

		switch section {
		case "script info":
			if key == "playresy" {
				if v, err := strconv.ParseFloat(val, 64); err == nil && v > 0 {
					playResY = v
				}
			}
		case "v4+ styles", "v4 styles":
			if key == "format" {
				styleCols = splitTrimLower(val)
			} else if key == "style" && len(styleCols) > 0 {
				fields := splitLimited(val, len(styleCols))
				if len(fields) < len(styleCols) {
					continue
				}
				var name string
				st := assStyle{fontSize: DefaultFontSize, color: DefaultColor}
				for i, col := range styleCols {
					f := strings.TrimSpace(fields[i])
					switch col {
					case "name":
						name = f
					case "fontsize":
						if fs, err := strconv.ParseFloat(f, 64); err == nil && fs > 0 {
							st.fontSize = fs
						}
					case "primarycolour":
						if c, err := parseASSColor(f); err == nil {
							st.color = c
						}
					case "alignment":
						if a, err := strconv.Atoi(f); err == nil {
							st.align = a
						}
					}
				}
				if name != "" {
					styles[strings.ToLower(name)] = st
				}
			}
		case "events", "":
			if key == "format" {
				eventCols = splitTrimLower(val)
				continue
			}
			if key != "dialogue" {
				continue
			}
			sawDialogue = true
			cols := eventCols
			if len(cols) == 0 {
				cols = []string{"layer", "start", "end", "style", "name", "marginl", "marginr", "marginv", "effect", "text"}
			}
			fields := splitLimited(val, len(cols))
			if len(fields) < len(cols) {
				continue
			}
			var startStr, endStr, styleName, author, rawText string
			for i, col := range cols {
				f := strings.TrimSpace(fields[i])
				switch col {
				case "start":
					startStr = f
				case "end":
					endStr = f
				case "style":
					styleName = f
				case "name", "actor":
					author = f
				case "text":
					rawText = fields[i]
				}
			}
			startMs, ok1 := parseASSTime(startStr)
			endMs, ok2 := parseASSTime(endStr)
			if !ok1 {
				continue
			}
			var durMs int64
			if ok2 && endMs > startMs {
				durMs = endMs - startMs
			}
			st, hasStyle := styles[strings.ToLower(styleName)]
			c := Comment{
				TimeMs:     startMs,
				DurationMs: durMs,
				Mode:       ModeScroll,
				Color:      DefaultColor,
				FontSize:   DefaultFontSize,
				Author:     author,
			}
			if hasStyle {
				c.Color = st.color
				c.FontSize = st.fontSize
				c.Mode = modeFromASSAlign(st.align)
			}
			// Infer mode from style name if explicit (common in Bilibili-to-ASS tools).
			lowerStyle := strings.ToLower(styleName)
			switch {
			case strings.Contains(lowerStyle, "top"):
				c.Mode = ModeTop
			case strings.Contains(lowerStyle, "bottom") || strings.Contains(lowerStyle, "btm"):
				c.Mode = ModeBottom
			case strings.Contains(lowerStyle, "r2l") || strings.Contains(lowerStyle, "scroll"):
				c.Mode = ModeScroll
			case strings.Contains(lowerStyle, "l2r") || strings.Contains(lowerStyle, "reverse"):
				c.Mode = ModeReverse
			}
			text := applyASSOverrides(rawText, &c, playResY)
			if text == "" {
				continue
			}
			dialogueIdx++
			c.ID = strconv.Itoa(dialogueIdx)
			c.Text = text
			comments = append(comments, normalizeComment(c))
			if len(comments) > MaxComments {
				return nil, ErrTooManyComments
			}
		}
	}
	if !sawSection && !sawDialogue {
		return nil, fmt.Errorf("%w: not an ASS script", ErrInvalidDocument)
	}
	return buildTimeline(FormatASS, comments), nil
}

var (
	assTagBlockRe = regexp.MustCompile(`\{([^}]*)\}`)
	assMoveRe     = regexp.MustCompile(`\\move\(\s*([-+]?\d+(?:\.\d+)?)\s*,\s*([-+]?\d+(?:\.\d+)?)\s*,\s*([-+]?\d+(?:\.\d+)?)\s*,\s*([-+]?\d+(?:\.\d+)?)`)
	assPosRe      = regexp.MustCompile(`\\pos\(\s*([-+]?\d+(?:\.\d+)?)\s*,\s*([-+]?\d+(?:\.\d+)?)\s*\)`)
	assAlignRe    = regexp.MustCompile(`\\an([1-9])`)
	assColorRe    = regexp.MustCompile(`\\(?:1c|c)&[Hh]([0-9A-Fa-f]{1,8})&?`)
	assFontSizeRe = regexp.MustCompile(`\\fs(\d+(?:\.\d+)?)`)
)

func applyASSOverrides(raw string, c *Comment, playResY float64) string {
	for _, m := range assTagBlockRe.FindAllStringSubmatch(raw, -1) {
		tags := m[1]
		if mm := assMoveRe.FindStringSubmatch(tags); len(mm) == 5 {
			x1, _ := strconv.ParseFloat(mm[1], 64)
			x2, _ := strconv.ParseFloat(mm[3], 64)
			if x2 > x1 {
				c.Mode = ModeReverse
			} else {
				c.Mode = ModeScroll
			}
		} else if am := assAlignRe.FindStringSubmatch(tags); len(am) == 2 {
			if a, err := strconv.Atoi(am[1]); err == nil {
				c.Mode = modeFromASSAlign(a)
			}
		} else if pm := assPosRe.FindStringSubmatch(tags); len(pm) == 3 {
			y, _ := strconv.ParseFloat(pm[2], 64)
			if playResY > 0 && y >= playResY*0.6 {
				c.Mode = ModeBottom
			} else {
				c.Mode = ModeTop
			}
		}
		if cm := assColorRe.FindStringSubmatch(tags); len(cm) == 2 {
			if col, err := parseASSColor("&H" + cm[1]); err == nil {
				c.Color = col
			}
		}
		if fm := assFontSizeRe.FindStringSubmatch(tags); len(fm) == 2 {
			if fs, err := strconv.ParseFloat(fm[1], 64); err == nil && fs > 0 {
				c.FontSize = fs
			}
		}
	}
	stripped := assTagBlockRe.ReplaceAllString(raw, "")
	stripped = strings.ReplaceAll(stripped, `\N`, "\n")
	stripped = strings.ReplaceAll(stripped, `\n`, "\n")
	stripped = strings.ReplaceAll(stripped, `\h`, " ")
	return normalizeText(stripped)
}

func parseASSTime(s string) (int64, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.ParseInt(parts[0], 10, 64)
	m, err2 := strconv.ParseInt(parts[1], 10, 64)
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || m < 0 || sec < 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return 0, false
	}
	return (h*3600+m*60)*1000 + secondsToMs(sec), true
}

// parseASSColor parses ASS color strings (&HAABBGGRR& or &HBBGGRR&) into 0xRRGGBB.
func parseASSColor(s string) (uint32, error) {
	v := strings.TrimSpace(s)
	v = strings.TrimSuffix(v, "&")
	v = strings.TrimPrefix(v, "&H")
	v = strings.TrimPrefix(v, "&h")
	v = strings.TrimPrefix(v, "0x")
	v = strings.TrimPrefix(v, "0X")
	if v == "" || len(v) > 8 {
		return DefaultColor, errors.New("invalid ASS color")
	}
	n, err := strconv.ParseUint(v, 16, 32)
	if err != nil {
		return DefaultColor, err
	}
	// ASS stores BGR in the lower 24 bits: 0xBBGGRR.
	b := (n >> 16) & 0xFF
	g := (n >> 8) & 0xFF
	r := n & 0xFF
	return uint32((r << 16) | (g << 8) | b), nil
}

func modeFromASSAlign(align int) Mode {
	switch align {
	case 1, 2, 3:
		return ModeBottom
	case 7, 8, 9:
		return ModeTop
	default:
		return ModeScroll
	}
}

// ParseJSON parses standard JSON danmaku arrays, wrapper objects
// ({"comments":[...]}, {"danmaku":[...]}, {"items":[...]}), DandanPlay-style
// {"p":"...","m":"..."} entries, compact tuples [time, mode, color, author, text],
// and serialized Timeline objects.
func ParseJSON(data []byte) (*Timeline, error) {
	clean := bytes.TrimSpace(stripBOM(data))
	if len(clean) == 0 {
		return nil, ErrEmptyInput
	}
	if len(clean) > MaxInputBytes {
		return nil, ErrInputTooLarge
	}
	dec := json.NewDecoder(bytes.NewReader(clean))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidDocument, err)
	}
	var rawList []any
	switch v := root.(type) {
	case []any:
		rawList = v
	case map[string]any:
		for _, key := range []string{"comments", "danmaku", "items", "data"} {
			if list, ok := v[key].([]any); ok {
				rawList = list
				break
			}
		}
		if rawList == nil {
			return nil, fmt.Errorf("%w: JSON object must contain a comments, danmaku, items or data array", ErrInvalidDocument)
		}
	default:
		return nil, fmt.Errorf("%w: expected JSON array or object", ErrInvalidDocument)
	}
	if len(rawList) > MaxComments {
		return nil, ErrTooManyComments
	}
	comments := make([]Comment, 0, len(rawList))
	for _, entry := range rawList {
		c, ok := parseJSONEntry(entry)
		if !ok {
			continue
		}
		comments = append(comments, c)
	}
	return buildTimeline(FormatJSON, comments), nil
}

func parseJSONEntry(entry any) (Comment, bool) {
	switch v := entry.(type) {
	case []any:
		// Compact tuple format: [timeSec, mode, color, author, text]
		if len(v) < 5 {
			return Comment{}, false
		}
		sec, ok := jsonFloat(v[0])
		if !ok {
			return Comment{}, false
		}
		text := normalizeText(jsonString(v[4]))
		if text == "" {
			return Comment{}, false
		}
		c := Comment{
			TimeMs:   secondsToMs(sec),
			Mode:     parseModeValue(v[1]),
			Color:    parseColorValue(v[2]),
			FontSize: DefaultFontSize,
			Author:   strings.TrimSpace(jsonString(v[3])),
			Text:     text,
		}
		return normalizeComment(c), true

	case map[string]any:
		// DandanPlay-style {"p": "time,mode,color,uid", "m": "text", "cid": ...}
		if pStr, ok := v["p"].(string); ok {
			text := normalizeText(firstNonEmpty(jsonString(v["m"]), jsonString(v["text"]), jsonString(v["content"])))
			if text == "" {
				return Comment{}, false
			}
			c, ok := parseDandanP(pStr, text)
			if !ok {
				return Comment{}, false
			}
			if id := strings.TrimSpace(firstNonEmpty(jsonString(v["id"]), jsonString(v["cid"]))); id != "" {
				c.ID = id
			}
			return normalizeComment(c), true
		}

		text := normalizeText(firstNonEmpty(jsonString(v["text"]), jsonString(v["content"]), jsonString(v["m"])))
		if text == "" {
			return Comment{}, false
		}
		var timeMs int64
		if ms, ok := jsonInt64(v["timeMs"]); ok {
			timeMs = ms
		} else if sec, ok := jsonFloat(v["time"]); ok {
			timeMs = secondsToMs(sec)
		} else if sec, ok := jsonFloat(v["timeSec"]); ok {
			timeMs = secondsToMs(sec)
		} else {
			return Comment{}, false
		}
		c := Comment{
			ID:       strings.TrimSpace(firstNonEmpty(jsonString(v["id"]), jsonString(v["cid"]))),
			TimeMs:   timeMs,
			Mode:     ModeScroll,
			Color:    DefaultColor,
			FontSize: DefaultFontSize,
			Text:     text,
			Author:   strings.TrimSpace(firstNonEmpty(jsonString(v["author"]), jsonString(v["user"]))),
		}
		if dur, ok := jsonInt64(v["durationMs"]); ok && dur > 0 {
			c.DurationMs = dur
		}
		if mv, exists := v["mode"]; exists {
			c.Mode = parseModeValue(mv)
		} else if tv, exists := v["type"]; exists {
			c.Mode = parseModeValue(tv)
		}
		if cv, exists := v["color"]; exists {
			c.Color = parseColorValue(cv)
		} else if ch, exists := v["colorHex"]; exists {
			c.Color = parseColorValue(ch)
		}
		if fs, ok := jsonFloat(v["fontSize"]); ok && fs > 0 {
			c.FontSize = fs
		} else if fs, ok := jsonFloat(v["size"]); ok && fs > 0 {
			c.FontSize = fs
		}
		if ts, ok := jsonInt64(v["timestamp"]); ok && ts > 0 {
			c.Timestamp = ts
		}
		return normalizeComment(c), true
	default:
		return Comment{}, false
	}
}

func parseDandanP(p, text string) (Comment, bool) {
	parts := strings.Split(strings.TrimSpace(p), ",")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return Comment{}, false
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return Comment{}, false
	}
	c := Comment{
		TimeMs:   secondsToMs(sec),
		Mode:     ModeScroll,
		Color:    DefaultColor,
		FontSize: DefaultFontSize,
		Text:     text,
	}
	if len(parts) > 1 {
		if m, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
			c.Mode = modeFromBili(m)
		}
	}
	if len(parts) > 2 {
		if col, err := parseColorString(parts[2]); err == nil {
			c.Color = col
		}
	}
	if len(parts) > 3 {
		c.Author = strings.TrimSpace(parts[3])
	}
	return normalizeComment(c), true
}

func buildTimeline(format Format, comments []Comment) *Timeline {
	if comments == nil {
		comments = []Comment{}
	}
	sort.SliceStable(comments, func(i, j int) bool {
		if comments[i].TimeMs != comments[j].TimeMs {
			return comments[i].TimeMs < comments[j].TimeMs
		}
		return comments[i].ID < comments[j].ID
	})
	return &Timeline{Format: format, Comments: comments}
}

func normalizeComment(c Comment) Comment {
	if c.TimeMs < 0 {
		c.TimeMs = 0
	}
	if c.DurationMs < 0 {
		c.DurationMs = 0
	}
	switch c.Mode {
	case ModeScroll, ModeTop, ModeBottom, ModeReverse:
	default:
		c.Mode = ModeScroll
	}
	c.Color &= 0xFFFFFF
	c.ColorHex = FormatColorHex(c.Color)
	if c.FontSize <= 0 || math.IsNaN(c.FontSize) || math.IsInf(c.FontSize, 0) {
		c.FontSize = DefaultFontSize
	}
	return c
}

func modeFromBili(m int) Mode {
	switch m {
	case 4:
		return ModeBottom
	case 5:
		return ModeTop
	case 6:
		return ModeReverse
	default:
		return ModeScroll
	}
}

func parseModeValue(v any) Mode {
	switch x := v.(type) {
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		switch s {
		case "scroll", "right", "rtl", "r2l", "1", "2", "3":
			return ModeScroll
		case "bottom", "btm", "4":
			return ModeBottom
		case "top", "5":
			return ModeTop
		case "reverse", "ltr", "l2r", "6":
			return ModeReverse
		default:
			return ModeScroll
		}
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return modeFromBili(int(n))
		}
	case float64:
		return modeFromBili(int(x))
	}
	return ModeScroll
}

func parseColorValue(v any) uint32 {
	switch x := v.(type) {
	case string:
		if c, err := parseColorString(x); err == nil {
			return c
		}
	case json.Number:
		if n, err := x.Int64(); err == nil && n >= 0 {
			return uint32(n) & 0xFFFFFF
		}
	case float64:
		if x >= 0 && !math.IsNaN(x) && !math.IsInf(x, 0) {
			return uint32(x) & 0xFFFFFF
		}
	}
	return DefaultColor
}

func parseColorString(s string) (uint32, error) {
	v := strings.TrimSpace(s)
	if v == "" {
		return DefaultColor, errors.New("empty color")
	}
	if strings.HasPrefix(v, "#") {
		hexStr := v[1:]
		if len(hexStr) == 3 {
			hexStr = string([]byte{hexStr[0], hexStr[0], hexStr[1], hexStr[1], hexStr[2], hexStr[2]})
		}
		n, err := strconv.ParseUint(hexStr, 16, 32)
		if err != nil {
			return DefaultColor, err
		}
		return uint32(n) & 0xFFFFFF, nil
	}
	if strings.HasPrefix(v, "0x") || strings.HasPrefix(v, "0X") {
		n, err := strconv.ParseUint(v[2:], 16, 32)
		if err != nil {
			return DefaultColor, err
		}
		return uint32(n) & 0xFFFFFF, nil
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return DefaultColor, err
	}
	return uint32(n) & 0xFFFFFF, nil
}

func secondsToMs(sec float64) int64 {
	if sec <= 0 {
		return 0
	}
	return int64(math.Round(sec * 1000))
}

func stripBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
}

func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

func splitTrimLower(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.ToLower(strings.TrimSpace(p))
	}
	return out
}

func splitLimited(s string, n int) []string {
	if n <= 1 {
		return []string{s}
	}
	return strings.SplitN(s, ",", n)
}

func jsonString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return ""
	}
}

func jsonFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func jsonInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		if f, err := x.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return int64(math.Round(f)), true
		}
	case float64:
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			return int64(math.Round(x)), true
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
