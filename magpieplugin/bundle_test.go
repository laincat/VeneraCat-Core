package magpieplugin

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

func TestProviderPackageIsIndependentSnapshot(t *testing.T) {
	files := map[string][]byte{
		"package.json":  []byte(`{"name":"test-provider","version":"1.0","main":"./index.mjs","extra":{"id":9007199254740993}}`),
		"index.mjs":     []byte(`import { value } from "./lib/token.mjs"; export default value;`),
		"lib/token.mjs": []byte(`export const value = 1;`),
	}
	b, err := NewBundle(files)
	if err != nil {
		t.Fatal(err)
	}
	files["index.mjs"][0] = 'X'
	got, _ := b.Read("index.mjs")
	if !strings.HasPrefix(string(got), "import") {
		t.Fatal("bundle retained caller's buffer")
	}
	got[0] = 'Y'
	again, _ := b.Read("index.mjs")
	if !strings.HasPrefix(string(again), "import") {
		t.Fatal("read leaked mutable source")
	}
	meta := b.Manifest()
	if meta.Name != "test-provider" || meta.Version != "1.0" || !strings.Contains(string(meta.Raw), "9007199254740993") {
		t.Fatalf("metadata lost: %+v", meta)
	}
	meta.Raw[0] = 'X'
	if b.Manifest().Raw[0] != '{' {
		t.Fatal("manifest leaked internal bytes")
	}
	entries, err := b.Entries()
	if err != nil || !reflect.DeepEqual(entries, []string{"index.mjs"}) {
		t.Fatalf("entries = %v, %v", entries, err)
	}
	module, err := b.Resolve(entries[0], "./lib/token.mjs")
	if err != nil || module != "lib/token.mjs" {
		t.Fatalf("module = %q, %v", module, err)
	}
}

func TestEntriesFollowHostOrder(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./old.mjs","exports":{"./server":{"import":"./new.mjs"},".":{"default":"./old.mjs"}}}`),
		"new.mjs":      {}, "old.mjs": {}, "index.ts": {}, "index.js": {},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Entries()
	if want := []string{"new.mjs", "old.mjs", "index.ts", "index.js"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %v, %v; want %v", got, err, want)
	}
	for _, manifest := range []string{`{"exports":"./mod.mjs"}`, `{"exports":{"import":"./mod.mjs"}}`} {
		b, err := NewBundle(map[string][]byte{"package.json": []byte(manifest), "mod.mjs": {}})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := b.Entries(); err != nil || len(got) != 1 || got[0] != "mod.mjs" {
			t.Fatalf("root export = %v, %v", got, err)
		}
	}
}

func TestPathsAndUnsupportedModules(t *testing.T) {
	for _, name := range []string{"../outside", "/abs", `C:/abs`, `a\b`, "a/../b", "./a", ".", "a\x00b"} {
		if _, err := NewBundle(map[string][]byte{"package.json": []byte(`{}`), name: {}}); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("accepted package path %q: %v", name, err)
		}
	}
	b, _ := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"../escape.mjs"}`), "lib/a.mjs": {}, "index.mjs": {},
	})
	if _, err := b.Entries(); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("entry escaped: %v", err)
	}
	if _, err := b.Read("../index.mjs"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("read escaped: %v", err)
	}
	for _, spec := range []string{"../../escape.mjs", "./bad\\name", "./bad\x00name"} {
		if _, err := b.Resolve("lib/a.mjs", spec); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("accepted import %q: %v", spec, err)
		}
	}
	for _, spec := range []string{"node:crypto", "some-library", "https://x/module.js"} {
		if _, err := b.Resolve("index.mjs", spec); !errors.Is(err, ErrUnsupportedModule) {
			t.Errorf("module %q must require adapter: %v", spec, err)
		}
	}
	if got, err := b.Resolve("lib/a.mjs", "../index.mjs"); err != nil || got != "index.mjs" {
		t.Fatalf("valid parent import = %q, %v", got, err)
	}
	if _, err := b.Resolve("index.mjs", "./missing.mjs"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing module = %v", err)
	}
}

func TestInvalidPackagesAndLimits(t *testing.T) {
	for _, body := range []string{"null", "[]", "{", `{"main":17}`} {
		b, err := NewBundle(map[string][]byte{"package.json": []byte(body)})
		if err == nil {
			_, err = b.Entries()
		}
		if err == nil {
			t.Errorf("accepted invalid manifest %q", body)
		}
	}
	if _, err := NewBundle(nil); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing manifest = %v", err)
	}
	b, _ := NewBundle(map[string][]byte{"package.json": []byte(`{"bin":"cli.js"}`), "cli.js": {}})
	if _, err := b.Entries(); !errors.Is(err, ErrNoEntry) {
		t.Fatalf("command-only package accepted: %v", err)
	}
	files := map[string][]byte{"package.json": []byte(`{}`)}
	for i := range MaxFiles {
		files[strings.Repeat("a", i+1)] = nil
	}
	if _, err := NewBundle(files); !errors.Is(err, ErrLimit) {
		t.Fatalf("file count not bounded: %v", err)
	}
	if _, err := NewBundle(map[string][]byte{"package.json": []byte(`{}`), "large.js": make([]byte, MaxFileBytes+1)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("file bytes not bounded: %v", err)
	}
	files = map[string][]byte{"package.json": []byte(`{}`)}
	large := make([]byte, MaxFileBytes)
	for _, name := range []string{"a", "b", "c", "d"} {
		files[name] = large
	}
	if _, err := NewBundle(files); !errors.Is(err, ErrLimit) {
		t.Fatalf("total bytes not bounded: %v", err)
	}
}
