package kazumi

import (
	"context"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/laincat/veneracat-core/host"
)

// fakeTransport records every request and answers from handler.
type fakeTransport struct {
	mu      sync.Mutex
	reqs    []*host.Request
	handler func(*host.Request) (*host.Response, error)
}

func (f *fakeTransport) Do(_ context.Context, req *host.Request) (*host.Response, error) {
	c := *req
	c.Header = req.Header.Clone()
	c.Body = append([]byte(nil), req.Body...)
	f.mu.Lock()
	f.reqs = append(f.reqs, &c)
	f.mu.Unlock()
	return f.handler(req)
}

func (f *fakeTransport) last(t *testing.T) *host.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("transport received no request")
	}
	return f.reqs[len(f.reqs)-1]
}

// respondWith answers every request with the same canned response.
func respondWith(status int, body string, header ...string) *fakeTransport {
	h := http.Header{}
	for i := 0; i+1 < len(header); i += 2 {
		h.Set(header[i], header[i+1])
	}
	return &fakeTransport{handler: func(req *host.Request) (*host.Response, error) {
		return &host.Response{Status: status, Header: h, Body: []byte(body), FinalURL: req.URL}, nil
	}}
}

func mustRule(t *testing.T, data string) *Rule {
	t.Helper()
	r, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

func publishedRule(t *testing.T, name string) *Rule {
	t.Helper()
	data, err := os.ReadFile("testdata/rules/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return mustRule(t, string(data))
}
