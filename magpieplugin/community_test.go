package magpieplugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// communityAuditDir holds audit copies of community packages. They are MIT but
// not vendored into this repository, so a checkout without them skips.
const communityAuditDir = "E:/Temp/nkprobe/magpie-community-audit"

// communityPackages is what happened when each package from the community
// repository's "no Node built-ins" shortlist was actually loaded. The static
// shortlist was wrong: index.mjs imports its own modules, and those import
// node:http, node:fs or node:crypto. Reading one file's imports is not an audit.
//
// Each entry says what this system must do with that package: load it and read a
// provider out of it, load it as something that is not a provider, or refuse it
// with an error that names what was missing.
type communityCase struct {
	name string
	// wantErr is a substring the refusal must contain; empty means it must load.
	wantErr string
	// wantProvider means the loaded package's plugin must declare a provider.
	wantProvider bool
}

var communityPackages = []communityCase{
	{name: "cloudflare-workers-ai", wantProvider: true},
	// Loads, but it is a calculator, not a provider: no auth to read.
	{name: "calc"},
	// Loads, and is a routing helper rather than a provider.
	{name: "river"},
	{name: "chat", wantErr: "node:http"},
	{name: "kimi-desktop", wantErr: "node:fs"},
	{name: "nous", wantErr: "node:crypto"},
	{name: "zen-free", wantErr: "node:crypto"},
	// Its entry is TypeScript, which this runtime does not parse. The error has
	// to say that, not report a syntax error that reads like a broken file.
	{name: "search1api", wantErr: "TypeScript"},
}

// bundleFromDir reads a package as a flat file table, which is what an installer
// produces. Paths are relative and slash-separated, like an archive.
func bundleFromDir(t *testing.T, dir string) *Bundle {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = body
		return nil
	})
	if err != nil {
		t.Skip("audit fixtures not present:", err)
	}
	if len(files) == 0 {
		t.Skip("audit fixtures not present")
	}
	b, err := NewBundle(files)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A real community package that uses only the Web APIs must load and describe
// itself. This is the shape of package that can actually run on a phone: no
// Node built-ins, no npm dependencies, requests through the host.
func TestCommunityPackagesBehaveAsRecorded(t *testing.T) {
	for _, c := range communityPackages {
		t.Run(c.name, func(t *testing.T) {
			b := bundleFromDir(t, filepath.Join(communityAuditDir, c.name))
			loader, err := Load(b)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("loaded, expected a refusal naming %q", c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("refusal does not name %q: %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("should load: %v", err)
			}
			inst, err := loader.NewInstance(Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer inst.Close()
			if err := inst.UseHost(&pluginHostStub{}); err != nil {
				t.Fatal(err)
			}
			if err := inst.InstallPluginBridge(); err != nil {
				t.Fatal(err)
			}
			answer, err := inst.hook("plugin", "{}")
			if err != nil {
				t.Fatalf("the package's plugin factory failed: %v", err)
			}
			if !c.wantProvider {
				return
			}
			plugin, ok := answer.(map[string]any)
			if !ok {
				t.Fatalf("answer = %T", answer)
			}
			auth, ok := plugin["auth"].(map[string]any)
			if !ok {
				t.Fatalf("auth = %T", plugin["auth"])
			}
			if got, _ := auth["provider"].(string); got == "" {
				t.Fatalf("no provider declared: %v", auth)
			}
			methods, _ := auth["methods"].([]any)
			if len(methods) > 0 {
				first, _ := methods[0].(map[string]any)
				if first["label"] == nil || first["label"] == "" {
					t.Fatalf("first method has no label: %v", first)
				}
			}
		})
	}
}

// The rejections have to name the module, not just fail: a user reading "cannot
// load" cannot tell a broken package from one that needs Node.
func TestUnsupportedModuleNamesWhatItWanted(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"name":"needs-node","version":"0.0.0","main":"./index.mjs"}`),
		"index.mjs":    []byte("import fs from 'node:fs'\nexport async function Plugin() { return {} }\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Load(b)
	if err == nil {
		t.Fatal("a package importing node:fs loaded")
	}
	if !strings.Contains(err.Error(), "node:fs") {
		t.Fatalf("error does not name the module: %v", err)
	}
}
