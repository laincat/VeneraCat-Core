// Package hostapi implements the Core side of the legacy ComicSource
// sendMessage methods that need the client: "http" and "cookie".
//
// It is independent of the JavaScript engine. The engine binding decodes the
// sendMessage argument into HTTPMessage or CookieMessage, calls DoHTTP or
// DoCookie, and encodes the result back into JavaScript.
package hostapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/laincat/veneracat-core/challenge"
	"github.com/laincat/veneracat-core/host"
)

// CloudflareErrorPrefix starts the HTTPResult.Error of a request that hit a
// Cloudflare challenge the user has not passed. Official clients and some
// scripts match this exact prefix, so it is part of the contract.
const CloudflareErrorPrefix = "CloudflareException: "

// Partition returns the cookie and clearance partition of a comic source.
// The same value must be used for http and cookie calls, so a cookie set by
// a script is sent by later requests and the reverse.
func Partition(sourceKey string) string { return "comic:" + sourceKey }

// HTTPMessage is the argument of sendMessage({method: "http", ...}).
type HTTPMessage struct {
	// Method defaults to GET and is upper-cased.
	Method  string         `json:"http_method"`
	URL     string         `json:"url"`
	Headers map[string]any `json:"headers"`
	// Data is a string, []byte, a []any of byte values, a map (sent as JSON)
	// or nil.
	Data any `json:"data"`
	// Bytes selects a []byte response body instead of a string.
	Bytes bool `json:"bytes"`
	// Extra is meant for the client's interceptors. host.Request has no field
	// for it yet, so DoHTTP does not forward it.
	Extra map[string]any `json:"extra"`
}

// HTTPResult is the sendMessage result: {status, headers, body, error}.
type HTTPResult struct {
	// Status is nil when no response was received.
	Status *int `json:"status"`
	// Headers has lower-case names; repeated headers are joined with ",".
	Headers map[string]string `json:"headers"`
	// Body is a string (invalid UTF-8 replaced with U+FFFD) or, when
	// HTTPMessage.Bytes is set, a []byte. It is nil when there is no response.
	Body  any     `json:"body"`
	Error *string `json:"error"`
}

// DoHTTP performs a comic-source HTTP request through t.
//
// 4xx and 5xx statuses are not errors. A transport failure gives a nil Status
// and a non-nil Error. A Cloudflare challenge that is not passed gives the
// real Status, Headers and Body plus Error "CloudflareException: <url>", so
// scripts that catch errors still work and the client can offer verification.
// defaultUA is added only when the script supplied no User-Agent; an empty
// defaultUA leaves the choice to the client.
func DoHTTP(ctx context.Context, t host.Transport, sourceKey string, msg HTTPMessage, defaultUA string) HTTPResult {
	if t == nil {
		return httpFailure(host.ErrUnsupported.Error())
	}
	if sourceKey == "" {
		return httpFailure("http: empty source key")
	}
	if err := validateURL(msg.URL); err != nil {
		return httpFailure(err.Error())
	}
	method, err := normalizeMethod(msg.Method)
	if err != nil {
		return httpFailure(err.Error())
	}
	header, err := buildHeader(msg.Headers)
	if err != nil {
		return httpFailure(err.Error())
	}
	if defaultUA != "" && host.HeaderValue(header, "User-Agent") == "" {
		header.Set("User-Agent", defaultUA)
	}
	body, err := encodeBody(msg.Data, header)
	if err != nil {
		return httpFailure(err.Error())
	}

	resp, err := t.Do(ctx, &host.Request{
		Method:    method,
		URL:       msg.URL,
		Header:    header,
		Body:      body,
		Cookies:   true,
		Partition: Partition(sourceKey),
	})
	if err != nil {
		var need *challenge.ErrChallengeRequired
		if errors.As(err, &need) {
			res := httpFailure("")
			if resp != nil {
				res = httpResponse(resp, msg.Bytes)
			}
			s := CloudflareErrorPrefix + msg.URL
			res.Error = &s
			return res
		}
		return httpFailure(err.Error())
	}
	if resp == nil {
		return httpFailure("http: transport returned no response")
	}
	return httpResponse(resp, msg.Bytes)
}

func httpFailure(msg string) HTTPResult {
	return HTTPResult{Headers: map[string]string{}, Error: &msg}
}

func httpResponse(resp *host.Response, wantBytes bool) HTTPResult {
	status := resp.Status
	res := HTTPResult{Status: &status, Headers: map[string]string{}}
	joined := map[string][]string{}
	for k, vs := range resp.Header {
		name := strings.ToLower(k)
		joined[name] = append(joined[name], vs...)
	}
	for name, vs := range joined {
		res.Headers[name] = strings.Join(vs, ",")
	}
	if wantBytes {
		res.Body = append([]byte{}, resp.Body...)
	} else {
		res.Body = decodeLossy(resp.Body)
	}
	return res
}

// validateURL accepts absolute http and https URLs with a host and no
// credentials. Credentials in a URL would leak into logs and referrers.
// ValidateURL is host.ValidateURL.
func ValidateURL(raw string) error { return host.ValidateURL(raw) }

func validateURL(raw string) error { return host.ValidateURL(raw) }

func normalizeMethod(m string) (string, error) {
	m = strings.ToUpper(strings.TrimSpace(m))
	if m == "" {
		return http.MethodGet, nil
	}
	for i := 0; i < len(m); i++ {
		c := m[i]
		ok := c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
		if !ok {
			return "", fmt.Errorf("invalid http method %q", m)
		}
	}
	return m, nil
}

func buildHeader(in map[string]any) (http.Header, error) {
	h := http.Header{}
	for k, v := range in {
		if v == nil {
			continue
		}
		val := fmt.Sprint(v)
		if err := host.ValidHeader(k, val); err != nil {
			return nil, err
		}
		h.Set(k, val)
	}
	return h, nil
}

// ValidHeader is host.ValidHeader.
func ValidHeader(name, value string) error { return host.ValidHeader(name, value) }

// encodeBody turns the sendMessage data field into request bytes. A map is
// sent as JSON, with Content-Type set when the script gave none.
func encodeBody(data any, header http.Header) ([]byte, error) {
	switch d := data.(type) {
	case nil:
		return nil, nil
	case string:
		return []byte(d), nil
	case []byte:
		return d, nil
	case []any:
		out := make([]byte, len(d))
		for i, e := range d {
			b, ok := byteValue(e)
			if !ok {
				return nil, fmt.Errorf("data[%d] is not a byte value: %v", i, e)
			}
			out[i] = b
		}
		return out, nil
	case map[string]any:
		out, err := json.Marshal(d)
		if err != nil {
			return nil, fmt.Errorf("data is not JSON encodable: %w", err)
		}
		if host.HeaderValue(header, "Content-Type") == "" {
			header.Set("Content-Type", "application/json")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported data type %T", data)
	}
}

func byteValue(v any) (byte, bool) {
	var f float64
	switch n := v.(type) {
	case int:
		f = float64(n)
	case int8:
		f = float64(n)
	case int16:
		f = float64(n)
	case int32:
		f = float64(n)
	case int64:
		f = float64(n)
	case uint:
		f = float64(n)
	case uint8:
		return n, true
	case uint16:
		f = float64(n)
	case uint32:
		f = float64(n)
	case uint64:
		f = float64(n)
	case float32:
		f = float64(n)
	case float64:
		f = n
	case json.Number:
		parsed, err := n.Float64()
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if f < 0 || f > 255 || f != math.Trunc(f) {
		return 0, false
	}
	return byte(f), true
}

// decodeLossy converts bytes to a string, replacing each maximal invalid
// subpart with one U+FFFD, as lenient UTF-8 decoders (WHATWG, Dart's
// allowMalformed) do.
func decodeLossy(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out bytes.Buffer
	out.Grow(len(b))
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || size > 1 {
			out.Write(b[i : i+size])
			i += size
			continue
		}
		out.WriteRune(utf8.RuneError)
		i += invalidPrefixLen(b[i:])
	}
	return out.String()
}

// invalidPrefixLen returns the length of the longest prefix of b that is a
// valid but truncated UTF-8 sequence, or 1 when b starts with a byte that
// cannot begin one.
func invalidPrefixLen(b []byte) int {
	lo, hi := byte(0x80), byte(0xBF)
	need := 0
	switch c := b[0]; {
	case c >= 0xC2 && c <= 0xDF:
		need = 1
	case c == 0xE0:
		need, lo = 2, 0xA0
	case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
		need = 2
	case c == 0xED:
		need, hi = 2, 0x9F
	case c == 0xF0:
		need, lo = 3, 0x90
	case c >= 0xF1 && c <= 0xF3:
		need = 3
	case c == 0xF4:
		need, hi = 3, 0x8F
	default:
		return 1
	}
	n := 1
	for ; n <= need && n < len(b); n++ {
		if b[n] < lo || b[n] > hi {
			break
		}
		lo, hi = 0x80, 0xBF
	}
	return n
}
