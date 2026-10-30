// Package attribution describes who made a source or plugin and where it
// lives. It is shared by comic sources, video plugins, Kazumi rules,
// built-ins and subscription index entries.
//
// Both fields are optional: official comic sources and published Kazumi
// rules carry neither, so a missing value is shown to the user ("author
// unknown") rather than rejected. When a script and its subscription entry
// both give a value, the script wins (Merge).
package attribution

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Author is one person or group.
type Author struct {
	Name string `json:"name"`
	// URL is a profile or contact page; optional.
	URL string `json:"url,omitempty"`
}

// Info is the attribution of one plugin.
type Info struct {
	Authors  []Author `json:"authors,omitempty"`
	Homepage string   `json:"homepage,omitempty"`
}

// Limits keep hostile metadata from flooding the UI.
const (
	MaxAuthors   = 16
	MaxNameRunes = 100
	MaxURLBytes  = 2048
)

// Missing reports what the UI should flag.
func (i Info) Missing() (author, homepage bool) {
	return len(i.Authors) == 0, i.Homepage == ""
}

// Merge returns primary with empty parts filled from fallback. The script's
// own declaration is primary; the subscription entry is the fallback.
func Merge(primary, fallback Info) Info {
	out := primary
	if len(out.Authors) == 0 {
		out.Authors = append([]Author(nil), fallback.Authors...)
	}
	if out.Homepage == "" {
		out.Homepage = fallback.Homepage
	}
	return out
}

// Parse normalises the raw "author" and "homepage" values a plugin or index
// declares. author may be:
//
//	"Name"                                  a single name
//	{"name": "Name", "url": "https://..."}  one author
//	[ ... ]                                 a list of the two forms above
//
// Invalid parts are dropped with a warning instead of failing the install:
// attribution is informational. URLs must be http(s) without credentials.
func Parse(author, homepage any) (Info, []string) {
	var info Info
	var warn []string
	add := func(a Author) {
		a.Name = clip(strings.TrimSpace(a.Name), MaxNameRunes)
		if a.Name == "" {
			warn = append(warn, "author without a name ignored")
			return
		}
		if a.URL != "" {
			u, err := cleanURL(a.URL)
			if err != nil {
				warn = append(warn, fmt.Sprintf("author %q url ignored: %v", a.Name, err))
				u = ""
			}
			a.URL = u
		}
		if len(info.Authors) >= MaxAuthors {
			warn = append(warn, fmt.Sprintf("more than %d authors, extra ignored", MaxAuthors))
			return
		}
		info.Authors = append(info.Authors, a)
	}
	var one func(v any, top bool)
	one = func(v any, top bool) {
		switch x := v.(type) {
		case nil:
		case string:
			if strings.TrimSpace(x) != "" {
				add(Author{Name: x})
			}
		case map[string]any:
			name, _ := x["name"].(string)
			u, _ := x["url"].(string)
			add(Author{Name: name, URL: u})
		case []any:
			if !top {
				warn = append(warn, "nested author list ignored")
				return
			}
			for _, item := range x {
				one(item, false)
			}
		default:
			warn = append(warn, fmt.Sprintf("author of type %T ignored", v))
		}
	}
	one(author, true)
	if h, ok := homepage.(string); ok && strings.TrimSpace(h) != "" {
		u, err := cleanURL(h)
		if err != nil {
			warn = append(warn, fmt.Sprintf("homepage ignored: %v", err))
		} else {
			info.Homepage = u
		}
	} else if homepage != nil {
		if _, isString := homepage.(string); !isString {
			warn = append(warn, fmt.Sprintf("homepage of type %T ignored", homepage))
		}
	}
	return info, warn
}

// ParseJSON is Parse for raw JSON values (from an index entry or a rule).
func ParseJSON(author, homepage json.RawMessage) (Info, []string) {
	var a, h any
	if len(author) > 0 {
		if err := json.Unmarshal(author, &a); err != nil {
			return Info{}, []string{"author is not valid JSON"}
		}
	}
	if len(homepage) > 0 {
		if err := json.Unmarshal(homepage, &h); err != nil {
			return Info{}, []string{"homepage is not valid JSON"}
		}
	}
	return Parse(a, h)
}

func cleanURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > MaxURLBytes {
		return "", fmt.Errorf("longer than %d bytes", MaxURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("scheme %q is not http(s)", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host")
	}
	if u.User != nil {
		return "", fmt.Errorf("contains credentials")
	}
	return u.String(), nil
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
