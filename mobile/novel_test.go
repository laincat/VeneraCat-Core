package mobile

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/novel"
	"github.com/laincat/veneracat-core/plugin"
)

const novelFixture = `class MobileNovel extends NovelSource {
  name = "Mobile Novel"; key = "mobile_novel"; version = "1.0.0"
  url = "https://novel.test"

  async popular(page) {
    const res = await Network.get("https://novel.test/popular?page=" + page)
    return {
      items: [{id: "n1", title: res.body, cover: "https://novel.test/c1.jpg", author: "Alice"}],
      maxPage: 3
    }
  }

  async search(keyword, page) {
    const res = await Network.get("https://novel.test/search?q=" + encodeURIComponent(keyword) + "&p=" + page)
    return {
      items: [{id: "s1", title: res.body, author: "Bob"}],
      maxPage: 2
    }
  }

  async detail(id) {
    return {
      id: id,
      title: "Starlit Tower",
      cover: "https://novel.test/cover.jpg",
      author: "Alice",
      summary: "A short probe summary.",
      status: "ongoing",
      tags: ["fantasy", "mystery"],
      chapters: [
        {id: "ch1", title: "Chapter 1", time: "2026-11-24"},
        {id: "ch2", title: "Chapter 2"}
      ]
    }
  }

  async chapter(novelId, chapterId) {
    const res = await Network.get("https://novel.test/chapter/" + novelId + "/" + chapterId)
    return {html: res.body}
  }
}`

const videoFixture = `class MobileVideo extends VideoSource {
  name = "Mobile Video"; key = "mobile_video"; version = "1.0.0"
  async search(keyword, cursor) { return {items: []} }
  async loadInfo(id) { return new VideoDetails({title: "Clip", lines: []}) }
  async loadPlay(itemId, lineId, epId) { return new PlayInfo({url: "https://video.test/a.m3u8"}) }
}`

const rawChapterHTML = `<div class="content" data-chapter="ch1"><p>Line 1 &amp; <em>Line 2</em></p><img src="https://novel.test/p1.png" /></div>`

func novelTestHost() *fakeHost {
	return &fakeHost{
		routes: map[string]string{
			"https://novel.test/popular?page=2": "popular-page-2",
			"https://novel.test/popular?page=1": "popular-page-1",
			"https://novel.test/search?q=star":  "search-star-light-2",
			"https://novel.test/chapter/n1/ch1": rawChapterHTML,
		},
		data: map[string]string{},
	}
}

func assertNovelFourEntryPoints(t *testing.T, c *Core, key string) {
	t.Helper()

	popJSON, err := c.NovelPopular(c.NewCall(), key, 2)
	if err != nil {
		t.Fatalf("NovelPopular: %v", err)
	}
	var pop novel.Page
	if err := json.Unmarshal([]byte(popJSON), &pop); err != nil {
		t.Fatalf("decode NovelPopular JSON %q: %v", popJSON, err)
	}
	if pop.MaxPage != 3 || len(pop.Items) != 1 || pop.Items[0].ID != "n1" || pop.Items[0].Title != "popular-page-2" || pop.Items[0].Author != "Alice" {
		t.Fatalf("unexpected NovelPopular result: %+v", pop)
	}

	searchJSON, err := c.NovelSearch(c.NewCall(), key, "star light", 2)
	if err != nil {
		t.Fatalf("NovelSearch: %v", err)
	}
	var found novel.Page
	if err := json.Unmarshal([]byte(searchJSON), &found); err != nil {
		t.Fatalf("decode NovelSearch JSON %q: %v", searchJSON, err)
	}
	if found.MaxPage != 2 || len(found.Items) != 1 || found.Items[0].ID != "s1" || found.Items[0].Title != "search-star-light-2" {
		t.Fatalf("unexpected NovelSearch result: %+v", found)
	}

	detailJSON, err := c.NovelDetail(c.NewCall(), key, "n1")
	if err != nil {
		t.Fatalf("NovelDetail: %v", err)
	}
	var detail novel.Detail
	if err := json.Unmarshal([]byte(detailJSON), &detail); err != nil {
		t.Fatalf("decode NovelDetail JSON %q: %v", detailJSON, err)
	}
	if detail.ID != "n1" || detail.Title != "Starlit Tower" || detail.Author != "Alice" || detail.Status != "ongoing" {
		t.Fatalf("unexpected NovelDetail metadata: %+v", detail)
	}
	if len(detail.Tags) != 2 || len(detail.Chapters) != 2 || detail.Chapters[0].ID != "ch1" || detail.Chapters[0].Time != "2026-11-24" {
		t.Fatalf("unexpected NovelDetail chapters/tags: %+v", detail)
	}

	chapJSON, err := c.NovelChapter(c.NewCall(), key, "n1", "ch1")
	if err != nil {
		t.Fatalf("NovelChapter: %v", err)
	}
	var chap novel.ChapterContent
	if err := json.Unmarshal([]byte(chapJSON), &chap); err != nil {
		t.Fatalf("decode NovelChapter JSON %q: %v", chapJSON, err)
	}
	if chap.HTML != rawChapterHTML {
		t.Fatalf("NovelChapter altered HTML:\n got: %q\nwant: %q", chap.HTML, rawChapterHTML)
	}
}

func TestEndToEndNovelAndRestore(t *testing.T) {
	h := novelTestHost()
	c := newCore(t, h)

	recJSON, err := c.Install(c.NewCall(), string(plugin.TypeNovelJS), novelFixture, "file", "", "")
	if err != nil {
		t.Fatalf("Install novel-js: %v", err)
	}
	if !strings.Contains(recJSON, `"key":"mobile_novel"`) || !strings.Contains(recJSON, `"type":"novel-js"`) {
		t.Fatalf("unexpected install record: %s", recJSON)
	}

	assertNovelFourEntryPoints(t, c, "mobile_novel")

	list, err := c.Plugins()
	if err != nil {
		t.Fatalf("Plugins: %v", err)
	}
	if strings.Contains(list, "Network.get") {
		t.Fatal("Plugins() must omit novel script bodies")
	}
	recs, err := c.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if !strings.Contains(recs, "Network.get") {
		t.Fatal("Records() must include novel script bodies for persistence")
	}

	// Simulate app restart by restoring persisted records into a fresh Core.
	c2 := newCore(t, h)
	failed, err := c2.Restore(recs)
	if err != nil || failed != "{}" {
		t.Fatalf("Restore = %s, %v", failed, err)
	}
	assertNovelFourEntryPoints(t, c2, "mobile_novel")

	// Persisting a disabled novel source must keep it disabled after restart.
	if err := c2.SetEnabled("mobile_novel", false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	disabledRecs, err := c2.Records()
	if err != nil {
		t.Fatalf("Records(disabled): %v", err)
	}
	c3 := newCore(t, h)
	failed, err = c3.Restore(disabledRecs)
	if err != nil || failed != "{}" {
		t.Fatalf("Restore(disabled) = %s, %v", failed, err)
	}
	if _, err := c3.NovelPopular(c3.NewCall(), "mobile_novel", 1); !errors.Is(err, plugin.ErrDisabled) {
		t.Fatalf("restored disabled novel expected ErrDisabled, got %v", err)
	}
	if err := c3.SetEnabled("mobile_novel", true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	assertNovelFourEntryPoints(t, c3, "mobile_novel")
}

func TestNovelErrorsNotFoundDisabledAndWrongUse(t *testing.T) {
	h := novelTestHost()
	c := newCore(t, h)

	novelCalls := func(key string) map[string]func() error {
		return map[string]func() error{
			"NovelPopular": func() error { _, err := c.NovelPopular(c.NewCall(), key, 1); return err },
			"NovelSearch":  func() error { _, err := c.NovelSearch(c.NewCall(), key, "star", 1); return err },
			"NovelDetail":  func() error { _, err := c.NovelDetail(c.NewCall(), key, "n1"); return err },
			"NovelChapter": func() error { _, err := c.NovelChapter(c.NewCall(), key, "n1", "ch1"); return err },
		}
	}

	// Uninstalled source -> ErrNotFound on all four entry points.
	for name, call := range novelCalls("missing_novel") {
		if err := call(); !errors.Is(err, plugin.ErrNotFound) {
			t.Fatalf("%s on missing source = %v, want ErrNotFound", name, err)
		}
	}

	// Install novel, comic, and video sources.
	if _, err := c.Install(c.NewCall(), string(plugin.TypeNovelJS), novelFixture, "file", "", ""); err != nil {
		t.Fatalf("install novel: %v", err)
	}
	if _, err := c.Install(c.NewCall(), string(plugin.TypeComic), comic, "file", "", ""); err != nil {
		t.Fatalf("install comic: %v", err)
	}
	if _, err := c.Install(c.NewCall(), string(plugin.TypeVideoJS), videoFixture, "file", "", ""); err != nil {
		t.Fatalf("install video: %v", err)
	}

	// Disabled novel source -> ErrDisabled on all four entry points.
	if err := c.SetEnabled("mobile_novel", false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	for name, call := range novelCalls("mobile_novel") {
		if err := call(); !errors.Is(err, plugin.ErrDisabled) {
			t.Fatalf("%s on disabled source = %v, want ErrDisabled", name, err)
		}
	}
	if err := c.SetEnabled("mobile_novel", true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}

	// Cross-type: calling novel entry points on comic or video sources -> ErrWrongUse.
	for _, wrongKey := range []string{"mini", "mobile_video"} {
		for name, call := range novelCalls(wrongKey) {
			if err := call(); !errors.Is(err, plugin.ErrWrongUse) {
				t.Fatalf("%s on %q = %v, want ErrWrongUse", name, wrongKey, err)
			}
		}
	}

	// Cross-type: calling comic or video entry points on a novel source -> ErrWrongUse.
	if _, err := c.CallComic(c.NewCall(), "mobile_novel", "search.load", "[]"); !errors.Is(err, plugin.ErrWrongUse) {
		t.Fatalf("CallComic on novel source = %v, want ErrWrongUse", err)
	}
	if _, err := c.VideoSearch(c.NewCall(), "mobile_novel", "q", ""); !errors.Is(err, plugin.ErrWrongUse) {
		t.Fatalf("VideoSearch on novel source = %v, want ErrWrongUse", err)
	}

	// Every early-return error call must have released its callID reservation.
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cancels) != 0 || len(c.reserved) != 0 {
		t.Fatalf("leaked call reservations after novel error calls: cancels=%d reserved=%d", len(c.cancels), len(c.reserved))
	}
}
