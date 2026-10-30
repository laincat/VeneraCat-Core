// Package magpieplugin handles Magpie provider plugin packages independently of comic
// sources. It understands package entry paths, not provider hooks yet. Network,
// archive decoding, authentication and JavaScript execution belong to callers
// or later runtime layers.
package magpieplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

const (
	MaxFiles      = 1024
	MaxFileBytes  = 8 << 20
	MaxTotalBytes = 32 << 20
)

var (
	ErrInvalidPath       = errors.New("magpieplugin: invalid package path")
	ErrLimit             = errors.New("magpieplugin: package exceeds limits")
	ErrNoEntry           = errors.New("magpieplugin: no loadable entry")
	ErrUnsupportedModule = errors.New("magpieplugin: module requires a host adapter or bundled dependency")
	// ErrUnsupportedSource means the package ships a language this runtime does
	// not read, such as TypeScript.
	ErrUnsupportedSource = errors.New("magpieplugin: unsupported source language")
)

// Manifest keeps npm metadata and unrecognised fields in their original JSON
// form. Reading a manifest does not install dependencies or run scripts.
type Manifest struct {
	Name    string
	Version string
	Raw     json.RawMessage
}

// Bundle is an immutable snapshot of regular package files supplied by the
// client. Keys are package-relative slash paths, with no enclosing package/
// directory. Archive extraction must reject symlinks and non-regular entries
// before passing files here. No filesystem paths are opened by this package.
type Bundle struct {
	files map[string][]byte
	meta  Manifest
}

// NewBundle checks a package against the limits and path rules, then keeps copies
// of its files. Nothing outside the package's own tree is reachable from the result.
func NewBundle(files map[string][]byte) (*Bundle, error) {
	if len(files) > MaxFiles {
		return nil, ErrLimit
	}
	b := &Bundle{files: make(map[string][]byte, len(files))}
	total := 0
	for name, body := range files {
		if !validPath(name) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidPath, name)
		}
		if len(body) > MaxFileBytes || len(body) > MaxTotalBytes-total {
			return nil, ErrLimit
		}
		total += len(body)
		b.files[name] = append([]byte(nil), body...)
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	raw, ok := b.files["package.json"]
	if !ok {
		return nil, fmt.Errorf("magpieplugin: package.json: %w", fs.ErrNotExist)
	}
	if len(raw) == 0 || raw[0] == 0 {
		return nil, errors.New("magpieplugin: invalid package.json")
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, fmt.Errorf("magpieplugin: package.json: %w", err)
	}
	// null is JSON, but is not a package object.
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("magpieplugin: package.json must be an object")
	}
	b.meta = Manifest{Name: pkg.Name, Version: pkg.Version, Raw: append([]byte(nil), raw...)}
	return b, nil
}

func validPath(name string) bool {
	return fs.ValidPath(name) && name != "." && !strings.ContainsAny(name, "\\:\x00")
}

// Manifest is the package's declaration, with its raw JSON copied so a caller
// cannot alter the bundle by editing what it was handed.
func (b *Bundle) Manifest() Manifest {
	m := b.meta
	m.Raw = append([]byte(nil), m.Raw...)
	return m
}

// Read returns a copy; plugin source and JSON number spelling stay untouched.
func (b *Bundle) Read(name string) ([]byte, error) {
	if !validPath(name) {
		return nil, ErrInvalidPath
	}
	body, ok := b.files[name]
	if !ok {
		return nil, fmt.Errorf("magpieplugin: %s: %w", name, fs.ErrNotExist)
	}
	return append([]byte(nil), body...), nil
}

// Entries mirrors Magpie's provider entry candidate order: ./server export,
// main, root export, then index files. It returns existing candidates in order;
// the future runtime must check exports and may fall back to the next entry.
// TypeScript candidates are retained; this package does not transpile them.
func (b *Bundle) Entries() ([]string, error) {
	var pkg struct {
		Main    string          `json:"main"`
		Exports json.RawMessage `json:"exports"`
	}
	if err := json.Unmarshal(b.meta.Raw, &pkg); err != nil {
		return nil, err
	}
	var exports map[string]json.RawMessage
	_ = json.Unmarshal(pkg.Exports, &exports)
	candidates := []string{pick(exports["./server"]), pkg.Main, pick(exports["."]), pick(pkg.Exports)}
	candidates = append(candidates, "index.ts", "index.tsx", "index.js", "index.mjs", "index.cjs")
	var out []string
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		name, err := rootPath(strings.TrimSpace(candidate))
		if err != nil {
			return nil, err
		}
		if _, exists := b.files[name]; exists && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil, ErrNoEntry
	}
	return out, nil
}

func pick(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var c map[string]json.RawMessage
	if json.Unmarshal(raw, &c) == nil {
		if json.Unmarshal(c["import"], &s) == nil {
			return s
		}
		_ = json.Unmarshal(c["default"], &s)
	}
	return s
}

func rootPath(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:\x00") {
		return "", ErrInvalidPath
	}
	name = path.Clean(name)
	if !validPath(name) {
		return "", ErrInvalidPath
	}
	return name, nil
}

// Resolve locates an explicit relative module inside the snapshot. It does not
// parse imports, resolve npm dependencies, execute Node modules or access disk.
// Bare and node: specifiers are reported separately from invalid paths so the
// runtime can route them to a declared compatibility adapter in the future.
func (b *Bundle) Resolve(from, spec string) (string, error) {
	if !validPath(from) {
		return "", ErrInvalidPath
	}
	if _, ok := b.files[from]; !ok {
		return "", fs.ErrNotExist
	}
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedModule, spec)
	}
	if strings.ContainsAny(spec, "\\:\x00") {
		return "", ErrInvalidPath
	}
	name, err := rootPath(path.Join(path.Dir(from), spec))
	if err != nil {
		return "", err
	}
	if _, ok := b.files[name]; !ok {
		return "", fmt.Errorf("magpieplugin: %s: %w", name, fs.ErrNotExist)
	}
	return name, nil
}
