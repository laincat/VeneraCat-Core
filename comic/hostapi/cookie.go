package hostapi

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// CookieMessage is the argument of sendMessage({method: "cookie", ...}).
type CookieMessage struct {
	// Function is "set", "get" or "delete".
	Function string `json:"function"`
	URL      string `json:"url"`
	// Cookies is used by "set": [{name, value, domain?, path?}].
	Cookies []map[string]any `json:"cookies"`
}

// CookieStore is the client's cookie jar, one per partition. The client owns
// storage and matching.
//
// A cookie's Domain follows the usual convention: a leading dot means the
// cookie also matches subdomains, no leading dot means host-only. Path may be
// empty, then the store applies the default path of the URL. Core does not
// know the public suffix list, so rejecting cookies for a public suffix such
// as ".co.uk" is the store's job.
type CookieStore interface {
	// Set stores cookies received for url.
	Set(partition, url string, cookies []*http.Cookie)
	// Get returns the cookies that would be sent to url.
	Get(partition, url string) []*http.Cookie
	// Delete removes every cookie that matches url.
	Delete(partition, url string)
}

// DoCookie handles a comic-source cookie message. "set" and "delete" return
// nil. "get" returns []map[string]any with the keys name, value, domain,
// path, expires, max-age, secure, httpOnly and session.
//
// expires is Unix milliseconds or nil. max-age is seconds or nil. session is
// true when the cookie has neither. A cookie without a domain is host-only
// and gets the host of the URL; no registrable domain is guessed.
func DoCookie(store CookieStore, sourceKey string, msg CookieMessage) (any, error) {
	if store == nil {
		return nil, errors.New("cookie: no cookie store")
	}
	if sourceKey == "" {
		return nil, errors.New("cookie: empty source key")
	}
	if err := validateURL(msg.URL); err != nil {
		return nil, fmt.Errorf("cookie: %w", err)
	}
	u, _ := url.Parse(msg.URL)
	urlHost := strings.ToLower(u.Hostname())
	partition := Partition(sourceKey)

	switch msg.Function {
	case "set":
		cookies := make([]*http.Cookie, 0, len(msg.Cookies))
		for i, m := range msg.Cookies {
			c, err := cookieFromMap(m, urlHost)
			if err != nil {
				return nil, fmt.Errorf("cookie: cookies[%d]: %w", i, err)
			}
			cookies = append(cookies, c)
		}
		store.Set(partition, msg.URL, cookies)
		return nil, nil
	case "get":
		stored := store.Get(partition, msg.URL)
		out := make([]map[string]any, 0, len(stored))
		for _, c := range stored {
			if c != nil {
				out = append(out, cookieToMap(c, urlHost))
			}
		}
		return out, nil
	case "delete":
		store.Delete(partition, msg.URL)
		return nil, nil
	default:
		return nil, fmt.Errorf("cookie: unknown function %q", msg.Function)
	}
}

func cookieFromMap(m map[string]any, urlHost string) (*http.Cookie, error) {
	name, ok := m["name"].(string)
	if !ok || name == "" {
		return nil, errors.New("name must be a non-empty string")
	}
	if strings.ContainsAny(name, " \t\r\n;=,") {
		return nil, fmt.Errorf("invalid cookie name %q", name)
	}
	value := ""
	if v, present := m["value"]; present && v != nil {
		value = fmt.Sprint(v)
	}
	if strings.ContainsAny(value, "\r\n;") {
		return nil, fmt.Errorf("invalid value for cookie %q", name)
	}
	c := &http.Cookie{Name: name, Value: value, Domain: urlHost}
	if d, present := m["domain"]; present && d != nil {
		ds, ok := d.(string)
		if !ok {
			return nil, errors.New("domain must be a string")
		}
		if ds = strings.ToLower(strings.TrimSpace(ds)); ds != "" {
			if !domainMatches(urlHost, ds) {
				return nil, fmt.Errorf("domain %q does not match url host %q", ds, urlHost)
			}
			c.Domain = ds
		}
	}
	if p, present := m["path"]; present && p != nil {
		ps, ok := p.(string)
		if !ok {
			return nil, errors.New("path must be a string")
		}
		if strings.ContainsAny(ps, "\r\n;") {
			return nil, errors.New("invalid path")
		}
		c.Path = ps
	}
	return c, nil
}

// domainMatches reports whether a cookie domain attribute may be set from a
// page on host (RFC 6265 section 5.1.3, ignoring a leading dot). An IP
// address host only matches itself.
func domainMatches(host, domain string) bool {
	d := strings.TrimPrefix(domain, ".")
	if d == "" {
		return false
	}
	if host == d {
		return true
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return false
	}
	return strings.HasSuffix(host, "."+d)
}

func cookieToMap(c *http.Cookie, urlHost string) map[string]any {
	domain := c.Domain
	if domain == "" {
		domain = urlHost
	}
	path := c.Path
	if path == "" {
		path = "/"
	}
	var expires, maxAge any
	if !c.Expires.IsZero() {
		expires = c.Expires.UnixMilli()
	}
	if c.MaxAge != 0 {
		maxAge = c.MaxAge
	}
	return map[string]any{
		"name":     c.Name,
		"value":    c.Value,
		"domain":   domain,
		"path":     path,
		"expires":  expires,
		"max-age":  maxAge,
		"secure":   c.Secure,
		"httpOnly": c.HttpOnly,
		"session":  c.Expires.IsZero() && c.MaxAge == 0,
	}
}
