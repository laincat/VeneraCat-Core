package gohost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/laincat/veneracat-core/host"
)

// A source that sets Host means "answer as this site", the way curl --resolve
// does. Go ignores a Host entry in the header map and sends the URL's host
// instead, so a client that only copies headers asks the wrong virtual host and
// gets that host's 403/HTML, which a source reports as a JSON parse error at
// position 0. comic_walker.js is one that does this.
func TestAHostHeaderReachesTheServer(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	tr := NewTransport(nil, "test-agent", 0)
	tr.client = server.Client()
	_, err := tr.Do(context.Background(), &host.Request{
		Method: http.MethodGet,
		URL:    server.URL,
		Header: http.Header{"Host": []string{"mobileapp.example.com"}},
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if got := <-seen; got != "mobileapp.example.com" {
		t.Fatalf("the server was asked for %q, not the Host the source named", got)
	}
}

// Without the header the address is the host, so the fix above does not change
// what every other source already sends.
func TestWithoutAHostHeaderTheAddressIsTheHost(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	tr := NewTransport(nil, "test-agent", 0)
	tr.client = server.Client()
	if _, err := tr.Do(context.Background(), &host.Request{Method: http.MethodGet, URL: server.URL}); err != nil {
		t.Fatalf("request: %v", err)
	}
	want := server.Listener.Addr().String()
	if got := <-seen; got != want {
		t.Fatalf("the server was asked for %q, expected %q", got, want)
	}
}
