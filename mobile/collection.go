package mobile

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/laincat/veneracat-core/subscription"
)

// ParseSourceCollection returns the shared collection and compatibility rows.
// The client provides its platform and capabilities; this does not fetch,
// install or authorize any entries.
func (c *Core) ParseSourceCollection(data, sourceURL, platform, capabilitiesJSON string) (_ string, err error) {
	defer guard(&err)
	if strings.TrimSpace(platform) == "" {
		return "", fmt.Errorf("collection: platform is required")
	}
	var capabilities []string
	if err := json.Unmarshal([]byte(capabilitiesJSON), &capabilities); err != nil || capabilities == nil {
		return "", fmt.Errorf("collection: capabilities must be a JSON array of strings")
	}
	collection, err := subscription.ParseCollection([]byte(data), sourceURL)
	if err != nil {
		return "", err
	}
	type row struct {
		ID         string   `json:"id"`
		Compatible bool     `json:"compatible"`
		Missing    []string `json:"missing"`
	}
	rows := make([]row, 0, len(collection.Entries))
	for _, entry := range collection.Entries {
		ok, missing := entry.Compatible(platform, capabilities)
		rows = append(rows, row{entry.ID, ok, missing})
	}
	return encode(struct {
		Collection    *subscription.Collection `json:"collection"`
		Compatibility []row                    `json:"compatibility"`
	}{collection, rows})
}

// VerifySourceArtifact verifies downloaded bytes against an artifact record.
// The client owns download and installation; base64 preserves arbitrary bytes.
func (c *Core) VerifySourceArtifact(artifactJSON, dataBase64 string) (err error) {
	defer guard(&err)
	var artifact subscription.CollectionArtifact
	if err = json.Unmarshal([]byte(artifactJSON), &artifact); err != nil {
		return fmt.Errorf("collection: invalid artifact: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(dataBase64)
	if err != nil {
		return fmt.Errorf("collection: invalid artifact data: %w", err)
	}
	return artifact.Verify(data)
}
