package gohost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Fetch downloads indexes and source scripts: no cookies, a size limit, and a
// content type trimmed of its parameters. These are the rules a subscription
// index depends on, and they were untested.
func TestFetchTrimsTheContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"comics":[]}`))
	}))
	defer srv.Close()

	tr, _ := newTestTransport(t)
	body, ctype, err := tr.Fetch(context.Background(), srv.URL+"/index.json", 1<<20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ctype != "application/json" {
		t.Fatalf("content type = %q; want the parameters trimmed", ctype)
	}
	if !strings.Contains(string(body), `"comics"`) {
		t.Fatalf("body = %q", body)
	}
}

// Fetch carries no cookies: a subscription index is not a source request and
// must not inherit a partition's session.
func TestFetchSendsNoCookies(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Cookie")
	}))
	defer srv.Close()

	tr, jar := newTestTransport(t)
	jar.SetURL("alpha", mustURL(t, srv.URL+"/"), []*http.Cookie{{Name: "sid", Value: "jar"}})
	if _, _, err := tr.Fetch(context.Background(), srv.URL+"/index.json", 1<<20); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got != "" {
		t.Fatalf("Cookie header = %q; want none", got)
	}
}

// A status that is not 2xx is an error with the status in it, not a body that
// happens to parse as HTML.
func TestFetchRefusesANonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not here"))
	}))
	defer srv.Close()

	tr, _ := newTestTransport(t)
	_, _, err := tr.Fetch(context.Background(), srv.URL+"/missing.js", 1<<20)
	if err == nil {
		t.Fatal("a 404 was accepted")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error %q does not name the status", err)
	}
}

// The limit applies to Fetch as well, or a large index exhausts memory.
func TestFetchRefusesABodyOverTheLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer srv.Close()

	tr, _ := newTestTransport(t)
	if _, _, err := tr.Fetch(context.Background(), srv.URL+"/big.json", 1024); err != ErrTooLarge {
		t.Fatalf("err = %v; want ErrTooLarge", err)
	}
}
