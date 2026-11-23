package library

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/laincat/veneracat-core/gohost"
)

// onePixel is a 1x1 PNG: enough for the image path to be exercised.
const onePixel = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89" +
	"\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82"

// sourceScript reads a search page over the network, keeps a value, and
// answers details and pages without one.
const sourceScript = `
class Test extends ComicSource {
  name = "测试源"; key = "t"; version = "1.0.0"
  author = {name: "laincat", url: "https://example.test"}
  search = { load: async (keyword, options, page) => {
    const res = await Network.get(BASE + "/search?q=" + encodeURIComponent(keyword), {})
    const doc = new HtmlDocument(res.body)
    const comics = doc.querySelectorAll("a").map((a) => new Comic({
      id: a.attributes.href, title: a.text, cover: BASE + "/cover.png",
    }))
    doc.dispose()
    this.saveData("last", {keyword, n: 12345678901234567890n})
    return {comics, maxPage: 1}
  } }
  comic = {
    // Chapters and tags are Maps, as real sources write them: a JS object
    // would order its integer-like keys itself, but a Map keeps the
    // source's order, which is reading order.
    loadInfo: async (id) => {
      const chapters = new Map(); chapters.set("10", "第十话"); chapters.set("2", "第二话")
      const tags = new Map(); tags.set("作者", ["甲"]); tags.set("标签", ["b", "a"])
      return {title: id.toUpperCase(), description: "说明", tags, chapters}
    },
    loadEp: async (comicId, epId) => ({images: [BASE + "/p1.png", BASE + "/p2.png"]}),
  }
  thumbnail = { load: async (url) => ({url, headers: {"X-Test": "1"}}) }
}
`

func TestInstallSearchRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Add("Set-Cookie", "sid=abc; Path=/")
			_, _ = w.Write([]byte(`<ul><li><a href="c1">一号</a></li><li><a href="c2">二号</a></li></ul>`))
		case strings.HasSuffix(r.URL.Path, ".png"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte(onePixel))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	c, err := core.Open(core.Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lib := New(c)

	// The script needs the server's address; Core has no globals, so the
	// address is part of the script.
	body := "const BASE = " + `"` + srv.URL + `"` + ";\n" + sourceScript
	rec, err := lib.InstallText(ctx, body, "test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Key != "t" || rec.Name != "测试源" {
		t.Fatalf("record = %+v", rec)
	}
	if len(lib.Sources()) != 1 {
		t.Fatalf("sources = %+v", lib.Sources())
	}

	opts, err := c.DefaultOptions(ctx, "t")
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.Search(ctx, "t", "关键词", opts, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Comics) != 2 || page.Comics[0].ID != "c1" || page.Comics[0].Title != "一号" {
		t.Fatalf("search = %+v", page)
	}
	if page.MaxPage != 1 {
		t.Errorf("maxPage = %d", page.MaxPage)
	}
	// The source's data survived, digit for digit.
	stored, ok, _ := c.Store().LoadData("t", "last")
	if !ok || !strings.Contains(string(stored), "12345678901234567890") {
		t.Errorf("stored = %s", stored)
	}
	// The search response's cookie went to this source's partition only.
	if got := c.Jar().Header("comic:t", srv.URL+"/"); got != "sid=abc" {
		t.Errorf("cookie = %q", got)
	}
	if got := c.Jar().Header("comic:other", srv.URL+"/"); got != "" {
		t.Errorf("another partition saw %q", got)
	}

	d, err := c.Details(ctx, "t", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "C1" || d.Description != "说明" {
		t.Fatalf("details = %+v", d)
	}
	if len(d.Chapters) != 2 || d.Chapters[0].ID != "10" || d.Chapters[1].ID != "2" {
		t.Fatalf("chapters = %+v", d.Chapters)
	}
	if len(d.Tags) != 2 || d.Tags[0].Name != "作者" {
		t.Fatalf("tags = %+v", d.Tags)
	}

	images, err := c.ChapterImages(ctx, "t", "c1", "10")
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || !strings.HasSuffix(images[0], "/p1.png") {
		t.Fatalf("images = %v", images)
	}
	data, ctype, err := c.FetchImage(ctx, "t", images[0], "c1", "10")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || ctype != "image/png" {
		t.Fatalf("image = %d bytes, %q", len(data), ctype)
	}

	// A new Core restores the same sources from disk.
	c2, err := core.Open(core.Options{DataDir: c.Store().Root(), AppVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if failed, err := New(c2).Restore(ctx); err != nil || len(failed) != 0 {
		t.Fatalf("restore: %v %v", err, failed)
	}
	if opts, err := c2.DefaultOptions(ctx, "t"); err != nil {
		t.Fatal(err)
	} else if page, err := c2.Search(ctx, "t", "关键词", opts, 1); err != nil || len(page.Comics) != 2 {
		t.Fatalf("restored search = %+v %v", page, err)
	}
}

func TestInstallRejectsPlainScriptURL(t *testing.T) {
	ctx := context.Background()
	c, err := core.Open(core.Options{DataDir: filepath.Join(t.TempDir(), "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lib := New(c)
	if _, err := lib.InstallURL(ctx, "file:///etc/passwd"); err == nil {
		t.Fatal("file URL accepted")
	}
	if _, err := lib.InstallText(ctx, "not a source", "test"); err == nil {
		t.Fatal("invalid script accepted")
	}
}
