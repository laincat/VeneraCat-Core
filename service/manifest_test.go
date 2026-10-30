package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func manifest(extra string) []byte {
	base := `{"id":"trakt","name":"Trakt","version":"1","kind":"service.js","entry":"index.mjs","permissions":["network","storage"]`
	return []byte(base + extra + "}")
}

func TestParseManifestAcceptsAWellFormedService(t *testing.T) {
	m, err := ParseManifest(manifest(""))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.ID != "trakt" || m.Entry != "index.mjs" || len(m.Permissions) != 2 {
		t.Fatalf("manifest = %+v", m)
	}
}

// A field the host does not understand is a field the service believes it set;
// ignoring it would run the service with less than it asked for.
func TestParseManifestRejectsUnknownFields(t *testing.T) {
	for _, extra := range []string{`,"sandbox":true`, `,"permissions":["network"],"timeout":5`} {
		if _, err := ParseManifest(manifest(extra)); err == nil {
			t.Fatalf("unknown field accepted: %s", extra)
		}
	}
}

func TestParseManifestRejectsBadIdentityAndEntry(t *testing.T) {
	cases := map[string]string{
		"trailing data":  `{"id":"a","name":"n","version":"1","kind":"service.js","entry":"i.mjs"} {}`,
		"upper id":       `{"id":"Trakt","name":"n","version":"1","kind":"service.js","entry":"i.mjs"}`,
		"empty id":       `{"id":"","name":"n","version":"1","kind":"service.js","entry":"i.mjs"}`,
		"no name":        `{"id":"a","name":" ","version":"1","kind":"service.js","entry":"i.mjs"}`,
		"no version":     `{"id":"a","name":"n","version":"","kind":"service.js","entry":"i.mjs"}`,
		"wrong kind":     `{"id":"a","name":"n","version":"1","kind":"comic-source.js","entry":"i.mjs"}`,
		"no entry":       `{"id":"a","name":"n","version":"1","kind":"service.js"}`,
		"parent entry":   `{"id":"a","name":"n","version":"1","kind":"service.js","entry":"../i.mjs"}`,
		"nested dot":     `{"id":"a","name":"n","version":"1","kind":"service.js","entry":"./i.mjs"}`,
		"absolute":       `{"id":"a","name":"n","version":"1","kind":"service.js","entry":"/i.mjs"}`,
		"backslash":      `{"id":"a","name":"n","version":"1","kind":"service.js","entry":"dir\\i.mjs"}`,
		"unknown perm":   `{"id":"a","name":"n","version":"1","kind":"service.js","entry":"i.mjs","permissions":["root"]}`,
		"duplicate perm": `{"id":"a","name":"n","version":"1","kind":"service.js","entry":"i.mjs","permissions":["network","network"]}`,
	}
	for name, body := range cases {
		if _, err := ParseManifest([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseManifestBoundsItsSize(t *testing.T) {
	big := manifest(`,"padding":"` + strings.Repeat("x", MaxManifestBytes) + `"`)
	if _, err := ParseManifest(big); err == nil {
		t.Fatal("an oversized manifest was accepted")
	}
	if _, err := ParseManifest(nil); err == nil {
		t.Fatal("an empty manifest was accepted")
	}
}

// A host may grant less than the service asks for, and the service then has to
// work with less: nothing may report a permission that is not in both sets.
func TestGrantsAreTheIntersection(t *testing.T) {
	m, err := ParseManifest(manifest(""))
	if err != nil {
		t.Fatal(err)
	}
	g, err := Grant(m, []Permission{PermissionNetwork})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Allows(PermissionNetwork) {
		t.Error("a requested and granted permission was refused")
	}
	if g.Allows(PermissionStorage) {
		t.Error("a requested but ungranted permission was allowed")
	}
	if got := strings.Join(permissionStrings(g.Effective()), ","); got != "network" {
		t.Errorf("effective = %s", got)
	}
	if got := strings.Join(permissionStrings(g.Denied()), ","); got != "storage" {
		t.Errorf("denied = %s", got)
	}
}

func TestGrantRefusesPermissionTheHostDoesNotKnow(t *testing.T) {
	m, err := ParseManifest(manifest(""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Grant(m, []Permission{"root"}); err == nil {
		t.Fatal("the host allowed a permission it does not know")
	}
	if _, err := Grant(nil, nil); err == nil {
		t.Fatal("granting without a manifest was accepted")
	}
}

func permissionStrings(list []Permission) []string {
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = string(p)
	}
	return out
}

// The manifest is also how a client shows what it will allow, so it must survive
// a round trip through JSON unchanged.
func TestManifestRoundTrips(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"id": "tmdb", "name": "TMDB", "version": "2", "kind": Kind, "entry": "src/index.mjs",
		"permissions": []string{"network"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	again, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(again); err != nil {
		t.Fatalf("re-parse: %v\n%s", err, again)
	}
}
