package hls

import (
	"strings"
	"testing"
)

const mediaLF = `#EXTM3U
#EXT-X-VERSION:3
## a comment line
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:7
#EXT-X-VENDOR-THING:foo=bar,baz="q,r"

#EXT-X-KEY:METHOD=AES-128,URI="key.bin",IV=0x000102030405060708090a0b0c0d0e0f
#EXTINF:9.009,first title
seg0.ts
#EXTINF:10,
seg1.ts
#EXT-X-KEY:METHOD=NONE
#EXT-X-DISCONTINUITY
#EXT-X-MAP:URI="init.mp4",BYTERANGE="720@0"
#EXTINF:4.5,
#EXT-X-BYTERANGE:100@0
seg2.m4s
#EXT-X-ENDLIST
`

const masterLF = `#EXTM3U
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="en",URI="audio/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,AVERAGE-BANDWIDTH=700000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2",AUDIO="aud"
low/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud"
mid/index.m3u8?token=a,b
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,AUDIO="aud"
high/index.m3u8
#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=100000,RESOLUTION=1920x1080,URI="high/iframes.m3u8"
`

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func TestParseRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"media LF", mediaLF},
		{"media CRLF", crlf(mediaLF)},
		{"master LF", masterLF},
		{"master CRLF", crlf(masterLF)},
		{"no trailing newline", strings.TrimSuffix(mediaLF, "\n")},
		{"mixed endings", "#EXTM3U\r\n#EXTINF:1,\na.ts\n#EXTINF:1,\r\nb.ts\r\n"},
		{"BOM", "\xef\xbb\xbf#EXTM3U\n#EXTINF:1,\na.ts\n"},
		{"leading blank lines", "\n\n#EXTM3U\n#EXTINF:1,\na.ts\n"},
		{"trailing blank lines", "#EXTM3U\n#EXTINF:1,\na.ts\n\n\n"},
		{"trailing spaces", "#EXTM3U  \n#EXTINF:1, \na.ts  \n"},
		{"lone CR kept", "#EXTM3U\n#EXTINF:1,\na.ts\r"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if got := string(p.Marshal()); got != tc.in {
				t.Errorf("round trip changed the playlist:\n got %q\nwant %q", got, tc.in)
			}
		})
	}
}

func TestParseMedia(t *testing.T) {
	p, err := Parse([]byte(mediaLF))
	if err != nil {
		t.Fatal(err)
	}
	if p.Master || !p.Endlist || p.Version != 3 || p.TargetDuration != 10 || p.MediaSequence != 7 {
		t.Fatalf("header fields wrong: %+v", p)
	}
	if len(p.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(p.Segments))
	}
	s0, s1, s2 := p.Segments[0], p.Segments[1], p.Segments[2]
	if s0.URI != "seg0.ts" || s0.Duration != 9.009 || s0.Title != "first title" || s0.Sequence != 7 {
		t.Errorf("seg0 = %+v", s0)
	}
	if s0.Key == nil || s0.Key.Method != "AES-128" || s0.Key.URI != "key.bin" || s0.Key.IV != "0x000102030405060708090a0b0c0d0e0f" {
		t.Errorf("seg0 key = %+v", s0.Key)
	}
	if s1.Key != s0.Key || s1.Sequence != 8 {
		t.Errorf("seg1 should inherit the key state: %+v", s1)
	}
	if s2.Key != nil {
		t.Errorf("METHOD=NONE should clear the key, got %+v", s2.Key)
	}
	if !s2.Discontinuity || s2.ByteRange != "100@0" || s2.Map == nil || s2.Map.URI != "init.mp4" || s2.Map.ByteRange != "720@0" {
		t.Errorf("seg2 = %+v", s2)
	}
	if s0.Map != nil {
		t.Errorf("seg0 must not have a map")
	}
	wantHeader := []string{
		"#EXTM3U", "#EXT-X-VERSION:3", "## a comment line", "#EXT-X-TARGETDURATION:10",
		"#EXT-X-MEDIA-SEQUENCE:7", `#EXT-X-VENDOR-THING:foo=bar,baz="q,r"`, "",
		`#EXT-X-KEY:METHOD=AES-128,URI="key.bin",IV=0x000102030405060708090a0b0c0d0e0f`,
	}
	if strings.Join(p.Header, "|") != strings.Join(wantHeader, "|") {
		t.Errorf("header = %q", p.Header)
	}
}

func TestParseMaster(t *testing.T) {
	p, err := Parse([]byte(masterLF))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Master || len(p.Segments) != 0 {
		t.Fatalf("master=%v segments=%d", p.Master, len(p.Segments))
	}
	if len(p.Variants) != 3 || len(p.IFrames) != 1 || len(p.Media) != 1 {
		t.Fatalf("variants=%d iframes=%d media=%d", len(p.Variants), len(p.IFrames), len(p.Media))
	}
	v := p.Variants[0]
	if v.URI != "low/index.m3u8" || v.Bandwidth != 800000 || v.AverageBandwidth != 700000 ||
		v.Width != 640 || v.Height != 360 || v.Codecs != "avc1.4d401e,mp4a.40.2" || v.Audio != "aud" {
		t.Errorf("variant 0 = %+v", v)
	}
	if p.Variants[1].URI != "mid/index.m3u8?token=a,b" {
		t.Errorf("variant 1 URI = %q", p.Variants[1].URI)
	}
	if p.IFrames[0].URI != "high/iframes.m3u8" || p.Media[0].URI != "audio/en.m3u8" || p.Media[0].GroupID != "aud" {
		t.Errorf("iframe/media = %+v / %+v", p.IFrames[0], p.Media[0])
	}
}

func TestParseTolerant(t *testing.T) {
	p, err := Parse([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:abc\n#EXTINF:oops,\nx.ts\n#EXT-X-STREAM-INF:BANDWIDTH=\nv.m3u8\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Segments) != 1 || p.Segments[0].Duration != 0 || p.TargetDuration != 0 {
		t.Errorf("tolerant parse: %+v", p)
	}
	for _, in := range []string{"", "\n\n", "<html>nope</html>", "hello\n#EXTM3U\n"} {
		if _, err := Parse([]byte(in)); err != ErrNotPlaylist {
			t.Errorf("Parse(%q) error = %v, want ErrNotPlaylist", in, err)
		}
	}
}

func TestKeyFormatDoesNotReplaceIdentityKey(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n" +
		"#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://x\",KEYFORMAT=\"com.apple.streamingkeydelivery\"\n" +
		"#EXTINF:1,\na.ts\n"
	p, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if k := p.Segments[0].Key; k == nil || k.Method != "AES-128" {
		t.Errorf("key = %+v", k)
	}
}

func TestParseAttrs(t *testing.T) {
	a := parseAttrs(`A=1, B="x,y" ,C=,D="open,E=5`)
	want := map[string]string{"A": "1", "B": "x,y", "C": "", "D": "open,E=5"}
	if len(a) != len(want) {
		t.Fatalf("got %+v", a)
	}
	for _, x := range a {
		if want[x.Name] != x.Value {
			t.Errorf("%s = %q, want %q", x.Name, x.Value, want[x.Name])
		}
	}
	if got := attrValue(parseAttrs("junk,uri=\"u\""), "URI"); got != "u" {
		t.Errorf("case-insensitive lookup = %q", got)
	}
}
