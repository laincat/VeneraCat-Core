// Package cookies keeps one cookie jar per partition.
//
// Core asks for cookies only through a partition name ("comic:<source key>"
// for comic sources), and sources must never see each other's login state.
// The system cookie store cannot be partitioned that way, so jars live here:
// matching follows RFC 6265 (host-only versus domain cookies, path prefixes,
// Secure, expiry), and persistent cookies are written to one JSON file.
package cookies

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cookie is a stored cookie.
type Cookie struct {
	Name     string     `json:"name"`
	Value    string     `json:"value"`
	Domain   string     `json:"domain"` // lower case, no leading dot
	HostOnly bool       `json:"hostOnly"`
	Path     string     `json:"path"`
	Secure   bool       `json:"secure"`
	HTTPOnly bool       `json:"httpOnly"`
	Expires  *time.Time `json:"expires,omitempty"` // nil: session cookie
}

// Jar holds the partitions. It is safe for concurrent use.
type Jar struct {
	mu   sync.Mutex
	jars map[string][]Cookie
	file string
	now  func() time.Time
}

// Open loads jars from file. An empty file name keeps them in memory only.
func Open(file string) (*Jar, error) {
	j := &Jar{jars: map[string][]Cookie{}, file: file, now: time.Now}
	if file == "" {
		return j, nil
	}
	data, err := os.ReadFile(file)
	switch {
	case os.IsNotExist(err):
		return j, nil
	case err != nil:
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &j.jars); err != nil {
			// A damaged file must not stop the app: start with no cookies.
			j.jars = map[string][]Cookie{}
		}
	}
	if j.jars == nil {
		j.jars = map[string][]Cookie{}
	}
	return j, nil
}

// For returns the cookies that would be sent to rawURL in partition,
// longest paths first.
func (j *Jar) For(partition, rawURL string) []Cookie {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	return j.ForURL(partition, u)
}

// ForURL is For for a parsed URL.
func (j *Jar) ForURL(partition string, u *url.URL) []Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.match(partition, u, false)
}

// Header is the Cookie header value for rawURL, or "".
func (j *Jar) Header(partition, rawURL string) string {
	var parts []string
	for _, c := range j.For(partition, rawURL) {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// SetURL stores cookies received for u. A cookie whose expiry has passed
// is removed instead, as RFC 6265 requires.
func (j *Jar) SetURL(partition string, u *url.URL, cs []*http.Cookie) error {
	if len(cs) == 0 {
		return nil
	}
	host := hostOf(u)
	if host == "" {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	jar := slices.Clone(j.jars[partition])
	now := j.now()
	for _, hc := range cs {
		c, ok := fromHTTP(hc, u, host, now)
		if !ok {
			continue
		}
		jar = setCookie(jar, c, now)
	}
	return j.commit(partition, jar)
}

// DeleteURL removes every cookie that matches u.
func (j *Jar) DeleteURL(partition string, u *url.URL) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	doomed := j.match(partition, u, true)
	if len(doomed) == 0 {
		return nil
	}
	jar := slices.Clone(j.jars[partition])
	for _, d := range doomed {
		jar = slices.DeleteFunc(jar, func(c Cookie) bool { return sameIdentity(c, d) })
	}
	return j.commit(partition, jar)
}

// Clear drops a whole partition, on uninstall or logout.
func (j *Jar) Clear(partition string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.commit(partition, nil)
}

// Import copies cookies a WebView collected, as a challenge's clearance.
func (j *Jar) Import(partition string, cs []*http.Cookie) error {
	if len(cs) == 0 {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	jar := slices.Clone(j.jars[partition])
	now := j.now()
	for _, hc := range cs {
		if hc == nil || hc.Name == "" {
			continue
		}
		c := Cookie{Name: hc.Name, Value: hc.Value, Domain: strings.TrimPrefix(strings.ToLower(hc.Domain), "."),
			HostOnly: !strings.HasPrefix(hc.Domain, "."), Path: hc.Path, Secure: hc.Secure, HTTPOnly: hc.HttpOnly}
		if c.Path == "" {
			c.Path = "/"
		}
		if !hc.Expires.IsZero() {
			t := hc.Expires
			c.Expires = &t
		}
		if c.Domain != "" {
			jar = setCookie(jar, c, now)
		}
	}
	return j.commit(partition, jar)
}

// --- hostapi.CookieStore, which takes URLs as text. ---

// Set implements hostapi.CookieStore: it takes the request URL as text.
func (j *Jar) Set(partition, rawURL string, cs []*http.Cookie) {
	if u, err := url.Parse(rawURL); err == nil {
		if err := j.SetURL(partition, u, cs); err != nil {
			slog.Error("Could not save source cookies", "partition", partition, "error", err)
		}
	}
}

// Get implements hostapi.CookieStore.
func (j *Jar) Get(partition, rawURL string) []*http.Cookie {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	var out []*http.Cookie
	for _, c := range j.ForURL(partition, u) {
		hc := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly}
		if c.HostOnly {
			hc.Domain = ""
		} else {
			hc.Domain = "." + c.Domain
		}
		if c.Expires != nil {
			hc.Expires = *c.Expires
		}
		out = append(out, hc)
	}
	return out
}

// Delete implements hostapi.CookieStore.
func (j *Jar) Delete(partition, rawURL string) {
	if u, err := url.Parse(rawURL); err == nil {
		if err := j.DeleteURL(partition, u); err != nil {
			slog.Error("Could not delete source cookies", "partition", partition, "error", err)
		}
	}
}

// --- internals; callers hold the lock. ---

func (j *Jar) match(partition string, u *url.URL, ignoreSecure bool) []Cookie {
	host := hostOf(u)
	if host == "" {
		return nil
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	secure := strings.EqualFold(u.Scheme, "https")
	now := j.now()
	var out []Cookie
	for _, c := range j.jars[partition] {
		if c.Expires != nil && !c.Expires.After(now) {
			continue
		}
		if !c.HostOnly && host != c.Domain && !strings.HasSuffix(host, "."+c.Domain) {
			continue
		}
		if c.HostOnly && host != c.Domain {
			continue
		}
		if !pathMatches(path, c.Path) {
			continue
		}
		if c.Secure && !secure && !ignoreSecure {
			continue
		}
		if c.Expires != nil {
			expires := *c.Expires
			c.Expires = &expires
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(a, b int) bool { return len(out[a].Path) > len(out[b].Path) })
	return out
}

func (j *Jar) commit(partition string, next []Cookie) error {
	if j.file != "" && !slices.EqualFunc(persistent(j.jars[partition]), persistent(next), sameCookie) {
		saved := make(map[string][]Cookie)
		for key, jar := range j.jars {
			if key != partition {
				if records := persistent(jar); len(records) != 0 {
					saved[key] = records
				}
			}
		}
		if records := persistent(next); len(records) != 0 {
			saved[partition] = records
		}
		if err := j.persist(saved); err != nil {
			return fmt.Errorf("cookies: save %s: %w", j.file, err)
		}
	}
	if len(next) == 0 {
		delete(j.jars, partition)
	} else {
		j.jars[partition] = next
	}
	return nil
}

func (j *Jar) persist(saved map[string][]Cookie) error {
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	dir := filepath.Dir(j.file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".cookies-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), j.file)
}

func persistent(jar []Cookie) []Cookie {
	var out []Cookie
	for _, c := range jar {
		if c.Expires != nil {
			out = append(out, c)
		}
	}
	return out
}

func sameCookie(a, b Cookie) bool {
	if !a.Expires.Equal(*b.Expires) {
		return false
	}
	a.Expires, b.Expires = nil, nil
	return a == b
}

func sameIdentity(a, b Cookie) bool {
	return a.Name == b.Name && a.Domain == b.Domain && a.Path == b.Path
}

func setCookie(jar []Cookie, c Cookie, now time.Time) []Cookie {
	index := slices.IndexFunc(jar, func(old Cookie) bool { return sameIdentity(old, c) })
	if c.Expires != nil && !c.Expires.After(now) {
		if index >= 0 {
			return slices.Delete(jar, index, index+1)
		}
		return jar
	}
	if index >= 0 {
		jar[index] = c
		return jar
	}
	return append(jar, c)
}

func fromHTTP(hc *http.Cookie, u *url.URL, host string, now time.Time) (Cookie, bool) {
	if hc == nil || hc.Name == "" {
		return Cookie{}, false
	}
	c := Cookie{Name: hc.Name, Value: hc.Value, Secure: hc.Secure, HTTPOnly: hc.HttpOnly}
	domain := strings.ToLower(hc.Domain)
	switch {
	case domain == "":
		c.Domain, c.HostOnly = host, true
	default:
		bare := strings.TrimPrefix(domain, ".")
		// A page may set a cookie only for its own host or a parent domain,
		// and never for a bare IP's parent.
		if bare != host && (net.ParseIP(host) != nil || !strings.HasSuffix(host, "."+bare) || !strings.Contains(bare, ".")) {
			return Cookie{}, false
		}
		c.Domain, c.HostOnly = bare, !strings.HasPrefix(domain, ".")
	}
	c.Path = hc.Path
	if !strings.HasPrefix(c.Path, "/") {
		c.Path = defaultPath(u)
	}
	if hc.MaxAge != 0 {
		t := now.Add(time.Duration(hc.MaxAge) * time.Second)
		c.Expires = &t
	} else if !hc.Expires.IsZero() {
		t := hc.Expires
		c.Expires = &t
	}
	return c, true
}

func hostOf(u *url.URL) string { return strings.ToLower(u.Hostname()) }

func defaultPath(u *url.URL) string {
	p := u.Path
	if !strings.HasPrefix(p, "/") {
		return "/"
	}
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}

// pathMatches reports whether a request path is covered by a cookie path.
func pathMatches(request, cookie string) bool {
	if cookie == "" || cookie == "/" || request == cookie {
		return true
	}
	if !strings.HasPrefix(request, cookie) {
		return false
	}
	return strings.HasSuffix(cookie, "/") || request[len(cookie)] == '/'
}
