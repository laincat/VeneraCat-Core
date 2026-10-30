package magpieplugin

import (
	"errors"
	"strings"
	"testing"
)

func TestLoadUsesOnlySelectedEntryAndItsImports(t *testing.T) {
	for _, tc := range []struct{ name, unusedFile, unusedBody string }{
		{"bundled JS with original TypeScript", "index.ts", `export const answer: string = "uncompiled source"`},
		{"bundled JS with invalid fallback", "index.js", `export const =`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle, err := NewBundle(map[string][]byte{
				"package.json":   []byte(`{"name":"built-package","main":"./dist/index.mjs"}`),
				"dist/index.mjs": []byte(`import { value } from "./value.mjs"; export function selected() { return value }`),
				"dist/value.mjs": []byte(`export const value = "built entry"`),
				tc.unusedFile:    []byte(tc.unusedBody),
			})
			if err != nil {
				t.Fatal(err)
			}
			loader, err := Load(bundle)
			if err != nil {
				t.Fatalf("unselected source prevented loading built entry: %v", err)
			}
			instance, err := loader.NewInstance(Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer instance.Close()
			got, err := instance.Call("selected", nil)
			if err != nil || got != "built entry" {
				t.Fatalf("selected entry returned %v, %v", got, err)
			}
		})
	}
}

func TestSelectedEntryErrorsAreNotHiddenByFallbackFiles(t *testing.T) {
	for _, tc := range []struct {
		name, selected, imported string
		unsupported              bool
	}{
		{"selected syntax error", `export const =`, "", false},
		{"selected imports TypeScript", `import { value } from "./value.ts"; export function selected(){return value}`, `export const value: string = "source"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{
				"package.json":   []byte(`{"name":"broken-built-package","main":"./dist/index.mjs"}`),
				"dist/index.mjs": []byte(tc.selected),
				"index.js":       []byte(`export function selected(){return "wrong fallback"}`),
			}
			if tc.imported != "" {
				files["dist/value.ts"] = []byte(tc.imported)
			}
			bundle, err := NewBundle(files)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Load(bundle)
			if err == nil {
				t.Fatal("broken selected entry silently used a fallback")
			}
			if tc.unsupported && !errors.Is(err, ErrUnsupportedSource) {
				t.Fatalf("TypeScript import error = %v", err)
			}
			if !tc.unsupported && !strings.Contains(err.Error(), "dist/index.mjs") {
				t.Fatalf("syntax error lost selected filename: %v", err)
			}
		})
	}
}

func TestLoadSelectedTypeScriptStillNeedsPrebuild(t *testing.T) {
	bundle, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"name":"source-package","main":"./index.ts"}`),
		"index.ts":     []byte(`export function selected(): string {return "source"}`),
		"index.js":     []byte(`export function selected(){return "unselected"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Load(bundle)
	if !errors.Is(err, ErrUnsupportedSource) {
		t.Fatalf("selected TypeScript policy changed: %v", err)
	}
}
