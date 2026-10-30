package attribution

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseForms(t *testing.T) {
	cases := []struct {
		name     string
		author   string
		homepage string
		want     Info
		warnings int
	}{
		{"string", `"Alice"`, `"https://alice.test/src"`, Info{Authors: []Author{{Name: "Alice"}}, Homepage: "https://alice.test/src"}, 0},
		{"object", `{"name":"Bob","url":"https://github.com/bob"}`, ``, Info{Authors: []Author{{Name: "Bob", URL: "https://github.com/bob"}}}, 0},
		{"list", `["A", {"name":"B","url":"https://b.test"}]`, ``, Info{Authors: []Author{{Name: "A"}, {Name: "B", URL: "https://b.test"}}}, 0},
		{"absent", ``, ``, Info{}, 0},
		{"bad url kept name", `{"name":"C","url":"javascript:alert(1)"}`, `"file:///etc"`, Info{Authors: []Author{{Name: "C"}}}, 2},
		{"empty name", `{"url":"https://x.test"}`, ``, Info{}, 1},
		{"credentials", `"D"`, `"https://u:p@h.test/"`, Info{Authors: []Author{{Name: "D"}}}, 1},
		{"wrong types", `42`, `7`, Info{}, 2},
		{"nested list", `[["x"]]`, ``, Info{}, 1},
	}
	for _, c := range cases {
		got, warn := ParseJSON(json.RawMessage(c.author), json.RawMessage(c.homepage))
		if !equal(got, c.want) || len(warn) != c.warnings {
			t.Errorf("%s: got %+v warnings %v, want %+v with %d warnings", c.name, got, warn, c.want, c.warnings)
		}
	}
}

func TestLimits(t *testing.T) {
	list := make([]any, MaxAuthors+5)
	for i := range list {
		list[i] = "a"
	}
	got, warn := Parse(list, nil)
	if len(got.Authors) != MaxAuthors || len(warn) == 0 {
		t.Fatalf("authors = %d, warnings %v", len(got.Authors), warn)
	}
	long := strings.Repeat("名", MaxNameRunes+20)
	got, _ = Parse(long, nil)
	if n := len([]rune(got.Authors[0].Name)); n != MaxNameRunes {
		t.Fatalf("name runes = %d", n)
	}
	got, warn = Parse(nil, "https://h.test/"+strings.Repeat("x", MaxURLBytes))
	if got.Homepage != "" || len(warn) != 1 {
		t.Fatalf("long homepage = %q %v", got.Homepage, warn)
	}
}

func TestMergeScriptWins(t *testing.T) {
	script := Info{Authors: []Author{{Name: "Script Author"}}}
	index := Info{Authors: []Author{{Name: "Index Author"}}, Homepage: "https://index.test/"}
	got := Merge(script, index)
	if got.Authors[0].Name != "Script Author" || got.Homepage != "https://index.test/" {
		t.Fatalf("merge = %+v", got)
	}
	if a, h := (Info{}).Missing(); !a || !h {
		t.Fatal("empty info must report both missing")
	}
	if a, h := got.Missing(); a || h {
		t.Fatal("merged info reports missing")
	}
}

func equal(a, b Info) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
