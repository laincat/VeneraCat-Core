package gohost

import (
	"context"
	"path/filepath"
	"testing"
)

const linkSource = `
class Linked extends ComicSource {
  name = 'Linked'; key = 'linked'; version = '1'
  search = {load: async () => ({comics: [], maxPage: 1})}
  comic = {
    link: {
      domains: ['linked.example', 'www.linked.example'],
      linkToId: (url) => {
        let m = /\/g\/(\d+)/.exec(url)
        return m ? m[1] : null
      },
    },
    loadInfo: async (id) => ({title: 'C' + id}),
  }
}`

func linkCore(t *testing.T) *Core {
	t.Helper()
	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "d"), AppVersion: "1.6.3", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Install(context.Background(), linkSource, "file", ""); err != nil {
		t.Fatal(err)
	}
	return c
}

// A pasted address is opened by the source that claims its host, and the id
// comes from the source rather than from a regex this client invented.
func TestOpenLinkFindsTheSource(t *testing.T) {
	c := linkCore(t)
	ctx := context.Background()
	if got := c.LinkDomains(ctx, "linked"); len(got) != 2 || got[0] != "linked.example" {
		t.Fatalf("domains = %v", got)
	}
	for _, raw := range []string{
		"https://linked.example/g/424242/",
		"https://www.linked.example/g/424242",
		"linked.example/g/424242",
	} {
		key, id, err := c.OpenLink(ctx, raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if key != "linked" || id != "424242" {
			t.Errorf("%s -> %q %q", raw, key, id)
		}
	}
}

// A link another site owns, or one this source cannot read, opens nothing — and
// that is not an error.
func TestOpenLinkIgnoresForeignLinks(t *testing.T) {
	c := linkCore(t)
	ctx := context.Background()
	for _, raw := range []string{
		"https://elsewhere.example/g/1",
		"https://linked.example/not-a-comic",
		"just some words",
		"",
	} {
		key, id, err := c.OpenLink(ctx, raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if key != "" || id != "" {
			t.Errorf("%q opened %q %q", raw, key, id)
		}
	}
}

// The host rules: a scheme is optional, "www." is not part of the comparison,
// and something that is not an address at all is not treated as one.
func TestHostAndClaims(t *testing.T) {
	cases := map[string]string{
		"https://a.example/x": "a.example",
		"http://A.Example":    "a.example",
		"a.example/path":      "a.example",
		"www.a.example":       "www.a.example",
		"not a link":          "",
		"keyword":             "",
		"":                    "",
	}
	for raw, want := range cases {
		if got := hostOf(raw); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", raw, got, want)
		}
	}
	if !claims([]string{"a.example"}, "www.a.example") {
		t.Error("www. was not ignored on the link side")
	}
	if !claims([]string{"WWW.a.example"}, "a.example") {
		t.Error("www. was not ignored on the source side")
	}
	if claims([]string{"a.example"}, "b.example") {
		t.Error("a different host was claimed")
	}
}
