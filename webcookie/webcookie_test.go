package webcookie

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestMatches(t *testing.T) {
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		cookie http.Cookie
		url    string
		want   bool
	}{
		{"host-only on its host", http.Cookie{Domain: "e-hentai.org", Path: "/"}, "https://e-hentai.org/home.php", true},
		{"host-only skips a subdomain", http.Cookie{Domain: "e-hentai.org", Path: "/"}, "https://forums.e-hentai.org/", false},
		{"domain cookie reaches a subdomain", http.Cookie{Domain: ".e-hentai.org", Path: "/"}, "https://forums.e-hentai.org/", true},
		{"domain cookie reaches the domain", http.Cookie{Domain: ".e-hentai.org", Path: "/"}, "https://e-hentai.org/", true},
		{"no suffix match without a dot boundary", http.Cookie{Domain: ".hentai.org", Path: "/"}, "https://e-hentai.org/", false},
		{"secure needs https", http.Cookie{Domain: "a.test", Path: "/", Secure: true}, "http://a.test/", false},
		{"expired is gone", http.Cookie{Domain: "a.test", Path: "/", Expires: now.Add(-time.Second)}, "https://a.test/", false},
		{"path prefix at a boundary", http.Cookie{Domain: "a.test", Path: "/api"}, "https://a.test/api/v1", true},
		{"path prefix inside a segment", http.Cookie{Domain: "a.test", Path: "/api"}, "https://a.test/apix", false},
	}
	for _, c := range cases {
		u, err := url.Parse(c.url)
		if err != nil {
			t.Fatal(err)
		}
		if got := matches(&c.cookie, u, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
