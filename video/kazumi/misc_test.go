package kazumi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/laincat/veneracat-core/challenge"
	"github.com/laincat/veneracat-core/host"
)

func antiCrawlerRule(t *testing.T, ac string) *Rule {
	return mustRule(t, `{"name": "AC", "baseURL": "https://www.example.com/", "searchURL": "https://www.example.com/s?q=@keyword",
		"searchList": "//li", "searchName": "//a", "searchResult": "//a", "chapterRoads": "//ul", "chapterResult": "//a",
		"antiCrawlerConfig": `+ac+`}`)
}

func TestAntiCrawlerDetection(t *testing.T) {
	const okPage = `<ul><li><a href="/d/1">ok</a></li></ul>`
	const verifyPage = `<html><body><h1>请完成安全验证 Verify</h1><img id="cap" src="/c.png"><button id="go">go</button></body></html>`
	cases := []struct {
		name, ac, body string
		captcha        bool
	}{
		{"text hit", `{"enabled":true,"captchaDetectType":2,"captchaDetectValue":"安全验证"}`, verifyPage, true},
		{"text miss", `{"enabled":true,"captchaDetectType":2,"captchaDetectValue":"安全验证"}`, okPage, false},
		{"regex hit, case-insensitive", `{"enabled":true,"captchaDetectType":3,"captchaDetectValue":"VERIFY\\s*</h1>"}`, verifyPage, true},
		{"regex dot matches newline", `{"enabled":true,"captchaDetectType":3,"captchaDetectValue":"安全.*go"}`, verifyPage, true},
		{"regex miss", `{"enabled":true,"captchaDetectType":3,"captchaDetectValue":"verify\\d+"}`, okPage, false},
		{"invalid regex never matches", `{"enabled":true,"captchaDetectType":3,"captchaDetectValue":"("}`, okPage, false},
		{"xpath hit", `{"enabled":true,"captchaDetectType":1,"captchaDetectValue":"//img[@id='cap']"}`, verifyPage, true},
		{"xpath miss", `{"enabled":true,"captchaDetectType":1,"captchaDetectValue":"//img[@id='cap']"}`, okPage, false},
		{"xpath is the default type", `{"enabled":true,"captchaDetectValue":"//button[@id='go']"}`, verifyPage, true},
		{"fallback to image selector", `{"enabled":true,"captchaImage":"//img[@id='cap']"}`, verifyPage, true},
		{"fallback to button selector", `{"enabled":true,"captchaButton":"//button[@id='go']"}`, verifyPage, true},
		{"fallback miss", `{"enabled":true,"captchaImage":"//img[@id='cap']","captchaButton":"//button[@id='go']"}`, okPage, false},
		{"disabled", `{"enabled":false,"captchaDetectType":2,"captchaDetectValue":"安全验证"}`, verifyPage, false},
		{"nothing configured", `{"enabled":true}`, verifyPage, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := New(antiCrawlerRule(t, c.ac), respondWith(200, c.body))
			_, err := p.Search(context.Background(), "k", "")
			if c.captcha {
				var cap *ErrCaptchaRequired
				if !errors.As(err, &cap) || !IsCaptcha(err) || cap.Rule != "AC" {
					t.Fatalf("err = %v, want ErrCaptchaRequired", err)
				}
				return
			}
			if IsCaptcha(err) {
				t.Fatalf("unexpected captcha error: %v", err)
			}
		})
	}
}

// Sites often serve their verification page with a 403/503 status; Kazumi
// still hands that body to the captcha flow.
func TestAntiCrawlerOnErrorStatus(t *testing.T) {
	p := New(antiCrawlerRule(t, `{"enabled":true,"captchaDetectType":2,"captchaDetectValue":"verify-me"}`), respondWith(403, "<p>verify-me</p>"))
	if _, err := p.Search(context.Background(), "k", ""); !IsCaptcha(err) {
		t.Fatalf("err = %v, want captcha", err)
	}
}

func TestCloudflareChallenge(t *testing.T) {
	const page = `<html><head><title>Just a moment...</title></head><body><script>window._cf_chl_opt={cvId:'3'};</script></body></html>`
	r := antiCrawlerRule(t, `{"enabled":false}`)
	tr := respondWith(503, page, "Server", "cloudflare", "Content-Type", "text/html")
	_, err := New(r, tr).Search(context.Background(), "k", "")
	var ce *challenge.ErrChallengeRequired
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *challenge.ErrChallengeRequired", err)
	}
	if ce.Kind != host.ChallengeCloudflare || ce.URL != "https://www.example.com/s?q=k" {
		t.Errorf("challenge = %+v", ce)
	}
	if IsCaptcha(err) {
		t.Error("a Cloudflare challenge is not a rule captcha")
	}
	// The same body from a non-Cloudflare server is just an HTTP 503.
	_, err = New(r, respondWith(503, page)).Search(context.Background(), "k", "")
	if err == nil || errors.As(err, &ce) {
		t.Errorf("err = %v, want a plain HTTP error", err)
	}
	// Details goes through the same check.
	_, err = New(r, tr).Details(context.Background(), "/d/1")
	if !errors.As(err, &ce) {
		t.Errorf("Details err = %v", err)
	}
}

func TestNormalizeEpisodeURL(t *testing.T) {
	const base = "https://www.example.com"
	cases := []struct{ name, base, raw, want string }{
		{"relative absolute path", base, "/play/123.html", "https://www.example.com/play/123.html"},
		{"relative no slash, base slash", "https://www.example.com/", "play/123.html", "https://www.example.com/play/123.html"},
		{"relative against base path", "https://www.example.com/a/b/", "c.html", "https://www.example.com/a/b/c.html"},
		{"absolute unchanged", base, "https://www.example.com/play/123.html", "https://www.example.com/play/123.html"},
		{"same host http aligned to https", base, "http://www.example.com/play/123.html", "https://www.example.com/play/123.html"},
		{"same host https aligned to http", "http://www.example.com", "https://www.example.com/play/123.html", "http://www.example.com/play/123.html"},
		{"relative under http base stays http", "http://www.example.com", "/play/1", "http://www.example.com/play/1"},
		{"other host keeps scheme", base, "http://cdn.other.com/play/123", "http://cdn.other.com/play/123"},
		{"other port is not rewritten", base, "http://www.example.com:8080/play/123", "http://www.example.com:8080/play/123"},
		{"explicit port kept", base, "https://www.example.com:8080/play/123", "https://www.example.com:8080/play/123"},
		{"protocol-relative", base, "//cdn.example.com/play/123", "https://cdn.example.com/play/123"},
		{"protocol-relative same host", "http://www.example.com", "//www.example.com/x", "http://www.example.com/x"},
		{"trailing slash", base, "/play/123/", "https://www.example.com/play/123"},
		{"many trailing slashes", base, "https://www.example.com/play/123///", "https://www.example.com/play/123"},
		{"root slash kept", base, "https://www.example.com/", "https://www.example.com/"},
		{"query kept, path slash dropped", base, "/play/?id=1&ep=4", "https://www.example.com/play?id=1&ep=4"},
		{"whitespace trimmed", base, "  /play/123.html \n", "https://www.example.com/play/123.html"},
		{"empty query dropped", base, "/play/?", "https://www.example.com/play"},
		{"host lower-cased", base, "https://WWW.Example.COM/Play/", "https://www.example.com/Play"},
		{"fragment kept", base, "/play/1/#top", "https://www.example.com/play/1#top"},
		{"empty", base, "", ""},
		{"blank", base, "   ", ""},
		{"empty base, relative", "", "/play/1", "/play/1"},
		{"invalid base (no host)", "not a url", "/play/1", "/play/1"},
		{"invalid base (scheme only)", "https://", "/play/1", "/play/1"},
		{"invalid base, absolute raw", "::bad", "https://a.test/x/", "https://a.test/x"},
		{"percent-encoded path kept", base, "/p/%E7%AC%AC1%E9%9B%86/", "https://www.example.com/p/%E7%AC%AC1%E9%9B%86"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizeEpisodeURL(c.base, c.raw)
			if got != c.want {
				t.Fatalf("NormalizeEpisodeURL(%q, %q) = %q, want %q", c.base, c.raw, got, c.want)
			}
			if again := NormalizeEpisodeURL(c.base, got); again != got {
				t.Errorf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}

func TestPlay(t *testing.T) {
	const page = "https://www.example.com/play/1-1"
	cases := []struct {
		name, json string
		followIfr  bool
		filterAds  bool
		referer    string
	}{
		{"defaults", `{"referer":"https://ref.test/"}`, false, false, "https://ref.test/"},
		{"legacy parser + adblock", `{"useLegacyParser":true,"adBlocker":true,"referer":"https://ref.test/"}`, true, true, "https://ref.test/"},
		{"referer falls back to baseURL", `{"baseURL":"https://www.example.com"}`, false, false, "https://www.example.com/"},
		{"no referer at all", `{"baseURL":""}`, false, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rule := mustRule(t, `{"name":"P","searchURL":"https://www.example.com/s?q=@keyword","searchList":"//li","searchName":"//a",
				"searchResult":"//a","chapterRoads":"//ul","chapterResult":"//a",`+c.json[1:])
			tr := respondWith(200, "")
			p, err := New(rule, tr).Play(context.Background(), "item", "road0", page)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Validate(); err != nil {
				t.Fatal(err)
			}
			if p.URL != "" || p.Sniff == nil || p.Sniff.Page != page || p.Sniff.FollowIframes != c.followIfr || p.Sniff.TimeoutMillis <= 0 {
				t.Errorf("sniff = %+v url=%q", p.Sniff, p.URL)
			}
			if got := p.Header.Get("Referer"); got != c.referer {
				t.Errorf("Referer = %q, want %q", got, c.referer)
			}
			if (p.HLS != nil && p.HLS.FilterAds) != c.filterAds {
				t.Errorf("HLS = %+v, want filterAds=%v", p.HLS, c.filterAds)
			}
			if len(tr.reqs) != 0 {
				t.Error("Play must not send requests")
			}
		})
	}
	rule := publishedRule(t, "AGE")
	for _, bad := range []string{"", "/relative/1", "javascript:alert(1)", "ftp://x/y"} {
		if _, err := New(rule, nil).Play(context.Background(), "", "", bad); err == nil {
			t.Errorf("Play(%q) succeeded", bad)
		}
	}
	// Referer sent by Play must equal what Info()/Key() identify.
	info := New(rule, nil).Info()
	if info.Key != "kz_AGE" || info.Kind != "kazumi-rule" {
		t.Errorf("info = %+v", info)
	}
	_ = http.Header{}
}

func TestJSONPathRejectsUnsupported(t *testing.T) {
	for _, expr := range []string{
		"", "data", "$..x", "$..", "$[?(@.a)]", "$[?(@.a>1)]", "$[0:2]", "$[1:]", "$[-1]", "$[0,1]", "$.a[", "$.", "$..[0]",
		"$.a b", "$['a'", "$[a]", "$.a.", "$ .a", "$.a()", "$.a[*]]", "$.*",
	} {
		if _, err := compilePath(expr); err == nil {
			t.Errorf("compilePath(%q) accepted", expr)
		}
	}
}

func TestJSONPathSupported(t *testing.T) {
	doc, err := decodeJSON([]byte(`{
		"a": {"b": [{"c": 1}, {"c": 2}, {"c": 12345678901234567890}]},
		"quoted key": {"x.y": "dot", "q'uote": "single", "d\"q": "double"},
		"data": [[10, 20], [30]],
		"-dash$": "ok",
		"obj": {"z": 3, "a": 1, "m": 2, "b": 4},
		"nil": null
	}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		expr string
		want []any
	}{
		{"$", []any{doc}},
		{"$.a.b[0].c", []any{json.Number("1")}},
		{"$.a.b[*].c", []any{json.Number("1"), json.Number("2"), json.Number("12345678901234567890")}},
		{"$.a.b[2].c", []any{json.Number("12345678901234567890")}},
		{"$['a']['b'][1]['c']", []any{json.Number("2")}},
		{`$["a"]["b"][ 1 ]["c"]`, []any{json.Number("2")}},
		{"$['quoted key']['x.y']", []any{"dot"}},
		{`$['quoted key']['q\'uote']`, []any{"single"}},
		{`$["quoted key"]["d\"q"]`, []any{"double"}},
		{"$.data[*][0]", []any{json.Number("10"), json.Number("30")}},
		{"$.data[*][*]", []any{json.Number("10"), json.Number("20"), json.Number("30")}},
		{"$.data[5]", nil},
		{"$.a.b[0].missing", nil},
		{"$.a.b.c", nil},
		{"$.a[0]", nil},
		{"$.-dash$", []any{"ok"}},
		{"$.nil", []any{nil}},
		{"$.obj[*]", []any{json.Number("1"), json.Number("4"), json.Number("2"), json.Number("3")}}, // sorted by key: a b m z
	}
	for _, c := range cases {
		got, err := readAll(doc, c.expr)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
		}
	}
	if v, _ := readFirst(doc, "$.a.b[2].c"); stringValue(v) != "12345678901234567890" {
		t.Errorf("large id = %v", v)
	}
	if v, err := readFirst(doc, "$.nothing"); v != nil || err != nil {
		t.Errorf("readFirst missing = %v, %v", v, err)
	}
}

func TestJSONPathObjectWildcardIsDeterministic(t *testing.T) {
	doc, err := decodeJSON([]byte(`{"k9":9,"k1":1,"k5":5,"k3":3,"k7":7,"k2":2,"k8":8,"k4":4,"k6":6,"k0":0}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []any{}
	for i := 0; i < 10; i++ {
		want = append(want, json.Number(string(rune('0'+i))))
	}
	for i := 0; i < 50; i++ {
		got, _ := readAll(doc, "$[*]")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: %v, want %v", i, got, want)
		}
	}
}

func TestStringValue(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""}, {"  x \n", "x"}, {json.Number("12345678901234567890"), "12345678901234567890"},
		{true, "true"}, {false, "false"}, {[]any{"a", json.Number("1")}, `["a",1]`}, {map[string]any{"k": "v"}, `{"k":"v"}`},
	}
	for _, c := range cases {
		if got := stringValue(c.in); got != c.want {
			t.Errorf("stringValue(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRuleKeyAndDefaults(t *testing.T) {
	r := mustRule(t, `{"name":"  Ani-Mate (v2) ","searchURL":"https://a.test/?q=@keyword","searchList":"//li","searchName":"//a","searchResult":"//a","chapterRoads":"//ul","chapterResult":"//a"}`)
	if r.Key() != "kz_Ani_Mate_v2" {
		t.Errorf("Key = %q", r.Key())
	}
	if !r.MultiSource || !r.UseWebview || !r.UseNativePlayer || r.API != "1" || r.Type != "anime" || r.SearchMode != ModeXPath || r.ChapterMode != ModeXPath {
		t.Errorf("defaults = %+v", r)
	}
	r = mustRule(t, `{"name":"x","muliSources":false,"useWebview":false,"useNativePlayer":false,"searchURL":"https://a.test/?q=@keyword","searchList":"//li","searchName":"//a","searchResult":"//a","chapterRoads":"//ul","chapterResult":"//a"}`)
	if r.MultiSource || r.UseWebview || r.UseNativePlayer {
		t.Errorf("explicit false lost: %+v", r)
	}
	for _, bad := range []string{`{`, `{"name":"x"}}`, `{"name":"x"} {}`, `{"name":""}`, `{"name":"x"}`, `{"name":"x","searchURL":"https://a.test","searchList":"//li[","searchName":"//a","searchResult":"//a"}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
}
