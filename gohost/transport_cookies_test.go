package gohost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/gohost/cookies"
	"github.com/laincat/veneracat-core/host"
)

// newTestTransport builds a transport with a jar that is not written to disk:
// partitions are the isolation boundary being tested, not persistence.
func newTestTransport(t *testing.T) (*Transport, *cookies.Jar) {
	t.Helper()
	jar, err := cookies.Open(filepath.Join(t.TempDir(), "cookies.json"))
	if err != nil {
		t.Fatalf("open jar: %v", err)
	}
	return NewTransport(jar, "nk-test/1", 0), jar
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestTransportSendsStoredCookiesAndCallersOwn(t *testing.T) {
	tr, jar := newTestTransport(t)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Cookie")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	jar.SetURL("alpha", mustURL(t, srv.URL+"/"), []*http.Cookie{{Name: "sid", Value: "jar"}})
	resp, err := tr.Do(context.Background(), &host.Request{
		URL: srv.URL + "/page", Cookies: true, Partition: "alpha",
		Header: map[string][]string{"Cookie": {"mine=1"}},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status %d", resp.Status)
	}
	if !strings.Contains(got, "mine=1") || !strings.Contains(got, "sid=jar") {
		t.Fatalf("Cookie header = %q; want the caller's and the jar's", got)
	}
}

// A redirect hop that sets a cookie must be stored, or a login that bounces
// through another host silently loses it.
func TestTransportKeepsCookiesSetOnRedirectHops(t *testing.T) {
	tr, jar := newTestTransport(t)
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	}))
	defer final.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "hop", Value: "1", Path: "/"})
		http.Redirect(w, r, final.URL+"/next", http.StatusFound)
	}))
	defer first.Close()

	resp, err := tr.Do(context.Background(), &host.Request{
		URL: first.URL + "/start", Cookies: true, Partition: "alpha",
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Status != http.StatusOK || string(resp.Body) != "done" {
		t.Fatalf("status %d body %q", resp.Status, resp.Body)
	}
	if resp.FinalURL != final.URL+"/next" {
		t.Fatalf("FinalURL = %q", resp.FinalURL)
	}
	if got := jar.Header("alpha", first.URL+"/anything"); !strings.Contains(got, "hop=1") {
		t.Fatalf("cookie from the redirect hop was not stored: %q", got)
	}
}

// With cookies off the jar is neither read nor written.
func TestTransportWithoutCookiesIgnoresTheJar(t *testing.T) {
	tr, jar := newTestTransport(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := r.Header.Get("Cookie"); c != "" {
			t.Errorf("Cookie header = %q; want none", c)
		}
		http.SetCookie(w, &http.Cookie{Name: "kept", Value: "no", Path: "/"})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	jar.SetURL("alpha", mustURL(t, srv.URL+"/"), []*http.Cookie{{Name: "sid", Value: "jar"}})
	if _, err := tr.Do(context.Background(), &host.Request{
		URL: srv.URL + "/page", Cookies: false, Partition: "alpha",
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got := jar.Header("alpha", srv.URL+"/page"); strings.Contains(got, "kept=no") {
		t.Fatalf("a response cookie was stored although cookies were off: %q", got)
	}
}

// Partitions do not share cookies: two sources on one host stay separate.
func TestTransportKeepsPartitionsApart(t *testing.T) {
	tr, jar := newTestTransport(t)
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Cookie"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	jar.SetURL("alpha", mustURL(t, srv.URL+"/"), []*http.Cookie{{Name: "who", Value: "alpha"}})
	for _, p := range []string{"alpha", "beta"} {
		if _, err := tr.Do(context.Background(), &host.Request{
			URL: srv.URL + "/page", Cookies: true, Partition: p,
		}); err != nil {
			t.Fatalf("Do(%s): %v", p, err)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("saw %d requests", len(seen))
	}
	if !strings.Contains(seen[0], "who=alpha") {
		t.Fatalf("alpha sent %q", seen[0])
	}
	if strings.Contains(seen[1], "who=") {
		t.Fatalf("beta sent alpha's cookie: %q", seen[1])
	}
}

// Only http and https reach the network: a source cannot ask Core to open a file
// or another scheme.
func TestTransportRefusesOtherSchemes(t *testing.T) {
	tr, _ := newTestTransport(t)
	if _, err := tr.Do(context.Background(), &host.Request{URL: "file:///etc/passwd"}); err == nil {
		t.Fatal("a file:// request was attempted")
	}
}
