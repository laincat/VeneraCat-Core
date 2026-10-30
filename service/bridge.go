package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBridgeMaxBytes bounds one response a service may read.
const DefaultBridgeMaxBytes = 4 << 20

// DefaultBridgeTimeout bounds one request a service may make.
const DefaultBridgeTimeout = 20 * time.Second

// BridgePolicy is what a host allows a service to reach.
//
// The zero value allows any host, which is only reasonable for a service the
// user installed by hand. A client that ships a catalogue sets Hosts.
type BridgePolicy struct {
	// Hosts limits requests to these hosts and their subdomains, written as
	// "example.com" or ".example.com". Empty allows any host.
	Hosts []string
	// MaxBytes bounds the response a service may read; zero uses
	// DefaultBridgeMaxBytes.
	MaxBytes int64
	// Timeout bounds one request; zero uses DefaultBridgeTimeout.
	Timeout time.Duration
	// AllowCookies lets the request carry the jar's cookies. Services usually
	// talk to their own API and do not need them.
	AllowCookies bool
}

// Bridge is the HTTP path a service may use.
//
// It exists so the restriction lives in one place the host controls, rather
// than in whatever a plugin decided to call: a request is checked before it is
// made, every redirect is checked again, the scheme must be HTTP or HTTPS, and
// the response is bounded.
type Bridge struct {
	policy BridgePolicy
	client *http.Client
}

// NewBridge builds a bridge that follows its policy.
func NewBridge(policy BridgePolicy) *Bridge {
	if policy.MaxBytes <= 0 {
		policy.MaxBytes = DefaultBridgeMaxBytes
	}
	if policy.Timeout <= 0 {
		policy.Timeout = DefaultBridgeTimeout
	}
	b := &Bridge{policy: policy}
	b.client = &http.Client{
		Timeout: policy.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("service: too many redirects")
			}
			// A redirect is a new request the service did not write, so it is
			// checked exactly like the first one.
			return b.Allows(req.URL.String())
		},
	}
	return b
}

// Allows reports whether the bridge may request rawURL.
func (b *Bridge) Allows(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("service: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("service: only http and https URLs, not %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("service: URL has no host")
	}
	if len(b.policy.Hosts) == 0 {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range b.policy.Hosts {
		if hostMatches(host, allowed) {
			return nil
		}
	}
	return fmt.Errorf("service: %s is not an allowed host", host)
}

// hostMatches reports whether host is allowed by pattern, which may be written
// as "example.com" or ".example.com"; either form matches the domain itself and
// its subdomains.
func hostMatches(host, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(pattern, ".")))
	if pattern == "" {
		return false
	}
	return host == pattern || strings.HasSuffix(host, "."+pattern)
}

// Fetch performs one request for a service and returns the body.
func (b *Bridge) Fetch(ctx context.Context, rawURL string) (string, error) {
	if err := b.Allows(rawURL); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	if !b.policy.AllowCookies {
		// A service is not a browser page: it has no business carrying the
		// user's jars along.
		req.Header.Del("Cookie")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("service: %s returned HTTP %s", req.URL.Host, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, b.policy.MaxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(body)) > b.policy.MaxBytes {
		return "", fmt.Errorf("service: response is larger than %d bytes", b.policy.MaxBytes)
	}
	return string(body), nil
}

// Host returns a Host whose Fetch goes through this bridge, for handing to a
// Runtime.
func (b *Bridge) Host(host Host) Host {
	host.Fetch = b.Fetch
	return host
}
