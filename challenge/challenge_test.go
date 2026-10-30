package challenge

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/laincat/veneracat-core/host"
)

const cfPage = `<html><head><title>Just a moment...</title></head><body>
<script>window._cf_chl_opt={cvId:'3'};</script></body></html>`

func cfResponse(status int) *host.Response {
	return &host.Response{Status: status, Header: http.Header{"Server": {"cloudflare"}}, Body: []byte(cfPage)}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		resp *host.Response
		want bool
	}{
		{"cf-mitigated header", &host.Response{Status: 403, Header: http.Header{"Cf-Mitigated": {"challenge"}}}, true},
		{"503 challenge body", cfResponse(503), true},
		{"429 challenge body", cfResponse(429), true},
		{"plain 403 is a permission block", &host.Response{Status: 403, Header: http.Header{"Server": {"cloudflare"}}, Body: []byte("Forbidden")}, false},
		{"markers without cloudflare server", &host.Response{Status: 503, Header: http.Header{"Server": {"nginx"}}, Body: []byte(cfPage)}, false},
		{"200 with marker text is content", &host.Response{Status: 200, Header: http.Header{"Server": {"cloudflare"}}, Body: []byte(cfPage)}, false},
		{"lower-case header key", &host.Response{Status: 403, Header: http.Header{"cf-mitigated": {"challenge"}}}, true},
	}
	for _, c := range cases {
		if _, got := Detect(c.resp); got != c.want {
			t.Errorf("%s: Detect = %v, want %v", c.name, got, c.want)
		}
	}
}

// fakeSite serves a challenge until a request carries both the clearance
// cookie and the matching User-Agent, as Cloudflare does.
type fakeSite struct {
	mu       sync.Mutex
	cleared  bool
	ua       string
	requests int32
	gotUAs   []string
}

func (s *fakeSite) Do(_ context.Context, r *host.Request) (*host.Response, error) {
	atomic.AddInt32(&s.requests, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	ua := r.Header.Get("User-Agent")
	s.gotUAs = append(s.gotUAs, ua)
	if s.cleared && r.Cookies && ua == s.ua {
		return &host.Response{Status: 200, Body: []byte("ok")}, nil
	}
	return cfResponse(403), nil
}

type fakeVerifier struct {
	site  *fakeSite
	calls int32
	ua    string
	err   error
}

func (v *fakeVerifier) Verify(_ context.Context, c *host.Challenge) (*host.Clearance, error) {
	atomic.AddInt32(&v.calls, 1)
	if v.err != nil {
		return nil, v.err
	}
	if len(c.Done.RequiredCookies) == 0 || c.Done.RequiredCookies[0] != "cf_clearance" {
		panic("missing cf_clearance completion rule")
	}
	v.site.mu.Lock()
	v.site.cleared, v.site.ua = true, v.ua
	v.site.mu.Unlock()
	return &host.Clearance{UserAgent: v.ua}, nil
}

func TestVerifyAndRetryReplaysWebViewUA(t *testing.T) {
	site := &fakeSite{}
	v := &fakeVerifier{site: site, ua: "WebView/1.0"}
	tr := NewTransport(site, v)
	req := &host.Request{Method: "GET", URL: "https://example.test/", Partition: "src:demo",
		Header: http.Header{"user-agent": {"SourceScript/9"}}}
	resp, err := tr.Do(context.Background(), req)
	if err != nil || resp.Status != 200 {
		t.Fatalf("Do = %v, %v; want 200", resp, err)
	}
	if v.calls != 1 {
		t.Fatalf("verifier calls = %d, want 1", v.calls)
	}
	// The source's own User-Agent must not leak into the retry.
	if last := site.gotUAs[len(site.gotUAs)-1]; last != "WebView/1.0" {
		t.Fatalf("retry UA = %q, want WebView/1.0", last)
	}
	if host.HeaderValue(req.Header, "User-Agent") != "SourceScript/9" {
		t.Fatal("caller's request was mutated")
	}
	// Later requests of the partition reuse clearance without a WebView.
	if resp, err := tr.Do(context.Background(), req); err != nil || resp.Status != 200 {
		t.Fatalf("second Do = %v, %v", resp, err)
	}
	if v.calls != 1 {
		t.Fatalf("verifier reopened: calls = %d", v.calls)
	}
	// Other partitions keep their own identity.
	if ua := tr.UserAgent("src:other"); ua != "" {
		t.Fatalf("partition leak: %q", ua)
	}
}

func TestParallelRequestsOpenOneVerifier(t *testing.T) {
	site := &fakeSite{}
	v := &fakeVerifier{site: site, ua: "WebView/2"}
	tr := NewTransport(site, v)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := tr.Do(context.Background(), &host.Request{URL: "https://example.test/img", Partition: "p"})
			if err == nil && resp.Status != 200 {
				err = &ErrChallengeRequired{URL: "status"}
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if v.calls != 1 {
		t.Fatalf("verifier calls = %d, want 1", v.calls)
	}
}

func TestDismissedAndMissingVerifier(t *testing.T) {
	site := &fakeSite{}
	tr := NewTransport(site, &fakeVerifier{site: site, err: host.ErrChallengeDismissed})
	_, err := tr.Do(context.Background(), &host.Request{URL: "https://example.test/", Partition: "p"})
	var need *ErrChallengeRequired
	if !asChallenge(err, &need) || need.Kind != host.ChallengeCloudflare {
		t.Fatalf("dismissed: err = %v", err)
	}
	tr = NewTransport(site, nil)
	resp, err := tr.Do(context.Background(), &host.Request{URL: "https://example.test/", Partition: "p"})
	if !asChallenge(err, &need) || resp == nil || resp.Status != 403 {
		t.Fatalf("no verifier: resp=%v err=%v", resp, err)
	}
}

func asChallenge(err error, out **ErrChallengeRequired) bool {
	e, ok := err.(*ErrChallengeRequired)
	if ok {
		*out = e
	}
	return ok
}
