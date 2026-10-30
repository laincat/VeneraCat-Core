package js

import (
	"context"
	"strings"
	"testing"
)

// One official source builds a Referer from `new URL(imageUrl).host` and falls
// back to a regex when that throws, so the class it wanted has to exist. These
// cases are the shapes sources use: an absolute address with a port and query,
// a relative reference against a base, and query editing.
func TestURLPolyfillCoversWhatSourcesUse(t *testing.T) {
	r, err := New(Options{}, Services{Env: Environment{AppVersion: "1.6.3"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()

	cases := []struct{ script, want string }{
		{`new URL("https://Images.Example.Test:8443/a/b.jpg?w=1").host`, `"images.example.test:8443"`},
		{`new URL("https://Images.Example.Test/a/b.jpg").href`, `"https://images.example.test/a/b.jpg"`},
		{`new URL("https://x.test/p").host`, `"x.test"`},
		{`new URL("https://images.example.test:8443/a/b.jpg?w=1#f").host`, `"images.example.test:8443"`},
		{`new URL("https://images.example.test/a/b.jpg").hostname`, `"images.example.test"`},
		{`new URL("https://images.example.test/a/b.jpg").pathname`, `"/a/b.jpg"`},
		{`new URL("https://x.test/p?w=1").search`, `"?w=1"`},
		{`new URL("https://x.test/p#f").hash`, `"#f"`},
		{`new URL("https://x.test/p").origin`, `"https://x.test"`},
		{`new URL("/img/1.jpg", "https://x.test/manga/9").pathname`, `"/img/1.jpg"`},
		{`new URL("2.jpg", "https://x.test/manga/9/").pathname`, `"/manga/9/2.jpg"`},
		{`new URL("https://x.test/p?a=1&b=2").searchParams.get("b")`, `"2"`},
		{`(() => { const p = new URLSearchParams("a=1"); p.append("a", "2"); return p.getAll("a").join(",") })()`, `"1,2"`},
		{`(() => { const u = new URL("https://x.test/p?b=2&a=1"); u.searchParams.sort(); return u.href })()`, `"https://x.test/p?a=1&b=2"`},
		{`URL.canParse("https://x.test/")`, `true`},
		{`URL.canParse("nope")`, `false`},
	}
	for _, c := range cases {
		got, err := r.Eval(ctx, c.script)
		if err != nil || string(got) != c.want {
			t.Fatalf("%s: got %s, want %s, err %v", c.script, got, c.want, err)
		}
	}
	if _, err := r.Eval(ctx, `new URL("nope")`); err == nil || !strings.Contains(err.Error(), "Invalid URL") {
		t.Fatalf("invalid input should throw, got %v", err)
	}
}

// A source that edits the query through searchParams and then fetches u.search
// has to get the edited query; in the browser the two are one value.
func TestURLSearchAndSearchParamsAgree(t *testing.T) {
	r, err := New(Options{}, Services{Env: Environment{AppVersion: "1.6.3"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()

	cases := []struct{ script, want string }{
		{`(() => { const u = new URL("https://x.test/p?a=1"); u.searchParams.set("page", "2"); return u.search })()`, `"?a=1&page=2"`},
		{`(() => { const u = new URL("https://x.test/p?a=1"); u.searchParams.delete("a"); return u.search })()`, `""`},
		{`(() => { const u = new URL("https://x.test/p"); u.search = "?page=3"; return u.searchParams.get("page") })()`, `"3"`},
		{`(() => { const u = new URL("https://x.test/p?a=1"); u.searchParams.set("page", "2"); return u.href })()`, `"https://x.test/p?a=1&page=2"`},
	}
	for _, c := range cases {
		got, err := r.Eval(ctx, c.script)
		if err != nil || string(got) != c.want {
			t.Fatalf("%s: got %s, want %s, err %v", c.script, got, c.want, err)
		}
	}
}
