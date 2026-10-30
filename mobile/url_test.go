package mobile

import "testing"

// The client asks Core to fill in a missing scheme, so both clients apply the
// same rule and neither has to keep its own copy.
func TestNormalizeURLFillsInTheScheme(t *testing.T) {
	c := newCore(t, &fakeHost{routes: map[string]string{}, data: map[string]string{}})
	cases := []struct{ raw, base, want string }{
		{"//images.example.com/1.jpg", "", "https://images.example.com/1.jpg"},
		{"//images.example.com/1.jpg", "https://api.example.com", "https://images.example.com/1.jpg"},
		{"//images.example.com/1.jpg", "http://api.example.com", "http://images.example.com/1.jpg"},
		{"https://images.example.com/1.jpg", "", "https://images.example.com/1.jpg"},
		{"/1.jpg", "", "/1.jpg"},
		{"", "", ""},
	}
	for _, c2 := range cases {
		if got := c.NormalizeURL(c2.raw, c2.base); got != c2.want {
			t.Errorf("NormalizeURL(%q, %q) = %q, want %q", c2.raw, c2.base, got, c2.want)
		}
	}
}

// A file or a script address is not rewritten, so nothing gets through by
// leaving the scheme out.
func TestNormalizeURLDoesNotRewriteUnsafeSchemes(t *testing.T) {
	c := newCore(t, &fakeHost{routes: map[string]string{}, data: map[string]string{}})
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "ftp://x.test/y"} {
		if got := c.NormalizeURL(raw, "https://api.example.com"); got != raw {
			t.Errorf("NormalizeURL(%q) = %q", raw, got)
		}
	}
}
