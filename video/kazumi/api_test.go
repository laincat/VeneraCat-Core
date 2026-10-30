package kazumi

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/video"
)

func TestAPISearchLMMShape(t *testing.T) {
	r := publishedRule(t, "LMM")
	tr := respondWith(200, `{"code":1,"list":[
		{"id":12345678901234567890,"name":" 葬送的芙莉莲 ","en":"Frieren"},
		{"id":42,"name":"Second"},
		{"id":null,"name":"no id"},
		{"id":7,"name":""}
	]}`)
	page, err := New(r, tr).Search(context.Background(), "芙莉莲 &1", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []video.Item{
		{ID: "https://www.lmm85.com/index.php/vod/detail/id/12345678901234567890.html", Title: "葬送的芙莉莲"},
		{ID: "https://www.lmm85.com/index.php/vod/detail/id/42.html", Title: "Second"},
	}
	if !reflect.DeepEqual(page.Items, want) {
		t.Fatalf("items = %+v\nwant   %+v", page.Items, want)
	}
	req := tr.last(t)
	u, err := url.Parse(req.URL)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" || u.Scheme+"://"+u.Host+u.Path != "https://www.lmm85.com/index.php/ajax/suggest" {
		t.Errorf("request = %s %s", req.Method, req.URL)
	}
	if q := u.Query(); q.Get("wd") != "芙莉莲 &1" || q.Get("mid") != "1" {
		t.Errorf("query = %v", q)
	}
	if req.URL != "https://www.lmm85.com/index.php/ajax/suggest?mid=1&wd=%E8%8A%99%E8%8E%89%E8%8E%B2+%261" {
		t.Errorf("URL = %s", req.URL)
	}
}

func TestAPISearchSourceEncodeComponent(t *testing.T) {
	r := mustRule(t, `{"name": "A", "searchMode": "api", "chapterMode": "xpath", "baseURL": "https://x.test",
		"searchApiConfig": {"request": {"url": "https://x.test/s"}, "listPath": "$.d[*]", "namePath": "$.n", "sourcePath": "$.s",
			"sourceTemplate": "https://x.test/GV@source/end"},
		"chapterRoads": "//ul", "chapterResult": "//a"}`)
	page, err := New(r, respondWith(200, `{"d":[{"n":"a","s":"a/b?c&d=e f+g"}]}`)).Search(context.Background(), "k", "")
	if err != nil {
		t.Fatal(err)
	}
	// Dart's Uri.encodeComponent: everything except A-Z a-z 0-9 - _ . ! ~ * ' ( )
	if got, want := page.Items[0].ID, "https://x.test/GVa%2Fb%3Fc%26d%3De%20f%2Bg/end"; got != want {
		t.Fatalf("ID = %q, want %q", got, want)
	}
}

func TestAPISearchWithoutTemplateKeepsSource(t *testing.T) {
	r := publishedRule(t, "sorani")
	page, err := New(r, respondWith(200, `{"data":{"records":[{"id":98765432109876543210,"title":"T"}]}}`)).Search(context.Background(), "k", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "98765432109876543210" {
		t.Fatalf("items = %+v", page.Items)
	}
}

func TestAPISearchQueryTypesAndPOSTBodies(t *testing.T) {
	r := publishedRule(t, "sorani")
	tr := respondWith(200, `{"data":{"records":[]}}`)
	if _, err := New(r, tr).Search(context.Background(), "a b", ""); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(tr.last(t).URL)
	want := url.Values{"page": {"1"}, "size": {"50"}, "keyword": {"a b"}, "enabled": {"true"}, "sortMode": {"latest"}, "sortDesc": {"false"}}
	if !reflect.DeepEqual(u.Query(), want) {
		t.Errorf("query = %v, want %v", u.Query(), want)
	}

	// milimili: POST with a JSON body, "@keyword" exact so the string stays a string.
	m := publishedRule(t, "milimili")
	tr = respondWith(200, `{"videos":[]}`)
	if _, err := New(m, tr).Search(context.Background(), "x\"y", ""); err != nil {
		t.Fatal(err)
	}
	req := tr.last(t)
	if req.Method != "POST" || req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request = %s %v", req.Method, req.Header)
	}
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["query"] != "x\"y" || body["limit"] != float64(50) || body["sectionSlug"] != "anime" {
		t.Errorf("body = %v", body)
	}
}

func TestAPISearchNotJSON(t *testing.T) {
	r := publishedRule(t, "LMM")
	if _, err := New(r, respondWith(200, `<html>`)).Search(context.Background(), "k", ""); err == nil {
		t.Fatal("want error for a non-JSON body")
	}
}

func TestAPIChaptersNested(t *testing.T) {
	r := mustRule(t, `{"name": "N", "searchMode": "api", "chapterMode": "api", "baseURL": "https://x.test/",
		"searchApiConfig": {"request": {"url": "https://x.test/s"}},
		"chapterApiConfig": {
			"request": {"method": "GET", "url": "https://x.test/api/@source/detail", "query": {"id": "@source"}},
			"format": "nested",
			"roadsPath": "$.data.roads[*]", "roadNamePath": "$.name",
			"episodesPath": "$.eps[*]", "episodeNamePath": "$.title", "episodeUrlPath": "$.link"}}`)
	tr := respondWith(200, `{"data":{"roads":[
		{"name":"Main","eps":[{"title":"EP1","link":"/watch/1/"},{"title":"","link":"/watch/2"},{"title":"skip","link":""},{"title":"x"}]},
		{"name":"","eps":[]},
		{"name":"","eps":[{"title":"A","link":"https://x.test/watch/9"}]}
	]}}`)
	d, err := New(r, tr).Details(context.Background(), "a/b c")
	if err != nil {
		t.Fatal(err)
	}
	want := []video.Line{
		{ID: "road0", Name: "Main", Episodes: []video.Episode{
			{ID: "https://x.test/watch/1", Title: "EP1"},
			{ID: "https://x.test/watch/2", Title: "第2集"},
		}},
		{ID: "road1", Name: "播放线路2", Episodes: []video.Episode{{ID: "https://x.test/watch/9", Title: "A"}}},
	}
	if !reflect.DeepEqual(d.Lines, want) {
		t.Fatalf("lines = %+v\nwant    %+v", d.Lines, want)
	}
	if got, want := tr.last(t).URL, "https://x.test/api/a%2Fb%20c/detail?id=a%2Fb+c"; got != want {
		t.Errorf("URL = %s, want %s", got, want)
	}
}

func TestAPIChaptersDelimited(t *testing.T) {
	r := publishedRule(t, "giriGiriLove")
	tr := respondWith(200, `{"info":{
		"vod_play_from":"线路A$$$$$$线路C",
		"vod_play_url":"第01集$https://cdn-a.test/1.m3u8#第02集$/2.m3u8#bad entry##$https://cdn-a.test/3.m3u8$$$ $$$单集$https://cdn-c.test/x.m3u8"}}`)
	d, err := New(r, tr).Details(context.Background(), "555")
	if err != nil {
		t.Fatal(err)
	}
	// The blank middle group is skipped; road names stay aligned with the
	// group index. An entry without a field separator is skipped, an empty
	// title becomes 第N集 with N the 1-based position among the raw entries.
	want := []video.Line{
		{ID: "road0", Name: "线路A", Episodes: []video.Episode{
			{ID: "https://cdn-a.test/1.m3u8", Title: "第01集"},
			{ID: "https://ani.girigirilove.com/2.m3u8", Title: "第02集"},
			{ID: "https://cdn-a.test/3.m3u8", Title: "第5集"},
		}},
		{ID: "road1", Name: "线路C", Episodes: []video.Episode{{ID: "https://cdn-c.test/x.m3u8", Title: "单集"}}},
	}
	if !reflect.DeepEqual(d.Lines, want) {
		t.Fatalf("lines = %+v\nwant    %+v", d.Lines, want)
	}
	if got := tr.last(t).URL; got != "https://m3u8.girigirilove.com/api.php/Vod/get_detail?vod_id=555" {
		t.Errorf("URL = %s", got)
	}
}

func TestAPIChaptersVariablesAndEpisodePage(t *testing.T) {
	r := mustRule(t, `{"name": "V", "searchMode": "api", "chapterMode": "api", "baseURL": "https://x.test",
		"searchApiConfig": {"request": {"url": "https://x.test/s"}},
		"chapterApiConfig": {
			"request": {"url": "https://x.test/api/@source"},
			"roadsPath": "$.sources[*]", "roadNamePath": "$.name",
			"episodesPath": "$.eps[*]", "episodeNamePath": "$.label", "episodeUrlPath": "$.id",
			"variables": {"slug": "$.slug", "big": "$.big", "title": "$.meta['the title']"},
			"episodePage": {
				"url": "/watch/@slug/@source/r/@roadNumber-@roadIndex/e/@episodeNumber-@episodeIndex/@episodeUrl",
				"query": {"n": "@episodeNumber", "t": "@title", "big": "@big", "fixed": "a@b"}}}}`)
	tr := respondWith(200, `{"slug":"my slug","big":12345678901234567890,"meta":{"the title":"T&C"},
		"sources":[{"name":"S1","eps":[{"label":"one","id":"x/y"},{"label":"two","id":"z"}]},{"name":"S2","eps":[{"label":"three","id":"q"}]}]}`)
	d, err := New(r, tr).Details(context.Background(), "k/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Lines) != 2 || len(d.Lines[0].Episodes) != 2 {
		t.Fatalf("lines = %+v", d.Lines)
	}
	cases := []struct {
		got, path string
		query     url.Values
	}{
		{d.Lines[0].Episodes[0].ID, "/watch/my%20slug/k%2F1/r/1-0/e/1-0/x%2Fy", url.Values{"n": {"1"}, "t": {"T&C"}, "big": {"12345678901234567890"}, "fixed": {"a@b"}}},
		{d.Lines[0].Episodes[1].ID, "/watch/my%20slug/k%2F1/r/1-0/e/2-1/z", url.Values{"n": {"2"}, "t": {"T&C"}, "big": {"12345678901234567890"}, "fixed": {"a@b"}}},
		{d.Lines[1].Episodes[0].ID, "/watch/my%20slug/k%2F1/r/2-1/e/1-0/q", url.Values{"n": {"1"}, "t": {"T&C"}, "big": {"12345678901234567890"}, "fixed": {"a@b"}}},
	}
	for _, c := range cases {
		u, err := url.Parse(c.got)
		if err != nil {
			t.Fatal(err)
		}
		if u.Host != "x.test" || u.Scheme != "https" || u.EscapedPath() != c.path {
			t.Errorf("episode %s: path %q, want %q", c.got, u.EscapedPath(), c.path)
		}
		if !reflect.DeepEqual(u.Query(), c.query) {
			t.Errorf("episode %s: query %v, want %v", c.got, u.Query(), c.query)
		}
	}
}

func TestAPIChaptersMissingVariable(t *testing.T) {
	r := mustRule(t, `{"name": "V", "searchMode": "api", "chapterMode": "api", "baseURL": "https://x.test",
		"searchApiConfig": {"request": {"url": "https://x.test/s"}},
		"chapterApiConfig": {"request": {"url": "https://x.test/api"}, "variables": {"slug": "$.slug"},
			"episodesPath": "$.eps[*]", "roadsPath": "",
			"episodePage": {"url": "/w/@slug"}}}`)
	if _, err := New(r, respondWith(200, `{"eps":[{"name":"a","url":"1"}]}`)).Details(context.Background(), "1"); err == nil {
		t.Fatal("want error when a chapter variable matches nothing")
	}
	r2 := mustRule(t, `{"name": "V", "searchMode": "api", "chapterMode": "api", "baseURL": "https://x.test",
		"searchApiConfig": {"request": {"url": "https://x.test/s"}},
		"chapterApiConfig": {"request": {"url": "https://x.test/api"}, "roadsPath": "",
			"episodesPath": "$.eps[*]", "episodePage": {"url": "/w/@nope"}}}`)
	if _, err := New(r2, respondWith(200, `{"eps":[{"name":"a","url":"1"}]}`)).Details(context.Background(), "1"); err == nil {
		t.Fatal("want error for an undefined template variable")
	}
}

func TestPublishedAPIChapterRules(t *testing.T) {
	// sorani: roadsPath "$.data" is an object, roadNamePath empty.
	tr := respondWith(200, `{"data":{"episodes":[{"episodeLabel":"第一话","episodeOrder":1},{"episodeLabel":"","episodeOrder":2}]}}`)
	d, err := New(publishedRule(t, "sorani"), tr).Details(context.Background(), "77")
	if err != nil {
		t.Fatal(err)
	}
	want := []video.Line{{ID: "road0", Name: "播放线路1", Episodes: []video.Episode{
		{ID: "https://www.sorani.net/anime/mal/77/episode/1", Title: "第一话"},
		{ID: "https://www.sorani.net/anime/mal/77/episode/2", Title: "第2集"},
	}}}
	if !reflect.DeepEqual(d.Lines, want) {
		t.Fatalf("lines = %+v", d.Lines)
	}
	if got := tr.last(t).URL; got != "https://api.sorani.cc/sorani-cms/api/video/77" {
		t.Errorf("URL = %s", got)
	}

	// milimili: episodePage with a literal URL and an @episodeUrl query.
	tr = respondWith(200, `{"video":{"title":"Show"},"episodes":[{"episodeLabel":"E1","ep":"abc"}]}`)
	d, err = New(publishedRule(t, "milimili"), tr).Details(context.Background(), "sid")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Lines) != 1 || d.Lines[0].Name != "Show" || d.Lines[0].Episodes[0].ID != "https://milimili.moe/watch?ep=abc" {
		t.Fatalf("lines = %+v", d.Lines)
	}
	if req := tr.last(t); req.Method != "POST" || string(req.Body) != `{"shortId":"sid"}` {
		t.Errorf("request = %s %s", req.Method, req.Body)
	}
}

func TestRender(t *testing.T) {
	vars := map[string]any{"a": "1", "b": "2", "name": "x y", "n": json.Number("12345678901234567890")}
	cases := []struct {
		tpl, want string
		encode    bool
	}{
		{"@a", "1", false},
		{"@a@b", "1@b", false}, // the second @ follows a name character: literal
		{"@a/@b", "1/2", false},
		{"@a@a@b", "1@a@b", false},
		{"@a @b", "1 2", false},
		{"@a,@b;@a", "1,2;1", false},
		{"/@a/@b/", "/1/2/", false},
		{"@a?x=@b", "1?x=2", false},
		{"@a-@b @a", "1-2 1", false},
		{"?q=@name&x=@a", "?q=x y&x=1", false},
		{"?q=@name&x=@a", "?q=x%20y&x=1", true},
		{"mail@b.com", "mail@b.com", false},
		{"x@a", "x@a", false},
		{"_@a 9@b", "_@a 9@b", false},
		{"@@a", "@1", false},
		{"(@a)[@b]", "(1)[2]", false},
		{"@n", "12345678901234567890", false},
		{"@", "@", false},
		{"@1", "@1", false},
		{"中文@a", "中文1", false},
		{"/GV@a", "/GV@a", false},
	}
	for _, c := range cases {
		got, err := render(c.tpl, vars, c.encode)
		if err != nil {
			t.Errorf("render(%q): %v", c.tpl, err)
			continue
		}
		if got != c.want {
			t.Errorf("render(%q, encode=%v) = %q, want %q", c.tpl, c.encode, got, c.want)
		}
	}
	for _, tpl := range []string{"@missing", "ok @a and @nope", "@a/@zzz", "/@zzz", "@a @zzz"} {
		if _, err := render(tpl, vars, false); err == nil {
			t.Errorf("render(%q) succeeded, want missing-variable error", tpl)
		}
	}
}

func TestRenderValueKeepsExactType(t *testing.T) {
	vars := map[string]any{"id": json.Number("12345678901234567890"), "ok": true, "s": "str", "obj": map[string]any{"k": "v"}}
	in := map[string]any{
		"exact":  "@id",
		"bool":   "@ok",
		"obj":    "@obj",
		"mixed":  "id=@id",
		"nested": []any{"@s", map[string]any{"x": "@id"}, float64(3), nil},
		"plain":  "no placeholder",
	}
	got, err := renderValue(in, vars)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"exact":  json.Number("12345678901234567890"),
		"bool":   true,
		"obj":    map[string]any{"k": "v"},
		"mixed":  "id=12345678901234567890",
		"nested": []any{"str", map[string]any{"x": json.Number("12345678901234567890")}, float64(3), nil},
		"plain":  "no placeholder",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	if _, err := renderValue("@gone", vars); err == nil {
		t.Error("exact missing variable must be an error")
	}
	if _, err := renderValue(map[string]any{"k": []any{"a @gone"}}, vars); err == nil {
		t.Error("missing variable inside a template must be an error")
	}
}

func TestAPIRequestBodyKeepsJSONTypes(t *testing.T) {
	r := mustRule(t, `{"name": "B", "searchMode": "api", "chapterMode": "xpath", "baseURL": "https://x.test",
		"searchApiConfig": {"request": {"method": "post", "url": "https://x.test/s", "bodyType": "json",
			"headers": {"x-token": "t-@keyword"},
			"body": {"id": 12345678901234567890, "kw": "@keyword", "tags": ["@keyword", 1], "n": null}}},
		"chapterRoads": "//ul", "chapterResult": "//a"}`)
	tr := respondWith(200, `{"data":[]}`)
	if _, err := New(r, tr).Search(context.Background(), "k", ""); err != nil {
		t.Fatal(err)
	}
	req := tr.last(t)
	if got, want := string(req.Body), `{"id":12345678901234567890,"kw":"k","n":null,"tags":["k",1]}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	if req.Header.Get("X-Token") != "t-k" {
		t.Errorf("headers = %v", req.Header)
	}
}

func TestAPIRequestFormBody(t *testing.T) {
	r := mustRule(t, `{"name": "F", "searchMode": "api", "chapterMode": "xpath", "baseURL": "https://x.test",
		"searchApiConfig": {"request": {"method": "POST", "url": "https://x.test/s", "bodyType": "form",
			"body": {"wd": "@keyword", "page": 1, "all": true}}},
		"chapterRoads": "//ul", "chapterResult": "//a"}`)
	tr := respondWith(200, `{"data":[]}`)
	if _, err := New(r, tr).Search(context.Background(), "a b", ""); err != nil {
		t.Fatal(err)
	}
	req := tr.last(t)
	vals, err := url.ParseQuery(string(req.Body))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(vals, url.Values{"wd": {"a b"}, "page": {"1"}, "all": {"true"}}) {
		t.Errorf("body = %s", req.Body)
	}
	if req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", req.Header.Get("Content-Type"))
	}
}

func TestAPIRequestGETIgnoresBody(t *testing.T) {
	r := mustRule(t, `{"name": "G", "searchMode": "api", "chapterMode": "xpath", "baseURL": "https://x.test",
		"searchApiConfig": {"request": {"method": "GET", "url": "https://x.test/s", "bodyType": "json", "body": {"a": "@keyword"}}},
		"chapterRoads": "//ul", "chapterResult": "//a"}`)
	tr := respondWith(200, `{"data":[]}`)
	if _, err := New(r, tr).Search(context.Background(), "k", ""); err != nil {
		t.Fatal(err)
	}
	if req := tr.last(t); len(req.Body) != 0 || req.Method != "GET" {
		t.Errorf("request = %s %q", req.Method, req.Body)
	}
}

func TestAPIRequestInvalid(t *testing.T) {
	_, err := Parse([]byte(`{"name": "M", "searchMode": "api", "chapterMode": "xpath",
		"searchApiConfig": {"request": {"method": "PUT", "url": "https://x.test/s"}}, "chapterRoads": "//ul", "chapterResult": "//a"}`))
	if err == nil {
		t.Error("PUT must be rejected")
	}
	_, err = Parse([]byte(`{"name": "M", "searchMode": "api", "chapterMode": "xpath",
		"searchApiConfig": {"request": {"url": "https://x.test/s"}, "sourceTemplate": "https://x.test/d"}, "chapterRoads": "//ul", "chapterResult": "//a"}`))
	if err == nil {
		t.Error("sourceTemplate without @source must be rejected")
	}
	r := mustRule(t, `{"name": "U", "searchMode": "api", "chapterMode": "xpath",
		"searchApiConfig": {"request": {"url": "https://x.test/@nope"}}, "chapterRoads": "//ul", "chapterResult": "//a"}`)
	tr := respondWith(200, "{}")
	if _, err := New(r, tr).Search(context.Background(), "k", ""); err == nil || len(tr.reqs) != 0 {
		t.Errorf("undefined URL variable: err=%v requests=%d", err, len(tr.reqs))
	}
}

func TestAPIHeaderOverridesDefault(t *testing.T) {
	r := mustRule(t, `{"name": "H", "searchMode": "api", "chapterMode": "xpath", "baseURL": "https://x.test", "userAgent": "RuleUA",
		"searchApiConfig": {"request": {"url": "https://x.test/s", "headers": {"referer": "https://other.test/", "user-agent": "Custom"}}},
		"chapterRoads": "//ul", "chapterResult": "//a"}`)
	tr := respondWith(200, `{"data":[]}`)
	if _, err := New(r, tr).Search(context.Background(), "k", ""); err != nil {
		t.Fatal(err)
	}
	req := tr.last(t)
	if got := req.Header.Values("Referer"); !reflect.DeepEqual(got, []string{"https://other.test/"}) {
		t.Errorf("Referer = %v", got)
	}
	if got := req.Header.Values("User-Agent"); !reflect.DeepEqual(got, []string{"Custom"}) {
		t.Errorf("User-Agent = %v", got)
	}
}

func TestFetchErrors(t *testing.T) {
	r := publishedRule(t, "LMM")
	p := New(r, respondWith(500, `{"error":1}`))
	if _, err := p.Search(context.Background(), "k", ""); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err = %v", err)
	}
	if _, err := New(r, nil).Search(context.Background(), "k", ""); err != host.ErrUnsupported {
		t.Errorf("nil transport: err = %v", err)
	}
	big := respondWith(200, strings.Repeat("x", MaxBody+1))
	if _, err := New(r, big).Search(context.Background(), "k", ""); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("oversize err = %v", err)
	}
}

func TestAPIQueryNumbersStayExact(t *testing.T) {
	r := mustRule(t, `{"name": "Q", "searchMode": "api", "chapterMode": "xpath", "baseURL": "https://x.test",
		"searchApiConfig": {"request": {"url": "https://x.test/s", "headers": {"X-Id": 12345678901234567890},
			"query": {"id": 12345678901234567890, "page": 1, "ratio": 1.5, "flag": false, "kw": "@keyword"}}},
		"chapterRoads": "//ul", "chapterResult": "//a"}`)
	tr := respondWith(200, `{"data":[]}`)
	if _, err := New(r, tr).Search(context.Background(), " a ", ""); err != nil {
		t.Fatal(err)
	}
	req := tr.last(t)
	u, _ := url.Parse(req.URL)
	want := url.Values{"id": {"12345678901234567890"}, "page": {"1"}, "ratio": {"1.5"}, "flag": {"false"}, "kw": {" a "}}
	if !reflect.DeepEqual(u.Query(), want) {
		t.Errorf("query = %v, want %v", u.Query(), want)
	}
	if got := req.Header.Get("X-Id"); got != "12345678901234567890" {
		t.Errorf("X-Id = %q", got)
	}
}

func TestEncodeComponent(t *testing.T) {
	cases := map[string]string{
		"abc-_.!~*'()XYZ019": "abc-_.!~*'()XYZ019",
		"a b":                "a%20b",
		"a/b?c&d=e+f#g%h":    "a%2Fb%3Fc%26d%3De%2Bf%23g%25h",
		"芙":                  "%E8%8A%99",
		"":                   "",
		"[x]{y}|<>@:;,":      "%5Bx%5D%7By%7D%7C%3C%3E%40%3A%3B%2C",
	}
	for in, want := range cases {
		if got := encodeComponent(in); got != want {
			t.Errorf("encodeComponent(%q) = %q, want %q", in, got, want)
		}
	}
}
