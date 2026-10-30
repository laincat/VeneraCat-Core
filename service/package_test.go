package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePackage(t *testing.T, manifest string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const packageManifest = `{"id":"demo","name":"Demo","version":"1","kind":"service.js","entry":"index.mjs","permissions":[]}`

func TestLoadPackageRunsTheEntry(t *testing.T) {
	dir := writePackage(t, packageManifest, map[string]string{
		"index.mjs": "export function handle(method) { return { method: method }; }",
	})
	r := NewRuntime(Host{})
	defer r.Close()
	m, err := LoadPackage(context.Background(), r, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m.ID != "demo" {
		t.Fatalf("manifest = %+v", m)
	}
	out, err := r.Call(context.Background(), "ping", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(out), "ping") {
		t.Fatalf("result = %s", out)
	}
}

func TestLoadPackageRefusesABadManifest(t *testing.T) {
	dir := writePackage(t, `{"id":"Demo"}`, map[string]string{"index.mjs": "export function handle() {}"})
	r := NewRuntime(Host{})
	defer r.Close()
	if _, err := LoadPackage(context.Background(), r, dir); err == nil {
		t.Fatal("a package with a bad manifest was loaded")
	}
}

func TestLoadPackageRefusesAMissingEntry(t *testing.T) {
	dir := writePackage(t, packageManifest, nil)
	r := NewRuntime(Host{})
	defer r.Close()
	if _, err := LoadPackage(context.Background(), r, dir); err == nil {
		t.Fatal("a package without its entry was loaded")
	}
}

// The manifest is what says which file to run; it may not point outside the
// package, however it was spelled.
func TestLoadPackageRefusesAnEntryOutsideThePackage(t *testing.T) {
	for _, entry := range []string{"../outside.mjs", "/etc/passwd", "sub/../../outside.mjs"} {
		dir := writePackage(t,
			`{"id":"demo","name":"Demo","version":"1","kind":"service.js","entry":"`+entry+`"}`,
			map[string]string{"index.mjs": "export function handle() {}"})
		r := NewRuntime(Host{})
		if _, err := LoadPackage(context.Background(), r, dir); err == nil {
			t.Errorf("entry %q was accepted", entry)
		}
		r.Close()
	}
}
