package hls

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Reasons reported in DroppedGroup.Reasons.
const (
	ReasonHost      = "host-differs" // short group served from another host or path prefix
	ReasonKey       = "key-differs"  // group uses EXT-X-KEY state the main stream never uses
	ReasonCue       = "cue-out"      // group lies between EXT-X-CUE-OUT and EXT-X-CUE-IN
	ReasonDateRange = "daterange-ad" // group is covered by an EXT-X-DATERANGE whose CLASS or ID names an ad
)

const (
	adShareLimit    = 0.20 // a host-differs candidate must be shorter than this share of the total
	adDropLimit     = 0.40 // never drop more than this share of the total duration
	durationEpsilon = 1e-6
)

// DroppedGroup describes one group of segments removed by DropAds.
type DroppedGroup struct {
	Index         int      // position among all groups of the input playlist
	FirstSegment  int      // index of the group's first segment in the input
	Segments      int      // number of segments removed
	FirstSequence int64    // media sequence number of the first removed segment
	FirstURI      string   // absolute URI of the first removed segment (as written when BaseURL is empty)
	Duration      float64  // seconds
	Reasons       []string // Reason* values that made the group a candidate
}

// group is a run of consecutive segments that share an origin: a
// discontinuity-delimited section, further split at cue and ad-range markers.
type group struct {
	lo, hi int // segment index range [lo, hi)
	dur    float64
	cue    bool // between EXT-X-CUE-OUT and EXT-X-CUE-IN
	rng    bool // covered by an ad EXT-X-DATERANGE
}

// blockInfo is what a segment's tag block says about ad boundaries.
type blockInfo struct {
	cueOut, cueCont, cueIn bool
	adRange                bool
	rangeDur               float64
}

func (p *Playlist) blockInfo(s *Segment) blockInfo {
	var b blockInfo
	for i := s.Start; i < s.URILine; i++ {
		name, ok := tagOf(p.Lines[i].Text)
		if !ok {
			continue
		}
		switch name {
		case "EXT-X-CUE-OUT":
			b.cueOut = true
		case "EXT-X-CUE-OUT-CONT":
			b.cueCont = true
		case "EXT-X-CUE-IN":
			b.cueIn = true
		case "EXT-X-DATERANGE":
			a := parseAttrs(tagValue(p.Lines[i].Text))
			if isAdName(attrValue(a, "CLASS")) || isAdName(attrValue(a, "ID")) {
				b.adRange = true
				d := attrValue(a, "DURATION")
				if d == "" {
					d = attrValue(a, "PLANNED-DURATION")
				}
				if f, err := strconv.ParseFloat(d, 64); err == nil && f > 0 {
					b.rangeDur = f
				}
			}
		}
	}
	return b
}

var adSuffixes = map[string]bool{
	"break": true, "breaks": true, "marker": true, "slot": true, "pod": true,
	"insert": true, "insertion": true, "splice": true, "start": true,
	"end": true, "opportunity": true, "roll": true,
}

// isAdName reports whether a DATERANGE CLASS or ID names an advertisement.
// The value is split into words at non-alphanumeric characters and at
// lower-to-upper case changes; a word must be "ad", "ads", "ad" plus digits,
// "ad" plus a marker word such as "break" or "slot" ("adbreak"), or start
// with "advert". Plain substring matching would flag "load", "header" or
// "broadcast".
func isAdName(s string) bool {
	if s == "" {
		return false
	}
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	var prev rune
	for _, r := range s {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
		prev = r
	}
	flush()
	for _, w := range words {
		if w == "ad" || w == "ads" || strings.HasPrefix(w, "advert") {
			return true
		}
		if strings.HasPrefix(w, "ad") && len(w) > 2 {
			rest := w[2:]
			if strings.Trim(rest, "0123456789") == "" || adSuffixes[rest] {
				return true
			}
		}
	}
	return false
}

// buildGroups splits the segments into groups. A group starts at the first
// segment, at every EXT-X-DISCONTINUITY, and at EXT-X-CUE-OUT, EXT-X-CUE-IN
// and ad EXT-X-DATERANGE markers (so that cue-bracketed ads are isolated even
// without a discontinuity). A DATERANGE with a DURATION covers segments until
// that much media time has passed; without one it ends at the next
// discontinuity or CUE-IN.
func (p *Playlist) buildGroups() []group {
	var groups []group
	var (
		inCue    bool
		rngOn    bool
		rngKnown bool
		rngLeft  float64
	)
	for i := range p.Segments {
		s := &p.Segments[i]
		b := p.blockInfo(s)
		boundary := i == 0 || s.Discontinuity
		if b.cueIn {
			inCue = false
			boundary = true
		}
		if b.cueOut || (b.cueCont && !inCue) {
			inCue = true
			boundary = true
		}
		if rngOn {
			switch {
			case rngKnown && rngLeft <= durationEpsilon:
				rngOn = false
				boundary = true
			case !rngKnown && (s.Discontinuity || b.cueIn):
				rngOn = false
			}
		}
		if b.adRange {
			rngOn, rngKnown, rngLeft = true, b.rangeDur > 0, b.rangeDur
			boundary = true
		}
		if boundary {
			groups = append(groups, group{lo: i, cue: inCue, rng: rngOn})
		}
		g := &groups[len(groups)-1]
		g.hi = i + 1
		g.dur += s.Duration
		if rngOn && rngKnown {
			rngLeft -= s.Duration
		}
	}
	return groups
}

// segOrigin returns the lower-cased authority and the directory part of a
// segment's path, resolved against base when possible.
func segOrigin(base string, s *Segment) (host, dir string) {
	abs := s.URI
	if base != "" {
		if r, err := Resolve(base, s.URI); err == nil {
			abs = r
		}
	}
	u := splitURI(abs)
	return strings.ToLower(u.authority), u.path[:strings.LastIndexByte(u.path, '/')+1]
}

// commonDirPrefix returns the longest shared directory prefix (ending in "/"
// or empty).
func commonDirPrefix(dirs []string) string {
	if len(dirs) == 0 {
		return ""
	}
	cp := dirs[0]
	for _, d := range dirs[1:] {
		for cp != "" && !strings.HasPrefix(d, cp) {
			cp = cp[:strings.LastIndexByte(cp[:len(cp)-1], '/')+1]
		}
	}
	return cp
}

// keyID identifies a key for comparison. Segments without encryption share
// the empty identity. With methodOnly the URI is ignored, for streams that
// rotate keys per segment.
func keyID(base string, k *Key, methodOnly bool) string {
	if k == nil {
		return ""
	}
	if methodOnly {
		return strings.ToUpper(k.Method)
	}
	uri := k.URI
	if base != "" {
		if r, err := Resolve(base, uri); err == nil {
			uri = r
		}
	}
	return strings.ToUpper(k.Method) + "|" + uri
}

// planAdDrop decides which groups are ads and records the edits in pl. It
// leaves the playlist untouched, with a warning, when the decision would
// remove too much.
func (p *Playlist) planAdDrop(base string, pl *plan, res *RewriteResult) {
	if len(p.Segments) == 0 {
		return
	}
	groups := p.buildGroups()
	var total float64
	maj := 0
	for i := range groups {
		total += groups[i].dur
		if groups[i].dur > groups[maj].dur {
			maj = i
		}
	}
	if total <= 0 || len(groups) < 2 {
		return
	}

	// Signature of the main stream: the hosts, common directory prefix and
	// set of keys of the largest group.
	mg := groups[maj]
	hosts := map[string]bool{}
	var dirs []string
	mainKeys := map[string]bool{}
	for i := mg.lo; i < mg.hi; i++ {
		h, d := segOrigin(base, &p.Segments[i])
		hosts[h] = true
		dirs = append(dirs, d)
		mainKeys[keyID(base, p.Segments[i].Key, false)] = true
	}
	methodOnly := len(mainKeys) > 1
	if methodOnly {
		mainKeys = map[string]bool{}
		for i := mg.lo; i < mg.hi; i++ {
			mainKeys[keyID(base, p.Segments[i].Key, true)] = true
		}
	}
	prefix := commonDirPrefix(dirs)

	var found []DroppedGroup
	for gi, g := range groups {
		if gi == maj {
			continue // the largest group is always content
		}
		var reasons []string
		if g.dur < adShareLimit*total {
			differs := true
			for i := g.lo; i < g.hi && differs; i++ {
				h, d := segOrigin(base, &p.Segments[i])
				differs = !hosts[h] || !strings.HasPrefix(d, prefix)
			}
			if differs {
				reasons = append(reasons, ReasonHost)
			}
		}
		newKey := true
		for i := g.lo; i < g.hi && newKey; i++ {
			newKey = !mainKeys[keyID(base, p.Segments[i].Key, methodOnly)]
		}
		if newKey {
			reasons = append(reasons, ReasonKey)
		}
		if g.cue {
			reasons = append(reasons, ReasonCue)
		}
		if g.rng {
			reasons = append(reasons, ReasonDateRange)
		}
		if len(reasons) == 0 {
			continue
		}
		first := &p.Segments[g.lo]
		uri := first.URI
		if r, err := Resolve(base, uri); err == nil {
			uri = r
		}
		found = append(found, DroppedGroup{
			Index: gi, FirstSegment: g.lo, Segments: g.hi - g.lo,
			FirstSequence: first.Sequence, FirstURI: uri, Duration: g.dur, Reasons: reasons,
		})
	}
	if len(found) == 0 {
		return
	}
	var dropDur float64
	for _, d := range found {
		dropDur += d.Duration
	}
	if dropDur > adDropLimit*total+durationEpsilon {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"ad removal skipped: %d candidate group(s) cover %.1fs of %.1fs (%.0f%%), above the %.0f%% safety limit",
			len(found), dropDur, total, 100*dropDur/total, 100*adDropLimit))
		return
	}
	res.DroppedGroups = found
	p.applyDrops(pl, res, found)
}

// applyDrops removes the dropped segments' lines and repairs the state that
// later segments depend on.
func (p *Playlist) applyDrops(pl *plan, res *RewriteResult, found []DroppedGroup) {
	dropped := make([]bool, len(p.Segments))
	for _, d := range found {
		for i := d.FirstSegment; i < d.FirstSegment+d.Segments; i++ {
			dropped[i] = true
			s := &p.Segments[i]
			for l := s.Start; l <= s.URILine; l++ {
				pl.keep[l] = false
			}
		}
	}

	head := 0
	for head < len(dropped) && dropped[head] {
		head++
	}
	if head > 0 {
		// The discontinuity sequence number of the new first segment is the
		// old number plus the discontinuities after the old first segment up
		// to and including the new first segment's own tag.
		headDisc := 0
		for j := 1; j <= head && j < len(p.Segments); j++ {
			if p.Segments[j].Discontinuity {
				headDisc++
			}
		}
		at := p.findTag("EXT-X-TARGETDURATION")
		if at < 0 {
			at = p.firstContentLine()
		}
		if i := p.findTag("EXT-X-MEDIA-SEQUENCE"); i >= 0 {
			pl.override[i] = "#EXT-X-MEDIA-SEQUENCE:" + strconv.FormatInt(p.MediaSequence+int64(head), 10)
		} else {
			// Without the tag the sequence starts at 0; segments with an
			// implicit IV need the advanced number.
			pl.after[at] = append(pl.after[at], "#EXT-X-MEDIA-SEQUENCE:"+strconv.Itoa(head))
		}
		if headDisc > 0 {
			if i := p.findTag("EXT-X-DISCONTINUITY-SEQUENCE"); i >= 0 {
				pl.override[i] = "#EXT-X-DISCONTINUITY-SEQUENCE:" + strconv.FormatInt(p.DiscontinuitySequence+int64(headDisc), 10)
			} else {
				if i := p.findTag("EXT-X-MEDIA-SEQUENCE"); i >= 0 {
					at = i
				}
				pl.after[at] = append(pl.after[at], "#EXT-X-DISCONTINUITY-SEQUENCE:"+strconv.Itoa(headDisc))
			}
		}
	}

	var outKey *Key
	var outMap *Map
	midDrop := false
	warnedIV := false
	for i := range p.Segments {
		s := &p.Segments[i]
		if dropped[i] {
			if i >= head {
				midDrop = true
			}
			continue
		}
		if i > 0 && dropped[i-1] {
			// The CUE-IN that closed a removed ad has nothing left to close.
			// A discontinuity after the ad stays: the content on both sides
			// may have different timelines, and an extra one is harmless.
			for l := s.Start; l < s.URILine; l++ {
				if name, ok := tagOf(p.Lines[l].Text); ok && name == "EXT-X-CUE-IN" {
					pl.keep[l] = false
				}
			}
		}
		// Key and map tags persist across segments. When the tag that set the
		// current state sat in a removed block, re-emit it.
		if s.ownKey {
			outKey = s.Key
		} else if s.Key != outKey {
			if s.Key != nil {
				pl.before[s.Start] = append(pl.before[s.Start], ins{src: s.Key.Line})
			} else {
				pl.before[s.Start] = append(pl.before[s.Start], ins{src: -1, text: "#EXT-X-KEY:METHOD=NONE"})
			}
			outKey = s.Key
		}
		if s.ownMap {
			outMap = s.Map
		} else if s.Map != outMap && s.Map != nil {
			pl.before[s.Start] = append(pl.before[s.Start], ins{src: s.Map.Line})
			outMap = s.Map
		}
		if midDrop && !warnedIV && s.Key != nil && strings.EqualFold(s.Key.Method, "AES-128") && s.Key.IV == "" {
			warnedIV = true
			res.Warnings = append(res.Warnings,
				"segments removed before encrypted segments that use the implicit IV; "+
					"the IV of those segments is derived from their original media sequence numbers")
		}
	}
}

// findTag returns the index of the first line carrying the named tag, or -1.
func (p *Playlist) findTag(name string) int {
	for i, l := range p.Lines {
		if n, ok := tagOf(l.Text); ok && n == name {
			return i
		}
	}
	return -1
}

func (p *Playlist) firstContentLine() int {
	for i, l := range p.Lines {
		if strings.TrimSpace(l.Text) != "" {
			return i
		}
	}
	return 0
}
