package mobile

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serviceZip(t *testing.T, id string) string {
	t.Helper()
	manifest := `{"id":"` + id + `","name":"Demo","version":"1","kind":"service.js","entry":"index.mjs","permissions":["storage"]}`
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"service.json": manifest,
		"index.mjs":    "export function handle() { return { ok: true }; }",
	} {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// The whole package path a client needs: install, list, read and remove.
func TestServicePackageRoundTrip(t *testing.T) {
	dir := t.TempDir()
	out, err := ServiceInstall(dir, "file:demo.zip", "abc", serviceZip(t, "demo"))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	var rec struct {
		ID      string   `json:"id"`
		Version string   `json:"version"`
		SHA256  string   `json:"sha256"`
		Perms   []string `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("record %q: %v", out, err)
	}
	if rec.ID != "demo" || rec.Version != "1" || rec.SHA256 != "abc" {
		t.Fatalf("record = %+v", rec)
	}
	if len(rec.Perms) != 1 || rec.Perms[0] != "storage" {
		t.Fatalf("permissions = %v", rec.Perms)
	}
	if _, err := os.Stat(filepath.Join(dir, "services", "demo", "index.mjs")); err != nil {
		t.Fatalf("installed file: %v", err)
	}

	list, err := ServiceList(dir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(list, `"id":"demo"`) {
		t.Fatalf("list = %s", list)
	}

	source, err := ServiceSource(dir, "demo")
	if err != nil || !strings.Contains(source, "handle") {
		t.Fatalf("source = %q (%v)", source, err)
	}

	if err := ServiceRemove(dir, "demo"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	empty, err := ServiceList(dir)
	if err != nil || empty != "[]" {
		t.Fatalf("list after remove = %q (%v)", empty, err)
	}
}

func TestServiceInstallRefusesBadInput(t *testing.T) {
	dir := t.TempDir()
	if _, err := ServiceInstall(dir, "x", "", "not base64!"); err == nil {
		t.Fatal("invalid base64 was accepted")
	}
	if _, err := ServiceInstall(dir, "x", "", base64.StdEncoding.EncodeToString([]byte("not a zip"))); err == nil {
		t.Fatal("a non-zip was accepted")
	}
	if err := ServiceRemove(dir, "missing"); err == nil {
		t.Fatal("removing an absent service was accepted")
	}
	if _, err := ServiceManifest(dir, "missing"); err == nil {
		t.Fatal("a missing manifest was returned")
	}
}
