package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestSharedCollectionContract(t *testing.T) {
	body := []byte("plugin body")
	sum := sha256.Sum256(body)
	source := Collection{Schema: CollectionSchema, ID: "publisher.example", Name: "Example", Version: "1", Entries: []CollectionEntry{
		{ID: "video", Kind: "video-source", Name: "Video", Version: "1", Status: "experimental", Supports: CollectionSupport{Platforms: []string{"ios", "windows"}, RequiresCapabilities: []string{"video-source.js"}}, Artifact: &CollectionArtifact{URL: "packages/video.js", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body))}},
		{ID: "service", Kind: "service", Name: "Service", Version: "1", Status: "planned", Supports: CollectionSupport{Platforms: []string{"ios", "windows"}, RequiresCapabilities: []string{"service.v1"}}},
	}}
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCollection(data, "https://example.test/source/source.json")
	if err != nil {
		t.Fatal(err)
	}
	e := got.Entries[0]
	if e.Artifact.URL != "https://example.test/source/packages/video.js" {
		t.Fatal(e.Artifact.URL)
	}
	if err := e.Artifact.Verify(body); err != nil {
		t.Fatal(err)
	}
	if err := e.Artifact.Verify([]byte("plugin bodx")); err == nil {
		t.Fatal("accepted incorrect artifact digest")
	}
	if err := e.Artifact.Verify([]byte("short")); err == nil {
		t.Fatal("accepted incorrect artifact size")
	}
	if ok, _ := e.Compatible("ios", []string{"video-source.js"}); !ok {
		t.Fatal("supported platform rejected")
	}
	if ok, missing := e.Compatible("windows", nil); ok || len(missing) != 1 || missing[0] != "video-source.js" {
		t.Fatalf("unexpected capability result: %v %v", ok, missing)
	}
	if ok, _ := e.Compatible("macos", []string{"video-source.js"}); ok {
		t.Fatal("unsupported platform accepted")
	}
	if got.Entries[1].Artifact != nil {
		t.Fatal("plan became installable")
	}
}

func TestSharedCollectionRejectsInvalidInputs(t *testing.T) {
	base := `{"$schema":"veneracat-source/1-draft","id":"a","name":"A","version":"1","entries":[{"id":"x","kind":"service","name":"X","version":"1","status":"planned","supports":{"platforms":["ios"],"requiresCapabilities":["service.v1"]}}]}`
	tests := []string{
		strings.Replace(base, `"veneracat-source/1-draft"`, `"unknown"`, 1),
		strings.Replace(base, `"entries":[`, `"entries":[{"id":"x","kind":"service","name":"X","version":"1","status":"planned","supports":{"platforms":["ios"],"requiresCapabilities":["service.v1"]}},`, 1),
		strings.Replace(base, `"platforms":["ios"]`, `"platforms":[]`, 1),
		strings.Replace(base, `"status":"planned"`, `"status":"planned","artifact":{"url":"x","sha256":"`+strings.Repeat("a", 64)+`","bytes":1}`, 1),
		strings.Replace(base, `"status":"planned"`, `"status":"experimental","artifact":{"url":"file:///secret","sha256":"`+strings.Repeat("a", 64)+`","bytes":1}`, 1),
	}
	for _, input := range tests {
		if _, err := ParseCollection([]byte(input), "https://example.test/source.json"); err == nil {
			t.Fatalf("accepted invalid collection: %s", input)
		}
	}
	if _, err := ParseCollection([]byte(base), "file:///tmp/source.json"); err == nil {
		t.Fatal("accepted non-network source URL")
	}
}
