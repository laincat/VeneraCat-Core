package magpieplugin

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

// NodeBuiltins are the Node specifiers a Magpie provider package may import.
// They are not implemented here: a report names them so a runtime can decide
// whether a declared host adapter covers the package, instead of hiding a
// missing capability behind a stub.
var NodeBuiltins = map[string]bool{
	"assert": true, "buffer": true, "child_process": true, "crypto": true,
	"dns": true, "events": true, "fs": true, "http": true, "http2": true,
	"https": true, "net": true, "os": true, "path": true, "process": true,
	"querystring": true, "stream": true, "string_decoder": true, "tls": true,
	"url": true, "util": true, "vm": true, "zlib": true,
}

// External kinds, as a report tells them apart.
const (
	KindRelative = "relative" // resolved inside the package
	KindNode     = "node"     // a Node built-in, needs a host adapter
	KindBare     = "bare"     // an npm dependency, must be installed or bundled
	KindURL      = "url"      // an absolute URL import, never loadable offline
)

// MaxModules bounds one package's reachable module graph.
const MaxModules = 512

// ErrTooManyModules is reported when a package's graph exceeds MaxModules.
var ErrTooManyModules = errors.New("magpieplugin: module graph exceeds limit")

// External is a specifier the package imports that is not a file in it.
type External struct {
	Spec string
	Kind string
	// From lists the package files that import it, sorted.
	From []string
}

// Report is what a package needs before its code can run. It is a static
// scan, not a proof of compatibility: a specifier reached only through
// computed code (eval, a dynamic template) is not seen, and loading a package
// still requires its hooks to run against a signed-in account.
type Report struct {
	// Entry is the first existing entry candidate.
	Entry string
	// Modules are the package files reachable from Entry, sorted.
	Modules []string
	// Externals are what must be provided by the host or bundled, sorted by
	// specifier.
	Externals []External
	// Missing are specifiers that look like package files but are not in it.
	Missing []string
}

// NodeSpecifiers lists the Node built-ins the package needs, without the
// node: prefix, sorted.
func (r Report) NodeSpecifiers() []string {
	var out []string
	for _, e := range r.Externals {
		if e.Kind == KindNode {
			out = append(out, strings.TrimPrefix(e.Spec, "node:"))
		}
	}
	// Sorting by specifier is not sorting the names the caller reads.
	sort.Strings(out)
	return out
}

// BareSpecifiers lists npm dependencies the package imports, sorted.
func (r Report) BareSpecifiers() []string {
	var out []string
	for _, e := range r.Externals {
		if e.Kind == KindBare {
			out = append(out, e.Spec)
		}
	}
	return out
}

// Static package imports. The scan is deliberately conservative: it reads
// quoted specifiers of import, export-from, side-effect import, dynamic import
// and require, and ignores everything else. Strings inside comments are
// scanned too, which can only add a specifier, never hide one.
var (
	staticImport = regexp.MustCompile(`(?m)^\s*(?:import|export)\b[^;'"\n]*\bfrom\s*["']([^"']+)["']`)
	sideImport   = regexp.MustCompile(`(?m)^\s*import\s*["']([^"']+)["']`)
	dynamicCall  = regexp.MustCompile(`\b(?:import|require)\s*\(\s*["']([^"']+)["']\s*\)`)
)

// Analyze walks the package from its first entry and reports what it needs
// outside itself. It opens no files: the snapshot is the only input.
func Analyze(b *Bundle) (Report, error) {
	entries, err := b.Entries()
	if err != nil {
		return Report{}, err
	}
	report := Report{Entry: entries[0]}
	seen := map[string]bool{report.Entry: true}
	queue := []string{report.Entry}
	externals := map[string]*External{}
	missing := map[string]bool{}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if len(seen) > MaxModules {
			return Report{}, ErrTooManyModules
		}
		source, err := b.Read(name)
		if err != nil {
			return Report{}, err
		}
		for _, spec := range specifiers(source) {
			if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
				target, err := b.Resolve(name, spec)
				if errors.Is(err, fs.ErrNotExist) {
					missing[spec] = true
					continue
				}
				if err != nil {
					return Report{}, fmt.Errorf("%s: %w", name, err)
				}
				if !seen[target] {
					seen[target] = true
					queue = append(queue, target)
				}
				continue
			}
			kind := KindBare
			switch {
			case strings.HasPrefix(spec, "node:"):
				kind = KindNode
			case NodeBuiltins[spec]:
				kind = KindNode
			case strings.Contains(spec, "://"):
				kind = KindURL
			}
			entry, ok := externals[spec]
			if !ok {
				entry = &External{Spec: spec, Kind: kind}
				externals[spec] = entry
			}
			if !contains(entry.From, name) {
				entry.From = append(entry.From, name)
			}
		}
	}
	for _, name := range sortedKeys(seen) {
		report.Modules = append(report.Modules, name)
	}
	for _, spec := range sortedKeys(externals) {
		e := externals[spec]
		sort.Strings(e.From)
		report.Externals = append(report.Externals, *e)
	}
	report.Missing = sortedKeys(missing)
	return report, nil
}

func specifiers(source []byte) []string {
	text := stripComments(string(source))
	var out []string
	for _, re := range []*regexp.Regexp{staticImport, sideImport, dynamicCall} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			spec := strings.TrimSpace(m[1])
			if spec != "" && !contains(out, spec) {
				out = append(out, spec)
			}
		}
	}
	return out
}

// stripComments blanks line and block comments, keeping string and template
// literals and every byte offset line structure. Real packages use JSDoc
// type references such as import("@opencode-ai/plugin").Plugin, which are not
// runtime specifiers; scanning them would report a dependency the package
// never loads. Regular-expression literals are not modelled, so a comment
// marker inside one can still be misread; that can only hide or add a
// specifier inside such a literal, never change what runs.
func stripComments(text string) string {
	out := []byte(text)
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case 0x22, 0x27, 0x60: // double quote, apostrophe, backtick
			quote := text[i]
			for i++; i < len(text); i++ {
				if text[i] == 0x5c { // backslash escapes the next byte
					i++
					continue
				}
				if text[i] == quote {
					break
				}
			}
		case 0x2f: // slash
			if i+1 >= len(text) {
				continue
			}
			switch text[i+1] {
			case 0x2f: // a line comment runs to the newline
				for i < len(text) && text[i] != 0x0a {
					out[i] = 0x20
					i++
				}
			case 0x2a: // a block comment, newlines kept
				out[i], out[i+1] = 0x20, 0x20
				for i += 2; i < len(text); i++ {
					if text[i] == 0x2a && i+1 < len(text) && text[i+1] == 0x2f {
						out[i], out[i+1] = 0x20, 0x20
						i++
						break
					}
					if text[i] != 0x0a {
						out[i] = 0x20
					}
				}
			}
		}
	}
	return string(out)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
