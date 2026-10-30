package hostapi

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/challenge"
	"github.com/laincat/veneracat-core/host"
)

// fakeTransport answers every request with fn and records what it saw.
type fakeTransport struct {
	mu   sync.Mutex
	reqs []*host.Request
	fn   func(*host.Request) (*host.Response, error)
}

func (f *fakeTransport) Do(_ context.Context, r *host.Request) (*host.Response, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	f.mu.Unlock()
	return f.fn(r)
}

func (f *fakeTransport) last() *host.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func respond(status int, body []byte) *fakeTransport {
	return &fakeTransport{fn: func(*host.Request) (*host.Response, error) {
		return &host.Response{Status: status, Header: http.Header{}, Body: body}, nil
	}}
}

func str(t *testing.T, v any) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("body is %T, want string", v)
	}
	return s
}

func TestDefaultUserAgent(t *testing.T) {
	ft := respond(200, []byte("ok"))
	res := DoHTTP(context.Background(), ft, "src", HTTPMessage{URL: "https://example.test/"}, "Venera/1")
	if res.Error != nil || res.Status == nil || *res.Status != 200 {
		t.Fatalf("result = %+v", res)
	}
	r := ft.last()
	if r.Method != "GET" || r.Header.Get("User-Agent") != "Venera/1" || !r.Cookies || r.Partition != "comic:src" {
		t.Fatalf("request = %+v", r)
	}

	// A User-Agent in any case is kept and the default is not added.
	DoHTTP(context.Background(), ft, "src", HTTPMessage{
		Method: "post", URL: "https://example.test/", Headers: map[string]any{"user-agent": "Mine/2", "X-N": 5},
	}, "Venera/1")
	r = ft.last()
	if r.Method != "POST" || r.Header.Get("User-Agent") != "Mine/2" || len(r.Header.Values("User-Agent")) != 1 {
		t.Fatalf("request = %+v", r)
	}
	if r.Header.Get("X-N") != "5" {
		t.Fatalf("X-N = %q", r.Header.Get("X-N"))
	}
}

func TestBodyModes(t *testing.T) {
	invalid := []byte{'a', 0xff, 'b', 0xe4, 0xbd, 'c', 0xe4, 0xbd, 0xa0}
	ft := respond(200, invalid)
	res := DoHTTP(context.Background(), ft, "s", HTTPMessage{URL: "https://example.test/"}, "")
	want := "a\uFFFDb\uFFFDc\u4f60"
	if got := str(t, res.Body); got != want {
		t.Fatalf("string body = %q, want %q", got, want)
	}
	res = DoHTTP(context.Background(), ft, "s", HTTPMessage{URL: "https://example.test/", Bytes: true}, "")
	b, ok := res.Body.([]byte)
	if !ok || string(b) != string(invalid) {
		t.Fatalf("bytes body = %#v", res.Body)
	}
}

func TestDecodeLossy(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\xf0\x9f", "\uFFFD"},                 // truncated 4-byte sequence is one replacement
		{"\xc0\xaf", "\uFFFD\uFFFD"},           // overlong: both bytes invalid
		{"\xed\xa0\x80", "\uFFFD\uFFFD\uFFFD"}, // surrogate
		{"\x80\x80", "\uFFFD\uFFFD"},
		{"ok", "ok"},
	}
	for _, c := range cases {
		if got := decodeLossy([]byte(c.in)); got != c.want {
			t.Errorf("decodeLossy(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStatusAndHeaders(t *testing.T) {
	ft := &fakeTransport{fn: func(*host.Request) (*host.Response, error) {
		return &host.Response{Status: 404, Header: http.Header{"Set-Cookie": {"a=1", "b=2"}, "Content-Type": {"text/html"}}, Body: []byte("nope")}, nil
	}}
	res := DoHTTP(context.Background(), ft, "s", HTTPMessage{URL: "https://example.test/x"}, "")
	if res.Error != nil || res.Status == nil || *res.Status != 404 || str(t, res.Body) != "nope" {
		t.Fatalf("result = %+v", res)
	}
	if res.Headers["set-cookie"] != "a=1,b=2" || res.Headers["content-type"] != "text/html" {
		t.Fatalf("headers = %v", res.Headers)
	}
}

func TestTransportError(t *testing.T) {
	ft := &fakeTransport{fn: func(*host.Request) (*host.Response, error) { return nil, errors.New("dial tcp: refused") }}
	res := DoHTTP(context.Background(), ft, "s", HTTPMessage{URL: "https://example.test/"}, "")
	if res.Status != nil || res.Body != nil || res.Error == nil || !strings.Contains(*res.Error, "refused") {
		t.Fatalf("result = %+v", res)
	}
}

func TestDataEncoding(t *testing.T) {
	ft := respond(200, nil)
	cases := []struct {
		name string
		data any
		want string
	}{
		{"numbers", []any{104, 105}, "hi"},
		{"floats from JSON", []any{float64(104), float64(105)}, "hi"},
		{"bytes", []byte("hi"), "hi"},
		{"string", "hi", "hi"},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		res := DoHTTP(context.Background(), ft, "s", HTTPMessage{Method: "POST", URL: "https://example.test/", Data: c.data}, "")
		if res.Error != nil {
			t.Fatalf("%s: %v", c.name, *res.Error)
		}
		if got := string(ft.last().Body); got != c.want {
			t.Errorf("%s: body = %q, want %q", c.name, got, c.want)
		}
	}
	for _, bad := range []any{[]any{300}, []any{"x"}, []any{1.5}, 42} {
		res := DoHTTP(context.Background(), ft, "s", HTTPMessage{URL: "https://example.test/", Data: bad}, "")
		if res.Error == nil || res.Status != nil {
			t.Errorf("data %#v accepted", bad)
		}
	}
}

func TestRejectBadURLs(t *testing.T) {
	ft := respond(200, nil)
	for _, u := range []string{"file:///etc/passwd", "ftp://example.test/", "javascript:alert(1)", "https://user:pw@example.test/", "/relative", "", "https://"} {
		res := DoHTTP(context.Background(), ft, "s", HTTPMessage{URL: u}, "")
		if res.Error == nil || res.Status != nil {
			t.Errorf("url %q accepted: %+v", u, res)
		}
	}
	if len(ft.reqs) != 0 {
		t.Fatalf("transport was called %d times", len(ft.reqs))
	}
}

const cfBody = `<html><script>window._cf_chl_opt={};</script></html>`

// cfSite serves a Cloudflare 503 page to a partition until it is cleared and
// then requires the clearance User-Agent, as the real service does.
type cfSite struct {
	mu      sync.Mutex
	cleared map[string]string // partition -> UA
	seen    []seenReq
}

type seenReq struct{ partition, ua string }

func (s *cfSite) Do(_ context.Context, r *host.Request) (*host.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ua := r.Header.Get("User-Agent")
	s.seen = append(s.seen, seenReq{r.Partition, ua})
	if want, ok := s.cleared[r.Partition]; ok && ua == want && r.Cookies {
		return &host.Response{Status: 200, Header: http.Header{}, Body: []byte("real page")}, nil
	}
	return &host.Response{Status: 503, Header: http.Header{"Server": {"cloudflare"}}, Body: []byte(cfBody)}, nil
}

type cfVerifier struct {
	site      *cfSite
	ua        string
	dismiss   bool
	partition []string
}

func (v *cfVerifier) Verify(_ context.Context, c *host.Challenge) (*host.Clearance, error) {
	v.partition = append(v.partition, c.Partition)
	if v.dismiss {
		return nil, host.ErrChallengeDismissed
	}
	v.site.mu.Lock()
	v.site.cleared[c.Partition] = v.ua
	v.site.mu.Unlock()
	return &host.Clearance{UserAgent: v.ua}, nil
}

func TestCloudflareVerifiedRetry(t *testing.T) {
	site := &cfSite{cleared: map[string]string{}}
	v := &cfVerifier{site: site, ua: "WebView/9"}
	tr := challenge.NewTransport(site, v)

	res := DoHTTP(context.Background(), tr, "demo", HTTPMessage{URL: "https://example.test/list"}, "Venera/1")
	if res.Error != nil || res.Status == nil || *res.Status != 200 || str(t, res.Body) != "real page" {
		t.Fatalf("result = %+v (err %v)", res, res.Error)
	}
	if last := site.seen[len(site.seen)-1]; last.ua != "WebView/9" || last.partition != "comic:demo" {
		t.Fatalf("retry = %+v", last)
	}
	if !reflect.DeepEqual(v.partition, []string{"comic:demo"}) {
		t.Fatalf("verified partitions = %v", v.partition)
	}
	if tr.UserAgent("comic:demo") != "WebView/9" {
		t.Fatal("clearance UA not stored")
	}
}

func TestCloudflareDismissed(t *testing.T) {
	site := &cfSite{cleared: map[string]string{}}
	tr := challenge.NewTransport(site, &cfVerifier{site: site, dismiss: true})
	url := "https://example.test/list?page=2"
	res := DoHTTP(context.Background(), tr, "demo", HTTPMessage{URL: url}, "Venera/1")
	if res.Error == nil || *res.Error != "CloudflareException: "+url {
		t.Fatalf("error = %v", res.Error)
	}
	if res.Status == nil || *res.Status != 503 || !strings.Contains(str(t, res.Body), "_cf_chl_opt") {
		t.Fatalf("result = %+v", res)
	}

	// Without any verifier the result is the same.
	tr = challenge.NewTransport(site, nil)
	res = DoHTTP(context.Background(), tr, "demo", HTTPMessage{URL: url, Bytes: true}, "")
	if res.Error == nil || !strings.HasPrefix(*res.Error, CloudflareErrorPrefix) {
		t.Fatalf("error = %v", res.Error)
	}
	if _, ok := res.Body.([]byte); !ok {
		t.Fatalf("body = %T, want []byte", res.Body)
	}
}

func TestSourcesKeepSeparatePartitions(t *testing.T) {
	site := &cfSite{cleared: map[string]string{}}
	v := &cfVerifier{site: site, ua: "WebView/A"}
	tr := challenge.NewTransport(site, v)
	msg := HTTPMessage{URL: "https://example.test/"}

	if res := DoHTTP(context.Background(), tr, "a", msg, "Venera/1"); res.Error != nil {
		t.Fatalf("a: %v", *res.Error)
	}
	if tr.UserAgent("comic:a") != "WebView/A" || tr.UserAgent("comic:b") != "" {
		t.Fatalf("UAs: a=%q b=%q", tr.UserAgent("comic:a"), tr.UserAgent("comic:b"))
	}
	// Source b has not been cleared: it must be challenged on its own.
	v.dismiss = true
	res := DoHTTP(context.Background(), tr, "b", msg, "Venera/1")
	if res.Error == nil || !strings.HasPrefix(*res.Error, CloudflareErrorPrefix) {
		t.Fatalf("b: result = %+v", res)
	}
	if got := site.seen[len(site.seen)-1]; got.partition != "comic:b" || got.ua != "Venera/1" {
		t.Fatalf("b request = %+v", got)
	}
	// Source a still passes without another verification.
	n := len(v.partition)
	if res := DoHTTP(context.Background(), tr, "a", msg, "Venera/1"); res.Error != nil {
		t.Fatalf("a again: %v", *res.Error)
	}
	if len(v.partition) != n {
		t.Fatal("source a was verified again")
	}
}

// memStore is an in-memory per-partition cookie store for tests.
type memStore struct {
	data map[string][]*http.Cookie
	ops  []string
}

func newMemStore() *memStore { return &memStore{data: map[string][]*http.Cookie{}} }

func (m *memStore) Set(p, u string, cs []*http.Cookie) {
	m.ops = append(m.ops, "set "+p+" "+u)
	m.data[p] = append(m.data[p], cs...)
}

func (m *memStore) Get(p, u string) []*http.Cookie {
	m.ops = append(m.ops, "get "+p+" "+u)
	return m.data[p]
}

func (m *memStore) Delete(p, u string) {
	m.ops = append(m.ops, "delete "+p+" "+u)
	delete(m.data, p)
}

func TestCookieSetGetDelete(t *testing.T) {
	st := newMemStore()
	u := "https://www.example.co.uk/a/b"
	_, err := DoCookie(st, "demo", CookieMessage{Function: "set", URL: u, Cookies: []map[string]any{
		{"name": "sid", "value": "123"},
		{"name": "lang", "value": "en", "domain": ".example.co.uk", "path": "/a"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if st.ops[0] != "set comic:demo "+u {
		t.Fatalf("ops = %v", st.ops)
	}
	// Host-only: the URL host, never a guessed ".co.uk" or ".a.b".
	if got := st.data["comic:demo"][0].Domain; got != "www.example.co.uk" {
		t.Fatalf("host-only domain = %q", got)
	}

	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	st.data["comic:demo"] = append(st.data["comic:demo"], &http.Cookie{
		Name: "tok", Value: "x", Domain: ".example.co.uk", Path: "/", Expires: exp, MaxAge: 60, Secure: true, HttpOnly: true,
	})
	v, err := DoCookie(st, "demo", CookieMessage{Function: "get", URL: u})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := v.([]map[string]any)
	if !ok || len(got) != 3 {
		t.Fatalf("get = %#v", v)
	}
	want0 := map[string]any{
		"name": "sid", "value": "123", "domain": "www.example.co.uk", "path": "/",
		"expires": nil, "max-age": nil, "secure": false, "httpOnly": false, "session": true,
	}
	if !reflect.DeepEqual(got[0], want0) {
		t.Fatalf("got[0] = %#v", got[0])
	}
	if got[1]["domain"] != ".example.co.uk" || got[1]["path"] != "/a" {
		t.Fatalf("got[1] = %#v", got[1])
	}
	want2 := map[string]any{
		"name": "tok", "value": "x", "domain": ".example.co.uk", "path": "/",
		"expires": exp.UnixMilli(), "max-age": 60, "secure": true, "httpOnly": true, "session": false,
	}
	if !reflect.DeepEqual(got[2], want2) {
		t.Fatalf("got[2] = %#v", got[2])
	}

	// A different source sees nothing.
	v, _ = DoCookie(st, "other", CookieMessage{Function: "get", URL: u})
	if l := v.([]map[string]any); l == nil || len(l) != 0 {
		t.Fatalf("other source get = %#v", v)
	}

	if _, err := DoCookie(st, "demo", CookieMessage{Function: "delete", URL: u}); err != nil {
		t.Fatal(err)
	}
	v, _ = DoCookie(st, "demo", CookieMessage{Function: "get", URL: u})
	if len(v.([]map[string]any)) != 0 {
		t.Fatalf("after delete: %#v", v)
	}
}

func TestCookieErrors(t *testing.T) {
	st := newMemStore()
	good := "https://example.test/"
	bad := []CookieMessage{
		{Function: "set", URL: "file:///x", Cookies: []map[string]any{{"name": "a", "value": "b"}}},
		{Function: "frobnicate", URL: good},
		{Function: "set", URL: good, Cookies: []map[string]any{{"value": "b"}}},
		{Function: "set", URL: good, Cookies: []map[string]any{{"name": "a", "value": "b", "domain": "evil.test"}}},
		{Function: "set", URL: good, Cookies: []map[string]any{{"name": "a", "value": "b;c"}}},
	}
	for i, m := range bad {
		if _, err := DoCookie(st, "s", m); err == nil {
			t.Errorf("case %d accepted: %+v", i, m)
		}
	}
	if len(st.data) != 0 {
		t.Fatalf("store modified by rejected messages: %v", st.data)
	}
	if _, err := DoCookie(st, "s", CookieMessage{Function: "get", URL: good}); err != nil {
		t.Errorf("valid get rejected: %v", err)
	}
}

func TestHeaderAndDomainRules(t *testing.T) {
	for _, bad := range []string{"", "a b", "a:b", "a\x00", "a(b", "a\"b", "ä"} {
		if ValidHeader(bad, "v") == nil {
			t.Errorf("header name %q accepted", bad)
		}
	}
	for _, bad := range []string{"a\r\nb", "a\x00", "a\x7f"} {
		if ValidHeader("X", bad) == nil {
			t.Errorf("header value %q accepted", bad)
		}
	}
	if err := ValidHeader("X-Test_1.ok~", "tab\tok"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		host, domain string
		want         bool
	}{
		{"www.example.com", ".example.com", true},
		{"example.com", "example.com", true},
		{"badexample.com", "example.com", false},
		{"1.2.3.4", "2.3.4", false},
		{"1.2.3.4", "1.2.3.4", true},
		{"[::1]", "::1", false},
	}
	for _, c := range cases {
		if got := domainMatches(c.host, c.domain); got != c.want {
			t.Errorf("domainMatches(%q, %q) = %v", c.host, c.domain, got)
		}
	}
}
