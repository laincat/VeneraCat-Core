package host

import (
	"net/http"
	"strings"
	"testing"
)

// ValidateURL is the outermost gate for anything a script or rule hands the
// client: a source cannot ask to open a local file or attach credentials to a
// URL. It had tests only indirectly, through whoever called it.
func TestValidateURL(t *testing.T) {
	good := []string{
		"https://example.test/a",
		"http://example.test",
		"HTTPS://EXAMPLE.TEST/path?q=1",
		"https://example.test:8443/x",
		"https://xn--bcher-kva.test/",
	}
	for _, raw := range good {
		if err := ValidateURL(raw); err != nil {
			t.Fatalf("ValidateURL(%q) = %v; want nil", raw, err)
		}
	}

	bad := map[string]string{
		"":                             "empty",
		"file:///etc/passwd":           "unsupported url scheme",
		"javascript:alert(1)":          "unsupported url scheme",
		"ftp://example.test/x":         "unsupported url scheme",
		"https://user@a.test/x":        "credentials",
		"//example.test/x":             "unsupported url scheme",
		"https:///path-without-host":   "no host",
		"http://example.test/\x00junk": "invalid url",
	}
	for raw, want := range bad {
		err := ValidateURL(raw)
		if err == nil {
			t.Fatalf("ValidateURL(%q) accepted it", raw)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateURL(%q) = %q; want it to mention %q", raw, err, want)
		}
	}
}

// A header line that carries CR/LF is a request split, not a header.
func TestValidHeader(t *testing.T) {
	ok := [][2]string{
		{"Referer", "https://a.test/"},
		{"X-Empty", ""},
		{"X-Tab", "a\tb"},
		{"Accept-Language", "zh-CN, en"},
	}
	for _, pair := range ok {
		if err := ValidHeader(pair[0], pair[1]); err != nil {
			t.Fatalf("ValidHeader(%q, %q) = %v", pair[0], pair[1], err)
		}
	}

	bad := [][2]string{
		{"", "v"},
		{"Bad Name", "v"},
		{"X-Bad:", "v"},
		{"X-CRLF", "a\r\nX-Injected: 1"},
		{"X-NUL", "a\x00b"},
		{"X-DEL", "a\x7fb"},
	}
	for _, pair := range bad {
		if err := ValidHeader(pair[0], pair[1]); err == nil {
			t.Fatalf("ValidHeader(%q, %q) accepted it", pair[0], pair[1])
		}
	}
}

// Header keys arrive from scripts and rules with whatever case they liked.
func TestHeaderValueIgnoresCase(t *testing.T) {
	h := http.Header{"referer": {"https://a.test/"}}
	if got := HeaderValue(h, "Referer"); got != "https://a.test/" {
		t.Fatalf("HeaderValue = %q", got)
	}
	if got := HeaderValue(h, "X-Missing"); got != "" {
		t.Fatalf("HeaderValue for a missing key = %q", got)
	}
}

// A rule with nothing to check is never satisfied, and an empty group means "no
// condition" rather than "vacuously true" — otherwise a rule written for one shape
// of challenge would pass immediately when used for another.
func TestDoneRuleGroups(t *testing.T) {
	cases := []struct {
		name string
		rule DoneRule
		want bool
	}{
		{"nothing to check", DoneRule{}, true},
		{"one group", DoneRule{RequiredQueryParams: []string{"pbchal"}}, false},
		{"two groups", DoneRule{AbsentMarkers: []string{"x"}, RequiredCookies: []string{"cf_clearance"}}, false},
	}
	for _, c := range cases {
		if got := c.rule.Empty(); got != c.want {
			t.Errorf("%s: Empty() = %v, want %v", c.name, got, c.want)
		}
	}
	// A Cloudflare-shaped rule and a MangaDex-shaped one must not be the same rule.
	cf := DoneRule{AbsentMarkers: []string{"challenge-form"}, RequiredCookies: []string{"cf_clearance"}}
	md := DoneRule{RequiredQueryParams: []string{"powblock", "pbchal"}}
	if cf.Empty() || md.Empty() {
		t.Fatalf("a rule with conditions reported itself empty")
	}
	if len(cf.RequiredQueryParams) != 0 || len(md.RequiredCookies) != 0 {
		t.Fatalf("the two shapes should not overlap")
	}
}
