// Package hls is a pure, network-free toolkit for HLS (RFC 8216) playlists.
//
// The client runs a local proxy because the platform players cannot add
// headers to every segment request. The proxy hands each playlist it
// downloads to this package, receives a rewritten playlist whose URIs point at
// the proxy, and performs all network I/O itself. Nothing here opens a
// connection.
package hls

import (
	"errors"
	"strconv"
	"strings"
)

// ErrNotPlaylist is returned by Parse when the data does not start with an
// HLS tag (for example an HTML error page served instead of a playlist).
var ErrNotPlaylist = errors.New("hls: data is not an M3U8 playlist")

const bomString = "\xef\xbb\xbf"

// Line is one physical line of a playlist. EOL is the terminator that
// followed Text in the input ("\n", "\r\n", or "" for an unterminated last
// line). Keeping it per line is what makes Marshal byte-exact.
type Line struct {
	Text string
	EOL  string
}

// Key is the state set by an EXT-X-KEY tag.
type Key struct {
	Method            string
	URI               string
	IV                string // as written, e.g. "0x00..01"; empty means "derive from media sequence"
	KeyFormat         string
	KeyFormatVersions string
	Line              int // index into Playlist.Lines of the defining tag
}

// Map is the state set by an EXT-X-MAP tag (initialisation section).
type Map struct {
	URI       string
	ByteRange string
	Line      int // index into Playlist.Lines of the defining tag
}

// Segment is one media segment together with the key and map state that
// applies to it.
type Segment struct {
	URI             string
	Duration        float64
	Title           string
	Sequence        int64 // media sequence number of this segment
	Discontinuity   bool  // an EXT-X-DISCONTINUITY precedes the segment
	Gap             bool
	ByteRange       string
	ProgramDateTime string
	Key             *Key // nil when the segment is not encrypted
	Map             *Map // nil when no EXT-X-MAP applies

	// Start is the index of the first segment-scoped tag line that belongs to
	// the segment (EXTINF, EXT-X-KEY, EXT-X-DISCONTINUITY ...) and URILine the
	// index of its URI line. Both index Playlist.Lines.
	Start   int
	URILine int

	ownKey bool // an effective EXT-X-KEY tag lies in Start..URILine
	ownMap bool // an EXT-X-MAP tag lies in Start..URILine
}

// Variant is an EXT-X-STREAM-INF entry, or an EXT-X-I-FRAME-STREAM-INF entry
// in Playlist.IFrames.
type Variant struct {
	URI              string
	Bandwidth        int64
	AverageBandwidth int64
	Width, Height    int
	Codecs           string
	FrameRate        string
	Audio            string
	Video            string
	Subtitles        string
	ClosedCaptions   string

	Start   int // index of the tag line
	URILine int // index of the URI line (== Start for I-frame streams)
}

// Media is an EXT-X-MEDIA rendition.
type Media struct {
	Type    string
	GroupID string
	Name    string
	URI     string // empty for renditions carried in the main stream
	Line    int
}

// Playlist is a parsed master or media playlist.
//
// Lines is the source of truth for Marshal. Header, Variants, IFrames, Media
// and Segments are read-only views derived from it; change playlists through
// Rewrite instead of editing the views.
type Playlist struct {
	Master                bool
	Version               int
	TargetDuration        int
	MediaSequence         int64
	DiscontinuitySequence int64
	PlaylistType          string
	Endlist               bool

	// Header holds the raw lines before the first segment, variant or
	// rendition entry.
	Header []string

	Variants []Variant
	IFrames  []Variant
	Media    []Media
	Segments []Segment

	Lines []Line

	bom bool
	eol string
}

// segmentTags are the tags that belong to the segment that follows them.
var segmentTags = map[string]bool{
	"EXTINF": true, "EXT-X-BYTERANGE": true, "EXT-X-DISCONTINUITY": true,
	"EXT-X-PROGRAM-DATE-TIME": true, "EXT-X-GAP": true, "EXT-X-CUE-OUT": true,
	"EXT-X-CUE-OUT-CONT": true, "EXT-X-CUE-IN": true, "EXT-X-DATERANGE": true,
	"EXT-X-KEY": true, "EXT-X-MAP": true, "EXT-X-BITRATE": true,
	"EXT-OATCLS-SCTE35": true, "EXT-X-SCTE35": true,
}

// Parse reads a master or media playlist. It is tolerant: unknown tags,
// comments and blank lines are kept verbatim and malformed attribute values
// read as zero values. It fails only when the data does not look like a
// playlist.
func Parse(data []byte) (*Playlist, error) {
	text := string(data)
	p := &Playlist{}
	if strings.HasPrefix(text, bomString) {
		p.bom = true
		text = text[len(bomString):]
	}
	p.Lines, p.eol = splitLines(text)
	first := -1
	for i, l := range p.Lines {
		if strings.TrimSpace(l.Text) != "" {
			first = i
			break
		}
	}
	if first < 0 || !strings.HasPrefix(strings.TrimSpace(p.Lines[first].Text), "#EXT") {
		return nil, ErrNotPlaylist
	}
	p.scan()
	return p, nil
}

// Marshal serialises the playlist. A playlist returned by Parse is written
// back byte for byte, including the line terminators of the input.
func (p *Playlist) Marshal() []byte {
	n := len(bomString)
	for _, l := range p.Lines {
		n += len(l.Text) + len(l.EOL)
	}
	b := make([]byte, 0, n)
	if p.bom {
		b = append(b, bomString...)
	}
	for _, l := range p.Lines {
		b = append(b, l.Text...)
		b = append(b, l.EOL...)
	}
	return b
}

// EOL returns the dominant line terminator of the input ("\n" or "\r\n").
func (p *Playlist) EOL() string {
	if p.eol == "" {
		return "\n"
	}
	return p.eol
}

func splitLines(text string) ([]Line, string) {
	var lines []Line
	crlf, lf := 0, 0
	for text != "" {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			lines = append(lines, Line{Text: text})
			break
		}
		l := Line{Text: text[:i], EOL: "\n"}
		if strings.HasSuffix(l.Text, "\r") {
			l.Text = l.Text[:len(l.Text)-1]
			l.EOL = "\r\n"
			crlf++
		} else {
			lf++
		}
		lines = append(lines, l)
		text = text[i+1:]
	}
	if crlf > lf {
		return lines, "\r\n"
	}
	return lines, "\n"
}

// tagOf returns the tag name (without '#') of a line that starts with "#EXT".
// Blank lines, comments and URI lines report false.
func tagOf(text string) (name string, ok bool) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "#EXT") {
		return "", false
	}
	name, _, _ = strings.Cut(t[1:], ":")
	return name, true
}

// tagValue returns the part of a tag line after the first colon.
func tagValue(text string) string {
	if _, v, ok := strings.Cut(strings.TrimSpace(text), ":"); ok {
		return v
	}
	return ""
}

func (p *Playlist) scan() {
	var (
		key       *Key
		mp        *Map
		pv        *Variant
		segStart  = -1
		cur       Segment
		sawInf    bool
		masterTag bool
		headerEnd = -1
	)
	markHeader := func(i int) {
		if headerEnd < 0 {
			headerEnd = i
		}
	}
	for i, ln := range p.Lines {
		t := strings.TrimSpace(ln.Text)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "#") {
			markHeader(i)
			if pv != nil {
				pv.URI = t
				pv.URILine = i
				p.Variants = append(p.Variants, *pv)
				pv = nil
			} else {
				cur.URI = t
				cur.Start = segStart
				if segStart < 0 {
					cur.Start = i
				}
				cur.URILine = i
				cur.Key = key
				cur.Map = mp
				p.Segments = append(p.Segments, cur)
			}
			cur = Segment{}
			segStart = -1
			continue
		}
		name, ok := tagOf(t)
		if !ok {
			continue // comment
		}
		val := tagValue(t)
		if segmentTags[name] && segStart < 0 {
			segStart = i
		}
		switch name {
		case "EXTINF":
			markHeader(i)
			sawInf = true
			d, title, _ := strings.Cut(val, ",")
			cur.Duration, _ = strconv.ParseFloat(strings.TrimSpace(d), 64)
			cur.Title = title
		case "EXT-X-DISCONTINUITY":
			cur.Discontinuity = true
		case "EXT-X-GAP":
			cur.Gap = true
		case "EXT-X-BYTERANGE":
			cur.ByteRange = val
		case "EXT-X-PROGRAM-DATE-TIME":
			cur.ProgramDateTime = val
		case "EXT-X-KEY":
			a := parseAttrs(val)
			k := &Key{
				Method:            attrValue(a, "METHOD"),
				URI:               attrValue(a, "URI"),
				IV:                attrValue(a, "IV"),
				KeyFormat:         attrValue(a, "KEYFORMAT"),
				KeyFormatVersions: attrValue(a, "KEYFORMATVERSIONS"),
				Line:              i,
			}
			// Only the "identity" key format (the default) is tracked; other
			// formats such as FairPlay run alongside it.
			if k.KeyFormat == "" || strings.EqualFold(k.KeyFormat, "identity") {
				cur.ownKey = true
				if strings.EqualFold(k.Method, "NONE") {
					key = nil
				} else {
					key = k
				}
			}
		case "EXT-X-MAP":
			a := parseAttrs(val)
			mp = &Map{URI: attrValue(a, "URI"), ByteRange: attrValue(a, "BYTERANGE"), Line: i}
			cur.ownMap = true
		case "EXT-X-VERSION":
			p.Version, _ = strconv.Atoi(strings.TrimSpace(val))
		case "EXT-X-TARGETDURATION":
			f, _ := strconv.ParseFloat(strings.TrimSpace(val), 64)
			p.TargetDuration = int(f + 0.5)
		case "EXT-X-MEDIA-SEQUENCE":
			p.MediaSequence, _ = strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		case "EXT-X-DISCONTINUITY-SEQUENCE":
			p.DiscontinuitySequence, _ = strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		case "EXT-X-PLAYLIST-TYPE":
			p.PlaylistType = strings.TrimSpace(val)
		case "EXT-X-ENDLIST":
			p.Endlist = true
		case "EXT-X-STREAM-INF":
			markHeader(i)
			masterTag = true
			v := variantFromAttrs(parseAttrs(val))
			v.Start = i
			pv = &v
		case "EXT-X-I-FRAME-STREAM-INF":
			markHeader(i)
			masterTag = true
			v := variantFromAttrs(parseAttrs(val))
			v.Start, v.URILine = i, i
			p.IFrames = append(p.IFrames, v)
		case "EXT-X-MEDIA":
			markHeader(i)
			masterTag = true
			a := parseAttrs(val)
			p.Media = append(p.Media, Media{
				Type: attrValue(a, "TYPE"), GroupID: attrValue(a, "GROUP-ID"),
				Name: attrValue(a, "NAME"), URI: attrValue(a, "URI"), Line: i,
			})
		case "EXT-X-SESSION-DATA", "EXT-X-SESSION-KEY", "EXT-X-CONTENT-STEERING":
			masterTag = true
		}
	}
	if headerEnd < 0 {
		headerEnd = len(p.Lines)
	}
	for _, l := range p.Lines[:headerEnd] {
		p.Header = append(p.Header, l.Text)
	}
	p.Master = !sawInf && (len(p.Variants) > 0 || masterTag)
	for i := range p.Segments {
		p.Segments[i].Sequence = p.MediaSequence + int64(i)
	}
}

func variantFromAttrs(a []attr) Variant {
	v := Variant{
		Codecs:         attrValue(a, "CODECS"),
		FrameRate:      attrValue(a, "FRAME-RATE"),
		Audio:          attrValue(a, "AUDIO"),
		Video:          attrValue(a, "VIDEO"),
		Subtitles:      attrValue(a, "SUBTITLES"),
		ClosedCaptions: attrValue(a, "CLOSED-CAPTIONS"),
		URI:            attrValue(a, "URI"),
	}
	v.Bandwidth, _ = strconv.ParseInt(attrValue(a, "BANDWIDTH"), 10, 64)
	v.AverageBandwidth, _ = strconv.ParseInt(attrValue(a, "AVERAGE-BANDWIDTH"), 10, 64)
	if w, h, ok := strings.Cut(strings.ToLower(attrValue(a, "RESOLUTION")), "x"); ok {
		v.Width, _ = strconv.Atoi(w)
		v.Height, _ = strconv.Atoi(h)
	}
	return v
}
