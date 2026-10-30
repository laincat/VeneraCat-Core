// Package host defines what VeneraCat Core asks of its client.
//
// Core never opens a network connection, a WebView or a player. Each client
// (Nekularity on iOS, Nekophage on the desktop) implements these interfaces
// with its own platform stack: URLSession, WKWebView and AVPlayer on Apple,
// net/http, a desktop WebView and mpv elsewhere.
package host

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Request is an HTTP request Core wants the client to send.
type Request struct {
	Method string
	URL    string
	// Header uses canonical keys. A missing User-Agent means the client
	// supplies its default one.
	Header http.Header
	Body   []byte
	// Cookies asks the client to attach its stored cookies for URL.
	Cookies bool
	// Partition isolates cookies and clearance between plugins. The client
	// keeps one cookie jar per partition.
	Partition string
}

// Response is what the client got back. A non-2xx status is not an error:
// sources and rules inspect it, and challenge detection needs the body.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	// FinalURL is the URL after redirects.
	FinalURL string
}

// Transport sends requests. It must honour ctx cancellation.
type Transport interface {
	Do(ctx context.Context, req *Request) (*Response, error)
}

// ChallengeKind tells the client what kind of interactive verification a
// response needs.
type ChallengeKind string

const (
	// ChallengeCloudflare is a Cloudflare managed or interactive challenge.
	ChallengeCloudflare ChallengeKind = "cloudflare"
	// ChallengeCaptcha is a site-specific image captcha or verification page.
	ChallengeCaptcha ChallengeKind = "captcha"
)

// Challenge describes a page the user must pass in a real browser.
type Challenge struct {
	Kind      ChallengeKind
	URL       string
	Partition string
	// Done reports whether the browser has passed the challenge. The client
	// evaluates it after each navigation. For Cloudflare it means the
	// challenge markers are gone and a cf_clearance cookie is present.
	Done DoneRule
}

// DoneRule is a declarative completion test the client runs in its WebView.
type DoneRule struct {
	// AbsentMarkers must all be missing from document.documentElement.outerHTML.
	AbsentMarkers []string
	// RequiredCookies must all be present for Challenge.URL.
	RequiredCookies []string
}

// Clearance is what the client hands back after the user passed a challenge.
// The client must also store Cookies in the jar of Challenge.Partition.
type Clearance struct {
	// UserAgent is the WebView's exact User-Agent. Cloudflare binds
	// cf_clearance to it, so later requests of the partition must send it.
	UserAgent string
	Cookies   []*http.Cookie
}

// Verifier shows a challenge to the user and waits until it is passed or
// dismissed. It returns ErrChallengeDismissed when the user gives up.
type Verifier interface {
	Verify(ctx context.Context, c *Challenge) (*Clearance, error)
}

// SniffRequest asks the client to load a page and report the first media
// request whose URL matches, as Kazumi and similar apps do with a WebView.
type SniffRequest struct {
	PageURL   string
	Referer   string
	Partition string
	// Match is a regular expression for media URLs. Empty means the client's
	// default (m3u8/mp4/mkv/flv/mpd).
	Match string
	// FollowIframes lets the client also inspect iframe src attributes.
	FollowIframes bool
	TimeoutMillis int
}

// SniffResult is the media the client found.
type SniffResult struct {
	URL    string
	Header http.Header
}

// Sniffer resolves media through a WebView.
type Sniffer interface {
	Sniff(ctx context.Context, req *SniffRequest) (*SniffResult, error)
}

// Errors the client returns.
var (
	ErrChallengeDismissed = errors.New("host: challenge dismissed by the user")
	ErrUnsupported        = errors.New("host: capability not supported by this client")
)

// Services bundles the capabilities a client provides. Nil fields mean the
// capability is unavailable; Core then reports a typed error instead of
// guessing.
type Services struct {
	Transport Transport
	Verifier  Verifier
	Sniffer   Sniffer
}

// HeaderValue returns the value of a header in a case-insensitive map whose
// keys may not be canonical (rules and scripts write them freely).
func HeaderValue(h http.Header, name string) string {
	if v := h.Get(name); v != "" {
		return v
	}
	for k, vs := range h {
		if strings.EqualFold(k, name) && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

// ValidateURL accepts absolute http(s) URLs with a host and no credentials.
func ValidateURL(raw string) error {
	if raw == "" {
		return errors.New("url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("unsupported url scheme %q: only http and https are allowed", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("url has no host")
	}
	if u.User != nil {
		return errors.New("url must not contain credentials")
	}
	return nil
}

// ValidHeader checks a header name against the RFC 9110 token grammar and a
// value for control characters other than horizontal tab.
func ValidHeader(name, value string) error {
	if name == "" {
		return errors.New("empty header name")
	}
	for i := 0; i < len(name); i++ {
		if !isTokenChar(name[i]) {
			return fmt.Errorf("invalid header name %q", name)
		}
	}
	for i := 0; i < len(value); i++ {
		if c := value[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return fmt.Errorf("invalid value for header %q", name)
		}
	}
	return nil
}

func isTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}
