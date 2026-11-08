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

// `.` and `..` path segments are resolved, as a browser does, before the address
// reaches the network. Expected values are what Node's WHATWG URL returns for
// the same input (E:/Temp/core-builtin-diff/url_node.json); before the fix Core
// sent the literal `/b/c/../img/1.jpg`, which a signed CDN path or a strict
// server rejects.
func TestURLResolvesDotSegments(t *testing.T) {
	r, err := New(Options{}, Services{Env: Environment{AppVersion: "1.6.3"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()

	cases := []struct{ script, want string }{
		{`new URL("../img/1.jpg", "https://a.com/b/c/d.html").href`, `"https://a.com/b/img/1.jpg"`},
		{`new URL("../img/1.jpg", "https://a.com/b/c/d.html").pathname`, `"/b/img/1.jpg"`},
		{`new URL("../../x", "https://a.com/b/c/d").href`, `"https://a.com/x"`},
		{`new URL("../../../x", "https://a.com/b/c").href`, `"https://a.com/x"`},
		{`new URL("./x", "https://a.com/b/c").href`, `"https://a.com/b/x"`},
		{`new URL("../x", "https://a.com:8443/b/c/d").href`, `"https://a.com:8443/b/x"`},
		{`new URL("/a/../b", "https://a.com/z").href`, `"https://a.com/b"`},
		{`new URL("https://a.com/b/./c/../d").href`, `"https://a.com/b/d"`},
		{`new URL("https://a.com/../x").href`, `"https://a.com/x"`},
		{`new URL("https://a.com/a/b/..").href`, `"https://a.com/a/"`},
		{`new URL("https://a.com/a/b/.").href`, `"https://a.com/a/b/"`},
		{`new URL("https://a.com/a/%2e%2e/b").href`, `"https://a.com/b"`},
		{`new URL("https://a.com/a//b/../c").href`, `"https://a.com/a//c"`},
		// Not dot segments: a file name with dots, and dots in the hash.
		{`new URL("https://a.com/a/..b/c...d").href`, `"https://a.com/a/..b/c...d"`},
		{`new URL("https://a.com/a/b#../c").href`, `"https://a.com/a/b#../c"`},
	}
	for _, c := range cases {
		got, err := r.Eval(ctx, c.script)
		if err != nil || string(got) != c.want {
			t.Fatalf("%s: got %s, want %s, err %v", c.script, got, c.want, err)
		}
	}
}

// Two differences from the browser stay on purpose, and are pinned so a change
// to either shows up here. `href` rebuilds the query from searchParams, so a
// `/` in a value comes back as `%2F` (the server receives the same parameter);
// and a scheme sources do not use keeps its path as written.
func TestURLKnownDifferencesFromTheBrowser(t *testing.T) {
	r, err := New(Options{}, Services{Env: Environment{AppVersion: "1.6.3"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()

	cases := []struct{ script, want string }{
		{`new URL("https://a.com/a/../b?p=../q").href`, `"https://a.com/b?p=..%2Fq"`},
		{`new URL("https://a.com/a/../b?p=../q").searchParams.get("p")`, `"../q"`},
		{`new URL("foo:/a/../b").pathname`, `"/a/../b"`},
	}
	for _, c := range cases {
		got, err := r.Eval(ctx, c.script)
		if err != nil || string(got) != c.want {
			t.Fatalf("%s: got %s, want %s, err %v", c.script, got, c.want, err)
		}
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
