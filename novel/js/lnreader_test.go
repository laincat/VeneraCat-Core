package js_test

import (
	"context"
	"strings"
	"testing"

	comicjs "github.com/laincat/veneracat-core/comic/js"
	"github.com/laincat/veneracat-core/host"
	noveljs "github.com/laincat/veneracat-core/novel/js"
)

const lnReaderFixture = `
class DemoLN extends NovelSource {
  name = "Demo LNReader"
  key = "ln_demo"
  version = "1.0.0"
  minAppVersion = "1.0.0"
  url = "https://ln.example.test/"

  constructor() {
    super()
    this._ln = LNReaderAdapter.attach(this, function(require, exports, module) {
      Object.defineProperty(exports, "__esModule", { value: true });
      const cheerio = require("cheerio");
      const { fetchApi, fetchText } = require("@libs/fetch");
      const { NovelStatus } = require("@libs/novelStatus");
      const { FilterTypes } = require("@libs/filterInputs");
      const { defaultCover } = require("@libs/defaultCover");
      const { storage } = require("@libs/storage");

      class FakeLNPlugin {
        constructor() {
          this.id = "ln_demo";
          this.name = "Demo LNReader";
          this.site = "https://ln.example.test/";
          this.version = "1.0.0";
          this.filters = {
            genre: { label: "Genre", type: FilterTypes.Picker, value: "all", options: [] }
          };
        }

        async popularNovels(page, opts) {
          storage.set("lastFilter", opts.filters.genre.value);
          const html = await fetchText(this.site + "popular?p=" + page);
          const $ = cheerio.load(html);
          const out = [];
          $(".book").each((_, el) => {
            const $el = $(el);
            out.push({
              name: $el.find("a").text().trim(),
              path: $el.find("a").attr("href"),
              cover: $el.find("img").attr("src") || defaultCover
            });
          });
          return out;
        }

        async searchNovels(term, page) {
          const res = await fetchApi(this.site + "search?q=" + encodeURIComponent(term) + "&p=" + page);
          const html = await res.text();
          const $ = cheerio.load(html);
          return [{
            name: $("h1").text().trim() + ":" + storage.get("lastFilter"),
            path: "book/42",
            cover: "/img/42.jpg"
          }];
        }

        async parseNovel(novelPath) {
          return {
            path: novelPath,
            name: "Chronicles",
            cover: "/img/42.jpg",
            author: "Jane",
            summary: "A multi-page novel.",
            status: NovelStatus.Ongoing,
            genres: "Fantasy, Magic",
            totalPages: 2,
            chapters: []
          };
        }

        async parsePage(novelPath, page) {
          const html = await fetchText(this.site + novelPath + "/toc?p=" + page);
          const $ = cheerio.load(html);
          const chapters = [];
          $("li a").each((_, el) => {
            chapters.push({
              name: $(el).text().trim(),
              path: $(el).attr("href"),
              releaseTime: $(el).attr("data-time")
            });
          });
          return { chapters };
        }

        async parseChapter(chapterPath) {
          const html = await fetchText(this.site + chapterPath);
          const $ = cheerio.load(html);
          return $("#body").html();
        }
      }
      exports.default = new FakeLNPlugin();
    })
  }
  popular(page) { return this._ln.popular(page) }
  search(keyword, page) { return this._ln.search(keyword, page) }
  detail(id) { return this._ln.detail(id) }
  chapter(novelId, chapId) { return this._ln.chapter(novelId, chapId) }
}
`

func TestLNReaderAdapterAndCheerioBundle(t *testing.T) {
	tr := &fakeTransport{
		handler: func(req *host.Request) (*host.Response, error) {
			switch {
			case strings.Contains(req.URL, "/popular?p=1"):
				return htmlResponse(200, `<div class="book"><a href="book/42">Chronicles</a><img src="/img/42.jpg"></div>`), nil
			case strings.Contains(req.URL, "/search?q=chron"):
				return htmlResponse(200, `<h1>Match</h1>`), nil
			case strings.Contains(req.URL, "/book/42/toc?p=1"):
				return htmlResponse(200, `<ul><li><a href="chap/1" data-time="2026-11-01">Ch 1</a></li></ul>`), nil
			case strings.Contains(req.URL, "/book/42/toc?p=2"):
				return htmlResponse(200, `<ul><li><a href="chap/2" data-time="2026-11-02">Ch 2</a></li></ul>`), nil
			case strings.Contains(req.URL, "chap/1"):
				return htmlResponse(200, `<div id="body"><p>Paragraph 1</p><p>Paragraph 2</p></div>`), nil
			default:
				return htmlResponse(404, "not found"), nil
			}
		},
	}

	rt, err := noveljs.New(noveljs.Options{}, noveljs.Services{
		Transport: tr,
		Storage:   newMemStorage(),
		Cookies:   newMemCookies(),
		Env:       comicjs.Environment{AppVersion: "1.0.0"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()

	ctx := context.Background()
	if _, err := rt.LoadSource(ctx, lnReaderFixture); err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	p, err := rt.Provider("ln_demo")
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}

	pop, err := p.Popular(ctx, 1)
	if err != nil {
		t.Fatalf("Popular: %v", err)
	}
	if len(pop.Items) != 1 || pop.Items[0].ID != "book/42" || pop.Items[0].Title != "Chronicles" || pop.Items[0].Cover != "https://ln.example.test/img/42.jpg" {
		t.Fatalf("unexpected popular: %+v", pop)
	}

	srch, err := p.Search(ctx, "chron", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(srch.Items) != 1 || srch.Items[0].Title != "Match:all" {
		t.Fatalf("unexpected search (storage check): %+v", srch)
	}

	det, err := p.Detail(ctx, "book/42")
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if det.Title != "Chronicles" || det.Status != "Ongoing" || len(det.Tags) != 2 || len(det.Chapters) != 2 {
		t.Fatalf("unexpected detail: %+v", det)
	}
	if det.Chapters[0].ID != "chap/1" || det.Chapters[1].ID != "chap/2" || det.Chapters[1].Time != "2026-11-02" {
		t.Fatalf("unexpected paginated chapters: %+v", det.Chapters)
	}

	ch, err := p.Chapter(ctx, "book/42", "chap/1")
	if err != nil {
		t.Fatalf("Chapter: %v", err)
	}
	if ch.HTML != "<p>Paragraph 1</p><p>Paragraph 2</p>" {
		t.Fatalf("unexpected chapter html: %q", ch.HTML)
	}
}

func TestLNReaderFormDataShim(t *testing.T) {
	var gotBody string
	var gotCT string
	tr := &fakeTransport{
		handler: func(req *host.Request) (*host.Response, error) {
			gotBody = string(req.Body)
			gotCT = req.Header.Get("Content-Type")
			return htmlResponse(200, "ok"), nil
		},
	}
	rt, err := noveljs.New(noveljs.Options{}, noveljs.Services{
		Transport: tr,
		Env:       comicjs.Environment{AppVersion: "1.0.0"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()

	script := `
class FormLN extends NovelSource {
  name = "Form LN"
  key = "ln_form"
  version = "1.0.0"
  url = "https://form.example.test/"
  constructor() {
    super()
    this._ln = LNReaderAdapter.attach(this, function(require, exports) {
      const { fetchApi } = require("@libs/fetch");
      exports.default = {
        site: "https://form.example.test/",
        async searchNovels(term) {
          const fd = new FormData();
          fd.append("searchkey", term);
          const res = await fetchApi(this.site + "search", { method: "POST", body: fd });
          return [{ name: await res.text(), path: "b/1" }];
        }
      };
    })
  }
  search(k, p) { return this._ln.search(k, p) }
}
`
	ctx := context.Background()
	if _, err := rt.LoadSource(ctx, script); err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	p, err := rt.Provider("ln_form")
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	page, err := p.Search(ctx, "hello", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Title != "ok" {
		t.Fatalf("Search: page=%+v err=%v", page, err)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data; boundary=") || !strings.Contains(gotBody, "hello") {
		t.Fatalf("unexpected multipart payload: ct=%q body=%q", gotCT, gotBody)
	}
}
