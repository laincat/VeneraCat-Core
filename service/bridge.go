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

	"github.com/laincat/veneracat-core/host"
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
	// Transport routes requests through the client's host.Transport instead of
	// an internal http.Client. Desktop and Go hosts should supply it so Core
	// never opens sockets of its own.
	Transport host.Transport
	// Partition identifies the cookie jar when Transport is used.
	Partition string
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
	if policy.Transport == nil {
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

// Fetch performs one GET request for a service and returns the body.
func (b *Bridge) Fetch(ctx context.Context, rawURL string) (string, error) {
	resp, err := b.FetchRequest(ctx, FetchRequest{Method: http.MethodGet, URL: rawURL})
	if err != nil {
		return "", err
	}
	if resp.Status < 200 || resp.Status > 299 {
		hostName := rawURL
		if u, parseErr := url.Parse(rawURL); parseErr == nil && u.Host != "" {
			hostName = u.Host
		}
		return "", fmt.Errorf("service: %s returned HTTP %d", hostName, resp.Status)
	}
	return resp.Body, nil
}

// FetchRequest performs one HTTP request with method, headers and body,
// returning the status code, body and flattened response headers. Non-2xx
// statuses are returned without error so services can inspect them directly.
func (b *Bridge) FetchRequest(ctx context.Context, req FetchRequest) (*FetchResponse, error) {
	if err := b.Allows(req.URL); err != nil {
		return nil, err
	}
	method, err := normalizeFetchMethod(req.Method)
	if err != nil {
		return nil, err
	}
	var hdr http.Header
	if len(req.Headers) > 0 {
		hdr = make(http.Header, len(req.Headers))
		for k, v := range req.Headers {
			if strings.EqualFold(strings.TrimSpace(k), "cookie") {
				if !b.policy.AllowCookies {
					return nil, errors.New("service: Cookie header is not allowed by bridge policy")
				}
			}
			if err := host.ValidHeader(k, v); err != nil {
				return nil, fmt.Errorf("service: %w", err)
			}
			hdr.Set(k, v)
		}
	}
	if !b.policy.AllowCookies && hdr != nil {
		hdr.Del("Cookie")
	}

	if b.policy.Transport != nil {
		reqCtx := ctx
		var cancel context.CancelFunc
		if b.policy.Timeout > 0 {
			reqCtx, cancel = context.WithTimeout(ctx, b.policy.Timeout)
			defer cancel()
		}
		var bodyBytes []byte
		if req.Body != "" {
			bodyBytes = []byte(req.Body)
		}
		resp, err := b.policy.Transport.Do(reqCtx, &host.Request{
			Method:    method,
			URL:       req.URL,
			Header:    hdr,
			Body:      bodyBytes,
			Cookies:   b.policy.AllowCookies,
			Partition: b.policy.Partition,
		})
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, errors.New("service: transport returned nil response")
		}
		if resp.FinalURL != "" && resp.FinalURL != req.URL {
			if err := b.Allows(resp.FinalURL); err != nil {
				return nil, err
			}
		}
		if int64(len(resp.Body)) > b.policy.MaxBytes {
			return nil, fmt.Errorf("service: response is larger than %d bytes", b.policy.MaxBytes)
		}
		return &FetchResponse{
			Status:  resp.Status,
			OK:      resp.Status >= 200 && resp.Status <= 299,
			Body:    string(resp.Body),
			Headers: flattenHeaders(resp.Header),
		}, nil
	}

	var bodyReader io.Reader
	if req.Body != "" {
		bodyReader = strings.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, bodyReader)
	if err != nil {
		return nil, err
	}
	for k, vals := range hdr {
		for _, v := range vals {
			httpReq.Header.Add(k, v)
		}
	}
	if !b.policy.AllowCookies {
		// A service is not a browser page: it has no business carrying the
		// user's jars along.
		httpReq.Header.Del("Cookie")
	}
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, b.policy.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > b.policy.MaxBytes {
		return nil, fmt.Errorf("service: response is larger than %d bytes", b.policy.MaxBytes)
	}
	return &FetchResponse{
		Status:  resp.StatusCode,
		OK:      resp.StatusCode >= 200 && resp.StatusCode <= 299,
		Body:    string(body),
		Headers: flattenHeaders(resp.Header),
	}, nil
}

func flattenHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(h))
	for k, vals := range h {
		if len(vals) > 0 {
			out[strings.ToLower(k)] = strings.Join(vals, ", ")
		}
	}
	return out
}

// Host returns a Host whose Fetch and FetchRequest go through this bridge, for
// handing to a Runtime.
func (b *Bridge) Host(host Host) Host {
	host.Fetch = b.Fetch
	host.FetchRequest = b.FetchRequest
	return host
}
