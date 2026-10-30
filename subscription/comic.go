// Package subscription reads comic-source subscription indexes ("index.json")
// in the format used by official Venera and community lists such as
// venera-app/venera-configs and laincat/vensrc: a JSON array of entries
//
//	{"name", "key", "version", "fileName", "url"?, "minAppVersion"?, "description"?}
//
// The client fetches the index and the scripts; this package only parses,
// validates and compares.
package subscription

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/laincat/veneracat-core/attribution"
	"github.com/laincat/veneracat-core/comic/version"
)

// Entry is one source of a comic index.
type Entry struct {
	Name          string `json:"name"`
	Key           string `json:"key"`
	Version       string `json:"version"`
	FileName      string `json:"fileName,omitempty"`
	URL           string `json:"url"` // always absolute after Parse
	MinAppVersion string `json:"minAppVersion,omitempty"`
	Description   string `json:"description,omitempty"`
	// Attribution from optional "author" and "homepage" fields. It is the
	// fallback for what the script itself declares.
	Attribution attribution.Info `json:"attribution"`
}

// Problem is an entry that was skipped. Community indexes are hand-edited,
// so one bad entry must not hide the others; problems are shown instead.
type Problem struct {
	Index  int    `json:"index"`
	Key    string `json:"key,omitempty"`
	Reason string `json:"reason"`
}

// ComicIndex is a parsed index.
type ComicIndex struct {
	Entries  []Entry   `json:"entries"`
	Problems []Problem `json:"problems,omitempty"`
}

// MaxComicEntries bounds an index.
const MaxComicEntries = 5000

// ErrNotComicIndex means the document is not a comic index. A vcvideo.json
// video index is an object, so it is rejected here and the caller can try
// subscription.Parse instead.
var ErrNotComicIndex = errors.New("subscription: not a comic source index")

// ParseComic parses an index fetched from indexURL.
func ParseComic(data []byte, indexURL string) (*ComicIndex, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, ErrNotComicIndex
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return nil, fmt.Errorf("subscription: invalid JSON: %w", err)
	}
	if len(raw) > MaxComicEntries {
		return nil, fmt.Errorf("subscription: more than %d entries", MaxComicEntries)
	}
	base, err := url.Parse(strings.TrimSpace(indexURL))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, fmt.Errorf("subscription: invalid index URL %q", indexURL)
	}
	idx := &ComicIndex{Entries: []Entry{}}
	seen := map[string]bool{}
	for i, r := range raw {
		var m map[string]any
		if err := json.Unmarshal(r, &m); err != nil {
			idx.Problems = append(idx.Problems, Problem{Index: i, Reason: "entry is not an object"})
			continue
		}
		e := Entry{Name: field(m, "name"), Key: field(m, "key"), Version: field(m, "version"),
			FileName: field(m, "fileName"), MinAppVersion: field(m, "minAppVersion"), Description: field(m, "description")}
		e.Attribution, _ = attribution.Parse(m["author"], m["homepage"])
		problem := func(reason string) {
			idx.Problems = append(idx.Problems, Problem{Index: i, Key: e.Key, Reason: reason})
		}
		switch {
		case e.Name == "":
			problem("name is missing")
			continue
		case e.Key == "":
			problem("key is missing")
			continue
		case !validKey(e.Key):
			problem("key may only contain letters, digits and _")
			continue
		case seen[e.Key]:
			problem("duplicate key")
			continue
		}
		u, err := resolveEntryURL(base, field(m, "url"), e.FileName)
		if err != nil {
			problem(err.Error())
			continue
		}
		e.URL = u
		seen[e.Key] = true
		idx.Entries = append(idx.Entries, e)
	}
	return idx, nil
}

func field(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

func validKey(k string) bool {
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !(c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return k != ""
}

// resolveEntryURL follows the official client: a valid absolute url wins;
// otherwise fileName is resolved against the index location (the index URL
// with its last path segment removed, or the URL itself when it has no path).
func resolveEntryURL(base *url.URL, raw, fileName string) (string, error) {
	if raw != "" {
		if u, err := url.Parse(raw); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			if u.User != nil {
				return "", errors.New("url must not contain credentials")
			}
			return u.String(), nil
		}
	}
	if fileName == "" {
		return "", errors.New("neither a valid url nor a fileName")
	}
	if strings.Contains(fileName, "..") || strings.HasPrefix(fileName, "/") || strings.Contains(fileName, "\\") {
		return "", errors.New("fileName must be a plain relative name")
	}
	dir := *base
	dir.RawQuery, dir.Fragment = "", ""
	if dir.Path == "" {
		dir.Path = "/"
	} else if !strings.HasSuffix(dir.Path, "/") {
		dir.Path = dir.Path[:strings.LastIndex(dir.Path, "/")+1]
	}
	ref, err := url.Parse(fileName)
	if err != nil || ref.Scheme != "" || ref.Host != "" {
		return "", errors.New("fileName must be a plain relative name")
	}
	return dir.ResolveReference(ref).String(), nil
}

// Installed is a source the client has installed.
type Installed struct {
	Key     string
	Version string
	// IndexURL is the index the source was installed from, if any. Updates
	// are offered only from that index unless the user re-links the source.
	IndexURL string
}

// Status of an index entry relative to the installed sources.
type Status string

const (
	StatusNew       Status = "new"       // not installed
	StatusUpdate    Status = "update"    // installed, index version is newer
	StatusInstalled Status = "installed" // installed, same or newer locally
	StatusOther     Status = "other"     // installed from a different index
	StatusTooNew    Status = "too-new"   // needs a newer app (minAppVersion)
)

// Row is an entry with its status, for the index screen.
type Row struct {
	Entry
	Status           Status `json:"status"`
	InstalledVersion string `json:"installedVersion,omitempty"`
}

// Compare annotates every entry of idx (fetched from indexURL) for an app
// at appVersion with the given installed sources.
func (idx *ComicIndex) Compare(indexURL, appVersion string, installed []Installed) []Row {
	byKey := map[string]Installed{}
	for _, s := range installed {
		byKey[s.Key] = s
	}
	app := strings.SplitN(appVersion, "-", 2)[0]
	rows := make([]Row, 0, len(idx.Entries))
	for _, e := range idx.Entries {
		r := Row{Entry: e}
		s, ok := byKey[e.Key]
		switch {
		case e.MinAppVersion != "" && version.Newer(e.MinAppVersion, app):
			r.Status = StatusTooNew
		case !ok:
			r.Status = StatusNew
		case s.IndexURL != "" && s.IndexURL != indexURL:
			r.Status = StatusOther
		case version.Newer(e.Version, s.Version):
			r.Status = StatusUpdate
		default:
			r.Status = StatusInstalled
		}
		if ok {
			r.InstalledVersion = s.Version
		}
		rows = append(rows, r)
	}
	return rows
}

// UpdateMismatch is returned when a downloaded script declares another key
// than the entry it was installed for.
type UpdateMismatch struct{ Want, Got string }

func (e *UpdateMismatch) Error() string {
	return fmt.Sprintf("subscription: source %s update key mismatch: %s", e.Want, e.Got)
}

// CheckUpdate compares installed metadata with the metadata of a freshly
// downloaded script (read with comic/js.ClassName and LoadSource, or any
// metadata reader). It reports whether the download is a newer version of
// the same source.
func CheckUpdate(installedKey, installedVersion, downloadedKey, downloadedVersion string) (bool, error) {
	if downloadedKey != installedKey {
		return false, &UpdateMismatch{Want: installedKey, Got: downloadedKey}
	}
	return version.Newer(downloadedVersion, installedVersion), nil
}
