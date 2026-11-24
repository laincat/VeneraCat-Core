package js_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	comicjs "github.com/laincat/veneracat-core/comic/js"
	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/novel"
	noveljs "github.com/laincat/veneracat-core/novel/js"
)

type fakeTransport struct {
	mu       sync.Mutex
	handler  func(*host.Request) (*host.Response, error)
	requests []*host.Request
}

func (f *fakeTransport) Do(_ context.Context, req *host.Request) (*host.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	h := f.handler
	f.mu.Unlock()
	return h(req)
}

type memStorage struct {
	mu   sync.Mutex
	data map[string]map[string]json.RawMessage
}

func newMemStorage() *memStorage {
	return &memStorage{data: map[string]map[string]json.RawMessage{}}
}

func (m *memStorage) LoadData(sourceKey, dataKey string) (json.RawMessage, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[sourceKey][dataKey]
	return v, ok, nil
}

func (m *memStorage) SaveData(sourceKey, dataKey string, value json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[sourceKey] == nil {
		m.data[sourceKey] = map[string]json.RawMessage{}
	}
	m.data[sourceKey][dataKey] = append(json.RawMessage(nil), value...)
	return nil
}

func (m *memStorage) DeleteData(sourceKey, dataKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data[sourceKey], dataKey)
	return nil
}

func (m *memStorage) LoadSetting(sourceKey, settingKey string) (json.RawMessage, bool, error) {
	return nil, false, nil
}

func (m *memStorage) IsLogged(sourceKey string) bool { return false }

type memCookies struct {
	mu   sync.Mutex
	vals map[string][]*http.Cookie
}

func newMemCookies() *memCookies { return &memCookies{vals: map[string][]*http.Cookie{}} }

func (c *memCookies) Get(partition, urlStr string) []*http.Cookie {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vals[partition+"|"+urlStr]
}

func (c *memCookies) Set(partition, urlStr string, cookies []*http.Cookie) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vals[partition+"|"+urlStr] = append([]*http.Cookie(nil), cookies...)
}

func (c *memCookies) Delete(partition, urlStr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.vals, partition+"|"+urlStr)
}

func htmlResponse(status int, body string) *host.Response {
	return &host.Response{
		Status: status,
		Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:   []byte(body),
	}
}

const demoNovelScript = `
class DemoNovel extends NovelSource {
  name = "Demo Novel"
  key = "demo_novel"
  version = "1.0.0"
  minAppVersion = "1.0.0"
  url = "https://novel.example.test"
  author = { name: "Lain", url: "https://github.com/laincat" }
  homepage = "https://novel.example.test/about"

  async popular(page) {
    const res = await Network.get("https://novel.example.test/popular?page=" + page)
    if (res.status !== 200) throw new Error("http " + res.status)
    return {
      items: [
        { id: 9007199254740993n, title: "Starlight", cover: "https://novel.example.test/c1.jpg", author: "Alice" },
        { id: "n2", title: "Moonfall", cover: "https://novel.example.test/c2.jpg" }
      ],
      maxPage: 3
    }
  }

  async search(keyword, page) {
    const res = await Network.get("https://novel.example.test/search?q=" + encodeURIComponent(keyword) + "&p=" + page)
    return {
      items: [
        { id: "s1", title: "Found: " + keyword + " (" + res.body + ")", cover: "https://novel.example.test/s1.jpg", author: "Bob" }
      ],
      maxPage: 1
    }
  }

  async detail(id) {
    return {
      id: id,
      title: "Starlight",
      cover: "https://novel.example.test/c1.jpg",
      author: "Alice",
      summary: "A test summary.",
      status: "ongoing",
      tags: ["fantasy", "adventure"],
      chapters: [
        { id: "ch1", title: "Chapter 1", time: "2026-11-24" },
        { id: "ch2", title: "Chapter 2" }
      ]
    }
  }

  async chapter(novelId, chapId) {
    return {
      html: "<div class=\"content\"><p>Hello " + novelId + "/" + chapId + "</p><script>ignore</script></div>"
    }
  }
}
`

func TestNovelFourMethods(t *testing.T) {
	tr := &fakeTransport{
		handler: func(req *host.Request) (*host.Response, error) {
			if strings.Contains(req.URL, "/search") {
				return htmlResponse(200, "ok-search"), nil
			}
			return htmlResponse(200, "ok-popular"), nil
		},
	}
	rt, err := noveljs.New(noveljs.Options{}, noveljs.Services{
		Transport: tr,
		Storage:   newMemStorage(),
		Cookies:   newMemCookies(),
		Env:       comicjs.Environment{AppVersion: "1.0.0", Locale: "en"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()

	ctx := context.Background()
	meta, err := rt.LoadSource(ctx, demoNovelScript)
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	if meta.Key != "demo_novel" || meta.Kind != comicjs.KindNovel || meta.MinAppVersion != "1.0.0" {
		t.Fatalf("unexpected meta: %+v", meta)
	}

	var p novel.Provider
	p, err = rt.Provider("demo_novel")
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	info := p.Info()
	if info.Key != "demo_novel" || info.Kind != "js" || info.BaseURL != "https://novel.example.test" {
		t.Fatalf("unexpected info: %+v", info)
	}

	pop, err := p.Popular(ctx, 2)
	if err != nil {
		t.Fatalf("Popular: %v", err)
	}
	if pop.MaxPage != 3 || len(pop.Items) != 2 {
		t.Fatalf("unexpected popular page: %+v", pop)
	}
	if pop.Items[0].ID != "9007199254740993" || pop.Items[0].Author != "Alice" {
		t.Fatalf("bigint ID or author mismatch: %+v", pop.Items[0])
	}
	if pop.Items[1].ID != "n2" || pop.Items[1].Author != "" {
		t.Fatalf("optional author mismatch: %+v", pop.Items[1])
	}

	srch, err := p.Search(ctx, "star", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(srch.Items) != 1 || srch.Items[0].Title != "Found: star (ok-search)" || srch.MaxPage != 1 {
		t.Fatalf("unexpected search result: %+v", srch)
	}

	det, err := p.Detail(ctx, "9007199254740993")
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if det.Title != "Starlight" || det.Status != "ongoing" || len(det.Tags) != 2 || len(det.Chapters) != 2 {
		t.Fatalf("unexpected detail: %+v", det)
	}
	if det.Chapters[0].Time != "2026-11-24" || det.Chapters[1].Time != "" {
		t.Fatalf("unexpected chapter time: %+v", det.Chapters)
	}

	chap, err := p.Chapter(ctx, "9007199254740993", "ch1")
	if err != nil {
		t.Fatalf("Chapter: %v", err)
	}
	wantHTML := `<div class="content"><p>Hello 9007199254740993/ch1</p><script>ignore</script></div>`
	if chap.HTML != wantHTML {
		t.Fatalf("chapter HTML was altered: got %q want %q", chap.HTML, wantHTML)
	}
}

func TestNovelEngineIsolation(t *testing.T) {
	storage := newMemStorage()
	cookies := newMemCookies()
	rt, err := noveljs.New(noveljs.Options{}, noveljs.Services{
		Storage: storage,
		Cookies: cookies,
		Env:     comicjs.Environment{AppVersion: "1.0.0"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()

	sourceA := `
class SourceA extends NovelSource {
  name = "Alpha"
  key = "novel_alpha"
  version = "1.0.0"
  init() {
    globalThis.__leakedSecret = "alpha-only"
    this.saveData("token", "secret-A")
    Network.setCookies("https://novel.example.test", [{ name: "sid", value: "cookie-A" }])
  }
  async popular(page) {
    const tok = this.loadData("token")
    return { items: [{ id: "1", title: String(tok) }] }
  }
}
`
	sourceB := `
class SourceB extends NovelSource {
  name = "Beta"
  key = "novel_beta"
  version = "1.0.0"
  async popular(page) {
    const leaked = typeof globalThis.__leakedSecret === "undefined" ? "none" : globalThis.__leakedSecret
    const tok = this.loadData("token")
    const ck = await Network.getCookies("https://novel.example.test")
    return {
      items: [{
        id: "1",
        title: "global=" + leaked + ";data=" + String(tok) + ";cookies=" + JSON.stringify(ck)
      }]
    }
  }
}
`
	ctx := context.Background()
	if _, err := rt.LoadSource(ctx, sourceA); err != nil {
		t.Fatalf("LoadSource A: %v", err)
	}
	if _, err := rt.LoadSource(ctx, sourceB); err != nil {
		t.Fatalf("LoadSource B: %v", err)
	}

	pA, err := rt.Provider("novel_alpha")
	if err != nil {
		t.Fatalf("Provider A: %v", err)
	}
	pageA, err := pA.Popular(ctx, 1)
	if err != nil || len(pageA.Items) != 1 || pageA.Items[0].Title != "secret-A" {
		t.Fatalf("A could not read its own storage: page=%+v err=%v", pageA, err)
	}

	pB, err := rt.Provider("novel_beta")
	if err != nil {
		t.Fatalf("Provider B: %v", err)
	}
	pageB, err := pB.Popular(ctx, 1)
	if err != nil {
		t.Fatalf("Popular B: %v", err)
	}
	got := pageB.Items[0].Title
	if strings.Contains(got, "alpha-only") || strings.Contains(got, "secret-A") || strings.Contains(got, "cookie-A") {
		t.Fatalf("isolation breach in source B: %q", got)
	}
}

func TestNovelErrorsShowLocation(t *testing.T) {
	rt, err := noveljs.New(noveljs.Options{}, noveljs.Services{
		Env: comicjs.Environment{AppVersion: "1.0.0"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()

	ctx := context.Background()

	if _, err := rt.LoadSource(ctx, "class Broken extends NovelSource { name = 'x'; key = 'broken'; version = '1'; bad( { }"); err == nil {
		t.Fatal("expected syntax error on LoadSource")
	}

	faulty := `
class FaultyNovel extends NovelSource {
  name = "Faulty"
  key = "faulty_novel"
  version = "1.0.0"

  async search(keyword, page) {
    return Promise.reject(new Error("search-rejected"))
  }

  async detail(id) {
    const broken = null
    return broken.title
  }

  async chapter(novelId, chapId) {
    return { text: "missing html key" }
  }
}
`
	if _, err := rt.LoadSource(ctx, faulty); err != nil {
		t.Fatalf("LoadSource faulty: %v", err)
	}
	p, err := rt.Provider("faulty_novel")
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}

	_, err = p.Popular(ctx, 1)
	if !errors.Is(err, comicjs.ErrNoFunction) {
		t.Fatalf("expected ErrNoFunction for missing popular, got %v", err)
	}
	if !strings.Contains(err.Error(), "faulty_novel.popular") {
		t.Fatalf("missing method error lacks source/method location: %v", err)
	}

	_, err = p.Search(ctx, "q", 1)
	if err == nil || !strings.Contains(err.Error(), "faulty_novel.search") || !strings.Contains(err.Error(), "search-rejected") {
		t.Fatalf("unexpected search rejection error: %v", err)
	}
	var se *noveljs.ScriptError
	if !errors.As(err, &se) {
		t.Fatalf("expected ScriptError in chain, got %T: %v", err, err)
	}

	_, err = p.Detail(ctx, "1")
	if err == nil || !strings.Contains(err.Error(), "faulty_novel.detail") || !strings.Contains(err.Error(), "TypeError") {
		t.Fatalf("unexpected detail runtime error: %v", err)
	}
	var ce *noveljs.CallError
	if !errors.As(err, &ce) || ce.Key != "faulty_novel" || ce.Method != "detail" {
		t.Fatalf("expected CallError with key/method, got %v", err)
	}

	_, err = p.Chapter(ctx, "1", "c1")
	if err == nil || !strings.Contains(err.Error(), "faulty_novel.chapter") || !strings.Contains(err.Error(), "html") {
		t.Fatalf("unexpected chapter shape error: %v", err)
	}
}
