package cookies

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

func testJar(t *testing.T) *Jar {
	t.Helper()
	j, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	j.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return j
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestPartitionsAreSeparate(t *testing.T) {
	j := testJar(t)
	page := mustURL(t, "https://www.u.test/a")
	j.SetURL("comic:a", page, []*http.Cookie{{Name: "sid", Value: "1"}})
	if got := j.Header("comic:a", page.String()); got != "sid=1" {
		t.Errorf("a = %q", got)
	}
	if got := j.Header("comic:b", page.String()); got != "" {
		t.Errorf("b saw %q", got)
	}
}

func TestHostOnlyAndDomainCookies(t *testing.T) {
	j := testJar(t)
	page := mustURL(t, "https://www.u.test/a")
	j.SetURL("p", page, []*http.Cookie{
		{Name: "host", Value: "1"},
		{Name: "dom", Value: "2", Domain: ".u.test", Path: "/"},
	})
	if got := j.Header("p", "https://img.u.test/x"); got != "dom=2" {
		t.Errorf("subdomain = %q, want dom=2", got)
	}
	if got := j.Header("p", "https://www.u.test/x"); got != "host=1; dom=2" && got != "dom=2; host=1" {
		t.Errorf("same host = %q", got)
	}
	// A page may not set cookies for an unrelated domain.
	j.SetURL("p", page, []*http.Cookie{{Name: "evil", Value: "1", Domain: ".other.test"}})
	if got := j.Header("p", "https://other.test/"); got != "" {
		t.Errorf("cross-domain cookie stored: %q", got)
	}
}

func TestPathSecureAndExpiry(t *testing.T) {
	j := testJar(t)
	page := mustURL(t, "https://www.u.test/a/b")
	j.SetURL("p", page, []*http.Cookie{
		{Name: "deep", Value: "1", Path: "/a"},
		{Name: "sec", Value: "1", Path: "/", Secure: true},
		{Name: "old", Value: "1", Path: "/", Expires: time.Unix(1_700_000_000, 0)},
	})
	if got := j.Header("p", "https://www.u.test/a/c"); got != "deep=1; sec=1" && got != "sec=1; deep=1" {
		t.Errorf("within path = %q", got)
	}
	if got := j.Header("p", "http://www.u.test/a"); got != "deep=1" {
		t.Errorf("http = %q", got)
	}
	if got := j.Header("p", "https://www.u.test/ab"); got != "sec=1" {
		t.Errorf("path prefix = %q", got)
	}
	if got := j.Header("p", "https://www.u.test/a/b"); got == "" || containsCookie(got, "old") {
		t.Errorf("expired cookie kept: %q", got)
	}
}

func containsCookie(header, name string) bool {
	for _, part := range splitHeader(header) {
		if len(part) > len(name) && part[:len(name)+1] == name+"=" {
			return true
		}
	}
	return false
}

func splitHeader(h string) []string {
	var out []string
	start := 0
	for i := 0; i < len(h); i++ {
		if h[i] == ';' {
			out = append(out, trimSpace(h[start:i]))
			start = i + 1
		}
	}
	return append(out, trimSpace(h[start:]))
}

func trimSpace(s string) string {
	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}

func TestDefaultPathIsTheDirectory(t *testing.T) {
	j := testJar(t)
	page := mustURL(t, "https://www.u.test/a/b")
	j.SetURL("p", page, []*http.Cookie{{Name: "d", Value: "1"}})
	got := j.ForURL("p", page)
	if len(got) != 1 || got[0].Path != "/a" {
		t.Fatalf("path = %+v", got)
	}
}

func TestDeleteAndClear(t *testing.T) {
	j := testJar(t)
	page := mustURL(t, "https://www.u.test/a")
	j.SetURL("p", page, []*http.Cookie{{Name: "a", Value: "1"}, {Name: "b", Value: "2"}})
	j.DeleteURL("p", page)
	if got := j.Header("p", page.String()); got != "" {
		t.Fatalf("delete left %q", got)
	}
	j.SetURL("p", page, []*http.Cookie{{Name: "a", Value: "1"}})
	j.Clear("p")
	if got := j.Header("p", page.String()); got != "" {
		t.Fatalf("clear left %q", got)
	}
}

func TestSessionCookiesAreNotSaved(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cookies.json")
	j, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	page := mustURL(t, "https://www.u.test/")
	j.SetURL("p", page, []*http.Cookie{
		{Name: "session", Value: "1", Path: "/"},
		{Name: "keep", Value: "1", Path: "/", MaxAge: 3600},
	})
	again, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	names := again.ForURL("p", page)
	if len(names) != 1 || names[0].Name != "keep" {
		t.Fatalf("reloaded = %+v", names)
	}
}

// hostapi.CookieStore takes the URL as text.
func TestCookieStoreInterface(t *testing.T) {
	j := testJar(t)
	page := "https://www.u.test/x"
	j.Set("p", page, []*http.Cookie{{Name: "a", Value: "1", Domain: ".u.test", Path: "/"}})
	got := j.Get("p", page)
	if len(got) != 1 || got[0].Name != "a" || got[0].Domain != ".u.test" {
		t.Fatalf("get = %+v", got)
	}
	j.Delete("p", page)
	if len(j.Get("p", page)) != 0 {
		t.Fatal("delete left a cookie")
	}
}
