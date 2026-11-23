package gohost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/host"
)

// A signed image address keeps the spelling the server signed. Re-escaping the
// decoded path turns "%2F" back into "/" and the server rejects the signature
// with "check sign failed".
func TestTransportKeepsASignedPathUnchanged(t *testing.T) {
	tr, _ := newTestTransport(t)
	signed := "https://images.example.test/z/%E8%BF%99%E4%B8%AA%E7%8F%AD/%E8%BF%9E%E8%BD%BD01%E8%AF%9D%2F00.jpg?sign=c535239dfe92a9df75469470f13674b1&t=1793833940"
	req, err := tr.request(context.Background(), &host.Request{Method: "GET", URL: signed})
	if err != nil {
		t.Fatal(err)
	}
	want := "/z/%E8%BF%99%E4%B8%AA%E7%8F%AD/%E8%BF%9E%E8%BD%BD01%E8%AF%9D%2F00.jpg?sign=c535239dfe92a9df75469470f13674b1&t=1793833940"
	if got := req.URL.RequestURI(); got != want {
		t.Fatalf("request uri\n got %s\nwant %s", got, want)
	}
}

// The server has to see the very same bytes the source handed over.
func TestTransportSendsTheSignedPathOnTheWire(t *testing.T) {
	tr, _ := newTestTransport(t)
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.EscapedPath()
		w.WriteHeader(200)
	}))
	defer srv.Close()
	path := "/z/%E8%BF%99%E4%B8%AA%E7%8F%AD/%E8%BF%9E%E8%BD%BD01%E8%AF%9D%2F00.jpg"
	if _, err := tr.Do(context.Background(), &host.Request{Method: "GET", URL: srv.URL + path + "?sign=abc"}); err != nil {
		t.Fatal(err)
	}
	if seen != path {
		t.Fatalf("server saw %s, want %s", seen, path)
	}
	if strings.Contains(seen, "/连载") {
		t.Fatal("the escaped slash was turned back into a separator")
	}
}

// A URL with a character a request line cannot carry is still escaped.
func TestTransportStillEscapesSpaces(t *testing.T) {
	tr, _ := newTestTransport(t)
	req, err := tr.request(context.Background(), &host.Request{Method: "GET", URL: "https://example.test/search?q=one piece"})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.RequestURI(); got != "/search?q=one%20piece" {
		t.Fatalf("request uri = %s", got)
	}
}
