package hls

import (
	"fmt"
	"strings"
	"testing"
)

// segs writes n segments of dur seconds named prefix0.ts ... under dir.
func segs(dir, prefix string, from, n int, dur float64) string {
	var b strings.Builder
	for i := from; i < from+n; i++ {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s%s%d.ts\n", dur, dir, prefix, i)
	}
	return b.String()
}

const adBase = "https://cdn.example.com/vod/movie/index.m3u8"

func adHeader(seq int) string {
	return fmt.Sprintf("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:%d\n", seq)
}

func dropped(res RewriteResult) []string {
	var out []string
	for _, d := range res.DroppedGroups {
		out = append(out, fmt.Sprintf("%d+%d:%s", d.FirstSegment, d.Segments, strings.Join(d.Reasons, ",")))
	}
	return out
}

func TestDropAds(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		want        string // expected output; empty means "same as input"
		wantDropped string // comma separated "firstSegment+count:reasons"
		wantWarn    string // substring of a warning, empty for none
	}{
		{
			name: "CUE-OUT/CUE-IN bracket on the same host",
			in: adHeader(0) + segs("seg/", "m", 0, 10, 6) +
				"#EXT-X-CUE-OUT:DURATION=12\n" + segs("seg/", "a", 0, 2, 6) + "#EXT-X-CUE-IN\n" +
				segs("seg/", "m", 10, 10, 6) + "#EXT-X-ENDLIST\n",
			want: adHeader(0) + segs("seg/", "m", 0, 10, 6) +
				segs("seg/", "m", 10, 10, 6) + "#EXT-X-ENDLIST\n",
			wantDropped: "10+2:cue-out",
		},
		{
			name: "CUE-OUT with a discontinuity before and after",
			in: adHeader(0) + segs("seg/", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n#EXT-X-CUE-OUT:12\n" + segs("seg/", "a", 0, 2, 6) +
				"#EXT-X-DISCONTINUITY\n#EXT-X-CUE-IN\n" + segs("seg/", "m", 10, 10, 6) + "#EXT-X-ENDLIST\n",
			want: adHeader(0) + segs("seg/", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("seg/", "m", 10, 10, 6) + "#EXT-X-ENDLIST\n",
			wantDropped: "10+2:cue-out",
		},
		{
			name: "CUE-OUT-CONT lines inside the bracket",
			in: adHeader(0) + segs("", "m", 0, 10, 6) +
				"#EXT-X-CUE-OUT:12\n#EXTINF:6,\na0.ts\n#EXT-X-CUE-OUT-CONT:6/12\n#EXTINF:6,\na1.ts\n#EXT-X-CUE-IN\n" +
				segs("", "m", 10, 5, 6),
			want:        adHeader(0) + segs("", "m", 0, 10, 6) + segs("", "m", 10, 5, 6),
			wantDropped: "10+2:cue-out",
		},
		{
			name: "different host discontinuity group",
			in: adHeader(0) + segs("https://cdn.example.com/vod/movie/", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://ads.example.org/x/", "ad", 0, 3, 5) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://cdn.example.com/vod/movie/", "m", 10, 10, 6) + "#EXT-X-ENDLIST\n",
			want: adHeader(0) + segs("https://cdn.example.com/vod/movie/", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://cdn.example.com/vod/movie/", "m", 10, 10, 6) + "#EXT-X-ENDLIST\n",
			wantDropped: "10+3:host-differs",
		},
		{
			name: "different path prefix on the same host",
			in: adHeader(0) + segs("/vod/movie/", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("/promo/", "ad", 0, 2, 5) +
				"#EXT-X-DISCONTINUITY\n" + segs("/vod/movie/", "m", 10, 10, 6),
			want: adHeader(0) + segs("/vod/movie/", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("/vod/movie/", "m", 10, 10, 6),
			wantDropped: "10+2:host-differs",
		},
		{
			name: "relative URIs resolve to the same host: nothing dropped",
			in: adHeader(0) + segs("", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("", "x", 0, 2, 5) + "#EXT-X-DISCONTINUITY\n" + segs("", "m", 10, 10, 6),
		},
		{
			name: "short group on the same host without markers is kept",
			in: adHeader(0) + segs("https://h.example/v/", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://h.example/v/", "n", 0, 2, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://h.example/v/", "m", 10, 10, 6),
		},
		{
			name: "different host but 20% or more of the duration is kept",
			in: adHeader(0) + segs("https://h.example/v/", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://o.example/v/", "o", 0, 3, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://h.example/v/", "m", 10, 1, 6),
			// 18s of 84s = 21%
		},
		{
			name: "different key group",
			in: adHeader(0) + "#EXT-X-KEY:METHOD=AES-128,URI=\"main.key\",IV=0x1\n" + segs("", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n#EXT-X-KEY:METHOD=AES-128,URI=\"ad.key\",IV=0x2\n" + segs("", "a", 0, 2, 6) +
				"#EXT-X-DISCONTINUITY\n#EXT-X-KEY:METHOD=AES-128,URI=\"main.key\",IV=0x1\n" + segs("", "m", 10, 10, 6),
			want: adHeader(0) + "#EXT-X-KEY:METHOD=AES-128,URI=\"main.key\",IV=0x1\n" + segs("", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n#EXT-X-KEY:METHOD=AES-128,URI=\"main.key\",IV=0x1\n" + segs("", "m", 10, 10, 6),
			wantDropped: "10+2:key-differs",
		},
		{
			name: "unencrypted ad inside an encrypted stream",
			in: adHeader(0) + "#EXT-X-KEY:METHOD=AES-128,URI=\"main.key\",IV=0x1\n" + segs("", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n#EXT-X-KEY:METHOD=NONE\n" + segs("", "a", 0, 2, 6) +
				"#EXT-X-DISCONTINUITY\n#EXT-X-KEY:METHOD=AES-128,URI=\"main.key\",IV=0x1\n" + segs("", "m", 10, 10, 6),
			want: adHeader(0) + "#EXT-X-KEY:METHOD=AES-128,URI=\"main.key\",IV=0x1\n" + segs("", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n#EXT-X-KEY:METHOD=AES-128,URI=\"main.key\",IV=0x1\n" + segs("", "m", 10, 10, 6),
			wantDropped: "10+2:key-differs",
		},
		{
			name: "rotating keys per segment are not ads",
			in: adHeader(0) + "#EXT-X-KEY:METHOD=AES-128,URI=\"k1\"\n" + segs("", "m", 0, 5, 6) +
				"#EXT-X-KEY:METHOD=AES-128,URI=\"k2\"\n" + segs("", "m", 5, 5, 6) +
				"#EXT-X-DISCONTINUITY\n#EXT-X-KEY:METHOD=AES-128,URI=\"k3\"\n" + segs("", "m", 10, 2, 6),
		},
		{
			name: "DATERANGE ad with DURATION",
			in: adHeader(0) + segs("", "m", 0, 10, 6) +
				"#EXT-X-DATERANGE:ID=\"break-1\",CLASS=\"com.example.ad\",START-DATE=\"2024-01-01T00:00:00Z\",DURATION=12\n" +
				segs("", "a", 0, 2, 6) + segs("", "m", 10, 10, 6),
			want:        adHeader(0) + segs("", "m", 0, 10, 6) + segs("", "m", 10, 10, 6),
			wantDropped: "10+2:daterange-ad",
		},
		{
			name: "DATERANGE ad without DURATION ends at the discontinuity",
			in: adHeader(0) + segs("", "m", 0, 10, 6) +
				"#EXT-X-DATERANGE:ID=\"ad-7\",START-DATE=\"2024-01-01T00:00:00Z\"\n" +
				segs("", "a", 0, 2, 6) + "#EXT-X-DISCONTINUITY\n" + segs("", "m", 10, 10, 6),
			want:        adHeader(0) + segs("", "m", 0, 10, 6) + "#EXT-X-DISCONTINUITY\n" + segs("", "m", 10, 10, 6),
			wantDropped: "10+2:daterange-ad",
		},
		{
			name: "DATERANGE for something else is ignored",
			in: adHeader(0) + segs("", "m", 0, 10, 6) +
				"#EXT-X-DATERANGE:ID=\"chapter-2\",CLASS=\"com.example.chapter\",START-DATE=\"2024-01-01T00:00:00Z\",DURATION=12\n" +
				segs("", "a", 0, 2, 6) + segs("", "m", 10, 10, 6),
		},
		{
			name: "head ad advances EXT-X-MEDIA-SEQUENCE",
			in: adHeader(100) + "#EXT-X-DISCONTINUITY\n" + segs("https://ads.example.org/", "ad", 0, 2, 5) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://cdn.example.com/vod/movie/", "m", 0, 20, 6),
			want: adHeader(102) + "#EXT-X-DISCONTINUITY-SEQUENCE:1\n#EXT-X-DISCONTINUITY\n" +
				segs("https://cdn.example.com/vod/movie/", "m", 0, 20, 6),
			wantDropped: "0+2:host-differs",
		},
		{
			name: "head ad without MEDIA-SEQUENCE tag adds one",
			in: "#EXTM3U\n#EXT-X-TARGETDURATION:10\n" + segs("https://ads.example.org/", "ad", 0, 1, 5) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://cdn.example.com/vod/movie/", "m", 0, 20, 6),
			want: "#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-DISCONTINUITY-SEQUENCE:1\n" +
				"#EXT-X-DISCONTINUITY\n" + segs("https://cdn.example.com/vod/movie/", "m", 0, 20, 6),
			wantDropped: "0+1:host-differs",
		},
		{
			name: "mid ad leaves MEDIA-SEQUENCE alone",
			in: adHeader(100) + segs("https://cdn.example.com/vod/movie/", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://ads.example.org/", "ad", 0, 2, 5) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://cdn.example.com/vod/movie/", "m", 10, 10, 6),
			want: adHeader(100) + segs("https://cdn.example.com/vod/movie/", "m", 0, 10, 6) +
				"#EXT-X-DISCONTINUITY\n" + segs("https://cdn.example.com/vod/movie/", "m", 10, 10, 6),
			wantDropped: "10+2:host-differs",
		},
		{
			name: "two ads",
			in: adHeader(0) + segs("", "m", 0, 10, 6) + "#EXT-X-CUE-OUT:6\n" + segs("", "a", 0, 1, 6) + "#EXT-X-CUE-IN\n" +
				segs("", "m", 10, 10, 6) + "#EXT-X-CUE-OUT:6\n" + segs("", "b", 0, 1, 6) + "#EXT-X-CUE-IN\n" + segs("", "m", 20, 5, 6),
			want:        adHeader(0) + segs("", "m", 0, 10, 6) + segs("", "m", 10, 10, 6) + segs("", "m", 20, 5, 6),
			wantDropped: "10+1:cue-out,21+1:cue-out",
		},
		{
			name: "key tag inside a removed head ad is re-emitted for the content after it",
			in: adHeader(0) + "#EXT-X-CUE-OUT:6\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\",IV=0x1\n" + segs("", "a", 0, 1, 6) +
				"#EXT-X-CUE-IN\n" + segs("", "m", 0, 20, 6),
			want:        adHeader(1) + "#EXT-X-KEY:METHOD=AES-128,URI=\"k\",IV=0x1\n" + segs("", "m", 0, 20, 6),
			wantDropped: "0+1:cue-out",
		},
		{
			name: "METHOD=NONE is re-emitted when a removed ad had switched the key off",
			in: adHeader(0) + segs("", "m", 0, 5, 6) + "#EXT-X-KEY:METHOD=AES-128,URI=\"k\",IV=0x1\n" + segs("", "m", 5, 5, 6) +
				"#EXT-X-CUE-OUT:6\n#EXT-X-KEY:METHOD=NONE\n" + segs("", "a", 0, 1, 6) + "#EXT-X-CUE-IN\n" + segs("", "m", 10, 10, 6),
			want: adHeader(0) + segs("", "m", 0, 5, 6) + "#EXT-X-KEY:METHOD=AES-128,URI=\"k\",IV=0x1\n" + segs("", "m", 5, 5, 6) +
				"#EXT-X-KEY:METHOD=NONE\n" + segs("", "m", 10, 10, 6),
			wantDropped: "10+1:cue-out",
		},
		{
			name: "playlist without markers is untouched",
			in:   adHeader(0) + segs("", "m", 0, 10, 6) + "#EXT-X-ENDLIST\n",
		},
		{
			name: "empty playlist",
			in:   "#EXTM3U\n#EXT-X-ENDLIST\n",
		},
		{
			name: "large cue group is refused with a warning",
			in: adHeader(0) + segs("", "m", 0, 10, 6) +
				"#EXT-X-CUE-OUT:54\n" + segs("", "a", 0, 9, 6) + "#EXT-X-CUE-IN\n" + segs("", "m", 10, 2, 6),
			wantWarn: "safety limit",
		},
		{
			name: "two candidates together exceed 40%",
			in: adHeader(0) + segs("", "m", 0, 10, 6) +
				"#EXT-X-CUE-OUT:30\n" + segs("", "a", 0, 5, 6) + "#EXT-X-CUE-IN\n" + segs("", "m", 10, 2, 6) +
				"#EXT-X-CUE-OUT:30\n" + segs("", "b", 0, 5, 6) + "#EXT-X-CUE-IN\n",
			wantWarn: "safety limit",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, crlfMode := range []bool{false, true} {
				in, want := tc.in, tc.want
				if want == "" {
					want = in
				}
				if crlfMode {
					in, want = crlf(in), crlf(want)
				}
				res := mustRewrite(t, in, RewriteOptions{BaseURL: adBase, DropAds: true, MapURL: func(abs string, _ URIKind) string {
					// Keep the playlist readable: rewrite only to the same text.
					return abs
				}})
				// Compare against the expectation with relative URIs resolved
				// the same way, so the test text can stay short.
				wantBody := resolveAll(t, want)
				if string(res.Body) != wantBody {
					t.Errorf("crlf=%v body mismatch:\n got:\n%s\nwant:\n%s", crlfMode, res.Body, wantBody)
				}
				if got := strings.Join(dropped(res), ","); got != tc.wantDropped {
					t.Errorf("dropped = %q, want %q", got, tc.wantDropped)
				}
				switch {
				case tc.wantWarn == "" && len(res.Warnings) != 0:
					t.Errorf("unexpected warnings %v", res.Warnings)
				case tc.wantWarn != "" && !strings.Contains(strings.Join(res.Warnings, "|"), tc.wantWarn):
					t.Errorf("warnings %v lack %q", res.Warnings, tc.wantWarn)
				}
			}
		})
	}
}

// resolveAll resolves every segment URI in a playlist against adBase using a
// plain rewrite without DropAds, so expected strings can be written compactly.
func resolveAll(t *testing.T, in string) string {
	t.Helper()
	b, err := Rewrite([]byte(in), RewriteOptions{BaseURL: adBase})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDropAdsRefusalKeepsPlaylistUnchanged(t *testing.T) {
	in := adHeader(5) + segs("https://cdn.example.com/", "m", 0, 10, 6) +
		"#EXT-X-DISCONTINUITY\n#EXT-X-CUE-OUT:54\n" + segs("https://cdn.example.com/", "a", 0, 9, 6) +
		"#EXT-X-DISCONTINUITY\n#EXT-X-CUE-IN\n"
	res := mustRewrite(t, in, RewriteOptions{DropAds: true})
	if string(res.Body) != in || len(res.DroppedGroups) != 0 || len(res.Warnings) != 1 {
		t.Errorf("body unchanged=%v dropped=%v warnings=%v", string(res.Body) == in, res.DroppedGroups, res.Warnings)
	}
}

func TestDropAdsDetails(t *testing.T) {
	in := adHeader(40) + segs("https://cdn.example.com/m/", "m", 0, 10, 6) +
		"#EXT-X-DISCONTINUITY\n" + segs("https://ads.example.org/x/", "ad", 0, 2, 5) +
		"#EXT-X-DISCONTINUITY\n" + segs("https://cdn.example.com/m/", "m", 10, 10, 6)
	res := mustRewrite(t, in, RewriteOptions{DropAds: true})
	if len(res.DroppedGroups) != 1 {
		t.Fatalf("dropped = %+v", res.DroppedGroups)
	}
	d := res.DroppedGroups[0]
	if d.Index != 1 || d.FirstSegment != 10 || d.Segments != 2 || d.FirstSequence != 50 ||
		d.FirstURI != "https://ads.example.org/x/ad0.ts" || d.Duration != 10 || len(d.Reasons) != 1 || d.Reasons[0] != ReasonHost {
		t.Errorf("group = %+v", d)
	}
}

func TestDropAdsIgnoredForMaster(t *testing.T) {
	res := mustRewrite(t, masterLF, RewriteOptions{BaseURL: "https://h.example/", DropAds: true,
		SelectVariant: VariantChooser{Mode: VariantAll}})
	if len(res.DroppedGroups) != 0 || len(res.Warnings) != 0 {
		t.Errorf("%+v", res)
	}
}

func TestDropAdsMainGroupIsNeverDropped(t *testing.T) {
	// The only marked group is also the largest, so it is the main stream.
	in := adHeader(0) + "#EXT-X-CUE-OUT:60\n" + segs("", "m", 0, 10, 6) + "#EXT-X-CUE-IN\n" + segs("", "e", 0, 1, 6)
	res := mustRewrite(t, in, RewriteOptions{BaseURL: adBase, DropAds: true})
	if len(res.DroppedGroups) != 0 {
		t.Errorf("main group dropped: %+v", res.DroppedGroups)
	}
}

func TestDropAdsImplicitIVWarning(t *testing.T) {
	in := adHeader(0) + "#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n" + segs("", "m", 0, 10, 6) +
		"#EXT-X-CUE-OUT:6\n" + segs("", "a", 0, 1, 6) + "#EXT-X-CUE-IN\n" + segs("", "m", 10, 10, 6)
	res := mustRewrite(t, in, RewriteOptions{BaseURL: adBase, DropAds: true})
	if len(res.DroppedGroups) != 1 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "implicit IV") {
		t.Errorf("dropped=%v warnings=%v", res.DroppedGroups, res.Warnings)
	}
	// With an explicit IV there is nothing to warn about.
	in = strings.Replace(in, `URI="k"`, `URI="k",IV=0x01`, 1)
	res = mustRewrite(t, in, RewriteOptions{BaseURL: adBase, DropAds: true})
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestIsAdName(t *testing.T) {
	yes := []string{"ad", "Ad", "ads", "AD", "ad-1", "ad_break", "com.example.ad", "com.apple.hls.ad", "break-ad-3",
		"adbreak", "AdBreak", "advertisement", "ad1", "midroll-ads", "SCTE35-ad", "x:ad:y"}
	no := []string{"", "chapter", "load", "header", "broadcast", "shadow", "badge", "address", "radio", "add", "adapt",
		"com.example.chapter", "download", "ready"}
	for _, s := range yes {
		if !isAdName(s) {
			t.Errorf("isAdName(%q) = false", s)
		}
	}
	for _, s := range no {
		if isAdName(s) {
			t.Errorf("isAdName(%q) = true", s)
		}
	}
}

func TestChooseNoVariants(t *testing.T) {
	if _, _, ok := (VariantChooser{}).Choose(nil); ok {
		t.Error("Choose(nil) selected something")
	}
	res := mustRewrite(t, "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"n\",URI=\"a.m3u8\"\n", RewriteOptions{BaseURL: "https://h.example/"})
	if len(res.Warnings) != 1 {
		t.Errorf("warnings = %v", res.Warnings)
	}
}
