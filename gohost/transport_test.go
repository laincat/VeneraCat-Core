package gohost

import (
	"net/url"
	"testing"
)

// Sources paste text into URLs; the request line cannot carry a space, which
// Dart escapes and Go would send raw.
func TestEscapeInvalid(t *testing.T) {
	cases := map[string]string{
		"https://api.test/manga?title=one piece&limit=20": "/manga?title=one%20piece&limit=20",
		"https://api.test/a b?q=1":                        "/a%20b?q=1",
		"https://api.test/x?q=海贼":                         "/x?q=%E6%B5%B7%E8%B4%BC",
		// An already escaped URL is left alone, not escaped twice.
		"https://api.test/x?q=%E6%B5%B7%E8%B4%BC": "/x?q=%E6%B5%B7%E8%B4%BC",
		// The characters that structure a query stay as they are.
		"https://api.test/x?a[]=1&b=2": "/x?a[]=1&b=2",
	}
	for in, want := range cases {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		escapeInvalid(u)
		if got := u.RequestURI(); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}
