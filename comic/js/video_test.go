package js

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const demoVideo = `
class DemoAnime extends VideoSource {
    name = "示例番剧站"
    key = "demo_anime"
    version = "0.1.0"
    minCoreVersion = "1.0.0"

    search = {
        load: async (keyword, options, page) => {
            const res = await Network.get("https://anime.test/s?q=" + encodeURIComponent(keyword), {})
            const doc = new HtmlDocument(res.body)
            const videos = doc.querySelectorAll(".item > a").map(a => new Video({
                id: a.attributes.href.split("/").pop(), title: a.text.trim(), tags: ["新番"]
            }))
            doc.dispose()
            return {videos, maxPage: 1}
        }
    }

    video = {
        loadInfo: async (id) => new VideoDetails({
            title: "作品" + id,
            lines: [{id: "l1", name: "线路一", episodes: [{id: "e1", title: "第1集"}, {id: "e2", title: "第2集"}]}]
        }),
        loadPlay: async (id, lineId, episodeId) => {
            if (episodeId === "e2") {
                return new PlayInfo({sniff: {page: "https://anime.test/play/" + id + "/" + episodeId, match: "\\.m3u8"}})
            }
            return new PlayInfo({url: "https://cdn.test/" + id + "/" + episodeId + ".m3u8", format: "hls",
                headers: {Referer: "https://anime.test/"}, hls: {filterAds: true}})
        }
    }
}
`

func TestVideoSourceLoadAndCall(t *testing.T) {
	site := &fakeSite{routes: map[string]route{
		"https://anime.test/s": {body: `<div class="item"><a href="/v/101"> 番一 </a></div><div class="item"><a href="/v/102">番二</a></div>`},
	}}
	r, err := New(Options{}, Services{Transport: site, Storage: newMem(), Env: Environment{AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	meta, err := r.LoadSource(ctx, demoVideo)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Kind != KindVideo || meta.Key != "demo_anime" || meta.MinAppVersion != "1.0.0" {
		t.Fatalf("meta = %+v", meta)
	}
	// It is registered in VideoSource.sources only.
	if got, _ := r.evalIn(ctx, "demo_anime", `[typeof VideoSource.sources.demo_anime, typeof ComicSource.sources.demo_anime]`); string(got) != `["object","undefined"]` {
		t.Fatalf("registries = %s", got)
	}
	out, err := r.Call(ctx, "demo_anime", "search.load", "番", []any{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var sr struct {
		Videos []struct {
			ID, Title string
			Tags      []string
		}
	}
	json.Unmarshal(out, &sr)
	if len(sr.Videos) != 2 || sr.Videos[0].ID != "101" || sr.Videos[0].Title != "番一" {
		t.Fatalf("search = %s", out)
	}
	if p := site.requests()[0].Partition; p != "comic:demo_anime" {
		t.Fatalf("partition = %q", p)
	}
	out, err = r.Call(ctx, "demo_anime", "video.loadInfo", "101")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"episodes":[{"id":"e1","title":"第1集"}`) {
		t.Fatalf("details = %s", out)
	}
	out, err = r.Call(ctx, "demo_anime", "video.loadPlay", "101", "l1", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"url":"https://cdn.test/101/e1.m3u8"`) || !strings.Contains(string(out), `"filterAds":true`) {
		t.Fatalf("play = %s", out)
	}
	out, _ = r.Call(ctx, "demo_anime", "video.loadPlay", "101", "l1", "e2")
	if !strings.Contains(string(out), `"sniff":{"page":"https://anime.test/play/101/e2"`) {
		t.Fatalf("sniff play = %s", out)
	}
	// Unload removes the source and its engine.
	r.Unload(ctx, "demo_anime")
	if _, err := r.Call(ctx, "demo_anime", "video.loadInfo", "1"); err == nil {
		t.Fatal("unloaded source still callable")
	}
}

func TestVideoSourceRules(t *testing.T) {
	r := newRT(t, Options{}, nil)
	ctx := context.Background()
	newer := strings.Replace(demoVideo, `minCoreVersion = "1.0.0"`, `minCoreVersion = "9.0.0"`, 1)
	if _, err := r.LoadSource(ctx, newer); err == nil || !strings.Contains(err.Error(), "minCoreVersion 9.0.0") {
		t.Fatalf("newer core: %v", err)
	}
	// minAppVersion is not what video plugins compare: an app version check
	// must not block a video plugin that only sets minCoreVersion.
	if _, err := r.LoadSource(ctx, demoVideo); err != nil {
		t.Fatal(err)
	}
	// Comic and video keys share one namespace.
	clash := "class X extends ComicSource { name = 'x'; key = 'demo_anime'; version = '1' }"
	if _, err := r.LoadSource(ctx, clash); err == nil {
		t.Fatal("a comic source reused a video key")
	}
	// Video plugins have storage and settings like comic sources.
	got, err := r.evalIn(ctx, "demo_anime", `VideoSource.sources.demo_anime.saveData("k", 1); VideoSource.sources.demo_anime.loadData("k")`)
	if err != nil || string(got) != "1" {
		t.Fatalf("storage = %s, %v", got, err)
	}
}

const bigIDVideo = `
class Big extends VideoSource {
    name = "Big"; key = "big_ids"; version = "1.0.0"
    search = { load: async (k, o, page) => ({videos: [{id: 9007199254740993n, title: "p" + page}], maxPage: 2}) }
    video = {
        loadInfo: async (id) => ({title: "t", lines: [{name: "", episodes: [{id: 7400000000000000001n}, {title: "no id"}]}]}),
        loadPlay: async (id, l, e) => ({format: "weird"})
    }
}
`

func TestVideoProviderAdapter(t *testing.T) {
	site := &fakeSite{routes: map[string]route{
		"https://anime.test/s": {body: `<div class="item"><a href="/v/101">番一</a></div>`},
	}}
	r, err := New(Options{}, Services{Transport: site, Storage: newMem(), Env: Environment{AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	if _, err := r.LoadSource(ctx, demoVideo); err != nil {
		t.Fatal(err)
	}
	p, err := r.VideoProvider("demo_anime")
	if err != nil {
		t.Fatal(err)
	}
	if p.Info().Kind != "js" {
		t.Fatalf("info = %+v", p.Info())
	}
	page, err := p.Search(ctx, "番", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "101" || page.Next != "" {
		t.Fatalf("search = %+v, %v", page, err)
	}
	d, err := p.Details(ctx, "101")
	if err != nil || len(d.Lines) != 1 || len(d.Lines[0].Episodes) != 2 {
		t.Fatalf("details = %+v, %v", d, err)
	}
	play, err := p.Play(ctx, "101", "l1", "e1")
	if err != nil || play.Format != "hls" || play.Header.Get("Referer") != "https://anime.test/" || play.HLS == nil || !play.HLS.FilterAds {
		t.Fatalf("play = %+v, %v", play, err)
	}
	play, err = p.Play(ctx, "101", "l1", "e2")
	if err != nil || play.Sniff == nil || play.Sniff.Match != `\.m3u8` {
		t.Fatalf("sniff = %+v, %v", play, err)
	}

	// Big IDs, paging, defaults and validation.
	if _, err := r.LoadSource(ctx, bigIDVideo); err != nil {
		t.Fatal(err)
	}
	b, _ := r.VideoProvider("big_ids")
	page, err = b.Search(ctx, "x", "")
	if err != nil || page.Items[0].ID != "9007199254740993" || page.Next != "2" {
		t.Fatalf("big search = %+v, %v", page, err)
	}
	page, _ = b.Search(ctx, "x", "2")
	if page.Next != "" || page.Items[0].Title != "p2" {
		t.Fatalf("last page = %+v", page)
	}
	d, _ = b.Details(ctx, "1")
	if len(d.Lines[0].Episodes) != 1 || d.Lines[0].Episodes[0].ID != "7400000000000000001" || d.Lines[0].Name != "Line 1" || d.Lines[0].Episodes[0].Title != "Episode 1" {
		t.Fatalf("big details = %+v", d)
	}
	if _, err := b.Play(ctx, "1", "line0", "7400000000000000001"); err == nil {
		t.Fatal("play without url or sniff accepted")
	}
	// Comic sources are not video providers.
	if _, err := r.LoadSource(ctx, miniSource); err != nil {
		t.Fatal(err)
	}
	if _, err := r.VideoProvider("mini"); err == nil {
		t.Fatal("comic source returned as video provider")
	}
}

func TestAttributionFromScripts(t *testing.T) {
	r := newRT(t, Options{}, nil)
	ctx := context.Background()
	comic := `class A extends ComicSource { name = 'A'; key = 'attr_comic'; version = '1'
	  author = [{name: 'Alice', url: 'https://github.com/alice'}, 'Bob']
	  homepage = 'https://github.com/alice/sources' }`
	meta, err := r.LoadSource(ctx, comic)
	if err != nil {
		t.Fatal(err)
	}
	a := meta.Attribution
	if len(a.Authors) != 2 || a.Authors[0].URL != "https://github.com/alice" || a.Authors[1].Name != "Bob" || a.Homepage != "https://github.com/alice/sources" {
		t.Fatalf("comic attribution = %+v", a)
	}
	video := `class V extends VideoSource { name = 'V'; key = 'attr_video'; version = '1'
	  author = 'Carol'; homepage = 'javascript:alert(1)' }`
	meta, err = r.LoadSource(ctx, video)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Attribution.Authors[0].Name != "Carol" || meta.Attribution.Homepage != "" || len(meta.Warnings) != 1 {
		t.Fatalf("video attribution = %+v %v", meta.Attribution, meta.Warnings)
	}
	// Official sources declare neither: both are reported missing, the load succeeds.
	meta, err = r.LoadSource(ctx, miniSource)
	if err != nil {
		t.Fatal(err)
	}
	if noAuthor, noHome := meta.Attribution.Missing(); !noAuthor || !noHome {
		t.Fatalf("official source attribution = %+v", meta.Attribution)
	}
}
