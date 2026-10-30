package hls

import (
	"errors"
	"strings"
)

// ErrBaseNotAbsolute is returned by Resolve when the reference is relative
// and the base URL has no scheme.
var ErrBaseNotAbsolute = errors.New("hls: base URL is not absolute")

type uriParts struct {
	scheme, authority, path, query, fragment string
	hasAuthority, hasQuery, hasFragment      bool
}

func validScheme(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

// splitURI applies the RFC 3986 appendix B decomposition without any
// re-encoding, so the original bytes survive a round trip.
func splitURI(s string) (u uriParts) {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		u.fragment, u.hasFragment = s[i+1:], true
		s = s[:i]
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		u.query, u.hasQuery = s[i+1:], true
		s = s[:i]
	}
	if i := strings.IndexByte(s, ':'); i > 0 && validScheme(s[:i]) {
		u.scheme = s[:i]
		s = s[i+1:]
	}
	if strings.HasPrefix(s, "//") {
		u.hasAuthority = true
		rest := s[2:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			u.authority, u.path = rest[:j], rest[j:]
		} else {
			u.authority = rest
		}
	} else {
		u.path = s
	}
	return u
}

func (u uriParts) String() string {
	var b strings.Builder
	if u.scheme != "" {
		b.WriteString(u.scheme)
		b.WriteByte(':')
	}
	if u.hasAuthority {
		b.WriteString("//")
		b.WriteString(u.authority)
	}
	b.WriteString(u.path)
	if u.hasQuery {
		b.WriteByte('?')
		b.WriteString(u.query)
	}
	if u.hasFragment {
		b.WriteByte('#')
		b.WriteString(u.fragment)
	}
	return b.String()
}

// removeDotSegments implements RFC 3986 section 5.2.4.
func removeDotSegments(path string) string {
	var out []string
	pop := func() {
		if len(out) > 0 {
			out = out[:len(out)-1]
		}
	}
	for path != "" {
		switch {
		case strings.HasPrefix(path, "../"):
			path = path[3:]
		case strings.HasPrefix(path, "./"):
			path = path[2:]
		case strings.HasPrefix(path, "/./"):
			path = path[2:]
		case path == "/.":
			path = "/"
		case strings.HasPrefix(path, "/../"):
			path = path[3:]
			pop()
		case path == "/..":
			path = "/"
			pop()
		case path == "." || path == "..":
			path = ""
		default:
			i := strings.IndexByte(path[1:], '/')
			if i < 0 {
				i = len(path)
			} else {
				i++
			}
			out = append(out, path[:i])
			path = path[i:]
		}
	}
	return strings.Join(out, "")
}

// Resolve resolves ref against base following RFC 3986 section 5.2 (strict
// mode). Absolute references come back with dot segments removed,
// protocol-relative references ("//host/x") take the base scheme, and query
// strings are kept verbatim. Leading and trailing whitespace is ignored. A
// relative ref with a base that has no scheme yields ErrBaseNotAbsolute.
func Resolve(base, ref string) (string, error) {
	r := splitURI(strings.TrimSpace(ref))
	if r.scheme != "" {
		if r.hasAuthority || strings.HasPrefix(r.path, "/") {
			r.path = removeDotSegments(r.path)
		}
		return r.String(), nil
	}
	b := splitURI(strings.TrimSpace(base))
	if b.scheme == "" {
		return "", ErrBaseNotAbsolute
	}
	t := uriParts{scheme: b.scheme, fragment: r.fragment, hasFragment: r.hasFragment}
	switch {
	case r.hasAuthority:
		t.authority, t.hasAuthority = r.authority, true
		t.path = removeDotSegments(r.path)
		t.query, t.hasQuery = r.query, r.hasQuery
	case r.path == "":
		t.authority, t.hasAuthority = b.authority, b.hasAuthority
		t.path = b.path
		if r.hasQuery {
			t.query, t.hasQuery = r.query, true
		} else {
			t.query, t.hasQuery = b.query, b.hasQuery
		}
	default:
		t.authority, t.hasAuthority = b.authority, b.hasAuthority
		if strings.HasPrefix(r.path, "/") {
			t.path = removeDotSegments(r.path)
		} else {
			merged := r.path
			if b.hasAuthority && b.path == "" {
				merged = "/" + r.path
			} else if i := strings.LastIndexByte(b.path, '/'); i >= 0 {
				merged = b.path[:i+1] + r.path
			}
			t.path = removeDotSegments(merged)
		}
		t.query, t.hasQuery = r.query, r.hasQuery
	}
	return t.String(), nil
}
