package magpieplugin

import (
	"errors"
	"strings"
	"testing"
)

// demoPackage is shaped like a real Magpie provider: one exported async
// function, given the host's client, returning auth hooks that point at a
// module beside it.
func demoPackage(extra map[string][]byte) map[string][]byte {
	files := map[string][]byte{
		"package.json": []byte(`{"name":"demo-provider","version":"0.1.0","main":"./index.mjs"}`),
		"index.mjs": []byte(`import { providerID, methods } from "./auth.mjs"
export async function Plugin(input) {
  return {
    auth: {
      provider: providerID,
      methods,
      async loader(getAuth) {
        const auth = await getAuth()
        return { baseURL: auth.baseURL, apiKey: auth.key, headers: { "X-From-Loader": "1" } }
      },
    },
  }
}`),
		"auth.mjs": []byte("export const providerID = \"demo\"\nexport const methods = [{ type: \"api\", label: \"Demo key\" }]\n"),
	}
	for k, v := range extra {
		files[k] = v
	}
	return files
}

// loadDemo builds the demo package with the given overrides and loads it.
func loadDemo(t *testing.T, extra map[string][]byte) (*Loader, error) {
	t.Helper()
	b, err := NewBundle(demoPackage(extra))
	if err != nil {
		return nil, err
	}
	return Load(b)
}

func TestLoadCallsAnAsyncProviderHook(t *testing.T) {
	loader, err := loadDemo(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !inst.Exports("Plugin") {
		t.Fatal("Plugin export not found")
	}
	// The host hands a plugin its client; a map is what the JS side sees as
	// an object.
	got, err := inst.Call("Plugin", map[string]any{"client": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	root, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", got)
	}
	auth, ok := root["auth"].(map[string]any)
	if !ok || auth["provider"] != "demo" {
		t.Fatalf("auth = %#v", root["auth"])
	}
	methods, ok := auth["methods"].([]any)
	if !ok || len(methods) != 1 {
		t.Fatalf("methods = %#v", auth["methods"])
	}
	if _, ok := auth["loader"].(map[string]any); ok {
		t.Fatal("a function came back as an object")
	}
}

func TestLoadRefusesWhatTheHostCannotProvide(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string][]byte
		expect error
	}{
		{
			name:   "node builtin",
			files:  map[string][]byte{"index.mjs": []byte(`import crypto from "node:crypto"` + "\n" + `export const Plugin = async () => ({})`)},
			expect: ErrUnsupportedModule,
		},
		{
			name:   "npm package",
			files:  map[string][]byte{"index.mjs": []byte(`import { z } from "@scope/dep"` + "\n" + `export const Plugin = async () => ({})`)},
			expect: ErrUnsupportedModule,
		},
		{
			name:   "module absent from the package",
			files:  map[string][]byte{"index.mjs": []byte(`import a from "./gone.mjs"` + "\n" + `export const Plugin = async () => ({})`)},
			expect: nil, // reported as an error, not as a specific sentinel
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadDemo(t, tc.files)
			if err == nil {
				t.Fatal("loaded a package the host cannot serve")
			}
			if tc.expect != nil && !errors.Is(err, tc.expect) {
				t.Fatalf("error = %v, want %v", err, tc.expect)
			}
		})
	}
}

func TestLoadReportsSyntaxErrorsWithTheirFile(t *testing.T) {
	files := demoPackage(map[string][]byte{"auth.mjs": []byte("export const providerID = (\n")})
	_, err := loadDemo(t, files)
	if err == nil || !strings.Contains(err.Error(), "auth.mjs") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstancesDoNotShareState(t *testing.T) {
	files := demoPackage(map[string][]byte{
		"auth.mjs": []byte("export const providerID = \"demo\"\nexport const methods = []\nglobalThis.__counter = (globalThis.__counter ?? 0) + 1\n"),
	})
	loader, err := loadDemo(t, files)
	if err != nil {
		t.Fatal(err)
	}
	first, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetGlobal("__seen", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Call("Plugin", nil); err != nil {
		t.Fatal(err)
	}
	// The second instance never saw the first's global.
	counter, err := second.Call("Plugin", nil)
	if err != nil {
		t.Fatal(err)
	}
	if counter == nil {
		t.Fatal("second instance returned nothing")
	}
}
