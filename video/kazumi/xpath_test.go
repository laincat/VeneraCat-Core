package kazumi

import (
	"context"
	"reflect"
	"testing"

	"github.com/laincat/veneracat-core/video"
)

const searchHTML = `<html><body>
<div class="result">
  <div class="item"><h3><a href="/detail/1/">  进击的巨人 </a></h3><span><a href="/other/x">noise</a></span></div>
  <div class="item"><h3><a href="http://www.example.com/detail/2">Name Two</a></h3></div>
  <div class="item"><h3><a href="/detail/3"></a></h3></div>
  <div class="item"><h3>no link here</h3></div>
</div>
<div class="sidebar"><h3><a href="/side/9">Sidebar</a></h3></div>
</body></html>`

func xpathSearchRule(t *testing.T, selector string) *Rule {
	return mustRule(t, `{
		"name": "Ex", "baseURL": "https://www.example.com/",
		"searchURL": "https://www.example.com/search?wd=@keyword",
		"searchList": "//div[@class='item']",
		"searchName": "`+selector+`", "searchResult": "`+selector+`",
		"chapterRoads": "//div", "chapterResult": "//a"}`)
}

func TestXPathSearchRelativeToEachItem(t *testing.T) {
	want := []video.Item{
		{ID: "https://www.example.com/detail/1", Title: "进击的巨人"},
		{ID: "https://www.example.com/detail/2", Title: "Name Two"},
	}
	// "//h3/a" is the published style: it must be read relative to the item.
	// ".//h3/a" and "h3/a" are explicit relative forms with the same result.
	for _, sel := range []string{"//h3/a", ".//h3/a", "h3/a"} {
		t.Run(sel, func(t *testing.T) {
			tr := respondWith(200, searchHTML)
			p := New(xpathSearchRule(t, sel), tr)
			page, err := p.Search(context.Background(), "进 x", "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(page.Items, want) {
				t.Fatalf("items = %+v, want %+v", page.Items, want)
			}
			req := tr.last(t)
			if req.Method != "GET" || req.URL != "https://www.example.com/search?wd=%E8%BF%9B+x" {
				t.Errorf("request = %s %s", req.Method, req.URL)
			}
			if !req.Cookies || req.Partition != "video:kz_Ex" {
				t.Errorf("cookies/partition = %v %q", req.Cookies, req.Partition)
			}
			if got := req.Header.Get("Referer"); got != "https://www.example.com/" {
				t.Errorf("Referer = %q", got)
			}
		})
	}
}

func TestXPathSearchTextNodeAndCursor(t *testing.T) {
	r := mustRule(t, `{"name": "T", "baseURL": "https://www.example.com",
		"searchURL": "https://www.example.com/s/@keyword",
		"searchList": "//div[@class='item']", "searchName": "//h3/a/text()", "searchResult": "//h3/a",
		"chapterRoads": "//div", "chapterResult": "//a"}`)
	page, err := New(r, respondWith(200, searchHTML)).Search(context.Background(), "k", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Title != "进击的巨人" {
		t.Fatalf("items = %+v", page.Items)
	}
	// Kazumi rules have no paging.
	tr := respondWith(200, searchHTML)
	page, err = New(r, tr).Search(context.Background(), "k", "2")
	if err != nil || len(page.Items) != 0 || len(tr.reqs) != 0 {
		t.Fatalf("cursor page = %+v, %v, %d requests", page, err, len(tr.reqs))
	}
}

func TestXPathSearchDedup(t *testing.T) {
	const page = `<ul><li><a href="/d/1">A</a></li><li><a href="http://www.example.com/d/1/">A again</a></li></ul>`
	r := mustRule(t, `{"name": "D", "baseURL": "https://www.example.com", "searchURL": "https://www.example.com/s?q=@keyword",
		"searchList": "//li", "searchName": "//a", "searchResult": "//a", "chapterRoads": "//ul", "chapterResult": "//a"}`)
	got, err := New(r, respondWith(200, page)).Search(context.Background(), "x", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "https://www.example.com/d/1" {
		t.Fatalf("items = %+v", got.Items)
	}
}

const chaptersHTML = "<html><body>" +
	`<div class="road"><a href="/play/1-1"> 第 1 集 </a><a href="/play/1-2">第` + "\n2\u3000" + `集</a><a>no href</a>` +
	`<a href="/play/1-4"> </a><a href="/play/1-5/">   </a><a href="  ">blank href</a></div>` +
	`<div class="road"><a>only without href</a></div>` +
	`<div class="road"><a href="http://www.example.com/play/2-1/">Ep 1</a></div>` +
	"</body></html>"

func TestXPathChapters(t *testing.T) {
	r := mustRule(t, `{"name": "Ch", "baseURL": "https://www.example.com/", "searchURL": "https://www.example.com/s?q=@keyword",
		"searchList": "//li", "searchName": "//a", "searchResult": "//a",
		"chapterRoads": "//div[@class='road']", "chapterResult": "//a"}`)
	tr := respondWith(200, chaptersHTML)
	d, err := New(r, tr).Details(context.Background(), "/detail/7/")
	if err != nil {
		t.Fatal(err)
	}
	// Titles lose all whitespace. An empty title becomes 第N集 with N the
	// 1-based position among the road's <a> nodes (the href-less third node
	// still counts, as in Kazumi).
	want := []video.Line{
		{ID: "road0", Name: "播放线路1", Episodes: []video.Episode{
			{ID: "https://www.example.com/play/1-1", Title: "第1集"},
			{ID: "https://www.example.com/play/1-2", Title: "第2集"},
			{ID: "https://www.example.com/play/1-4", Title: "第4集"},
			{ID: "https://www.example.com/play/1-5", Title: "第5集"},
		}},
		{ID: "road1", Name: "播放线路2", Episodes: []video.Episode{
			{ID: "https://www.example.com/play/2-1", Title: "Ep1"},
		}},
	}
	if !reflect.DeepEqual(d.Lines, want) {
		t.Fatalf("lines = %+v\nwant    %+v", d.Lines, want)
	}
	req := tr.last(t)
	if req.URL != "https://www.example.com/detail/7" || req.Method != "GET" {
		t.Errorf("request = %s %s", req.Method, req.URL)
	}
	if req.Cookies {
		t.Error("chapter request must not attach stored cookies")
	}
}

func TestXPathChaptersNoEpisodes(t *testing.T) {
	r := publishedRule(t, "AGE")
	tr := respondWith(200, "<html><body></body></html>")
	if _, err := New(r, tr).Details(context.Background(), "https://www.agedm.io/detail/1"); err == nil {
		t.Fatal("want error for a page without episodes")
	}
}

// The ezdmw rule joins four branches with "|". Each branch starts with "/"
// and must be read relative to the road node.
func TestXPathChapterResultUnionIsRelative(t *testing.T) {
	const page = `<section class="anthology">
<div class="line_button1">L1</div><a class="circuit_switch1" href="/p/1">e1</a><a class="circuit_switch1" href="/p/2">e2</a>
<div class="line_button2">L2</div><a class="circuit_switch2" href="/p/3">e3</a>
</section>`
	d, err := New(publishedRule(t, "ezdmw"), respondWith(200, page)).Details(context.Background(), "https://m.ezdmw.org/d/1")
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for _, l := range d.Lines {
		var ids []string
		for _, e := range l.Episodes {
			ids = append(ids, e.ID)
		}
		got = append(got, ids)
	}
	want := [][]string{{"https://m.ezdmw.org/p/1", "https://m.ezdmw.org/p/2"}, {"https://m.ezdmw.org/p/3"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("episodes per line = %v, want %v", got, want)
	}
}

func TestXPathUsePost(t *testing.T) {
	r := mustRule(t, `{"name": "Post", "api": "2", "usePost": true, "baseURL": "https://www.example.com/",
		"searchURL": "https://www.example.com/search.php?searchword=@keyword&order=asc&x=1",
		"searchList": "//li", "searchName": "//a", "searchResult": "//a", "chapterRoads": "//ul", "chapterResult": "//a"}`)
	tr := respondWith(200, `<li><a href="/d/1">A</a></li>`)
	page, err := New(r, tr).Search(context.Background(), "a b&c", "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("search = %+v, %v", page, err)
	}
	req := tr.last(t)
	if req.Method != "POST" || req.URL != "https://www.example.com/search.php" {
		t.Errorf("request = %s %s", req.Method, req.URL)
	}
	if got, want := string(req.Body), "searchword=a+b%26c&order=asc&x=1"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got := req.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", got)
	}
	if !req.Cookies {
		t.Error("search must attach cookies")
	}
}

func TestXPathSearchInvalidURL(t *testing.T) {
	r := mustRule(t, `{"name": "Bad", "baseURL": "https://www.example.com/", "searchURL": "/search?wd=@keyword",
		"searchList": "//li", "searchName": "//a", "searchResult": "//a", "chapterRoads": "//ul", "chapterResult": "//a"}`)
	tr := respondWith(200, "")
	if _, err := New(r, tr).Search(context.Background(), "x", ""); err == nil {
		t.Fatal("want error for a relative search URL")
	}
	if len(tr.reqs) != 0 {
		t.Fatal("no request may be sent")
	}
}

func TestXPathAttributeOperators(t *testing.T) {
	const page = `<ol class="a tvt-watch-episodes-x" id="x1" lang="en-US" title="x*=y"></ol><ol class="other" id="x2" lang="en"></ol>`
	doc, err := parseHTML([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		`//ol[@class*='tvt-watch-episodes']`: {"x1"},
		`//ol[@class^='a ']`:                 {"x1"},
		`//ol[@class$='-x']`:                 {"x1"},
		`//ol[@class~='a']`:                  {"x1"},
		`//ol[@class~="tvt-watch"]`:          nil,
		`//ol[@class~='a b']`:                nil,
		`//ol[@title='x*=y']`:                {"x1"},
		`//ol[@class = "other"]`:             {"x2"},
		`//ol[contains(@class, 'oth')]`:      {"x2"},
		`//ol[@class *= 'oth' or @id^='x1']`: {"x1", "x2"},
	}
	for expr, want := range cases {
		nodes, err := queryAll(doc, expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		var got []string
		for _, n := range nodes {
			got = append(got, attr(n, "id"))
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", expr, got, want)
		}
	}
}

// aafun's chapterRoads is "//": Kazumi evaluates it to the document element,
// so the whole page is the single road.
func TestXPathBareSlashRoadsSelectWholePage(t *testing.T) {
	r := mustRule(t, `{"name": "Bare", "baseURL": "https://www.example.com", "searchURL": "https://www.example.com/s?q=@keyword",
		"searchList": "//li", "searchName": "//a", "searchResult": "//a", "chapterRoads": "//", "chapterResult": "//ul/li/a"}`)
	d, err := New(r, respondWith(200, `<ul><li><a href="/e/1">1</a></li><li><a href="/e/2">2</a></li></ul>`)).Details(context.Background(), "/d/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Lines) != 1 || len(d.Lines[0].Episodes) != 2 {
		t.Fatalf("lines = %+v", d.Lines)
	}
}

func TestXPathUsePostKeepsFieldOrder(t *testing.T) {
	r := mustRule(t, `{"name": "Post", "api": "2", "usePost": true, "baseURL": "https://www.example.com/",
		"searchURL": "https://www.example.com/s?z=1&wd=@keyword&a=2&z=3&empty=&flag",
		"searchList": "//li", "searchName": "//a", "searchResult": "//a", "chapterRoads": "//ul", "chapterResult": "//a"}`)
	tr := respondWith(200, `<li><a href="/d/1">A</a></li>`)
	if _, err := New(r, tr).Search(context.Background(), "k", ""); err != nil {
		t.Fatal(err)
	}
	if got, want := string(tr.last(t).Body), "z=3&wd=k&a=2&empty=&flag="; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
