package mobile

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHost plays the Swift side: it answers HTTP from a route table and
// keeps data in memory.
type fakeHost struct {
	mu        sync.Mutex
	routes    map[string]string
	data      map[string]string
	logs      []string
	verified  int
	cancelled []int64
	panicHTTP bool
	slowHTTP  chan struct{}

	jars      map[string]map[string]wireCookie
	answers   []string
	events    []string
	clipboard string
}

func (h *fakeHost) HTTP(reqJSON string) (string, error) {
	if h.panicHTTP {
		panic("swift side blew up")
	}
	if h.slowHTTP != nil {
		<-h.slowHTTP
	}
	var r wireRequest
	if err := json.Unmarshal([]byte(reqJSON), &r); err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for prefix, body := range h.routes {
		if strings.HasPrefix(r.URL, prefix) {
			out, _ := json.Marshal(wireResponse{Status: 200, Headers: http.Header{"Content-Type": {"text/html"}},
				BodyBase64: base64.StdEncoding.EncodeToString([]byte(body)), FinalURL: r.URL})
			return string(out), nil
		}
	}
	if strings.HasPrefix(r.URL, "https://cf.test/") {
		if h.verified > 0 && r.Headers.Get("User-Agent") == "WKWebView/1" {
			out, _ := json.Marshal(wireResponse{Status: 200, BodyBase64: base64.StdEncoding.EncodeToString([]byte("ok"))})
			return string(out), nil
		}
		out, _ := json.Marshal(wireResponse{Status: 503, Headers: http.Header{"Server": {"cloudflare"}},
			BodyBase64: base64.StdEncoding.EncodeToString([]byte("<script>window._cf_chl_opt={}</script>"))})
		return string(out), nil
	}
	return "", errors.New("offline: " + r.URL)
}

func (h *fakeHost) Verify(string) (string, error) {
	h.mu.Lock()
	h.verified++
	h.mu.Unlock()
	return `{"userAgent":"WKWebView/1"}`, nil
}

func (h *fakeHost) LoadData(k, d string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.data[k+"/"+d], nil
}
func (h *fakeHost) SaveData(k, d, v string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.data[k+"/"+d] = v
	return nil
}
func (h *fakeHost) DeleteData(k, d string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.data, k+"/"+d)
	return nil
}
func (h *fakeHost) LoadSetting(string, string) (string, error) { return "", nil }
func (h *fakeHost) IsLogged(string) bool                       { return false }
func (h *fakeHost) CancelHTTP(id int64) {
	h.mu.Lock()
	h.cancelled = append(h.cancelled, id)
	h.mu.Unlock()
}

func (h *fakeHost) Log(level, title, content string) {
	h.mu.Lock()
	h.logs = append(h.logs, level+" "+title+" "+content)
	h.mu.Unlock()
}

// Cookies: one jar per partition, keyed by name; url is recorded but not
// matched, which is enough for these tests.
func (h *fakeHost) LoadCookies(partition, url string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.event("loadCookies " + partition)
	list := []wireCookie{}
	for _, c := range h.jars[partition] {
		list = append(list, c)
	}
	out, _ := json.Marshal(list)
	return string(out), nil
}
func (h *fakeHost) SaveCookies(partition, url, cookiesJSON string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var list []wireCookie
	if err := json.Unmarshal([]byte(cookiesJSON), &list); err != nil {
		return err
	}
	if h.jars == nil {
		h.jars = map[string]map[string]wireCookie{}
	}
	if h.jars[partition] == nil {
		h.jars[partition] = map[string]wireCookie{}
	}
	for _, c := range list {
		h.jars[partition][c.Name] = c
	}
	h.event("saveCookies " + partition + " " + url)
	return nil
}
func (h *fakeHost) DeleteCookies(partition, url string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.jars, partition)
	h.event("deleteCookies " + partition)
	return nil
}

// UI answers come from h.answers in order; each call is recorded.
func (h *fakeHost) answer(ev string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.event(ev)
	if len(h.answers) == 0 {
		return ""
	}
	a := h.answers[0]
	h.answers = h.answers[1:]
	return a
}
func (h *fakeHost) ShowMessage(m string) { h.answer("message " + m) }
func (h *fakeHost) ShowDialog(title, content, actionsJSON string) (string, error) {
	return h.answer("dialog " + title + " " + actionsJSON), nil
}
func (h *fakeHost) ShowInputDialog(title, image, errorMessage string) (string, error) {
	return h.answer("input " + title + " [" + errorMessage + "]"), nil
}
func (h *fakeHost) ShowSelectDialog(title, optionsJSON string, initial int64) (string, error) {
	return h.answer(fmt.Sprintf("select %s %s %d", title, optionsJSON, initial)), nil
}
func (h *fakeHost) LaunchURL(u string)     { h.answer("launch " + u) }
func (h *fakeHost) ShowLoading(id int64)   { h.answer(fmt.Sprintf("loading %d", id)) }
func (h *fakeHost) CancelLoading(id int64) { h.answer(fmt.Sprintf("cancelLoading %d", id)) }
func (h *fakeHost) WriteClipboard(t string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clipboard = t
	return nil
}
func (h *fakeHost) ReadClipboard() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clipboard, nil
}

// event records a host call; the caller holds h.mu.
func (h *fakeHost) event(s string) { h.events = append(h.events, s) }

func (h *fakeHost) eventList() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.events...)
}

func newCore(t *testing.T, h *fakeHost) *Core {
	t.Helper()
	c, err := New(`{"appVersion":"1.6.0","locale":"zh_CN","platform":"ios","defaultUserAgent":"VC/1"}`, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

const comic = `
class Mini extends ComicSource {
    name = "Mini"; key = "mini"; version = "1.0.0"
    search = {
        load: async (keyword, options, page) => {
            const res = await Network.get("https://mini.test/s?q=" + keyword, {})
            const doc = new HtmlDocument(res.body)
            const comics = doc.querySelectorAll("a").map(a => new Comic({id: a.attributes.href, title: a.text}))
            doc.dispose()
            this.saveData("last", {q: keyword, big: 12345678901234567890n})
            return {comics, maxPage: 1}
        }
    }
    comic = { loadInfo: async (id) => { const r = await Network.get("https://cf.test/" + id, {}); return {title: r.body} } }
}
`

func TestEndToEndComic(t *testing.T) {
	h := &fakeHost{routes: map[string]string{"https://mini.test/s": `<a href="c1">One</a><a href="c2">Two</a>`}, data: map[string]string{}}
	c := newCore(t, h)
	if _, err := c.Install(0, "comic", comic, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	out, err := c.CallComic(c.NewCall(), "mini", "search.load", `["kw", [], 1]`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"title":"Two"`) {
		t.Fatalf("search = %s", out)
	}
	if got := h.data["mini/last"]; got != `{"q":"kw","big":12345678901234567890}` {
		t.Fatalf("stored = %s", got)
	}
	// Cloudflare: Core asks the host to verify, then retries with its UA.
	out, err = c.CallComic(0, "mini", "comic.loadInfo", `["x"]`)
	if err != nil || out != `{"title":"ok"}` || h.verified != 1 {
		t.Fatalf("cf = %s, %v, verified %d", out, err, h.verified)
	}
	if !c.HasComic("mini", "search.load") || c.HasComic("mini", "nope") || c.HasComic("missing", "x") || c.HasComic("mini", "a b") {
		t.Fatal("HasComic")
	}
	list, _ := c.Plugins()
	if strings.Contains(list, "Network.get") {
		t.Fatal("Plugins() leaks script bodies")
	}
	recs, _ := c.Records()
	if !strings.Contains(recs, "Network.get") {
		t.Fatal("Records() must include bodies for persistence")
	}
	// Restore into a fresh Core.
	c2 := newCore(t, h)
	failed, err := c2.Restore(recs)
	if err != nil || failed != "{}" {
		t.Fatalf("restore = %s, %v", failed, err)
	}
	if _, err := c2.CallComic(0, "mini", "search.load", `["kw", [], 1]`); err != nil {
		t.Fatal(err)
	}
}

func TestEndToEndVideo(t *testing.T) {
	h := &fakeHost{routes: map[string]string{}, data: map[string]string{}}
	c := newCore(t, h)
	rule, err := os.ReadFile(filepath.Join("..", "video", "kazumi", "testdata", "rules", "AGE.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Install(0, "kazumi-rule", string(rule), "", "", ""); err != nil {
		t.Fatal(err)
	}
	play, err := c.VideoPlay(0, "kz_AGE", "x", "road0", "https://www.age.test/play/1-1.html")
	if err != nil || !strings.Contains(play, `"sniff":{"page":"https://www.age.test/play/1-1.html"`) {
		t.Fatalf("play = %s, %v", play, err)
	}
	if _, err := c.EnableBuiltin("builtin_hongguo"); err != nil {
		t.Fatal(err)
	}
	cats, _ := c.VideoCategories("builtin_hongguo")
	if !strings.Contains(cats, "real-drama") {
		t.Fatalf("categories = %s", cats)
	}
	if _, err := c.VideoSearch(0, "missing", "x", ""); err == nil {
		t.Fatal("missing plugin")
	}
}

func TestSearchOptionsJSON(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}})
	src := `class F extends ComicSource { name='F'; key='f'; version='1'
	  search = { optionList: [{label:'排序', options:['any-任意','hot-热门'], default:'hot'}],
	    load: async () => ({comics: []}) } }`
	if _, err := c.Install(0, "comic", src, "", "", ""); err != nil {
		t.Fatal(err)
	}
	out, err := c.SearchOptions("f")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"default":"hot"`) || !strings.Contains(out, `"labels":["任意","热门"]`) {
		t.Fatalf("options = %s", out)
	}
	// A source without optionList answers "[]", so clients can always parse.
	if _, err := c.Install(0, "comic", comic, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if out, err := c.SearchOptions("mini"); err != nil || out != "[]" {
		t.Fatalf("no options = %q, %v", out, err)
	}
	if _, err := c.SearchOptions("missing"); err == nil {
		t.Fatal("missing source: want an error")
	}
}

func TestCompareComicIndex(t *testing.T) {
	h := &fakeHost{data: map[string]string{}}
	c := newCore(t, h)
	if _, err := c.Install(0, "comic", comic, "https://h.test/mini.js", "https://h.test/index.json", ""); err != nil {
		t.Fatal(err)
	}
	idx := `[{"name":"Mini","key":"mini","version":"1.0.1","fileName":"mini.js"},
		{"name":"New","key":"fresh","version":"1","fileName":"fresh.js"},
		{"name":"Future","key":"future","version":"1","fileName":"f.js","minAppVersion":"9.0.0"},
		{"name":"Broken"}]`
	out, err := c.CompareComicIndex(idx, "https://h.test/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Rows []struct {
			Key, Status, InstalledVersion string
		}
		Problems []json.RawMessage
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"mini": "update", "fresh": "new", "future": "too-new"}
	if len(got.Rows) != 3 || len(got.Problems) != 1 {
		t.Fatalf("rows/problems = %s", out)
	}
	for _, r := range got.Rows {
		if want[r.Key] != r.Status {
			t.Errorf("%s: status %s, want %s", r.Key, r.Status, want[r.Key])
		}
	}
	if got.Rows[0].InstalledVersion != "1.0.0" {
		t.Errorf("installed version = %q", got.Rows[0].InstalledVersion)
	}
	// From another index the entry is "other", never an update.
	out, _ = c.CompareComicIndex(idx, "https://elsewhere.test/index.json")
	if !strings.Contains(out, `"key":"mini"`) || !strings.Contains(out, `"status":"other"`) {
		t.Errorf("other index = %s", out)
	}
}

func TestIndexesAndBackup(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}})
	out, err := c.ParseComicIndex(`[{"name":"A","key":"a","version":"1","fileName":"a.js"}]`, "https://h.test/index.json")
	if err != nil || !strings.Contains(out, `"url":"https://h.test/a.js"`) {
		t.Fatalf("comic index = %s, %v", out, err)
	}
	out, err = c.ParseVideoIndex(`{"$schema":"vcvideo/1","name":"n","plugins":[]}`, "https://h.test/vcvideo.json")
	if err != nil || !strings.Contains(out, `"name":"n"`) {
		t.Fatalf("video index = %s, %v", out, err)
	}
	// A minimal backup: appdata only.
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, _ := w.Create("appdata.json")
	fw.Write([]byte(`{"settings":{"theme":"dark","proxy":"x"},"searchHistory":["q"]}`))
	w.Close()
	dir := t.TempDir()
	p := filepath.Join(dir, "data.venera")
	os.WriteFile(p, buf.Bytes(), 0o600)
	out, err = c.ReadBackup(p, dir, `[]`)
	if err != nil || !strings.Contains(out, `"settingKeys":["theme"]`) {
		t.Fatalf("backup = %s, %v", out, err)
	}
	plan, err := c.PlanBackup(p, dir, `[]`, `{"settings":true,"searchHistory":true}`)
	if err != nil || !strings.Contains(plan, `"theme":"dark"`) || strings.Contains(plan, "proxy") {
		t.Fatalf("plan = %s, %v", plan, err)
	}
}

func TestHostPanicAndCancellation(t *testing.T) {
	h := &fakeHost{data: map[string]string{}, panicHTTP: true}
	c := newCore(t, h)
	src := `class P extends ComicSource { name = "P"; key = "p"; version = "1"
	  async go() { try { await Network.get("https://x.test/", {}); return "no" } catch (e) { return "caught" } } }`
	if _, err := c.Install(0, "comic", src, "", "", ""); err != nil {
		t.Fatal(err)
	}
	// A panic in the host's HTTP is reported to the script, not fatal.
	if out, err := c.CallComic(0, "p", "go", "[]"); err != nil || out != `"caught"` {
		t.Fatalf("out = %s, %v", out, err)
	}
	// Cancelling a call tells the host to abort its request.
	h.panicHTTP = false
	h.slowHTTP = make(chan struct{})
	defer close(h.slowHTTP)
	id := c.NewCall()
	done := make(chan error, 1)
	go func() {
		_, err := c.CallComic(id, "p", "go", "[]")
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	c.Cancel(id)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled call did not return")
	}
}

func TestBadInputDoesNotPanic(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}})
	calls := []func() error{
		func() error { _, err := c.CallComic(0, "", "", "not json"); return err },
		func() error { _, err := c.Restore("{"); return err },
		func() error { _, err := c.PlanBackup("/nonexistent", "", "x", "{"); return err },
		func() error { _, err := c.ReadBackup("", "", ""); return err },
		func() error { _, err := c.VideoPlay(0, "", "", "", ""); return err },
		func() error { _, err := c.Install(0, "nope", "", "", "", ""); return err },
		func() error { _, err := c.ParseComicIndex("", ""); return err },
	}
	for i, f := range calls {
		if err := f(); err == nil {
			t.Errorf("call %d accepted bad input", i)
		}
	}
	if _, err := New("{bad", &fakeHost{}); err == nil {
		t.Error("bad config accepted")
	}
	if _, err := New("{}", nil); err == nil {
		t.Error("nil host accepted")
	}
}

func TestCancelReachesHost(t *testing.T) {
	h := &fakeHost{data: map[string]string{}, slowHTTP: make(chan struct{})}
	defer close(h.slowHTTP)
	c := newCore(t, h)
	src := "class P extends ComicSource { name = 'P'; key = 'p'; version = '1'\n async go() { await Network.get('https://x.test/', {}); return 1 } }"
	if _, err := c.Install(0, "comic", src, "", "", ""); err != nil {
		t.Fatal(err)
	}
	id := c.NewCall()
	go c.CallComic(id, "p", "go", "[]")
	time.Sleep(100 * time.Millisecond)
	c.Cancel(id)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		n := len(h.cancelled)
		h.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cancelling the call did not reach Host.CancelHTTP")
}

func TestPluginsJSONCarriesAttribution(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}})
	src := "class A extends ComicSource { name = 'A'; key = 'a1'; version = '1'; author = 'Alice'; homepage = 'https://a.test' }"
	if _, err := c.Install(0, "comic", src, "", "", ""); err != nil {
		t.Fatal(err)
	}
	out, err := c.Plugins()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"attribution":{"authors":[{"name":"Alice"}],"homepage":"https://a.test"}`) {
		t.Fatalf("plugins JSON = %s", out)
	}
}
