package service

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"
)

// buildZip writes a zip archive holding the given files.
func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
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
	return buf.Bytes()
}

const zipManifest = `{"id":"demo","name":"Demo","version":"1","kind":"service.js","entry":"index.mjs","permissions":["network"]}`

func TestReadPackageZipRunsTheService(t *testing.T) {
	archive := buildZip(t, map[string]string{
		ManifestFileName: zipManifest,
		"index.mjs":      "export function handle(method) { return { method: method }; }",
	})
	m, source, files, err := ReadPackageZip(archive)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if m.ID != "demo" || !strings.Contains(source, "handle") || len(files) != 2 {
		t.Fatalf("m=%+v source=%q files=%d", m, source, len(files))
	}
	rt := NewRuntime(Host{}, PermissionNetwork)
	defer rt.Close()
	if err := rt.Load(context.Background(), m, source); err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := rt.Call(context.Background(), "ping", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(out), "ping") {
		t.Fatalf("result = %s", out)
	}
}

func TestReadPackageZipRefusesWhatItCannotUse(t *testing.T) {
	cases := map[string]map[string]string{
		"no manifest":  {"index.mjs": "export function handle() {}"},
		"no entry":     {ManifestFileName: zipManifest},
		"bad manifest": {ManifestFileName: `{"id":"Demo"}`, "index.mjs": "export function handle() {}"},
		"entry elsewhere": {
			ManifestFileName: `{"id":"demo","name":"Demo","version":"1","kind":"service.js","entry":"sub/index.mjs"}`,
			"index.mjs":      "export function handle() {}",
		},
		"traversal": {
			ManifestFileName: zipManifest,
			"index.mjs":      "export function handle() {}",
			"../escape.mjs":  "x",
		},
		"absolute": {
			ManifestFileName: zipManifest,
			"index.mjs":      "export function handle() {}",
			"/etc/passwd":    "x",
		},
	}
	for name, files := range cases {
		archive := buildZip(t, files)
		if _, _, _, err := ReadPackageZip(archive); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, _, err := ReadPackageZip(nil); err == nil {
		t.Error("an empty archive was accepted")
	}
	if _, _, _, err := ReadPackageZip([]byte("not a zip")); err == nil {
		t.Error("a non-zip was accepted")
	}
}

func TestReadPackageZipKeepsExtraFiles(t *testing.T) {
	archive := buildZip(t, map[string]string{
		ManifestFileName: zipManifest,
		"index.mjs":      "export function handle() { return null; }",
		"lib/helper.mjs": "export const helper = 1;",
	})
	_, _, files, err := ReadPackageZip(archive)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, ok := files["lib/helper.mjs"]; !ok {
		t.Fatalf("files = %v", files)
	}
}

func TestValidFilePath(t *testing.T) {
	for _, name := range []string{"index.mjs", "lib/helper.mjs", ManifestFileName} {
		if err := ValidFilePath(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"../x", "/x", "./x", "a\\b", "", "a/../../x"} {
		if err := ValidFilePath(name); err == nil {
			t.Errorf("%q: accepted", name)
		}
	}
}
