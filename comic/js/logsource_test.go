package js

import (
	"context"
	"testing"
)

// A client with several sources needs to know which one spoke; the plain Log
// path stays for hosts that cannot take the attribution.
func TestSourceLogsCarryTheirKey(t *testing.T) {
	type line struct{ key, level, title, content string }
	var got []line
	var plain int
	r, err := New(Options{}, Services{
		Storage: newMem(),
		LogSource: func(key, level, title, content string) {
			got = append(got, line{key, level, title, content})
		},
		Log: func(level, title, content string) { plain++ },
		Env: Environment{AppVersion: "1.6.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.LoadSource(context.Background(), `
		class A extends ComicSource {
			name = 'A'; key = 'a'; version = '1'
			search = {load: async () => { console.log('hello from a'); return {comics: [], maxPage: 1} }}
		}`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "a", "search.load", "x", 1); err != nil {
		t.Fatal(err)
	}
	if plain != 0 {
		t.Errorf("Log was called %d times even though LogSource is set", plain)
	}
	found := false
	for _, l := range got {
		if l.key == "a" && l.content == "hello from a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the source's line did not carry its key: %+v", got)
	}
}

// A host that only sets Log keeps the old behaviour.
func TestPlainLogStillWorks(t *testing.T) {
	var lines []string
	r, err := New(Options{}, Services{Storage: newMem(), Log: func(level, title, content string) {
		lines = append(lines, level+"|"+content)
	}, Env: Environment{AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.LoadSource(context.Background(), `
		class B extends ComicSource {
			name = 'B'; key = 'b'; version = '1'
			search = {load: async () => { console.log('plain'); return {comics: [], maxPage: 1} }}
		}`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "b", "search.load", "x", 1); err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 {
		t.Fatal("Log received nothing")
	}
}
