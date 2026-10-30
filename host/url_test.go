package host

import (
	"strings"
	"testing"
)

// A source may return an image address without its scheme. The scheme comes
// from the base, so the result is an ordinary absolute URL.
func TestNormalizeURLFillsInASchemeRelativeURL(t *testing.T) {
	cases := []struct{ raw, base, want string }{
		{"//images.example.com/1.jpg", "https://api.example.com/x", "https://images.example.com/1.jpg"},
		{"//cdn.example.com/a", "http://api.example.com", "http://cdn.example.com/a"},
		// An absolute URL is left alone.
		{"https://images.example.com/1.jpg", "https://api.example.com", "https://images.example.com/1.jpg"},
		{"http://images.example.com/1.jpg", "https://api.example.com", "http://images.example.com/1.jpg"},
		// A path is not scheme-relative, so it is left alone.
		{"/1.jpg", "https://api.example.com", "/1.jpg"},
		{"1.jpg", "https://api.example.com", "1.jpg"},
		// Without a usable base the scheme is https: every site that serves
		// images this way does.
		{"//images.example.com/1.jpg", "", "https://images.example.com/1.jpg"},
		{"//images.example.com/1.jpg", "not a url at all", "https://images.example.com/1.jpg"},
		// A base that is not http(s) does not decide the scheme.
		{"//images.example.com/1.jpg", "file:///tmp/x", "https://images.example.com/1.jpg"},
		{"//images.example.com/1.jpg", "javascript:alert(1)", "https://images.example.com/1.jpg"},
		// An empty URL stays empty.
		{"", "https://api.example.com", ""},
	}
	for _, c := range cases {
		if got := NormalizeURL(c.raw, c.base); got != c.want {
			t.Errorf("NormalizeURL(%q, %q) = %q, want %q", c.raw, c.base, got, c.want)
		}
	}
}

// The filled-in URL still has to pass the rules: a source cannot smuggle a
// file or a script through by leaving the scheme out.
func TestNormalizeURLDoesNotWeakenValidateURL(t *testing.T) {
	filled := NormalizeURL("//images.example.com/1.jpg", "https://api.example.com")
	if err := ValidateURL(filled); err != nil {
		t.Fatalf("the filled URL was refused: %v", err)
	}
	// Only http and https are ever produced, whatever the base says.
	for _, base := range []string{"file:///tmp/x", "javascript:alert(1)", "ftp://x.test/y"} {
		got := NormalizeURL("//images.example.com/1.jpg", base)
		if err := ValidateURL(got); err != nil {
			t.Errorf("NormalizeURL with base %q = %q, which ValidateURL refuses: %v", base, got, err)
		}
	}
	// A URL that is not scheme-relative is never rewritten, so a source cannot
	// smuggle a file or a script through the other way.
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "ftp://x.test/y", "/1.jpg"} {
		if got := NormalizeURL(raw, "https://api.example.com"); got != raw {
			t.Errorf("NormalizeURL(%q) = %q", raw, got)
		}
	}
}

// The scheme comes from the base, whatever case it was written in.
func TestNormalizeURLKeepsTheBaseScheme(t *testing.T) {
	if got := NormalizeURL("//x.test/a", "HTTPS://api.test"); !strings.HasPrefix(strings.ToLower(got), "https:") {
		t.Fatalf("got %q", got)
	}
}
