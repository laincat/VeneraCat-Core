// Package webcookie reads the cookies the system web view keeps.
//
// A source's browser login (account.loginWithWebview) and a challenge both end
// with a session the site handed to a web view. The toolkit that shows that web
// view need not expose a cookie API: this package reads the store the system
// keeps behind it, so the cookies, HttpOnly ones included, can go into the
// partition the source's later requests read.
//
//   - Apple platforms: WKWebsiteDataStore.defaultDataStore, the store a
//     WKWebView uses unless its configuration names another.
//   - Windows: the WebView2 profile in Options.WebView2Folder, read through a
//     hidden web view of its own on that folder.
//
// Other platforms answer ErrUnsupported.
package webcookie

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Options says where the client's web views keep their profile.
type Options struct {
	// WebView2Folder is the user data folder the client's WebView2 views use
	// (Windows). WebView2 shares one browser process per folder and refuses a
	// second environment on it whose options differ, so the client must create
	// its own with default options, as this package does.
	WebView2Folder string
}

// ErrUnsupported reports a platform without a readable web view store.
var ErrUnsupported = errors.New("webcookie: no web view cookie store on this platform")

// Read returns the cookies the web view would send to rawURL.
//
// It waits for the platform store at most until ctx ends.
func Read(ctx context.Context, rawURL string, opts Options) ([]*http.Cookie, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("webcookie: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("webcookie: %q is not an http(s) address", rawURL)
	}
	return read(ctx, u, opts)
}

// matches reports whether c would be sent to u, for a store that hands back
// every cookie it holds (WKHTTPCookieStore.getAllCookies).
//
// A Domain with a leading dot also matches the domain's subdomains; one without
// it matches its host alone, as NSHTTPCookie and WebView2 write them.
func matches(c *http.Cookie, u *url.URL, now time.Time) bool {
	if !c.Expires.IsZero() && !c.Expires.After(now) {
		return false
	}
	if c.Secure && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	domain := strings.ToLower(c.Domain)
	if bare, ok := strings.CutPrefix(domain, "."); ok {
		if host != bare && !strings.HasSuffix(host, domain) {
			return false
		}
	} else if host != domain {
		return false
	}
	return pathMatches(c.Path, u.EscapedPath())
}

// pathMatches is RFC 6265's path-match.
func pathMatches(cookiePath, requestPath string) bool {
	if cookiePath == "" || cookiePath == "/" {
		return true
	}
	if requestPath == "" {
		requestPath = "/"
	}
	if !strings.HasPrefix(requestPath, cookiePath) {
		return false
	}
	return len(requestPath) == len(cookiePath) || strings.HasSuffix(cookiePath, "/") || requestPath[len(cookiePath)] == '/'
}
