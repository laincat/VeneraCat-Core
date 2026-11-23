package gohost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// A source sometimes hands back an image address without its scheme, such as
// "//images.example.com/1.jpg": RFC 3986 reads that as "the scheme of the
// page", which for a reader is https. The client must be able to fetch such an
// address instead of refusing it.
//
// The source is a real one, so the whole path is exercised: install, ask the
// source for the image, fetch it.
func TestImageWithASchemeRelativeURLIsFetched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page/c1":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><body><img class="pic" src="//` + r.Host + `/img/1.jpg"></body></html>`))
		case "/img/1.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "1.6.0", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	script := `
class SchemeLess extends ComicSource {
    name = "SchemeLess"; key = "schemeless"; version = "1.0.0"
    comic = {
        loadEp: async (comicId, epId) => {
            const res = await Network.get("` + srv.URL + `/page/" + comicId, {})
            const doc = new HtmlDocument(res.body)
            const img = doc.querySelector("img.pic")
            return { images: [img.attributes["src"]] }
        }
    }
}
`
	if _, err := c.Install(context.Background(), script, "test", ""); err != nil {
		t.Fatalf("install: %v", err)
	}

	raw, err := c.Call(context.Background(), "schemeless", "comic.loadEp", "c1")
	if err != nil {
		t.Fatalf("loadEp: %v", err)
	}
	if !strings.Contains(string(raw), "//"+strings.TrimPrefix(srv.URL, "http://")) {
		t.Fatalf("the source did not return a scheme-relative address: %s", raw)
	}

	// The address without its scheme must still be fetchable.
	_, _, err = c.FetchImage(context.Background(), "schemeless", "//"+strings.TrimPrefix(srv.URL, "http://")+"/img/1.jpg", "c1", "e1")
	if err != nil {
		t.Fatalf("fetch image: %v", err)
	}
}

// The rule must not become a way through: a file or a script address is still
// refused, because it is not scheme-relative and nothing is rewritten.
func TestImageWithAnUnsafeSchemeIsRefused(t *testing.T) {
	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "1.6.0", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "ftp://x.test/a.jpg"} {
		if _, _, err := c.FetchImage(context.Background(), "mini", raw, "c1", "e1"); err == nil {
			t.Errorf("FetchImage(%q) was allowed", raw)
		}
	}
}

// Official sources build addresses with the standard URL class: manhuaren
// reads `new URL(imageUrl).host`. The engine's own realm must provide it, and
// link URLs back to their search parameters, or a source that works elsewhere
// fails here for a reason that looks like a source bug.
func TestSourceURLClassIsUsableThroughAComicSource(t *testing.T) {
	c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.3", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if _, err := c.Install(context.Background(), `class UrlProbe extends ComicSource {
  name = "UrlProbe"; key = "urlprobe"; version = "1.0.0"
  search = {load: async (keyword) => {
    const u = new URL("https://Images.Example.Test:8443/a/b.jpg?x=1&y=2")
    const p = new URLSearchParams("x=1&y=2")
    p.set("z", "3")
    return {comics: [{id: keyword, title: u.host + "|" + p.toString(), cover: u.href}], maxPage: 1}
  }}
}`, "test", ""); err != nil {
		t.Fatalf("install: %v", err)
	}

	page, err := c.Search(context.Background(), "urlprobe", "probe", nil, 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Comics) != 1 {
		t.Fatalf("search returned %d comics", len(page.Comics))
	}
	if got, want := page.Comics[0].Title, "images.example.test:8443|x=1&y=2&z=3"; got != want {
		t.Fatalf("URL results = %q, want %q", got, want)
	}
	if got, want := page.Comics[0].Cover, "https://images.example.test:8443/a/b.jpg?x=1&y=2"; got != want {
		t.Fatalf("URL href = %q, want %q", got, want)
	}
}

// A source that returns no image address leaves the reader saying only "not
// http(s)"; the log names the address it got, which is what makes that
// actionable.
func TestInvalidImageURLIsLogged(t *testing.T) {
	c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.3", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Install(context.Background(), `class NoImages extends ComicSource {
  name = 'NoImages'; key = 'noimages'; version = '1'
  search = {load: async () => ({comics: [], maxPage: 1})}
}`, "file", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.fetchImageOnce(context.Background(), "noimages", ImageRequest{URL: ""}); err == nil {
		t.Fatal("an empty address was accepted")
	}
	lines := c.LogLines()
	if len(lines) == 0 {
		t.Fatal("nothing was logged")
	}
	last := lines[len(lines)-1]
	if last.Key != "noimages" || !strings.Contains(last.Content, "image URL") {
		t.Errorf("logged %+v", last)
	}
}
