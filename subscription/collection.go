package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// CollectionSchema identifies the draft shared source collection format.
const CollectionSchema = "veneracat-source/1-draft"

// Collection describes one publisher's cross-platform source. Parsing does not
// fetch artifacts or grant permissions; those remain the client's responsibility.
type Collection struct {
	Schema      string            `json:"$schema"`
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Publisher   json.RawMessage   `json:"publisher,omitempty"`
	Description string            `json:"description,omitempty"`
	Entries     []CollectionEntry `json:"entries"`
}

type CollectionEntry struct {
	ID           string              `json:"id"`
	Kind         string              `json:"kind"`
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	Status       string              `json:"status"`
	Key          string              `json:"key,omitempty"`
	License      string              `json:"license,omitempty"`
	Supports     CollectionSupport   `json:"supports"`
	Artifact     *CollectionArtifact `json:"artifact,omitempty"`
	Design       *CollectionDesign   `json:"design,omitempty"`
	Verification json.RawMessage     `json:"verification,omitempty"`
}

type CollectionSupport struct {
	Platforms            []string `json:"platforms"`
	RequiresCapabilities []string `json:"requiresCapabilities"`
}

type CollectionArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type CollectionDesign struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// ParseCollection resolves artifact references against the collection URL.
// Identity is the pair of collection ID and entry ID, not a download URL.
func ParseCollection(data []byte, sourceURL string) (*Collection, error) {
	var c Collection
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("collection: invalid JSON: %w", err)
	}
	if c.Schema != CollectionSchema {
		return nil, fmt.Errorf("collection: unsupported schema %q", c.Schema)
	}
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Version) == "" {
		return nil, fmt.Errorf("collection: id, name and version are required")
	}
	base, err := url.Parse(sourceURL)
	if err != nil || base == nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, fmt.Errorf("collection: invalid source URL")
	}
	seen := make(map[string]bool, len(c.Entries))
	resolve := func(raw string) (string, error) {
		ref, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || strings.TrimSpace(raw) == "" {
			return "", fmt.Errorf("invalid URL")
		}
		u := base.ResolveReference(ref)
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", fmt.Errorf("URL must use HTTP or HTTPS")
		}
		return u.String(), nil
	}
	for i := range c.Entries {
		e := &c.Entries[i]
		where := fmt.Sprintf("collection: entries[%d]", i)
		if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.Kind) == "" || strings.TrimSpace(e.Name) == "" || strings.TrimSpace(e.Version) == "" || strings.TrimSpace(e.Status) == "" {
			return nil, fmt.Errorf("%s: id, kind, name, version and status are required", where)
		}
		if seen[e.ID] {
			return nil, fmt.Errorf("%s: duplicate id %q", where, e.ID)
		}
		seen[e.ID] = true
		if len(e.Supports.Platforms) == 0 || len(e.Supports.RequiresCapabilities) == 0 {
			return nil, fmt.Errorf("%s: platform and capability declarations are required", where)
		}
		for _, value := range append(append([]string(nil), e.Supports.Platforms...), e.Supports.RequiresCapabilities...) {
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("%s: empty compatibility declaration", where)
			}
		}
		if e.Artifact != nil {
			if e.Status == "planned" {
				return nil, fmt.Errorf("%s: planned entry cannot contain an artifact", where)
			}
			if !sha256Pattern.MatchString(e.Artifact.SHA256) || e.Artifact.Bytes < 0 {
				return nil, fmt.Errorf("%s: invalid artifact digest or size", where)
			}
			e.Artifact.URL, err = resolve(e.Artifact.URL)
			if err != nil {
				return nil, fmt.Errorf("%s: artifact: %w", where, err)
			}
		}
		if e.Design != nil {
			if !sha256Pattern.MatchString(e.Design.SHA256) {
				return nil, fmt.Errorf("%s: invalid design digest", where)
			}
			e.Design.URL, err = resolve(e.Design.URL)
			if err != nil {
				return nil, fmt.Errorf("%s: design: %w", where, err)
			}
		}
	}
	return &c, nil
}

// Verify checks downloaded artifact bytes before a client installs them.
func (a CollectionArtifact) Verify(data []byte) error {
	if int64(len(data)) != a.Bytes {
		return fmt.Errorf("collection: artifact size mismatch")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != a.SHA256 {
		return fmt.Errorf("collection: artifact digest mismatch")
	}
	return nil
}

// Compatible reports missing platform or capabilities without changing the
// collection. Unknown capabilities are unsupported until the host provides them.
func (e CollectionEntry) Compatible(platform string, capabilities []string) (bool, []string) {
	platformOK := false
	for _, v := range e.Supports.Platforms {
		if v == platform {
			platformOK = true
			break
		}
	}
	missing := []string{}
	if !platformOK {
		missing = append(missing, "platform:"+platform)
	}
	available := make(map[string]bool, len(capabilities))
	for _, v := range capabilities {
		available[v] = true
	}
	for _, v := range e.Supports.RequiresCapabilities {
		if !available[v] {
			missing = append(missing, v)
		}
	}
	return len(missing) == 0, missing
}
