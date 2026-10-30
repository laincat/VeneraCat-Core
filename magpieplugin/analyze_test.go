package magpieplugin

import (
	"errors"
	"io/fs"
	"reflect"
	"testing"
)

func TestAnalyzeFollowsThePackageOnly(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs": []byte(`import { h } from "./lib/http.mjs"
import "./side-effect.mjs"
export { x } from './lib/x.mjs'
const later = await import("./lib/late.mjs")
const c = require("node:crypto")
import fs from "fs"
import { z } from "@scope/dep"
import remote from "https://example.com/mod.mjs"
import absent from "./lib/absent.mjs"
export default h`),
		"lib/http.mjs": []byte(`import { join } from "node:path"
export const h = join`),
		"side-effect.mjs": {},
		"lib/x.mjs":       []byte(`export const x = 1`),
		"lib/late.mjs":    []byte(`import crypto from "node:crypto"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Analyze(b)
	if err != nil {
		t.Fatal(err)
	}
	if report.Entry != "index.mjs" {
		t.Fatalf("entry = %q", report.Entry)
	}
	want := []string{"index.mjs", "lib/http.mjs", "lib/late.mjs", "lib/x.mjs", "side-effect.mjs"}
	if !reflect.DeepEqual(report.Modules, want) {
		t.Fatalf("modules = %v, want %v", report.Modules, want)
	}
	if got := report.NodeSpecifiers(); !reflect.DeepEqual(got, []string{"crypto", "fs", "path"}) {
		t.Fatalf("node = %v", got)
	}
	if got := report.BareSpecifiers(); !reflect.DeepEqual(got, []string{"@scope/dep"}) {
		t.Fatalf("bare = %v", got)
	}
	if !reflect.DeepEqual(report.Missing, []string{"./lib/absent.mjs"}) {
		t.Fatalf("missing = %v", report.Missing)
	}
	var url []string
	for _, e := range report.Externals {
		if e.Kind == KindURL {
			url = append(url, e.Spec)
		}
	}
	if !reflect.DeepEqual(url, []string{"https://example.com/mod.mjs"}) {
		t.Fatalf("url imports = %v", url)
	}
	// A specifier is reported once, with every file that imports it.
	for _, e := range report.Externals {
		if e.Spec == "node:crypto" && !reflect.DeepEqual(e.From, []string{"index.mjs", "lib/late.mjs"}) {
			t.Fatalf("crypto imported from %v", e.From)
		}
	}
}

func TestAnalyzeHandlesCyclesAndReExecution(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"exports":{"./server":"./srv.mjs"}}`),
		"srv.mjs":      []byte(`import "./a.mjs"`),
		"a.mjs":        []byte(`import "./b.mjs"`),
		"b.mjs":        []byte(`import "./a.mjs"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Analyze(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.Modules, []string{"a.mjs", "b.mjs", "srv.mjs"}) {
		t.Fatalf("modules = %v", report.Modules)
	}
	// Analyzing again gives the same answer: nothing is cached between calls.
	again, err := Analyze(b)
	if err != nil || !reflect.DeepEqual(report, again) {
		t.Fatalf("second analysis differs: %v", err)
	}
}

func TestAnalyzeLimitsAndFailures(t *testing.T) {
	files := map[string][]byte{
		"package.json": []byte(`{"main":"./m0.mjs"}`),
		"m0.mjs":       {},
	}
	for i := 1; i <= MaxModules+5; i++ {
		// Each module imports the next, so the graph is a chain longer
		// than the limit.
		files["m"+itoa(i)+".mjs"] = []byte(`import "./m` + itoa(i+1) + `.mjs"`)
	}
	files["m0.mjs"] = []byte(`import "./m1.mjs"`)
	b, err := NewBundle(files)
	if err == nil {
		if _, err := Analyze(b); !errors.Is(err, ErrTooManyModules) {
			t.Fatalf("graph limit = %v", err)
		}
	}

	b, _ = NewBundle(map[string][]byte{"package.json": []byte(`{"main":"./a.mjs"}`), "a.mjs": []byte(`import "../../escape.mjs"`)})
	if _, err := Analyze(b); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("escaping import = %v", err)
	}
	b, _ = NewBundle(map[string][]byte{"package.json": []byte(`{"bin":"cli.js"}`), "cli.js": {}})
	if _, err := Analyze(b); !errors.Is(err, ErrNoEntry) {
		t.Fatalf("command-only package = %v", err)
	}
	// An entry the manifest names but the package lacks leaves no candidate:
	// that is "nothing to load", not a file that vanished mid-read.
	b, _ = NewBundle(map[string][]byte{"package.json": []byte(`{"main":"./missing.mjs"}`)})
	if _, err := Analyze(b); !errors.Is(err, ErrNoEntry) {
		t.Fatalf("manifest naming an absent entry = %v", err)
	}
	if _, err := b.Read("missing.mjs"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("reading an absent file = %v", err)
	}
}

func TestAnalyzeIgnoresTextThatIsNotAnImport(t *testing.T) {
	b, _ := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs":    []byte("// import x from './nope.mjs'\nconst s = \"import y from './nope2.mjs'\"\nimport z from './real.mjs'\n"),
		"real.mjs":     {},
	})
	report, err := Analyze(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.Modules, []string{"index.mjs", "real.mjs"}) {
		t.Fatalf("modules = %v", report.Modules)
	}
	if len(report.Missing) != 0 {
		t.Fatalf("string literals became imports: %v", report.Missing)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
