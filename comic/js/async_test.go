package js

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/host"
)

// --- fakes ----------------------------------------------------------------------

type route struct {
	status int
	header http.Header
	body   string
}

type fakeSite struct {
	mu     sync.Mutex
	routes map[string]route
	seen   []*host.Request
}

func (f *fakeSite) Do(ctx context.Context, r *host.Request) (*host.Response, error) {
	f.mu.Lock()
	f.seen = append(f.seen, r)
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for prefix, rt := range f.routes {
		if strings.HasPrefix(r.URL, prefix) {
			st := rt.status
			if st == 0 {
				st = 200
			}
			return &host.Response{Status: st, Header: rt.header, Body: []byte(rt.body), FinalURL: r.URL}, nil
		}
	}
	return &host.Response{Status: 404, Body: []byte("not found")}, nil
}

func (f *fakeSite) requests() []*host.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*host.Request(nil), f.seen...)
}

type memCookies struct {
	mu sync.Mutex
	m  map[string][]*http.Cookie
}

func (c *memCookies) Set(p, u string, cs []*http.Cookie) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string][]*http.Cookie{}
	}
	c.m[p+"|"+u] = append(c.m[p+"|"+u], cs...)
}
func (c *memCookies) Get(p, u string) []*http.Cookie {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*http.Cookie(nil), c.m[p+"|"+u]...)
}
func (c *memCookies) Delete(p, u string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, p+"|"+u)
}

type fakeUI struct {
	mu       sync.Mutex
	messages []string
	selected *int
	dialogs  int
	invoked  chan struct{}
}

func (u *fakeUI) ShowMessage(m string) {
	u.mu.Lock()
	u.messages = append(u.messages, m)
	u.mu.Unlock()
}
func (u *fakeUI) ShowDialog(_, _ string, actions []DialogAction) error {
	u.mu.Lock()
	u.dialogs++
	u.mu.Unlock()
	if len(actions) > 0 {
		return actions[0].Invoke()
	}
	return nil
}
func (u *fakeUI) LaunchURL(string)       {}
func (u *fakeUI) ShowLoading(func()) int { return 7 }
func (u *fakeUI) CancelLoading(int)      {}
func (u *fakeUI) ShowInputDialog(_ string, _ string, v func(string) string) (*string, error) {
	s := "typed"
	if v != nil {
		if msg := v(s); msg != "" {
			s = "invalid:" + msg
		}
	}
	return &s, nil
}
func (u *fakeUI) ShowSelectDialog(string, []string, *int) (*int, error) { return u.selected, nil }

type fakeClip struct{ text string }

func (c *fakeClip) SetText(s string) error { c.text = s; return nil }
func (c *fakeClip) Text() (string, error)  { return c.text, nil }

// --- async methods ------------------------------------------------------------------

func TestDelayAndTimers(t *testing.T) {
	r := newRT(t, Options{}, nil)
	start := time.Now()
	got := eval(t, r, `(async function(){
		var order = [];
		setTimeout(function(){ order.push("t"); }, 30);
		await new Promise(function(res){ setTimeout(res, 60); });
		return order;
	})()`)
	if got != `["t"]` {
		t.Fatal(got)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("delay did not wait")
	}
}

func TestHTTPThroughJS(t *testing.T) {
	site := &fakeSite{routes: map[string]route{
		"https://ex.test/json": {header: http.Header{"Content-Type": {"application/json"}}, body: `{"a":1}`},
		"https://ex.test/bin":  {body: "\x00\x01\x02"},
		"https://ex.test/404":  {status: 404, body: "gone"},
	}}
	r, err := New(Options{}, Services{Transport: site, Env: Environment{AppVersion: "1.6.0", DefaultUA: "VC/1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := eval(t, r, `(async function(){
		var a = await Network.get("https://ex.test/json", {});
		var b = await Network.fetchBytes("GET", "https://ex.test/bin", {});
		var c = await Network.get("https://ex.test/404", {});
		var f = await fetch("https://ex.test/json");
		return [a.status, JSON.parse(a.body).a, new Uint8Array(b.body).length, c.status, f.ok, (await f.json()).a];
	})()`)
	if got != `[200,1,3,404,true,1]` {
		t.Fatal(got)
	}
	reqs := site.requests()
	if ua := reqs[0].Header.Get("User-Agent"); ua != "VC/1" {
		t.Fatalf("default UA = %q", ua)
	}
	// Outside a source requests use the global engine's own partition,
	// which no source key can collide with.
	if reqs[0].Partition != "comic::global" {
		t.Fatalf("partition = %q", reqs[0].Partition)
	}
	// A transport failure surfaces as a thrown error from the helper.
	_, err = r.Eval(context.Background(), `Network.get("ftp://ex.test/x", {})`)
	if err == nil {
		t.Fatal("bad scheme accepted")
	}
}

func TestCloudflareThroughJS(t *testing.T) {
	cf := route{status: 503, header: http.Header{"Server": {"cloudflare"}}, body: `<script>window._cf_chl_opt={}</script>`}
	site := &fakeSite{routes: map[string]route{"https://cf.test/": cf}}
	r, err := New(Options{}, Services{Transport: site, Env: Environment{AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := eval(t, r, `(async function(){
		try { await Network.get("https://cf.test/", {}); return "no"; } catch (e) { return String(e); }
	})()`)
	if !strings.HasPrefix(got, `"CloudflareException: https://cf.test/`) {
		t.Fatalf("got %s", got)
	}
}

type passVerifier struct {
	site *fakeSite
	n    int
}

func (v *passVerifier) Verify(_ context.Context, c *host.Challenge) (*host.Clearance, error) {
	v.n++
	v.site.mu.Lock()
	v.site.routes[c.URL] = route{body: "passed"}
	v.site.mu.Unlock()
	return &host.Clearance{UserAgent: "WebView/9"}, nil
}

func TestCloudflareVerifiedThroughJS(t *testing.T) {
	site := &fakeSite{routes: map[string]route{
		"https://cf.test/": {status: 503, header: http.Header{"Server": {"cloudflare"}}, body: `<script>window._cf_chl_opt={}</script>`},
	}}
	v := &passVerifier{site: site}
	r, err := New(Options{}, Services{Transport: site, Verifier: v, Env: Environment{AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := eval(t, r, `(async function(){ var x = await Network.get("https://cf.test/", {}); return [x.status, x.body]; })()`)
	if got != `[200,"passed"]` || v.n != 1 {
		t.Fatalf("got %s, verifications %d", got, v.n)
	}
	reqs := site.requests()
	if ua := reqs[len(reqs)-1].Header.Get("User-Agent"); ua != "WebView/9" {
		t.Fatalf("retry UA = %q", ua)
	}
}

func TestCookieThroughJS(t *testing.T) {
	jar := &memCookies{}
	r, err := New(Options{}, Services{Cookies: jar, Env: Environment{AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := eval(t, r, `(function(){
		Network.setCookies("https://ex.test/", [new Cookie({name: "a", value: "1"})]);
		var c = Network.getCookies("https://ex.test/");
		Network.deleteCookies("https://ex.test/");
		return [c.length, c[0].name, c[0].value, c[0].domain, Network.getCookies("https://ex.test/").length];
	})()`)
	if got != `[1,"a","1","ex.test",0]` {
		t.Fatal(got)
	}
}

func TestComputeThroughJS(t *testing.T) {
	r := newRT(t, Options{}, nil)
	if got := eval(t, r, `compute("function(a, b){ return a * b; }", 6, 7)`); got != "42" {
		t.Fatal(got)
	}
	if got := eval(t, r, `compute(function(s){ return s.toUpperCase(); }, "hi")`); got != `"HI"` {
		t.Fatal(got)
	}
	if _, err := r.Eval(context.Background(), `compute("42")`); err == nil {
		t.Fatal("non-function accepted")
	}
}

func TestUIAndClipboardThroughJS(t *testing.T) {
	two := 2
	ui := &fakeUI{selected: &two}
	clip := &fakeClip{}
	r, err := New(Options{}, Services{UI: ui, Clipboard: clip, Env: Environment{AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := eval(t, r, `(async function(){
		UI.showMessage("hello");
		var hit = 0;
		await UI.showDialog("t", "c", [{text: "OK", callback: function(){ hit++; }}]);
		var sel = await UI.showSelectDialog("pick", ["a", "b", "c"], 0);
		var txt = await UI.showInputDialog("name", function(s){ return s === "typed" ? null : "bad"; });
		await setClipboard("copied");
		var back = await getClipboard();
		return [hit, sel, txt, back, UI.showLoading(null)];
	})()`)
	if got != `[1,2,"typed","copied",7]` {
		t.Fatal(got)
	}
	// showMessage is fire-and-forget on its own goroutine; wait for it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		ui.mu.Lock()
		msgs := append([]string(nil), ui.messages...)
		ui.mu.Unlock()
		if len(msgs) == 1 && msgs[0] == "hello" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("messages = %v", msgs)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Without a handler the call fails instead of pretending.
	r2 := newRT(t, Options{}, nil)
	if _, err := r2.Eval(context.Background(), `UI.showMessage("x")`); err == nil || !strings.Contains(err.Error(), "No JavaScript UI handler") {
		t.Fatalf("no handler: %v", err)
	}
}

// --- LoadSource -------------------------------------------------------------------------

const miniSource = `
class Mini extends ComicSource {
    name = "Mini"
    key = "mini"
    version = "1.0.0"
    minAppVersion = "1.0.0"
    settings = { lang: { default: "zh" } }
    init() { this.saveData("inited", {at: 12345678901234567890n}) }
    search = {
        load: async (keyword, options, page) => {
            const res = await Network.get("https://mini.test/s?q=" + encodeURIComponent(keyword) + "&p=" + page, {})
            const doc = new HtmlDocument(res.body)
            const comics = doc.querySelectorAll("li > a").map(a => new Comic({id: a.attributes.href.slice(1), title: a.text, cover: ""}))
            doc.dispose()
            return {comics, maxPage: 1, lang: this.loadSetting("lang")}
        }
    }
}
`

func TestLoadSourceAndCall(t *testing.T) {
	site := &fakeSite{routes: map[string]route{
		"https://mini.test/s": {body: `<ul><li><a href="/c1">One</a></li><li><a href="/c2">Two</a></li></ul>`},
	}}
	st := newMem()
	r, err := New(Options{}, Services{Transport: site, Storage: st, Env: Environment{AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	meta, err := r.LoadSource(context.Background(), miniSource)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Key != "mini" || meta.Name != "Mini" {
		t.Fatalf("meta = %+v", meta)
	}
	if got := string(st.data["mini/inited"]); got != `{"at":12345678901234567890}` {
		t.Fatalf("init did not run or lost precision: %s", got)
	}
	out, err := r.Call(context.Background(), "mini", "search.load", "a b", []any{}, json.Number("1"))
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Comics []struct{ ID, Title string }
		Lang   string
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Comics) != 2 || res.Comics[1].ID != "c2" || res.Lang != "zh" {
		t.Fatalf("search = %s", out)
	}
	reqs := site.requests()
	if p := reqs[len(reqs)-1].Partition; p != "comic:mini" {
		t.Fatalf("partition = %q", p)
	}
	if ok, _ := r.Has(context.Background(), "mini", "search.load"); !ok {
		t.Fatal("Has(search.load) = false")
	}
	if _, err := r.Call(context.Background(), "mini", "comic.loadInfo", "x"); !errors.Is(err, ErrNoFunction) {
		t.Fatalf("missing function: %v", err)
	}
}

func TestLoadSourceRejections(t *testing.T) {
	r := newRT(t, Options{}, nil)
	ctx := context.Background()
	if _, err := r.LoadSource(ctx, miniSource); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"duplicate key":   miniSource,
		"bad key":         strings.Replace(miniSource, `key = "mini"`, `key = "mi-ni"`, 1),
		"newer app":       strings.Replace(strings.Replace(miniSource, `"mini"`, `"mini2"`, 1), `minAppVersion = "1.0.0"`, `minAppVersion = "9.0.0"`, 1),
		"no class":        `var x = 1`,
		"wrong base":      "class Foo extends Bar {}",
		"throwing ctor":   "class Bad extends ComicSource { constructor(){ super(); throw new Error('nope') } }",
		"missing version": "class NoVer extends ComicSource { name = 'n'; key = 'nover' }",
		"init throws":     "class T extends ComicSource { name='t'; key='tthrow'; version='1'; init(){ throw new Error('x') } }",
	}
	for name, src := range cases {
		if _, err := r.LoadSource(ctx, src); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A rejected source must not stay registered.
	if ok, _ := r.Has(ctx, "tthrow", "init"); ok {
		t.Error("source whose init failed is still registered")
	}
	if len(r.Sources()) != 1 {
		t.Errorf("sources = %+v", r.Sources())
	}
}

// --- a real source --------------------------------------------------------------------------

// The wnacg source from laincat/vensrc (commit 2378bf3) is used as a real-world
// fixture. The script has no licence that would let us redistribute it, so
// it is not copied into this repository: set VC_WNACG_JS to the path of
// sources/wnacg.js in a vensrc checkout to run the test.
const wnacgEnv = "VC_WNACG_JS"

const wnacgRelease = `<html><head><title>紳士漫畫地址發布頁</title></head><body><ul>
<li>紳士漫畫最新地址：<a href="https://www.wn003.cfd/">www.wn003.cfd</a></li></ul></body></html>`

const wnacgSearch = `<html><body><p class="result">共 <b>2</b> 條</p>
<div class="gallary_wrap"><ul class="cc">
<li><div class="pic_box"><a href="/photos-index-aid-111.html"><img data-original="//img.wn.test/c/111.jpg" src="x"></a></div>
<div class="info"><div class="title"><a href="/photos-index-aid-111.html">作品一</a></div><div class="info_col">10張</div></div></li>
<li><div class="pic_box"><a href="/photos-index-aid-222.html"><img src="//img.wn.test/c/222.jpg"></a></div>
<div class="info"><div class="title"><a href="/photos-index-aid-222.html">作品二</a></div><div class="info_col">20張</div></div></li>
<li><div class="pic_box"><a href="/link-ad.html"><img src="//ad.test/a.jpg"></a></div>
<div class="info"><div class="title"><a href="/link-ad.html">广告</a></div></div></li>
</ul></div></body></html>`

const wnacgDetail = `<html><body><div class="userwrap"><h2>作品一</h2>
<div class="asTBcell uwthumb"><img src="//img.wn.test/c/111.jpg"></div>
<div class="asTBcell uwconn"><label>分類：同人誌</label><label>頁數：10P</label>
<a class="tagshow">tag1</a><a class="tagshow">tag2</a><p>简介文字</p></div>
<div class="asTBcell uwuinfo"><a><p>上传者</p></a></div></div></body></html>`

const wnacgGallery = `var imglist = [{ url: fast_img_host+\"//img.wn.test/p/1.jpg\", caption: \"1\"},{ url: fast_img_host+\"//img.wn.test/p/2.jpg\", caption: \"2\"}];`

func TestRealSourceWnacg(t *testing.T) {
	path := os.Getenv(wnacgEnv)
	if path == "" {
		t.Skip(wnacgEnv + " is not set")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	site := &fakeSite{routes: map[string]route{
		"https://wnacg01.link/":                             {body: wnacgRelease},
		"https://www.wnacg.com/search/":                     {body: wnacgSearch},
		"https://www.wnacg.com/photos-index-page-1-aid-111": {body: wnacgDetail},
		"https://www.wnacg.com/photos-gallery-aid-111":      {body: wnacgGallery},
	}}
	st := newMem()
	r, err := New(Options{}, Services{Transport: site, Storage: st, Cookies: &memCookies{},
		Env: Environment{AppVersion: "1.6.0", Locale: "zh_CN", Platform: "ios", DefaultUA: "VC/1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()

	meta, err := r.LoadSource(ctx, string(src))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Key != "wnacg" || meta.Version == "" {
		t.Fatalf("meta = %+v", meta)
	}
	// init() synchronised the mirror list from the release page.
	if !strings.Contains(string(st.data["wnacg/mirrorCache"]), "www.wn003.cfd") {
		t.Fatalf("mirrorCache = %s", st.data["wnacg/mirrorCache"])
	}

	out, err := r.Call(ctx, "wnacg", "search.load", "关键词", []any{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var search struct {
		Comics []struct {
			ID, Title, Cover, Description string
		}
		MaxPage int
	}
	if err := json.Unmarshal(out, &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Comics) != 2 || search.Comics[0].ID != "111" || search.Comics[0].Title != "作品一" ||
		search.Comics[0].Cover != "https://img.wn.test/c/111.jpg" || search.MaxPage != 1 {
		t.Fatalf("search = %s", out)
	}

	out, err = r.Call(ctx, "wnacg", "comic.loadInfo", "111")
	if err != nil {
		t.Fatal(err)
	}
	var details map[string]any
	if err := json.Unmarshal(out, &details); err != nil {
		t.Fatal(err)
	}
	if details["title"] != "作品一" || details["description"] != "简介文字" || details["uploader"] != "上传者" {
		t.Fatalf("details = %s", out)
	}

	out, err = r.Call(ctx, "wnacg", "comic.loadEp", "111", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ep struct{ Images []string }
	if err := json.Unmarshal(out, &ep); err != nil {
		t.Fatal(err)
	}
	if len(ep.Images) != 2 || ep.Images[0] != "https://img.wn.test/p/1.jpg" {
		t.Fatalf("images = %s", out)
	}
	for _, req := range site.requests() {
		if req.Partition != "comic:wnacg" {
			t.Fatalf("request %s used partition %q", req.URL, req.Partition)
		}
	}
}
