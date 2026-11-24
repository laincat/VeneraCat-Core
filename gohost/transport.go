package gohost

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/laincat/veneracat-core/gohost/cookies"
	"github.com/laincat/veneracat-core/host"
)

// Transport sends the requests Core asks for, with net/http.
//
// Cookies never come from a shared jar: Core names a partition, and the jar
// of that partition supplies the Cookie header and receives every Set-Cookie,
// including those on redirect hops. Responses larger than maxBody fail
// instead of exhausting memory.
type Transport struct {
	client *http.Client
	jar    *cookies.Jar
	ua     string
	// The scheme each partition last used, so a source that serves its pages
	// over http keeps its images over http too. A scheme-relative image
	// address inherits this, as RFC 3986 says it inherits the page's.
	mu      sync.Mutex
	schemes map[string]string
}

// ErrTooLarge is returned for a response over the size limit.
var ErrTooLarge = errors.New("core: response larger than the limit")

// NewTransport builds a Transport.
func NewTransport(jar *cookies.Jar, userAgent string, timeout time.Duration) *Transport {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	t := &Transport{jar: jar, ua: userAgent, schemes: map[string]string{}}
	t.client = &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := host.ValidateURL(req.URL.String()); err != nil {
				return err
			}
			escapeInvalid(req.URL)
			if len(via) >= 10 {
				return errors.New("core: too many redirects")
			}
			// Provider keys are not all recognised by net/http's default
			// sensitive-header policy. A change of origin must drop them.
			if len(via) > 0 {
				previous := via[0].URL
				if !strings.EqualFold(previous.Scheme, req.URL.Scheme) || !strings.EqualFold(previous.Host, req.URL.Host) {
					for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Proxy-Authorization", "Cookie"} {
						req.Header.Del(name)
					}
				}
			}
			// Store what the previous hop set, then send this partition's
			// cookies to the next URL, which may be another host.
			//
			// The redirect response is `req.Response`, not the last entry of
			// `via`: Go fills Response only on the request it creates for the
			// next hop, so reading it from `via` finds nil and quietly stores
			// nothing.
			part := partitionOf(req.Context())
			if part != "" {
				if prev := req.Response; prev != nil && prev.Request != nil && prev.Request.URL != nil {
					t.saveCookies(part, prev)
				}
				// Drop cookies copied by net/http before selecting for the next
				// host/path; absence in this jar must clear the previous header.
				req.Header.Del("Cookie")
				if h := t.jar.Header(part, req.URL.String()); h != "" {
					req.Header.Set("Cookie", h)
				}
			}
			return nil
		},
	}
	return t
}

func (t *Transport) saveCookies(partition string, resp *http.Response) {
	if err := t.jar.SetURL(partition, resp.Request.URL, resp.Cookies()); err != nil {
		slog.Error("Could not save response cookies", "partition", partition, "error", err)
	}
}

// escapeInvalid percent-encodes the bytes a request line cannot carry.
//
// Sources build URLs by pasting text into them: a keyword with a space
// ("?title=one piece") is common. Dart's Uri escapes it, and Go's client
// instead writes the raw byte, which servers answer with a protocol error.
// Existing percent escapes are left alone, and the characters that structure
// a URL stay as they are.
//
// A URL that already carries a valid escaped form keeps it exactly. The
// escaped form is what the server signed: an image address with an escaped
// slash and Chinese text in it has a signature over that spelling, and
// re-escaping the decoded text turns "%2F" back into "/" and the server
// answers "check sign failed".
func escapeInvalid(u *url.URL) {
	// The path is written from RawPath when it is set, so the escaped form
	// goes there and Path keeps the plain text: setting Path to an escaped
	// string would escape the percent signs again.
	//
	// RawPath is only replaced when there is none, or when the one that came
	// with the URL is not a valid encoding of the path.
	if u.RawPath == "" || u.EscapedPath() != u.RawPath {
		if escaped := escapeKeeping(u.Path, "/:@!$&'()*+,;=-._~%"); escaped != u.Path {
			u.RawPath = escaped
		}
	}
	u.RawQuery = escapeKeeping(u.RawQuery, "/?:@!$&'()*+,;=-._~%[]")
}

func escapeKeeping(s, allowed string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f || strings.IndexByte(" \"<>^`{}|\\", c) >= 0 {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		if strings.IndexByte(allowed, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		// Bytes of UTF-8 text are escaped; ASCII punctuation is kept.
		if c >= 0x80 {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// maxBody bounds one response body.
const maxBody = 64 << 20

type partitionKey struct{}

// partitionOf returns the partition recorded on the request context.
func partitionOf(ctx context.Context) string {
	if v, ok := ctx.Value(partitionKey{}).(string); ok {
		return v
	}
	return ""
}

// SchemeOf returns the scheme a partition last used, or "" when it has not
// asked for anything yet.
func (t *Transport) SchemeOf(partition string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.schemes[partition]
}

// request shares request construction between bounded source responses and
// streaming media, including the same partitioned cookie and redirect policy.
func (t *Transport) request(ctx context.Context, r *host.Request) (*http.Request, error) {
	if err := host.ValidateURL(r.URL); err != nil {
		return nil, err
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		return nil, err
	}
	escapeInvalid(u)
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if len(r.Body) > 0 {
		body = bytes.NewReader(r.Body)
	}
	// Only cookie-enabled requests carry a jar partition into redirect hops.
	partition := ""
	if r.Cookies && t.jar != nil {
		partition = r.Partition
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, partitionKey{}, partition), method, u.String(), body)
	if err != nil {
		return nil, err
	}
	for k, vs := range r.Header {
		if strings.EqualFold(k, "Cookie") {
			continue
		}
		// A source that names Host means the site behind this address, the way
		// curl's --resolve does: the API answers for that name, not for the
		// address a client happens to dial. Go sends the URL's host unless the
		// request's Host field is set, so copying the header alone would drop it.
		if strings.EqualFold(k, "Host") {
			if len(vs) > 0 {
				req.Host = vs[0]
			}
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", t.ua)
	}
	// Advertise only encodings this transport can decode, even when a source
	// requests additional encodings that Go's HTTP client cannot handle.
	if req.Header.Get("Accept-Encoding") != "" {
		req.Header.Set("Accept-Encoding", "gzip, deflate")
	}
	var cookie string
	for k, vs := range r.Header {
		if strings.EqualFold(k, "Cookie") {
			cookie = strings.Join(vs, "; ")
		}
	}
	if partition != "" {
		if stored := t.jar.Header(partition, u.String()); stored != "" {
			if cookie != "" {
				cookie += "; "
			}
			cookie += stored
		}
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	return req, nil
}

// OpenStream returns a live response. The caller owns Body.Close and supplies
// the request lifetime through ctx. Media must not be buffered into maxBody or
// interrupted by the source client's whole-response timeout.
func (t *Transport) OpenStream(ctx context.Context, r *host.Request) (*http.Response, error) {
	req, err := t.request(ctx, r)
	if err != nil {
		return nil, err
	}
	// Range offsets describe encoded media bytes, not a compressed HTTP body.
	req.Header.Set("Accept-Encoding", "identity")
	client := *t.client
	client.Timeout = 0
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if partition := partitionOf(req.Context()); partition != "" && resp.Request != nil && resp.Request.URL != nil {
		t.saveCookies(partition, resp)
	}
	return resp, nil
}

// Do implements host.Transport with its bounded, fully read response contract.
func (t *Transport) Do(ctx context.Context, r *host.Request) (*host.Response, error) {
	req, err := t.request(ctx, r)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// The partition's scheme is remembered, so an image address without one
	// can inherit it.
	if partition := partitionOf(req.Context()); partition != "" && resp.Request != nil && resp.Request.URL != nil {
		t.mu.Lock()
		t.schemes[partition] = resp.Request.URL.Scheme
		t.mu.Unlock()
		t.saveCookies(partition, resp)
	}
	var bodyReader io.Reader = resp.Body
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		decoded, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer decoded.Close()
		bodyReader = decoded
	case "deflate":
		decoded, err := zlib.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer decoded.Close()
		bodyReader = decoded
	default:
		return nil, fmt.Errorf("core: unsupported content encoding: %s", resp.Header.Get("Content-Encoding"))
	}
	data, err := io.ReadAll(io.LimitReader(bodyReader, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBody {
		return nil, ErrTooLarge
	}
	if resp.Header.Get("Content-Encoding") != "" {
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
	}
	final := r.URL
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return &host.Response{Status: resp.StatusCode, Header: resp.Header, Body: data, FinalURL: final}, nil
}

// Fetch downloads a URL with no cookies, for indexes and scripts.
func (t *Transport) Fetch(ctx context.Context, rawURL string, limit int64) ([]byte, string, error) {
	if err := host.ValidateURL(rawURL); err != nil {
		return nil, "", err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", err
	}
	escapeInvalid(u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", t.ua)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", errors.New("core: download failed: HTTP " + resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > limit {
		return nil, "", ErrTooLarge
	}
	ctype := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(ctype, ';'); i >= 0 {
		ctype = ctype[:i]
	}
	return data, strings.TrimSpace(ctype), nil
}
