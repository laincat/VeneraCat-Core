package video

import (
	"strings"
	"testing"
)

// Play.Validate is the gate every provider's answer goes through: it keeps a
// plugin from pointing the player at a local file or injecting header lines. It
// had no tests of its own.
func TestPlayValidate(t *testing.T) {
	cases := []struct {
		name string
		play *Play
		ok   bool
		want string
	}{
		{"a plain http url", &Play{URL: "https://cdn.test/a.m3u8"}, true, ""},
		{"neither url nor sniff", &Play{}, false, "neither url nor sniff"},
		{"both url and sniff", &Play{URL: "https://cdn.test/a.m3u8", Sniff: &Sniff{Page: "https://a.test/p"}}, false, "both url and sniff"},
		{"sniff without a page", &Play{Sniff: &Sniff{Match: `\.mp4`}}, false, "sniff page is empty"},
		{"a file url", &Play{URL: "file:///etc/passwd"}, false, "url"},
		{"a javascript url", &Play{URL: "javascript:alert(1)"}, false, "url"},
		{"a variant that is not http", &Play{URL: "https://cdn.test/a.m3u8", Variants: []Variant{{URL: "data:text/plain,x"}}}, false, "variant 0"},
		{"a sniff page that is not http", &Play{Sniff: &Sniff{Page: "file:///tmp/x"}}, false, "sniff page"},
		{"an invalid sniff match", &Play{Sniff: &Sniff{Page: "https://a.test/p", Match: "("}}, false, "sniff match"},
		{"a header with a newline", &Play{URL: "https://cdn.test/a.m3u8", Header: map[string][]string{"Referer": {"https://a.test/\r\nX-Injected: 1"}}}, false, "header"},
		{"a good header", &Play{URL: "https://cdn.test/a.m3u8", Header: map[string][]string{"Referer": {"https://a.test/"}}}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.play.Validate()
			if c.ok {
				if err != nil {
					t.Fatalf("Validate() = %v; want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() accepted it")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate() = %q; want it to mention %q", err, c.want)
			}
		})
	}
}
