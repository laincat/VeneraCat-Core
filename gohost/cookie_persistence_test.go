package gohost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/gohost/cookies"
	"github.com/laincat/veneracat-core/host"
)

func TestHTTPWritesOnlyChangedPersistentCookies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies.json")
	jar, err := cookies.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session":
			http.SetCookie(w, &http.Cookie{Name: "temporary", Value: "only-in-memory", Path: "/"})
		case "/save":
			http.SetCookie(w, &http.Cookie{Name: "remember", Value: "saved", Path: "/", Expires: expiry})
		case "/delete":
			http.SetCookie(w, &http.Cookie{Name: "remember", Path: "/", MaxAge: -1})
		}
	}))
	defer server.Close()
	part := ComicPartition("one")
	transport := NewTransport(jar, "", 0)
	get := func(route string) {
		t.Helper()
		if _, err := transport.Do(context.Background(), &host.Request{URL: server.URL + route, Cookies: true, Partition: part}); err != nil {
			t.Fatal(err)
		}
	}
	get("/empty")
	get("/session")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty or session response wrote to disk: %v", err)
	}
	get("/save")
	marker := time.Unix(1_600_000_000, 0)
	if err := os.Chtimes(path, marker, marker); err != nil {
		t.Fatal(err)
	}
	get("/empty")
	get("/save")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(marker) {
		t.Fatalf("unchanged response rewrote cookies: %v", info.ModTime())
	}
	reopen := func(want string) {
		t.Helper()
		loaded, err := cookies.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := loaded.Header(part, server.URL); got != want {
			t.Fatalf("reopened cookie = %q, want %q", got, want)
		}
	}
	reopen("remember=saved")
	get("/delete")
	reopen("")
}
