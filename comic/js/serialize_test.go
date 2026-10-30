package js

import (
	"context"
	"testing"
)

// Sources return tags and chapters as Map objects. They must reach Go as
// JSON objects with the entries in insertion order, as in the official app.
func TestCallSerialisesMapsAndBigInts(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class M extends ComicSource {
  name = 'M'; key = 'm'; version = '1.0.0'
  details() {
    const tags = new Map(); tags.set('作者', ['甲']); tags.set('标签', ['b', 'a'])
    const chapters = new Map(); chapters.set('10', '第十话'); chapters.set('2', '第二话'); chapters.set(3, '第三话')
    return new ComicDetails({ title: 'x', tags, chapters })
  }
  big() { return { id: 9007199254740993n, list: [1n], nested: new Map([['n', 2n], ['m', new Map([['q"', [3n]]])]]) } }
  forged() { return { s: '\u0000vcbig:1', m: new Map() } }
  circular() { const m = new Map(); m.set('self', m); return m }
  nothing() { return undefined }
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ path, want string }{
		// Integer-like keys keep insertion order; a plain JS object would
		// move them first in ascending order.
		{"details", `{"title":"x","tags":{"作者":["甲"],"标签":["b","a"]},"chapters":{"10":"第十话","2":"第二话","3":"第三话"}}`},
		{"big", `{"id":9007199254740993,"list":[1],"nested":{"n":2,"m":{"q\"":[3]}}}`},
		{"forged", `{"s":"\u0000vcbig:1","m":{}}`},
		{"nothing", `null`},
	}
	if _, err := r.Call(context.Background(), "m", "circular"); err == nil {
		t.Error("circular Map: want an error")
	}
	for _, c := range cases {
		out, err := r.Call(context.Background(), "m", c.path)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if string(out) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.path, out, c.want)
		}
	}
}

// A source declares its search filters as optionList; the client must pass
// one value per group, so each group's default has to be readable.
func TestSearchOptions(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class M extends ComicSource {
  name = 'M'; key = 'm'; version = '1'
  search = {
    optionList: [
      {label: '排序', type: 'select', options: ['any-任意', 'popular-人气', 'recent-最新'], default: 'popular'},
      {label: '分级', options: ['any-全部', 'safe-全年龄']},
      {label: '空组', options: []},
    ],
    load: async (k, o, p) => ({comics: [], keywords: k, options: o}),
  }
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	groups, err := r.SearchOptions(context.Background(), "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3 {
		t.Fatalf("groups = %+v", groups)
	}
	if g := groups[0]; g.Label != "排序" || g.Type != "select" || g.Default != "popular" ||
		len(g.Values) != 3 || g.Values[1] != "popular" || g.Labels[1] != "人气" {
		t.Errorf("group 0 = %+v", g)
	}
	// No declared default: the first option is the default, as the official
	// client shows it.
	if g := groups[1]; g.Default != "any" || g.Type != "" {
		t.Errorf("group 1 = %+v", g)
	}
	if g := groups[2]; len(g.Values) != 0 || g.Default != "" {
		t.Errorf("group 2 = %+v", g)
	}

	// A source without optionList has no groups, and that is not an error.
	plain := `class N extends ComicSource { name='N'; key='n'; version='1'
	  search = { load: async () => ({comics: []}) } }`
	if _, err := r.LoadSource(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	if groups, err := r.SearchOptions(context.Background(), "n"); err != nil || len(groups) != 0 {
		t.Fatalf("plain source: %v %v", groups, err)
	}
}

// A source's browses are its explore list: the client shows a tab per entry
// and calls explore[i].load with the type's arguments.
func TestExplorePages(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class E extends ComicSource {
  name = 'E'; key = 'e'; version = '1'
  explore = [
    {title: '熱門', type: 'multiPageComicList', load: async (page) => ({comics: [], page})},
    {title: '首頁', type: 'singlePageWithMultiPart', load: async () => ({})},
    {title: '混合', type: 'mixed'},
  ]
  search = { load: async () => ({comics: []}) }
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	pages, err := r.ExplorePages(context.Background(), "e")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("pages = %+v", pages)
	}
	if pages[0].Title != "熱門" || pages[0].Type != "multiPageComicList" {
		t.Errorf("pages[0] = %+v", pages[0])
	}
	if pages[2].Title != "混合" || pages[2].Type != "mixed" {
		t.Errorf("pages[2] = %+v", pages[2])
	}
	// A source without explore has none, which is not an error.
	plain := `class N extends ComicSource { name='N'; key='n'; version='1'
	  search = { load: async () => ({comics: []}) } }`
	if _, err := r.LoadSource(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	if pages, err := r.ExplorePages(context.Background(), "n"); err != nil || len(pages) != 0 {
		t.Fatalf("plain: %v %v", pages, err)
	}
}
