// Package service validates the shared service plugins of the unified source
// collection: the Trakt, TMDB and Bangumi kinds, plus anything later.
//
// A service plugin is JavaScript with a manifest. The manifest is the part the
// host can check without running anything, so it is where identity, the entry
// point and the requested permissions live. Nothing here executes code; a host
// that does gets its grants from here first.
package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
)

// MaxManifestBytes bounds a manifest: it is a small description, and one that
// needs more than this is not something a host should try to interpret.
const MaxManifestBytes = 64 << 10

// Permission names one capability a service may ask the host for.
type Permission string

const (
	// PermissionNetwork lets the service make HTTP requests through the host.
	// It never gets a raw socket.
	PermissionNetwork Permission = "network"
	// PermissionStorage lets it keep small values between runs.
	PermissionStorage Permission = "storage"
	// PermissionSecrets lets it read the account values the user entered for it.
	PermissionSecrets Permission = "secrets"
)

var knownPermissions = map[Permission]bool{
	PermissionNetwork: true,
	PermissionStorage: true,
	PermissionSecrets: true,
}

// Kinds the collection may declare for a service entry.
const Kind = "service.js"

// idPattern keeps an identity usable as a file name and inside a log line:
// lower-case words joined by single dots, dots or dashes.
var idPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*$`)

// Manifest is what a service plugin declares about itself.
type Manifest struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Kind        string       `json:"kind"`
	Entry       string       `json:"entry"`
	Permissions []Permission `json:"permissions"`
}

// ParseManifest reads and checks a manifest. Unknown fields are rejected: a
// service that believes it asked for something this host ignores would run with
// less than it assumes.
func ParseManifest(data []byte) (*Manifest, error) {
	if len(data) == 0 {
		return nil, errors.New("service: empty manifest")
	}
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("service: manifest is %d bytes, over the %d limit", len(data), MaxManifestBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var m Manifest
	if err := decoder.Decode(&m); err != nil {
		return nil, fmt.Errorf("service: invalid manifest: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("service: manifest has trailing data")
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	switch {
	case !idPattern.MatchString(m.ID):
		return fmt.Errorf("service: id %q is not a valid identity", m.ID)
	case strings.TrimSpace(m.Name) == "":
		return errors.New("service: manifest has no name")
	case strings.TrimSpace(m.Version) == "":
		return errors.New("service: manifest has no version")
	case m.Kind != Kind:
		return fmt.Errorf("service: kind %q is not %q", m.Kind, Kind)
	}
	if err := validEntry(m.Entry); err != nil {
		return err
	}
	seen := map[Permission]bool{}
	counts := map[Permission]int{}
	for _, p := range m.Permissions {
		counts[p]++
		if !knownPermissions[p] {
			return fmt.Errorf("service: permission %q is not known to this host", p)
		}
		if seen[p] {
			return fmt.Errorf("service: permission %q is listed twice", p)
		}
		seen[p] = true
	}
	return nil
}

// validEntry keeps the entry point inside the package: an absolute path, a
// parent segment or a Windows separator would read a file the plugin does not
// ship.
func validEntry(entry string) error {
	if entry == "" {
		return errors.New("service: manifest has no entry")
	}
	if strings.ContainsAny(entry, "\\:") {
		return fmt.Errorf("service: entry %q uses a path separator this host does not accept", entry)
	}
	if path.IsAbs(entry) || strings.HasPrefix(entry, "/") {
		return fmt.Errorf("service: entry %q is absolute", entry)
	}
	cleaned := path.Clean(entry)
	if cleaned != entry || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("service: entry %q leaves the package", entry)
	}
	return nil
}

// Grants is what the host actually allows, after both the manifest's request
// and the host's own decision. A permission is usable only when it is in both,
// so a host can grant less than a service asks for without rewriting it.
type Grants struct {
	requested map[Permission]bool
	granted   map[Permission]bool
}

// Grant builds the effective permissions for one manifest.
//
// A permission the host does not recognise is refused rather than ignored: the
// host and the service disagree about what is being allowed, and guessing which
// one is right is how a sandbox gets a hole.
func Grant(m *Manifest, allowed []Permission) (Grants, error) {
	if m == nil {
		return Grants{}, errors.New("service: no manifest")
	}
	g := Grants{requested: map[Permission]bool{}, granted: map[Permission]bool{}}
	for _, p := range m.Permissions {
		g.requested[p] = true
	}
	for _, p := range allowed {
		if !knownPermissions[p] {
			return Grants{}, fmt.Errorf("service: host allowed unknown permission %q", p)
		}
		g.granted[p] = true
	}
	return g, nil
}

// Allows reports whether the service may use p now.
func (g Grants) Allows(p Permission) bool {
	return g.requested[p] && g.granted[p]
}

// Effective lists the permissions in force, sorted, so a log line or a settings
// screen does not depend on declaration order.
func (g Grants) Effective() []Permission {
	out := make([]Permission, 0, len(g.requested))
	for p := range g.requested {
		if g.granted[p] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Denied lists what the service asked for and the host did not allow, sorted.
// A host shows this instead of silently running with fewer capabilities.
func (g Grants) Denied() []Permission {
	out := make([]Permission, 0, len(g.requested))
	for p := range g.requested {
		if !g.granted[p] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
