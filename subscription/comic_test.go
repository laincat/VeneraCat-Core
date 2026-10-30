package subscription

import (
	"errors"
	"strings"
	"testing"
)

// Shaped like venera-app/venera-configs: entries carry only fileName.
const officialShape = `[
  {"name": "拷贝漫画", "fileName": "copy_manga.js", "key": "copy_manga", "version": "1.4.3"},
  {"name": "Example", "fileName": "ex.js", "key": "ex", "version": "1.0.0", "description": "d"}
]`

// Shaped like laincat/vensrc: entries carry absolute pinned URLs.
const vensrcShape = `[
  {"name": "Asura Scans", "key": "asura_scans", "version": "1.3.0", "fileName": "asura_scans.js",
   "url": "https://raw.githubusercontent.com/o/r/cee408d/asura_scans.js", "minAppVersion": "1.0.0",
   "description": "引用源：o/r"}
]`

func TestParseComicOfficialShape(t *testing.T) {
	idx, err := ParseComic([]byte(officialShape), "https://cdn.jsdelivr.net/gh/venera-app/venera-configs@main/index.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 2 || len(idx.Problems) != 0 {
		t.Fatalf("idx = %+v", idx)
	}
	if got := idx.Entries[0].URL; got != "https://cdn.jsdelivr.net/gh/venera-app/venera-configs@main/copy_manga.js" {
		t.Fatalf("url = %s", got)
	}
}

func TestParseComicURLRules(t *testing.T) {
	cases := map[string]string{
		// index URL with no path: fileName goes under the root, as officially.
		"https://host.test":                     "https://host.test/a.js",
		"https://host.test/":                    "https://host.test/a.js",
		"https://host.test/list/":               "https://host.test/list/a.js",
		"https://host.test/list/index.json?x=1": "https://host.test/list/a.js",
	}
	for base, want := range cases {
		idx, err := ParseComic([]byte(`[{"name":"A","key":"a","version":"1","fileName":"a.js"}]`), base)
		if err != nil {
			t.Fatal(err)
		}
		if got := idx.Entries[0].URL; got != want {
			t.Errorf("base %s: url = %s, want %s", base, got, want)
		}
	}
	// An absolute url wins over fileName.
	idx, _ := ParseComic([]byte(vensrcShape), "https://github.com/o/r/releases/latest/download/index.json")
	if idx.Entries[0].URL != "https://raw.githubusercontent.com/o/r/cee408d/asura_scans.js" {
		t.Fatalf("url = %s", idx.Entries[0].URL)
	}
	// An invalid url falls back to fileName, as officially.
	idx, _ = ParseComic([]byte(`[{"name":"A","key":"a","version":"1","fileName":"a.js","url":"not a url"}]`), "https://h.test/i.json")
	if idx.Entries[0].URL != "https://h.test/a.js" {
		t.Fatalf("fallback url = %s", idx.Entries[0].URL)
	}
}

func TestParseComicSkipsBadEntries(t *testing.T) {
	doc := `[
	  {"name":"ok","key":"ok","version":"1","fileName":"ok.js"},
	  {"name":"","key":"noname","version":"1","fileName":"x.js"},
	  {"name":"bad key","key":"a-b","version":"1","fileName":"x.js"},
	  {"name":"dup","key":"ok","version":"2","fileName":"x.js"},
	  {"name":"no file","key":"nofile","version":"1"},
	  {"name":"escape","key":"esc","version":"1","fileName":"../../etc/x.js"},
	  {"name":"cred","key":"cred","version":"1","url":"https://u:p@h.test/x.js"},
	  "not an object",
	  {"name":"num version","key":"numv","version":2,"fileName":"n.js"}
	]`
	idx, err := ParseComic([]byte(doc), "https://h.test/i.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 2 || idx.Entries[1].Key != "numv" || idx.Entries[1].Version != "2" {
		t.Fatalf("entries = %+v", idx.Entries)
	}
	if len(idx.Problems) != 7 {
		t.Fatalf("problems = %+v", idx.Problems)
	}
}

func TestParseComicRejects(t *testing.T) {
	if _, err := ParseComic([]byte(`{"$schema":"vcvideo/1","name":"v","plugins":[]}`), "https://h.test/vcvideo.json"); !errors.Is(err, ErrNotComicIndex) {
		t.Fatalf("video index: %v", err)
	}
	if _, err := ParseComic([]byte(`[`), "https://h.test/i.json"); err == nil {
		t.Fatal("broken JSON accepted")
	}
	if _, err := ParseComic([]byte(`[]`), "file:///i.json"); err == nil {
		t.Fatal("file index URL accepted")
	}
	// A UTF-8 BOM, common in hand-edited files, is accepted.
	if _, err := ParseComic([]byte("\xef\xbb\xbf[]"), "https://h.test/i.json"); err != nil {
		t.Fatalf("BOM: %v", err)
	}
	big := "[" + strings.Repeat(`{"name":"a","key":"a","version":"1","fileName":"a.js"},`, MaxComicEntries) + `{"name":"a","key":"a","version":"1","fileName":"a.js"}]`
	if _, err := ParseComic([]byte(big), "https://h.test/i.json"); err == nil {
		t.Fatal("oversized index accepted")
	}
}

func TestCompare(t *testing.T) {
	doc := `[
	  {"name":"A","key":"a","version":"1.2.0","fileName":"a.js"},
	  {"name":"B","key":"b","version":"1.0.0","fileName":"b.js"},
	  {"name":"C","key":"c","version":"1.0.0","fileName":"c.js"},
	  {"name":"D","key":"d","version":"1.0.0","fileName":"d.js","minAppVersion":"9.0.0"},
	  {"name":"E","key":"e","version":"2.0.0","fileName":"e.js"}
	]`
	const at = "https://h.test/i.json"
	idx, _ := ParseComic([]byte(doc), at)
	rows := idx.Compare(at, "1.6.0-dev", []Installed{
		{Key: "a", Version: "1.1.0", IndexURL: at},
		{Key: "b", Version: "1.0.0"},
		{Key: "e", Version: "1.0.0", IndexURL: "https://other.test/i.json"},
	})
	want := map[string]Status{"a": StatusUpdate, "b": StatusInstalled, "c": StatusNew, "d": StatusTooNew, "e": StatusOther}
	for _, r := range rows {
		if r.Status != want[r.Key] {
			t.Errorf("%s: status %s, want %s", r.Key, r.Status, want[r.Key])
		}
	}
	if rows[0].InstalledVersion != "1.1.0" {
		t.Errorf("installed version = %q", rows[0].InstalledVersion)
	}
}

func TestCheckUpdate(t *testing.T) {
	if ok, err := CheckUpdate("a", "1.0.0", "a", "1.0.1"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := CheckUpdate("a", "1.0.1", "a", "1.0.1"); ok {
		t.Fatal("same version reported as update")
	}
	var m *UpdateMismatch
	if _, err := CheckUpdate("a", "1", "b", "2"); !errors.As(err, &m) {
		t.Fatalf("key mismatch: %v", err)
	}
}

func TestAttributionInIndexes(t *testing.T) {
	ci, err := ParseComic([]byte(`[
	  {"name":"A","key":"a","version":"1","fileName":"a.js","author":{"name":"Alice","url":"https://a.test"},"homepage":"https://a.test/src"},
	  {"name":"B","key":"b","version":"1","fileName":"b.js"}]`), "https://h.test/index.json")
	if err != nil {
		t.Fatal(err)
	}
	if a := ci.Entries[0].Attribution; a.Authors[0].Name != "Alice" || a.Homepage != "https://a.test/src" {
		t.Fatalf("comic entry = %+v", a)
	}
	if noA, noH := ci.Entries[1].Attribution.Missing(); !noA || !noH {
		t.Fatal("missing attribution not reported")
	}
	vi, err := Parse([]byte(`{"$schema":"vcvideo/1","name":"n","author":"Maintainer","homepage":"javascript:x",
	  "plugins":[{"type":"js","key":"v","name":"V","version":"1","url":"v.js","author":["P1",{"name":"P2"}],"homepage":"https://p.test"}]}`),
		"https://h.test/vcvideo.json")
	if err != nil {
		t.Fatal(err)
	}
	if vi.Attribution.Authors[0].Name != "Maintainer" || vi.Homepage != "" {
		t.Fatalf("index attribution = %+v homepage %q", vi.Attribution, vi.Homepage)
	}
	if p := vi.Plugins[0].Attribution; len(p.Authors) != 2 || p.Homepage != "https://p.test" {
		t.Fatalf("plugin attribution = %+v", p)
	}
}
