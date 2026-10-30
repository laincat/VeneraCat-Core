package magpieplugin

import "testing"

// JSDoc type references and commented-out imports are not runtime specifiers.
// Copilot's real package names "@opencode-ai/plugin" only in a type comment,
// and reporting it would claim a dependency the package never loads.
func TestCommentsAreNotImports(t *testing.T) {
	entry := "/**\n * @type {import('@opencode-ai/plugin').Plugin}\n */\n" +
		"// import absent from './gone.mjs'\n" +
		"/* require('node:fs') */\n" +
		"const s = '// not a comment'\n" +
		"import real from './real.mjs'\nexport default real\n"
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs":    []byte(entry),
		"real.mjs":     []byte("export default 1 // trailing comment\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Analyze(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Externals) != 0 {
		t.Fatalf("comments became externals: %v", report.Externals)
	}
	if len(report.Missing) != 0 {
		t.Fatalf("commented import became missing: %v", report.Missing)
	}
	if len(report.Modules) != 2 {
		t.Fatalf("modules = %v", report.Modules)
	}
}

// A string holding a comment marker is code, not a comment: the import after
// it must still be read.
func TestCodeAfterAStringIsStillRead(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs":    []byte("const s = 'a /* b'\nimport c from 'node:crypto'\nexport default c\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Analyze(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.NodeSpecifiers(); len(got) != 1 || got[0] != "crypto" {
		t.Fatalf("node specifiers = %v", got)
	}
}
