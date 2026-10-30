package hls

import (
	"bytes"
	"strings"
	"testing"
)

// fuzzSeeds are hand-written playlists, valid and broken, shared by the fuzz
// targets. Go runs them as ordinary test cases under `go test`.
func fuzzSeeds() []string {
	return []string{
		"",
		"\n",
		"#EXTM3U",
		"#EXTM3U\n",
		"#EXTM3U\r\n#EXTINF:1,\r\na.ts\r\n",
		mediaLF,
		crlf(mediaLF),
		masterLF,
		crlf(masterLF),
		"\xef\xbb\xbf#EXTM3U\n#EXTINF:1,\na.ts\n",
		"<html><body>403</body></html>",
		"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"unterminated\n#EXTINF:1,\na.ts\n",
		"#EXTM3U\n#EXT-X-KEY:\n#EXT-X-MAP:\n#EXT-X-MEDIA:\n#EXT-X-STREAM-INF:\n#EXT-X-I-FRAME-STREAM-INF:\n#EXTINF:\n",
		"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=99999999999999999999,RESOLUTION=axb\n",
		"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n\n# c\n#EXT-X-STREAM-INF:BANDWIDTH=2\nv.m3u8\n",
		"#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:-5\n#EXTINF:-3,\na.ts\n#EXTINF:NaN,\nb.ts\n#EXTINF:1e999,\nc.ts\n",
		"#EXTM3U\n#EXT-X-CUE-IN\n#EXT-X-CUE-OUT\n#EXT-X-CUE-OUT-CONT\n#EXT-X-DATERANGE:ID=\"ad\",DURATION=abc\n#EXTINF:1,\na.ts\n",
		"#EXTM3U\n#EXT-X-DATERANGE:ID=ad,DURATION=-1\n#EXTINF:0,\na.ts\n#EXTINF:0,\nb.ts\n",
		"#EXTM3U\n#EXT-X-DISCONTINUITY\n#EXT-X-DISCONTINUITY\n#EXTINF:1,\n//\n#EXTINF:1,\n:::\n#EXTINF:1,\n../../..\n",
		"#EXTM3U\n#EXTINF:1,\nhttp://[::1\n#EXTINF:1,\nhttp://h/%zz\n#EXTINF:1,\n?\n#EXTINF:1,\n#\n",
		"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXTINF:1,\na.ts\n#EXT-X-CUE-OUT:1\n#EXTINF:100,\nb.ts\n#EXT-X-CUE-IN\n#EXTINF:1,\nc.ts\n",
		"#EXTM3U\n#EXT-X-MAP:URI=\"i.mp4\"\n#EXT-X-DISCONTINUITY\n#EXTINF:1,\n#EXT-X-BYTERANGE:1@2\nx.m4s\n#EXT-X-ENDLIST\n#EXT-X-ENDLIST\n",
		"#EXTM3U\n" + strings.Repeat("#EXTINF:1,\nhttps://h/a.ts\n", 50),
		"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n",
		"#EXTM3U\r\r\n\n\r#EXTINF:1,\ra.ts\r",
		"#EXTM3U\n#EXT-X-DATERANGE:ID=\"ad\",CLASS=\"ad\"\n#EXT-X-DATERANGE:ID=\"ad2\"\n#EXTINF:1,\na.ts\n#EXT-X-DISCONTINUITY\n#EXTINF:1,\nb.ts\n",
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Parse(data)
		if err != nil {
			if p != nil {
				t.Fatal("non-nil playlist with an error")
			}
			return
		}
		if got := p.Marshal(); !bytes.Equal(got, data) {
			t.Fatalf("round trip changed the input:\n got %q\nwant %q", got, data)
		}
		for i, s := range p.Segments {
			if s.URILine < 0 || s.URILine >= len(p.Lines) || s.Start < 0 || s.Start > s.URILine {
				t.Fatalf("segment %d has bad line indexes %+v", i, s)
			}
		}
		for i, v := range p.Variants {
			if v.URILine <= v.Start || v.URILine >= len(p.Lines) {
				t.Fatalf("variant %d has bad line indexes %+v", i, v)
			}
		}
	})
}

func FuzzRewrite(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add([]byte(s), "https://h.example/dir/index.m3u8", true, uint8(0))
		f.Add([]byte(s), "", true, uint8(1))
		f.Add([]byte(s), "not a url", false, uint8(2))
	}
	f.Fuzz(func(t *testing.T, data []byte, base string, dropAds bool, mode uint8) {
		opts := RewriteOptions{
			BaseURL: base,
			DropAds: dropAds,
			MapURL:  func(abs string, k URIKind) string { return "p/" + k.String() + "/" + abs },
			SelectVariant: VariantChooser{
				Mode:      VariantMode(mode % 3),
				MaxHeight: int(mode) * 10,
				Output:    VariantOutput(mode / 3 % 2),
			},
		}
		res, err := RewriteDetailed(data, opts)
		if err != nil {
			return
		}
		if res.Body == nil {
			t.Fatal("nil body without error")
		}
		if opts.SelectVariant.Output == OutputReference && res.Selected != nil {
			return
		}
		// Whatever comes out must still be a playlist that parses and
		// round-trips, and rewriting must keep the line terminators uniform
		// for inputs that were uniform.
		p, err := Parse(res.Body)
		if err != nil {
			t.Fatalf("output does not parse: %v\n%q", err, res.Body)
		}
		if !bytes.Equal(p.Marshal(), res.Body) {
			t.Fatal("output does not round-trip")
		}
		if !bytes.Contains(data, []byte("\n")) || bytes.Contains(data, []byte("\r")) {
			return
		}
		if bytes.Contains(res.Body, []byte("\r")) {
			t.Fatalf("CR introduced into an LF-only playlist: %q", res.Body)
		}
	})
}

func FuzzResolve(f *testing.F) {
	for _, s := range [][2]string{
		{"https://h.example/a/b.m3u8", "../x?y#z"},
		{"https://h.example", "//h2/p"},
		{"", "x"},
		{"http://[::1]:80/a", ".."},
		{"a:b", "c"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, base, ref string) {
		got, err := Resolve(base, ref)
		if err != nil || splitURI(strings.TrimSpace(ref)).path == "" {
			return // a reference without a path copies the base path as is (RFC 3986 5.2.2)
		}
		again, err := Resolve(base, got)
		if err != nil {
			t.Fatalf("resolved URI %q cannot be resolved again: %v", got, err)
		}
		if again != got && strings.TrimSpace(got) == got {
			t.Fatalf("Resolve is not idempotent: %q then %q", got, again)
		}
	})
}

func FuzzStripSegmentPrefix(f *testing.F) {
	ts := buildTS(6)
	f.Add(ts)
	f.Add(append([]byte("junk"), ts...))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		out := StripSegmentPrefix(b)
		if len(out) > len(b) || !bytes.HasSuffix(b, out) {
			t.Fatal("result is not a suffix of the input")
		}
	})
}

func TestRewriteNeverPanicsOnTruncations(t *testing.T) {
	// Every prefix of every seed, including cuts inside quoted attributes.
	for _, s := range fuzzSeeds() {
		for n := 0; n <= len(s); n++ {
			for _, drop := range []bool{false, true} {
				_, _ = RewriteDetailed([]byte(s[:n]), RewriteOptions{BaseURL: "https://h.example/", DropAds: drop})
				_, _ = RewriteDetailed([]byte(s[:n]), RewriteOptions{BaseURL: "https://h.example/", DropAds: drop,
					SelectVariant: VariantChooser{Mode: VariantMaxHeight, MaxHeight: 480, Output: OutputReference}})
			}
		}
	}
}
