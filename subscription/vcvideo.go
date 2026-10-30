// Package subscription reads vcvideo.json video subscription indexes.
//
// A vcvideo.json file lists video plugins. It is distinct from comic
// subscription indexes (a bare JSON array, as used by Venera and vensrc) so
// that comic-only clients never try to import a video plugin.
//
// Discoverability: a repository that publishes a vcvideo.json should add the
// GitHub topic "vcvideo". Clients and tools can then search
// "filename:vcvideo.json" or "topic:vcvideo" to find third-party indexes.
// The top-level "$schema" value is a fixed string so a code search for it
// returns only real indexes.
package subscription

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/laincat/veneracat-core/attribution"
)

// SchemaID is the required value of "$schema". It is a plain identifier, not
// a URL, so it does not depend on a domain anyone has to keep registered.
const SchemaID = "vcvideo/1"

// FileName is the conventional file name.
const FileName = "vcvideo.json"

// Plugin types a vcvideo.json may list.
const (
	TypeJS     = "js"          // a VideoSource JavaScript plugin
	TypeKazumi = "kazumi-rule" // a Kazumi rule JSON file
)

// Index is a parsed vcvideo.json.
type Index struct {
	Schema      string          `json:"$schema"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Homepage    string          `json:"homepage,omitempty"`
	Author      json.RawMessage `json:"author,omitempty"`
	Plugins     []Plugin        `json:"plugins"`
	// Attribution of the index maintainer (author, homepage), filled by Parse.
	Attribution attribution.Info `json:"attribution"`
}

// Plugin is one entry of an index.
type Plugin struct {
	Type    string `json:"type"`
	Key     string `json:"key"`
	Name    string `json:"name"`
	Version string `json:"version"`
	// URL of the plugin file. Relative URLs resolve against the index URL.
	URL string `json:"url"`
	// SHA256 of the plugin file, lower-case hex. Optional but recommended;
	// when present the client must reject a mismatching download.
	SHA256         string   `json:"sha256,omitempty"`
	MinCoreVersion string   `json:"minCoreVersion,omitempty"`
	Description    string   `json:"description,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Deprecated     bool     `json:"deprecated,omitempty"`
	// Author and Homepage of the plugin, optional. A plugin's own
	// declaration takes precedence over these.
	Author      json.RawMessage  `json:"author,omitempty"`
	Homepage    string           `json:"homepage,omitempty"`
	Attribution attribution.Info `json:"attribution"`
}

var (
	keyPattern    = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// MaxPlugins bounds an index so a hostile file cannot exhaust memory in UI.
const MaxPlugins = 2000

// Parse decodes and validates an index fetched from indexURL. Entries with
// invalid fields make the whole index invalid: a partially trusted index is
// harder to reason about than a rejected one.
func Parse(data []byte, indexURL string) (*Index, error) {
	var idx Index
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&idx); err != nil {
		return nil, fmt.Errorf("vcvideo: invalid JSON: %w", err)
	}
	if idx.Schema != SchemaID {
		return nil, fmt.Errorf("vcvideo: $schema must be %q", SchemaID)
	}
	if strings.TrimSpace(idx.Name) == "" {
		return nil, errors.New("vcvideo: name is required")
	}
	if len(idx.Plugins) > MaxPlugins {
		return nil, fmt.Errorf("vcvideo: more than %d plugins", MaxPlugins)
	}
	base, err := url.Parse(indexURL)
	if err != nil {
		return nil, fmt.Errorf("vcvideo: invalid index URL: %w", err)
	}
	homepage, _ := json.Marshal(idx.Homepage)
	idx.Attribution, _ = attribution.ParseJSON(idx.Author, homepage)
	idx.Homepage = idx.Attribution.Homepage
	seen := map[string]bool{}
	for i := range idx.Plugins {
		p := &idx.Plugins[i]
		where := fmt.Sprintf("vcvideo: plugins[%d]", i)
		switch p.Type {
		case TypeJS, TypeKazumi:
		default:
			return nil, fmt.Errorf("%s: unknown type %q", where, p.Type)
		}
		if !keyPattern.MatchString(p.Key) {
			return nil, fmt.Errorf("%s: key %q must match %s", where, p.Key, keyPattern)
		}
		if seen[p.Key] {
			return nil, fmt.Errorf("%s: duplicate key %q", where, p.Key)
		}
		seen[p.Key] = true
		if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Version) == "" {
			return nil, fmt.Errorf("%s: name and version are required", where)
		}
		if p.SHA256 != "" && !sha256Pattern.MatchString(p.SHA256) {
			return nil, fmt.Errorf("%s: sha256 must be 64 lower-case hex digits", where)
		}
		ref, err := url.Parse(strings.TrimSpace(p.URL))
		if err != nil || p.URL == "" {
			return nil, fmt.Errorf("%s: invalid url", where)
		}
		abs := base.ResolveReference(ref)
		if abs.Scheme != "https" && abs.Scheme != "http" || abs.Host == "" || abs.User != nil {
			return nil, fmt.Errorf("%s: url must be http(s) without credentials", where)
		}
		p.URL = abs.String()
		ph, _ := json.Marshal(p.Homepage)
		p.Attribution, _ = attribution.ParseJSON(p.Author, ph)
		p.Homepage = p.Attribution.Homepage
	}
	return &idx, nil
}
