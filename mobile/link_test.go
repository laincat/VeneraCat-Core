package mobile

import (
	"encoding/json"
	"testing"
)

const linkSourceMobile = `
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
	c := newCore(t, &fakeHost{data: map[string]string{}, routes: map[string]string{}})
	if _, err := c.Install(0, "comic", linkSourceMobile, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	return c
}

// The phone gets the same three answers the desktop asks for, from the same
// reading of `comic.link` as an object.
func TestLinkDomainsAndOpen(t *testing.T) {
	c := linkCore(t)
	raw, err := c.LinkDomains("linked")
	if err != nil {
		t.Fatal(err)
	}
	var domains []string
	if err := json.Unmarshal([]byte(raw), &domains); err != nil {
		t.Fatalf("domains are not JSON: %v (%s)", err, raw)
	}
	if len(domains) != 2 || domains[0] != "linked.example" {
		t.Fatalf("domains = %v", domains)
	}

	id, err := c.LinkToID(0, "linked", "https://linked.example/g/424242/")
	if err != nil || id != "424242" {
		t.Fatalf("linkToId = %q, %v", id, err)
	}
	// A link this source cannot read is a "no", not an error.
	if id, err := c.LinkToID(0, "linked", "https://linked.example/not-a-comic"); err != nil || id != "" {
		t.Fatalf("unrelated link = %q, %v", id, err)
	}

	for _, link := range []string{
		"https://linked.example/g/77/",
		"linked.example/g/77",
		"https://WWW.linked.example/g/77",
	} {
		raw, err := c.OpenLink(0, link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		var target LinkTarget
		if err := json.Unmarshal([]byte(raw), &target); err != nil {
			t.Fatal(err)
		}
		if target.Key != "linked" || target.ID != "77" {
			t.Errorf("%s -> %+v", link, target)
		}
	}
}

// Anything that is not one of the source's links answers empty, so the client
// can fall back to a keyword search.
func TestOpenLinkForeign(t *testing.T) {
	c := linkCore(t)
	for _, link := range []string{"https://elsewhere.example/g/1", "keyword", "12.5", ""} {
		raw, err := c.OpenLink(0, link)
		if err != nil {
			t.Fatalf("%q: %v", link, err)
		}
		var target LinkTarget
		if err := json.Unmarshal([]byte(raw), &target); err != nil {
			t.Fatal(err)
		}
		if target.Key != "" || target.ID != "" {
			t.Errorf("%q opened %+v", link, target)
		}
	}
}

func TestLinkHostRules(t *testing.T) {
	cases := map[string]string{
		"https://a.example/x": "a.example",
		"a.example/path":      "a.example",
		"not a link":          "",
		"keyword":             "",
	}
	for raw, want := range cases {
		if got := linkHost(raw); got != want {
			t.Errorf("linkHost(%q) = %q, want %q", raw, got, want)
		}
	}
	if !claimsHost([]string{"a.example"}, "www.a.example") || claimsHost([]string{"a.example"}, "b.example") {
		t.Error("host comparison is wrong")
	}
}
