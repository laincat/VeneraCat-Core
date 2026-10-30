package hls

import (
	"strings"
	"testing"
)

// proxy maps every URI to a local proxy address and records what it saw.
type proxy struct {
	seen map[string]URIKind
	n    int
}

func newProxy() *proxy { return &proxy{seen: map[string]URIKind{}} }

func (px *proxy) fn(abs string, kind URIKind) string {
	px.seen[abs] = kind
	px.n++
	return "http://127.0.0.1:9000/p/" + kind.String() + "?u=" + abs
}

func mustRewrite(t *testing.T, in string, opts RewriteOptions) RewriteResult {
	t.Helper()
	res, err := RewriteDetailed([]byte(in), opts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRewriteMapURLEveryKind(t *testing.T) {
	const media = `#EXTM3U
#EXT-X-TARGETDURATION:10
#EXT-X-KEY:METHOD=AES-128,URI="../keys/k.bin?id=1",IV=0x01
#EXT-X-MAP:URI="init.mp4"
#EXTINF:5,
s1.m4s?sig=a=b
#EXTINF:5,
//other.example.net/s2.m4s
#EXT-X-ENDLIST
`
	const master = `#EXTM3U
#EXT-X-SESSION-KEY:METHOD=AES-128,URI="sk.bin"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="en",URI="a/en.m3u8"
#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",NAME="cc1",INSTREAM-ID="CC1"
#EXT-X-STREAM-INF:BANDWIDTH=1000,AUDIO="a"
v/index.m3u8
#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=100,URI="v/iframe.m3u8"
`
	tests := []struct {
		name string
		in   string
		eol  string
		want map[string]URIKind
		out  string
	}{
		{
			name: "media", in: media, eol: "\n",
			want: map[string]URIKind{
				"https://cdn.example.com/a/keys/k.bin?id=1":  KindKey,
				"https://cdn.example.com/a/b/init.mp4":       KindMap,
				"https://cdn.example.com/a/b/s1.m4s?sig=a=b": KindSegment,
				"https://other.example.net/s2.m4s":           KindSegment,
			},
			out: `#EXTM3U
#EXT-X-TARGETDURATION:10
#EXT-X-KEY:METHOD=AES-128,URI="http://127.0.0.1:9000/p/key?u=https://cdn.example.com/a/keys/k.bin?id=1",IV=0x01
#EXT-X-MAP:URI="http://127.0.0.1:9000/p/map?u=https://cdn.example.com/a/b/init.mp4"
#EXTINF:5,
http://127.0.0.1:9000/p/segment?u=https://cdn.example.com/a/b/s1.m4s?sig=a=b
#EXTINF:5,
http://127.0.0.1:9000/p/segment?u=https://other.example.net/s2.m4s
#EXT-X-ENDLIST
`,
		},
		{
			name: "master keeps all variants", in: master, eol: "\n",
			want: map[string]URIKind{
				"https://cdn.example.com/a/b/sk.bin":        KindKey,
				"https://cdn.example.com/a/b/a/en.m3u8":     KindMedia,
				"https://cdn.example.com/a/b/v/index.m3u8":  KindVariant,
				"https://cdn.example.com/a/b/v/iframe.m3u8": KindIFrame,
			},
		},
	}
	for _, tc := range tests {
		for _, crlfMode := range []bool{false, true} {
			name := tc.name
			in := tc.in
			if crlfMode {
				name += " CRLF"
				in = crlf(in)
			}
			t.Run(name, func(t *testing.T) {
				px := newProxy()
				res := mustRewrite(t, in, RewriteOptions{
					BaseURL: "https://cdn.example.com/a/b/index.m3u8?t=1", MapURL: px.fn,
					SelectVariant: VariantChooser{Mode: VariantAll},
				})
				if len(px.seen) != len(tc.want) {
					t.Errorf("MapURL saw %v", px.seen)
				}
				for u, k := range tc.want {
					if got, ok := px.seen[u]; !ok || got != k {
						t.Errorf("MapURL(%q) kind = %v (seen=%v), want %v", u, got, ok, k)
					}
				}
				if tc.out != "" {
					want := tc.out
					if crlfMode {
						want = crlf(want)
					}
					if string(res.Body) != want {
						t.Errorf("body:\n%s\nwant:\n%s", res.Body, want)
					}
				}
				// Every non-comment line of the output must go through the proxy.
				if tc.out == "" && !strings.Contains(string(res.Body), "p/iframe?u=") {
					t.Errorf("iframe URI not rewritten:\n%s", res.Body)
				}
			})
		}
	}
}

func TestRewriteWithoutMapURLWritesAbsolute(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k.bin\"\n#EXTINF:1,\nseg.ts\n"
	res := mustRewrite(t, in, RewriteOptions{BaseURL: "https://h.example/d/i.m3u8"})
	want := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://h.example/d/k.bin\"\n#EXTINF:1,\nhttps://h.example/d/seg.ts\n"
	if string(res.Body) != want {
		t.Errorf("got:\n%s", res.Body)
	}
}

func TestRewriteIdentityKeepsBytes(t *testing.T) {
	// Absolute URIs and an identity MapURL leave the playlist byte-identical.
	in := "#EXTM3U\r\n\r\n# note\r\n#EXT-X-UNKNOWN:1\r\n#EXTINF:1,\r\nhttps://h.example/a.ts\r\n#EXTINF:1,\r\nhttps://h.example/b.ts"
	res := mustRewrite(t, in, RewriteOptions{MapURL: func(abs string, _ URIKind) string { return abs }})
	if string(res.Body) != in {
		t.Errorf("got %q", res.Body)
	}
}

func TestRewriteErrors(t *testing.T) {
	if _, err := Rewrite([]byte("not a playlist"), RewriteOptions{}); err == nil {
		t.Error("expected an error for non-playlist data")
	}
	if _, err := Rewrite([]byte("#EXTM3U\n#EXTINF:1,\nrel.ts\n"), RewriteOptions{}); err == nil {
		t.Error("expected an error for a relative URI without BaseURL")
	}
	_, err := Rewrite([]byte("#EXTM3U\n#EXTINF:1,\na.ts\n"), RewriteOptions{
		BaseURL: "https://h.example/", MapURL: func(string, URIKind) string { return "" },
	})
	if err == nil {
		t.Error("expected an error when MapURL returns an empty string")
	}
}

func TestRewriteDataURIAndEmptyURI(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"data:text/plain;base64,AAAAAAAAAAAAAAAAAAAAAA==\"\n#EXT-X-MAP:URI=\"\"\n#EXTINF:1,\na.ts\n"
	px := newProxy()
	res := mustRewrite(t, in, RewriteOptions{BaseURL: "https://h.example/", MapURL: px.fn})
	if len(px.seen) != 1 {
		t.Errorf("MapURL saw %v, want only the segment", px.seen)
	}
	if !strings.Contains(string(res.Body), `URI="data:text/plain;base64,AAAAAAAAAAAAAAAAAAAAAA=="`) {
		t.Errorf("data URI changed:\n%s", res.Body)
	}
}

func TestRewriteEscapesMapURLResult(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXTINF:1,\na.ts\n"
	res := mustRewrite(t, in, RewriteOptions{
		BaseURL: "https://h.example/",
		MapURL:  func(abs string, _ URIKind) string { return "x\"\ny" + abs },
	})
	for _, l := range strings.Split(strings.TrimSuffix(string(res.Body), "\n"), "\n") {
		if strings.HasPrefix(l, "y") {
			t.Errorf("newline injected into the playlist:\n%s", res.Body)
		}
	}
}

func TestVariantSelection(t *testing.T) {
	const m = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a1",NAME="en",URI="a1.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a2",NAME="fr",URI="a2.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,AUDIO="a1"
v360.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1280x720,AUDIO="a2"
v720.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1920x1080,AUDIO="a1"
v1080.m3u8
#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=50000,URI="if.m3u8"
`
	const base = "https://h.example/m/master.m3u8"
	tests := []struct {
		name    string
		chooser VariantChooser
		wantURL string
		warn    bool
	}{
		{"default is highest bandwidth", VariantChooser{}, "https://h.example/m/v720.m3u8", false},
		{"max height 720", VariantChooser{Mode: VariantMaxHeight, MaxHeight: 720}, "https://h.example/m/v720.m3u8", false},
		{"max height 1000 picks 720", VariantChooser{Mode: VariantMaxHeight, MaxHeight: 1000}, "https://h.example/m/v720.m3u8", false},
		{"max height 400", VariantChooser{Mode: VariantMaxHeight, MaxHeight: 400}, "https://h.example/m/v360.m3u8", false},
		{"max height 100 falls back to shortest", VariantChooser{Mode: VariantMaxHeight, MaxHeight: 100}, "https://h.example/m/v360.m3u8", true},
		{"no cap picks tallest", VariantChooser{Mode: VariantMaxHeight}, "https://h.example/m/v1080.m3u8", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := tc.chooser
			ch.Output = OutputReference
			ref := mustRewrite(t, m, RewriteOptions{BaseURL: base, SelectVariant: ch})
			if ref.SelectedURL != tc.wantURL || string(ref.Body) != tc.wantURL {
				t.Errorf("reference = %q / %q, want %q", ref.Body, ref.SelectedURL, tc.wantURL)
			}
			if (len(ref.Warnings) > 0) != tc.warn {
				t.Errorf("warnings = %v", ref.Warnings)
			}

			ch.Output = OutputMaster
			px := newProxy()
			full := mustRewrite(t, m, RewriteOptions{BaseURL: base, SelectVariant: ch, MapURL: px.fn})
			if n := strings.Count(string(full.Body), "#EXT-X-STREAM-INF"); n != 1 {
				t.Errorf("%d variants left:\n%s", n, full.Body)
			}
			if strings.Contains(string(full.Body), "I-FRAME") {
				t.Errorf("I-frame stream kept:\n%s", full.Body)
			}
			if px.seen[tc.wantURL] != KindVariant {
				t.Errorf("chosen variant not mapped: %v", px.seen)
			}
			if len(px.seen) != 2 { // variant plus its audio rendition
				t.Errorf("MapURL saw %v", px.seen)
			}
		})
	}

	t.Run("reference goes through MapURL", func(t *testing.T) {
		px := newProxy()
		res := mustRewrite(t, m, RewriteOptions{BaseURL: base, MapURL: px.fn,
			SelectVariant: VariantChooser{Output: OutputReference}})
		if string(res.Body) != "http://127.0.0.1:9000/p/variant?u=https://h.example/m/v720.m3u8" {
			t.Errorf("got %q", res.Body)
		}
	})

	t.Run("renditions of unchosen groups are dropped", func(t *testing.T) {
		res := mustRewrite(t, m, RewriteOptions{BaseURL: base})
		if strings.Contains(string(res.Body), `GROUP-ID="a1"`) || !strings.Contains(string(res.Body), `GROUP-ID="a2"`) {
			t.Errorf("wrong renditions kept:\n%s", res.Body)
		}
	})

	t.Run("VariantAll keeps everything", func(t *testing.T) {
		res := mustRewrite(t, m, RewriteOptions{BaseURL: base, SelectVariant: VariantChooser{Mode: VariantAll}})
		if strings.Count(string(res.Body), "#EXT-X-STREAM-INF") != 3 || res.Selected != nil {
			t.Errorf("variants dropped:\n%s", res.Body)
		}
	})

	t.Run("no RESOLUTION falls back to bandwidth", func(t *testing.T) {
		in := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\na.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=9\nb.m3u8\n"
		res := mustRewrite(t, in, RewriteOptions{BaseURL: base,
			SelectVariant: VariantChooser{Mode: VariantMaxHeight, MaxHeight: 480, Output: OutputReference}})
		if string(res.Body) != "https://h.example/m/b.m3u8" || len(res.Warnings) == 0 {
			t.Errorf("got %q, warnings %v", res.Body, res.Warnings)
		}
	})

	t.Run("tie keeps the first", func(t *testing.T) {
		in := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=5\na.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=5\nb.m3u8\n"
		res := mustRewrite(t, in, RewriteOptions{BaseURL: base, SelectVariant: VariantChooser{Output: OutputReference}})
		if string(res.Body) != "https://h.example/m/a.m3u8" {
			t.Errorf("got %q", res.Body)
		}
	})

	t.Run("media playlist is untouched by selection", func(t *testing.T) {
		in := "#EXTM3U\n#EXTINF:1,\nhttps://h.example/a.ts\n"
		res := mustRewrite(t, in, RewriteOptions{SelectVariant: VariantChooser{Output: OutputReference}})
		if string(res.Body) != in || res.Selected != nil {
			t.Errorf("got %q", res.Body)
		}
	})

	t.Run("CRLF master stays CRLF", func(t *testing.T) {
		res := mustRewrite(t, crlf(m), RewriteOptions{BaseURL: base})
		if strings.Contains(strings.ReplaceAll(string(res.Body), "\r\n", ""), "\n") {
			t.Errorf("bare LF in output: %q", res.Body)
		}
	})
}

func TestStripPrefixIsEchoed(t *testing.T) {
	res := mustRewrite(t, "#EXTM3U\n#EXTINF:1,\nhttps://h.example/a.ts\n", RewriteOptions{StripPrefix: true})
	if !res.StripPrefix {
		t.Error("StripPrefix hint not echoed")
	}
}
