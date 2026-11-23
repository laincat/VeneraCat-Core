package gohost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/host"
)

func TestAuditRedirectWithCookiesDisabledDoesNotTouchJar(t *testing.T) {
	tr, jar := newTestTransport(t)
	seen := make(chan string, 1)
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen <- r.Header.Get("Cookie") }))
	defer final.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "hop", Value: "unexpected", Path: "/"})
		http.Redirect(w, r, final.URL+"/done", http.StatusFound)
	}))
	defer first.Close()
	jar.SetURL("alpha", mustURL(t, first.URL), []*http.Cookie{{Name: "sid", Value: "private", Path: "/"}})
	if _, err := tr.Do(context.Background(), &host.Request{URL: first.URL, Partition: "alpha", Cookies: false}); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "" {
		t.Errorf("redirect read disabled jar: %q", got)
	}
	if got := jar.Header("alpha", first.URL); strings.Contains(got, "hop=") {
		t.Errorf("redirect wrote disabled jar: %q", got)
	}
}

func TestAuditRedirectDoesNotForwardProviderCredentials(t *testing.T) {
	tr, _ := newTestTransport(t)
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Authorization", "X-Api-Key", "Proxy-Authorization"} {
			if r.Header.Get(name) != "" {
				t.Errorf("redirect forwarded %s at %s", name, r.URL.Path)
			}
		}
		if r.URL.Path != "/done" {
			http.Redirect(w, r, "/done", http.StatusFound)
		}
	}))
	defer final.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, final.URL, http.StatusFound) }))
	defer first.Close()
	_, err := tr.Do(context.Background(), &host.Request{URL: first.URL, Header: http.Header{"Authorization": {"Bearer private"}, "X-Api-Key": {"private"}, "Proxy-Authorization": {"Basic private"}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuditRedirectReplacesCookiesForTargetPath(t *testing.T) {
	tr, jar := newTestTransport(t)
	seen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/private/start" {
			http.Redirect(w, r, "/public/done", http.StatusFound)
			return
		}
		seen <- r.Header.Get("Cookie")
	}))
	defer srv.Close()
	jar.SetURL("alpha", mustURL(t, srv.URL+"/private/start"), []*http.Cookie{{Name: "secret", Value: "one", Path: "/private"}})
	if _, err := tr.Do(context.Background(), &host.Request{URL: srv.URL + "/private/start", Partition: "alpha", Cookies: true}); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "" {
		t.Errorf("private path cookie escaped to public path: %q", got)
	}
}
