package mobile

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestSourceCollectionMobileBridge(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}})
	data := []byte("plugin bytes")
	digest := sha256.Sum256(data)
	index := `{"$schema":"veneracat-source/1-draft","id":"source","name":"Source","version":"1","entries":[{"id":"plugin","kind":"video-source","name":"Plugin","version":"1","status":"experimental","supports":{"platforms":["ios","windows"],"requiresCapabilities":["video-source.js"]},"artifact":{"url":"files/a.js","sha256":"` + hex.EncodeToString(digest[:]) + `","bytes":12}}]}`
	output, err := c.ParseSourceCollection(index, "https://example.test/source.json", "ios", `["video-source.js"]`)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Collection struct {
			Entries []struct {
				Artifact json.RawMessage `json:"artifact"`
			} `json:"entries"`
		} `json:"collection"`
		Compatibility []struct {
			Compatible bool     `json:"compatible"`
			Missing    []string `json:"missing"`
		} `json:"compatibility"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Compatibility) != 1 || !result.Compatibility[0].Compatible || !strings.Contains(output, "https://example.test/files/a.js") {
		t.Fatalf("unexpected result: %s", output)
	}
	artifact := string(result.Collection.Entries[0].Artifact)
	if err := c.VerifySourceArtifact(artifact, base64.StdEncoding.EncodeToString(data)); err != nil {
		t.Fatal(err)
	}
	if err := c.VerifySourceArtifact(artifact, base64.StdEncoding.EncodeToString([]byte("plugin bytez"))); err == nil {
		t.Fatal("accepted altered artifact")
	}
	output, err = c.ParseSourceCollection(index, "https://example.test/source.json", "windows", `[]`)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Compatibility[0].Compatible || len(result.Compatibility[0].Missing) != 1 || result.Compatibility[0].Missing[0] != "video-source.js" {
		t.Fatalf("missing capability not reported: %s", output)
	}
	if _, err = c.ParseSourceCollection(index, "https://example.test/source.json", "ios", `null`); err == nil {
		t.Fatal("accepted null capabilities")
	}
	if err = c.VerifySourceArtifact(artifact, "not-base64!"); err == nil {
		t.Fatal("accepted invalid base64")
	}
}
