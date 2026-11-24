package gohost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/plugin"
)

// novelProbeScript is a NovelSource that reaches the fixture server through the
// same Network global a real source uses, so the four entry points, the cookie
// partition and the enable/disable gate are all exercised on the real path.
func novelProbeScript(base, key string) string {
	return fmt.Sprintf(`class NovelProbe extends NovelSource {
 name = "Novel probe %s"; key = %q; version = "1.0.0"
 url = %q

 async popular(page) {
   const res = await Network.get(%q + "/popular?page=" + page)
   return {items: [{id: "n1", title: res.body, author: "Alice"}], maxPage: 2}
 }

 async search(keyword, page) {
   const res = await Network.get(%q + "/search?q=" + encodeURIComponent(keyword) + "&p=" + page)
   return {items: [{id: "s1", title: res.body}], maxPage: 1}
 }

 async detail(id) {
   return {
     id: id,
     title: "Probe Novel",
     author: "Alice",
     summary: "A probe summary.",
     status: "ongoing",
     tags: ["fantasy", "adventure"],
     chapters: [
       {id: "ch1", title: "Chapter 1", time: "2026-11-24"},
       {id: "ch2", title: "Chapter 2"}
     ]
   }
 }

 async chapter(novelId, chapId) {
   const res = await Network.get(%q + "/chapter/" + novelId + "/" + chapId)
   return {html: res.body}
 }
}`, key, key, base, base, base, base)
}

func novelProbeServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case r.URL.Path == "/popular":
			fmt.Fprint(w, "popular-page-"+r.URL.Query().Get("page"))
		case r.URL.Path == "/search":
			fmt.Fprint(w, "search-"+r.URL.Query().Get("q")+"-"+r.URL.Query().Get("p"))
		case strings.HasPrefix(r.URL.Path, "/chapter/"):
			fmt.Fprint(w, `<div class="content"><p>`+strings.TrimPrefix(r.URL.Path, "/chapter/")+`</p></div>`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestNovelFourEntryPointsDelegateToTheProvider(t *testing.T) {
	srv := novelProbeServer()
	defer srv.Close()
	c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	record, err := c.InstallTyped(ctx, plugin.TypeNovelJS, novelProbeScript(srv.URL, "novel_probe"), "file", "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	page, err := c.NovelPopular(ctx, record.Key, 2)
	if err != nil || page == nil || page.MaxPage != 2 || len(page.Items) != 1 {
		t.Fatalf("popular = %+v, %v", page, err)
	}
	if page.Items[0].ID != "n1" || page.Items[0].Title != "popular-page-2" || page.Items[0].Author != "Alice" {
		t.Fatalf("popular item = %+v", page.Items[0])
	}
	found, err := c.NovelSearch(ctx, record.Key, "star light", 1)
	if err != nil || found == nil || len(found.Items) != 1 {
		t.Fatalf("search = %+v, %v", found, err)
	}
	if found.Items[0].Title != "search-star light-1" {
		t.Fatalf("search did not carry the keyword through: %q", found.Items[0].Title)
	}
	detail, err := c.NovelDetail(ctx, record.Key, "n1")
	if err != nil || detail == nil || detail.ID != "n1" || detail.Title != "Probe Novel" {
		t.Fatalf("detail = %+v, %v", detail, err)
	}
	if len(detail.Chapters) != 2 || detail.Chapters[0].ID != "ch1" || detail.Chapters[0].Time != "2026-11-24" || len(detail.Tags) != 2 {
		t.Fatalf("detail chapters/tags = %+v", detail)
	}
	chapter, err := c.NovelChapter(ctx, record.Key, "n1", "ch1")
	if err != nil || chapter == nil || !strings.Contains(chapter.HTML, "n1/ch1") {
		t.Fatalf("chapter = %+v, %v", chapter, err)
	}
	// The chapter body stays raw HTML: sanitising is the client's job, and
	// stripping it here would make both clients lose the markup they render.
	if !strings.Contains(chapter.HTML, `<div class="content">`) {
		t.Fatalf("chapter HTML was altered: %q", chapter.HTML)
	}
}

// Two novel sources on one host must not see each other's sign-in. The partition
// is per source key, and a shared cookie would let one source read the other's
// session.
func TestNovelCookiesStayInTheirOwnPartition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			who := r.URL.Query().Get("who")
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: who, Path: "/"})
			fmt.Fprint(w, "signed in as "+who)
		case "/echo":
			cookie, err := r.Cookie("sid")
			if err != nil {
				fmt.Fprint(w, "anonymous")
				return
			}
			fmt.Fprint(w, cookie.Value)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A signs in and reads the cookie back; B does nothing but read. If the two
	// shared a partition, B's echo would answer with A's session.
	loginScript := fmt.Sprintf(`class LoginPartitionNovel extends NovelSource {
 name = "partition a"; key = "novel_partition_a"; version = "1.0.0"
 async popular(page) {
   await Network.get(%q + "/login?who=novel_partition_a")
   const res = await Network.get(%q + "/echo")
   return {items: [{id: "n1", title: res.body}], maxPage: 1}
 }
}`, srv.URL, srv.URL)
	echoScript := fmt.Sprintf(`class EchoPartitionNovel extends NovelSource {
 name = "partition b"; key = "novel_partition_b"; version = "1.0.0"
 async popular(page) {
   const res = await Network.get(%q + "/echo")
   return {items: [{id: "n2", title: res.body}], maxPage: 1}
 }
}`, srv.URL)
	a, err := c.InstallTyped(ctx, plugin.TypeNovelJS, loginScript, "file", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.InstallTyped(ctx, plugin.TypeNovelJS, echoScript, "file", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.NovelPopular(ctx, a.Key, 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].Title != "novel_partition_a" {
		t.Fatalf("source a popular = %+v, %v", first, err)
	}
	second, err := c.NovelPopular(ctx, b.Key, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].Title != "anonymous" {
		t.Fatalf("source b saw another source's session: %+v, %v", second, err)
	}

	aJar := c.Jar().Get(ComicPartition(a.Key), srv.URL+"/")
	if len(aJar) != 1 || aJar[0].Name != "sid" || aJar[0].Value != "novel_partition_a" {
		t.Fatalf("partition a = %+v", aJar)
	}
	if got := c.Jar().Get(ComicPartition(b.Key), srv.URL+"/"); len(got) != 0 {
		t.Fatalf("partition b holds cookies it never received: %+v", got)
	}
	// The partition name is the shared comic:<key> one: a novel source keeps its
	// session where the runtime already looks for it.
	if got := c.Jar().Get("comic:novel_partition_a", srv.URL+"/"); len(got) != 1 {
		t.Fatalf("the novel partition is not the shared comic:<key> one: %+v", got)
	}
}

// A disabled or wrongly-typed source answers with the registry's own error, so a
// client can tell "no such source" from "this text is not a novel".
func TestNovelEntryPointsRefuseDisabledAndWrongKind(t *testing.T) {
	srv := novelProbeServer()
	defer srv.Close()
	c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	record, err := c.InstallTyped(ctx, plugin.TypeNovelJS, novelProbeScript(srv.URL, "novel_switch"), "file", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetEnabled(ctx, record.Key, false); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"popular": func() error { _, err := c.NovelPopular(ctx, record.Key, 1); return err },
		"search":  func() error { _, err := c.NovelSearch(ctx, record.Key, "q", 1); return err },
		"detail":  func() error { _, err := c.NovelDetail(ctx, record.Key, "n1"); return err },
		"chapter": func() error { _, err := c.NovelChapter(ctx, record.Key, "n1", "ch1"); return err },
	} {
		if err := call(); !errors.Is(err, plugin.ErrDisabled) {
			t.Fatalf("a disabled source answered %s: %v", name, err)
		}
	}
	if _, err := c.NovelPopular(ctx, "absent_novel", 1); !errors.Is(err, plugin.ErrNotFound) {
		t.Fatalf("an unknown key returned %v", err)
	}

	// A comic source shares the runtime kind but not the novel contract.
	comic, err := c.Install(ctx, `class NotANovel extends ComicSource {
 name = "Not a novel"; key = "novel_wrong_kind"; version = "1.0.0"
}`, "file", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.NovelPopular(ctx, comic.Key, 1); !errors.Is(err, plugin.ErrWrongUse) {
		t.Fatalf("a comic source answered a novel call: %v", err)
	}
}
