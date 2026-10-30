package hls

import (
	"fmt"
	"strings"
)

// URIKind says what a URI in a playlist refers to. It is passed to
// RewriteOptions.MapURL so the client can route each kind differently.
type URIKind int

// URI kinds.
const (
	KindSegment URIKind = iota // media segment
	KindKey                    // EXT-X-KEY / EXT-X-SESSION-KEY URI
	KindMap                    // EXT-X-MAP initialisation section
	KindMedia                  // EXT-X-MEDIA rendition playlist
	KindVariant                // EXT-X-STREAM-INF media playlist
	KindIFrame                 // EXT-X-I-FRAME-STREAM-INF playlist
)

func (k URIKind) String() string {
	switch k {
	case KindSegment:
		return "segment"
	case KindKey:
		return "key"
	case KindMap:
		return "map"
	case KindMedia:
		return "media"
	case KindVariant:
		return "variant"
	case KindIFrame:
		return "iframe"
	}
	return fmt.Sprintf("URIKind(%d)", int(k))
}

// VariantMode selects how a master playlist is reduced to one variant.
type VariantMode int

// Variant selection modes.
const (
	VariantHighestBandwidth VariantMode = iota // highest BANDWIDTH; the first one wins a tie (default)
	VariantMaxHeight                           // tallest RESOLUTION not above MaxHeight
	VariantAll                                 // keep every variant, no selection
)

// VariantOutput selects what Rewrite returns once a variant is chosen.
type VariantOutput int

// Variant output forms.
const (
	OutputMaster    VariantOutput = iota // the master playlist reduced to the chosen variant
	OutputReference                      // only the chosen variant's (rewritten) URI
)

// VariantChooser configures master playlist reduction. The zero value picks
// the highest-bandwidth variant and returns the master reduced to it; use
// VariantAll to keep every variant.
type VariantChooser struct {
	Mode VariantMode
	// MaxHeight is the height cap for VariantMaxHeight. Zero or negative
	// means no cap. When every variant is taller than the cap, the shortest
	// one is chosen. Variants without RESOLUTION are only chosen when no
	// variant has one, in which case the choice falls back to bandwidth.
	MaxHeight int
	Output    VariantOutput
}

// Choose returns the index of the selected variant and a note describing a
// fallback, if one was needed. ok is false when no selection applies.
func (c VariantChooser) Choose(vs []Variant) (idx int, note string, ok bool) {
	if c.Mode == VariantAll || len(vs) == 0 {
		return 0, "", false
	}
	best := 0
	for i := range vs {
		if vs[i].Bandwidth > vs[best].Bandwidth {
			best = i
		}
	}
	if c.Mode != VariantMaxHeight {
		return best, "", true
	}
	fit, small := -1, -1 // tallest within the cap; shortest overall
	for i := range vs {
		h := vs[i].Height
		if h <= 0 {
			continue
		}
		if small < 0 || h < vs[small].Height || h == vs[small].Height && vs[i].Bandwidth > vs[small].Bandwidth {
			small = i
		}
		if c.MaxHeight > 0 && h > c.MaxHeight {
			continue
		}
		if fit < 0 || h > vs[fit].Height || h == vs[fit].Height && vs[i].Bandwidth > vs[fit].Bandwidth {
			fit = i
		}
	}
	switch {
	case fit >= 0:
		return fit, "", true
	case small >= 0:
		return small, fmt.Sprintf("every variant is taller than %d; chose the shortest", c.MaxHeight), true
	}
	return best, "no variant declares RESOLUTION; chose the highest bandwidth", true
}

// RewriteOptions controls Rewrite.
type RewriteOptions struct {
	// BaseURL is the absolute URL the playlist was fetched from. Relative
	// URIs are resolved against it. It may be empty when every URI in the
	// playlist is absolute.
	BaseURL string
	// MapURL receives each absolute URI and returns the URI to write into the
	// playlist, typically a local proxy address. When nil the absolute URI is
	// written. URIs with the data: scheme are never passed to MapURL.
	MapURL func(abs string, kind URIKind) string
	// DropAds removes inserted ad groups from media playlists. See
	// RewriteDetailed for the heuristic.
	DropAds bool
	// SelectVariant reduces a master playlist to one variant.
	SelectVariant VariantChooser
	// StripPrefix is a hint for the client's proxy. Segment bytes are not
	// part of a playlist, so Rewrite does not act on it; it is echoed in
	// RewriteResult.StripPrefix and the proxy should pass segment bodies
	// through StripSegmentPrefix.
	StripPrefix bool
}

// RewriteResult is the outcome of RewriteDetailed.
type RewriteResult struct {
	// Body is the rewritten playlist, or the chosen variant's URI when
	// SelectVariant.Output is OutputReference.
	Body []byte
	// DroppedGroups lists the ad groups removed by DropAds, in playlist order.
	DroppedGroups []DroppedGroup
	// Warnings lists decisions the caller may want to log, such as a refused
	// ad removal.
	Warnings []string
	// Selected and SelectedURL describe the chosen variant of a master
	// playlist (SelectedURL is the absolute URI before MapURL).
	Selected    *Variant
	SelectedURL string
	// StripPrefix echoes RewriteOptions.StripPrefix.
	StripPrefix bool
}

// ins is a line to insert before an original line. src >= 0 copies (and
// URI-rewrites) the original line with that index, otherwise text is used
// literally.
type ins struct {
	src  int
	text string
}

// plan collects line-level edits before output is produced.
type plan struct {
	keep     []bool
	override map[int]string // replacement text, used as is
	before   map[int][]ins
	after    map[int][]string // synthetic lines following an original line
}

// Rewrite is RewriteDetailed without the decision details.
func Rewrite(data []byte, opts RewriteOptions) ([]byte, error) {
	r, err := RewriteDetailed(data, opts)
	if err != nil {
		return nil, err
	}
	return r.Body, nil
}

// RewriteDetailed rewrites a playlist for a client-side proxy.
//
// Every URI (segments, EXT-X-KEY, EXT-X-SESSION-KEY, EXT-X-MAP, EXT-X-MEDIA,
// variants and I-frame streams) is resolved against BaseURL and passed to
// MapURL. Unchanged lines keep their bytes and line terminators.
//
// Master playlists: when SelectVariant selects a variant, the result is that
// variant's URI (OutputReference) or the master reduced to the variant, the
// EXT-X-MEDIA renditions its AUDIO, VIDEO, SUBTITLES and CLOSED-CAPTIONS
// attributes name, and the unrelated tags (I-frame streams are dropped).
//
// Media playlists with DropAds: segments are grouped by splitting at
// EXT-X-DISCONTINUITY, EXT-X-CUE-OUT, EXT-X-CUE-IN and ad EXT-X-DATERANGE
// tags. The group with the most duration is the main stream and is never
// removed. Any other group is an ad when
//
//   - it is shorter than 20% of the total duration and all its segments come
//     from a host, or a directory outside the main stream's common directory
//     prefix, that the main stream does not use; or
//   - one of its segments uses a different EXT-X-KEY (method and URI) than the
//     main stream's most frequent key, including no key at all; or
//   - it lies between EXT-X-CUE-OUT and EXT-X-CUE-IN; or
//   - it starts at an EXT-X-DATERANGE whose CLASS or ID has an ad word
//     (see isAdName) and lasts for the DURATION or until the next marker.
//
// If the groups found cover more than 40% of the total duration nothing is
// dropped and a warning is returned. Dropped segments lose their whole tag
// block. EXT-X-MEDIA-SEQUENCE (added if absent) advances by the number of
// segments dropped at the head; dropping in the middle leaves it unchanged,
// so segments that use an implicit AES-128 IV after such a gap get a
// different sequence number than before (a warning says so).
// EXT-X-DISCONTINUITY-SEQUENCE follows head drops. Key and map tags that sat
// in a removed block are re-emitted where later segments still need them.
func RewriteDetailed(data []byte, opts RewriteOptions) (RewriteResult, error) {
	p, err := Parse(data)
	if err != nil {
		return RewriteResult{}, err
	}
	res := RewriteResult{StripPrefix: opts.StripPrefix}

	mapRef := func(ref string, kind URIKind) (string, error) {
		ref = strings.TrimSpace(ref)
		if ref == "" || len(ref) >= 5 && strings.EqualFold(ref[:5], "data:") {
			return ref, nil
		}
		abs, err := Resolve(opts.BaseURL, ref)
		if err != nil {
			return "", fmt.Errorf("hls: %s URI %q: %w", kind, ref, err)
		}
		if opts.MapURL == nil {
			return abs, nil
		}
		out := opts.MapURL(abs, kind)
		if out == "" {
			return "", fmt.Errorf("hls: MapURL returned an empty URI for %s %q", kind, abs)
		}
		return out, nil
	}

	pl := &plan{
		keep:     make([]bool, len(p.Lines)),
		override: map[int]string{},
		before:   map[int][]ins{},
		after:    map[int][]string{},
	}
	for i := range pl.keep {
		pl.keep[i] = true
	}
	uriLines := map[int]URIKind{}
	for _, s := range p.Segments {
		uriLines[s.URILine] = KindSegment
	}
	for _, v := range p.Variants {
		uriLines[v.URILine] = KindVariant
	}

	if p.Master {
		if idx, note, ok := opts.SelectVariant.Choose(p.Variants); ok {
			v := p.Variants[idx]
			res.Selected = &v
			if note != "" {
				res.Warnings = append(res.Warnings, note)
			}
			abs, err := Resolve(opts.BaseURL, v.URI)
			if err != nil {
				return RewriteResult{}, fmt.Errorf("hls: %s URI %q: %w", KindVariant, v.URI, err)
			}
			res.SelectedURL = abs
			if opts.SelectVariant.Output == OutputReference {
				out, err := mapRef(v.URI, KindVariant)
				if err != nil {
					return RewriteResult{}, err
				}
				res.Body = []byte(out)
				return res, nil
			}
			p.reduceMaster(pl, idx)
		} else if opts.SelectVariant.Mode != VariantAll {
			res.Warnings = append(res.Warnings, "master playlist has no variants to select")
		}
	} else if opts.DropAds {
		p.planAdDrop(opts.BaseURL, pl, &res)
	}

	var firstErr error
	render := func(i int) string {
		if t, ok := pl.override[i]; ok {
			return t
		}
		text := p.Lines[i].Text
		if kind, ok := uriLines[i]; ok {
			t := strings.TrimSpace(text)
			out, err := mapRef(t, kind)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return text
			}
			if out == t {
				return text
			}
			return escapeURI(out, false)
		}
		kind, ok := tagKind(text)
		if !ok {
			return text
		}
		out, err := rewriteAttrURI(text, func(uri string) (string, error) { return mapRef(uri, kind) })
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return text
		}
		return out
	}

	eol := p.EOL()
	var lines []Line
	for i, l := range p.Lines {
		for _, in := range pl.before[i] {
			text := in.text
			if in.src >= 0 {
				text = render(in.src)
			}
			lines = append(lines, Line{Text: text, EOL: eol})
		}
		if pl.keep[i] {
			lines = append(lines, Line{Text: render(i), EOL: l.EOL})
		}
		for _, text := range pl.after[i] {
			lines = append(lines, Line{Text: text, EOL: eol})
		}
	}
	if firstErr != nil {
		return RewriteResult{}, firstErr
	}
	for i := 0; i < len(lines)-1; i++ {
		if lines[i].EOL == "" {
			lines[i].EOL = eol
		}
	}
	res.Body = (&Playlist{Lines: lines, bom: p.bom, eol: p.eol}).Marshal()
	return res, nil
}

// reduceMaster removes everything that belongs to variants other than idx.
func (p *Playlist) reduceMaster(pl *plan, idx int) {
	for i, v := range p.Variants {
		if i == idx {
			continue
		}
		for l := v.Start; l <= v.URILine; l++ {
			pl.keep[l] = false
		}
	}
	for _, v := range p.IFrames {
		pl.keep[v.Start] = false
	}
	c := p.Variants[idx]
	for _, m := range p.Media {
		var want string
		switch strings.ToUpper(m.Type) {
		case "AUDIO":
			want = c.Audio
		case "VIDEO":
			want = c.Video
		case "SUBTITLES":
			want = c.Subtitles
		case "CLOSED-CAPTIONS":
			want = c.ClosedCaptions
		}
		if want == "" || want != m.GroupID {
			pl.keep[m.Line] = false
		}
	}
}

// tagKind reports the URI kind of a tag line that carries a URI attribute.
func tagKind(text string) (URIKind, bool) {
	name, ok := tagOf(text)
	if !ok {
		return 0, false
	}
	switch name {
	case "EXT-X-KEY", "EXT-X-SESSION-KEY":
		return KindKey, true
	case "EXT-X-MAP":
		return KindMap, true
	case "EXT-X-MEDIA":
		return KindMedia, true
	case "EXT-X-I-FRAME-STREAM-INF":
		return KindIFrame, true
	}
	return 0, false
}

// rewriteAttrURI replaces the value of the URI attribute of a tag line with
// f(value). The line is returned untouched when f returns the same value.
func rewriteAttrURI(line string, f func(string) (string, error)) (string, error) {
	c := strings.IndexByte(line, ':')
	if c < 0 {
		return line, nil
	}
	val := line[c+1:]
	for _, a := range parseAttrs(val) {
		if !strings.EqualFold(a.Name, "URI") {
			continue
		}
		if strings.TrimSpace(a.Value) == "" {
			return line, nil
		}
		nv, err := f(a.Value)
		if err != nil {
			return "", err
		}
		if nv == a.Value || nv == strings.TrimSpace(a.Value) {
			return line, nil
		}
		return line[:c+1] + val[:a.start] + `"` + escapeURI(nv, true) + `"` + val[a.end:], nil
	}
	return line, nil
}

// escapeURI percent-encodes the characters that would break the playlist
// line: CR, LF and, inside attribute values, the double quote.
func escapeURI(s string, quoted bool) string {
	if !strings.ContainsAny(s, "\r\n\"") {
		return s
	}
	r := strings.NewReplacer("\r", "%0D", "\n", "%0A", "\"", "%22")
	if !quoted {
		r = strings.NewReplacer("\r", "%0D", "\n", "%0A")
	}
	return r.Replace(s)
}
