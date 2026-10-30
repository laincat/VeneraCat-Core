// Package challenge detects anti-bot challenges and runs the verify-and-retry
// flow shared by comic sources and video rules.
//
// The official Venera client only recognised a Cloudflare challenge on HTTP
// 403 with "cf-mitigated: challenge", stored cookies under a guessed
// registrable domain, and kept a single global User-Agent. That breaks on
// 503 challenges, on sites under multi-label public suffixes (example.co.uk)
// and when two sites need different WebView identities. This package keeps
// clearance per partition and replays the WebView's own User-Agent.
package challenge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/laincat/veneracat-core/host"
)

// Cloudflare markers seen in challenge pages. They come from Cloudflare's
// public challenge page structure, not from any client implementation.
var cloudflareMarkers = []string{
	"window._cf_chl_opt",
	"/cdn-cgi/challenge-platform/",
	`id="challenge-form"`,
	`id="challenge-error-text"`,
	`id="challenge-success-text"`,
	"cf-turnstile",
}

// Detect reports whether resp is a challenge page and of which kind.
//
// Cloudflare is recognised by the cf-mitigated header (authoritative when
// present) or, on 403/429/503 from a Cloudflare server, by challenge markers
// in the body. A plain 403 is never treated as a challenge on its own: it is
// usually a permission or region block.
func Detect(resp *host.Response) (host.ChallengeKind, bool) {
	if resp == nil {
		return "", false
	}
	if strings.EqualFold(host.HeaderValue(resp.Header, "Cf-Mitigated"), "challenge") {
		return host.ChallengeCloudflare, true
	}
	switch resp.Status {
	case http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
	default:
		return "", false
	}
	if !strings.EqualFold(host.HeaderValue(resp.Header, "Server"), "cloudflare") {
		return "", false
	}
	if containsAny(resp.Body, cloudflareMarkers) {
		return host.ChallengeCloudflare, true
	}
	return "", false
}

func containsAny(body []byte, markers []string) bool {
	for _, m := range markers {
		if bytes.Contains(body, []byte(m)) {
			return true
		}
	}
	return false
}

// CloudflareDone is the completion rule clients evaluate in their WebView.
func CloudflareDone() host.DoneRule {
	return host.DoneRule{
		AbsentMarkers:   append([]string(nil), cloudflareMarkers...),
		RequiredCookies: []string{"cf_clearance"},
	}
}

// ErrChallengeRequired is returned when a challenge is detected but no
// Verifier is available or verification is disabled for this call.
type ErrChallengeRequired struct {
	Kind host.ChallengeKind
	URL  string
}

func (e *ErrChallengeRequired) Error() string {
	return fmt.Sprintf("challenge: %s verification required for %s", e.Kind, e.URL)
}

// Transport wraps a client transport. It applies the stored User-Agent of a
// partition, detects challenges, asks the Verifier once, and retries the
// request a single time with the new clearance.
type Transport struct {
	Next     host.Transport
	Verifier host.Verifier

	mu  sync.Mutex
	uas map[string]string // partition -> WebView User-Agent
	// inflight serialises verification per partition, so ten parallel image
	// requests hitting the same challenge open one WebView, not ten.
	inflight map[string]*sync.Mutex
}

// NewTransport returns a challenge-aware transport.
func NewTransport(next host.Transport, v host.Verifier) *Transport {
	return &Transport{Next: next, Verifier: v, uas: map[string]string{}, inflight: map[string]*sync.Mutex{}}
}

// UserAgent returns the clearance User-Agent stored for a partition.
func (t *Transport) UserAgent(partition string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.uas[partition]
}

// SetUserAgent restores a persisted clearance User-Agent.
func (t *Transport) SetUserAgent(partition, ua string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ua == "" {
		delete(t.uas, partition)
		return
	}
	t.uas[partition] = ua
}

func (t *Transport) lockFor(partition string) *sync.Mutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.inflight[partition]
	if m == nil {
		m = &sync.Mutex{}
		t.inflight[partition] = m
	}
	return m
}

// Do implements host.Transport.
func (t *Transport) Do(ctx context.Context, req *host.Request) (*host.Response, error) {
	if t.Next == nil {
		return nil, host.ErrUnsupported
	}
	resp, err := t.Next.Do(ctx, t.prepare(req))
	if err != nil {
		return nil, err
	}
	kind, ok := Detect(resp)
	if !ok {
		return resp, nil
	}
	if t.Verifier == nil {
		return resp, &ErrChallengeRequired{Kind: kind, URL: req.URL}
	}

	lock := t.lockFor(req.Partition)
	lock.Lock()
	// Another request may have passed the challenge while we waited.
	if retry, err := t.Next.Do(ctx, t.prepare(req)); err == nil {
		if _, still := Detect(retry); !still {
			lock.Unlock()
			return retry, nil
		}
	}
	clear, verr := t.Verifier.Verify(ctx, &host.Challenge{
		Kind: kind, URL: req.URL, Partition: req.Partition, Done: CloudflareDone(),
	})
	if verr == nil && clear != nil && clear.UserAgent != "" {
		t.SetUserAgent(req.Partition, clear.UserAgent)
	}
	lock.Unlock()
	if verr != nil {
		if errors.Is(verr, host.ErrChallengeDismissed) {
			return resp, &ErrChallengeRequired{Kind: kind, URL: req.URL}
		}
		return nil, verr
	}
	resp, err = t.Next.Do(ctx, t.prepare(req))
	if err != nil {
		return nil, err
	}
	if kind, still := Detect(resp); still {
		return resp, &ErrChallengeRequired{Kind: kind, URL: req.URL}
	}
	return resp, nil
}

// prepare copies req and applies the partition's clearance User-Agent. The
// WebView's User-Agent wins over a source-supplied one, because Cloudflare
// rejects cf_clearance sent with a different User-Agent.
func (t *Transport) prepare(req *host.Request) *host.Request {
	out := *req
	out.Header = req.Header.Clone()
	if out.Header == nil {
		out.Header = http.Header{}
	}
	if ua := t.UserAgent(req.Partition); ua != "" {
		for k := range out.Header {
			if strings.EqualFold(k, "User-Agent") {
				delete(out.Header, k)
			}
		}
		out.Header.Set("User-Agent", ua)
		out.Cookies = true
	}
	return &out
}
