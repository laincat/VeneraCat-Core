package library

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/laincat/veneracat-core/gohost"
	"github.com/laincat/veneracat-core/plugin"
)

func novelInstallScript(version string) string {
	return fmt.Sprintf(`class DurableNovel extends NovelSource {
 name = "Durable novel"; key = "durable_novel"; version = %q
 async popular(page) {
   return {items: [{id: "n1", title: "popular"}], maxPage: 1}
 }
 async search(keyword, page) {
   return {items: [{id: "s1", title: %q}], maxPage: 1}
 }
 async detail(id) {
   return {id: id, title: "Durable novel", chapters: [{id: "ch1", title: "Chapter 1"}]}
 }
 async chapter(novelId, chapId) {
   return {html: "<p>" + novelId + "/" + chapId + "</p>"}
 }
}`, version, version)
}

// A novel-js source is installed, updated and restored through exactly the same
// durable path as a video provider. Nothing about that path is type-specific, so
// this is the check that a novel source is not silently dropped on restart.
func TestNovelFileInstallURLUpdateAndRestartPreserveTypeAndDisabledState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	c, err := core.Open(core.Options{DataDir: root, AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lib := New(c)
	path := filepath.Join(t.TempDir(), "novel.js")
	if err := os.WriteFile(path, []byte(novelInstallScript("1.0.0")), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := lib.InstallFile(ctx, plugin.TypeNovelJS, path)
	if err != nil || record == nil || record.Type != plugin.TypeNovelJS {
		t.Fatalf("file installation = %+v, %v", record, err)
	}
	if len(lib.ComicSources()) != 0 {
		t.Fatal("the manga picker includes the novel provider")
	}
	if err := lib.SetEnabled(ctx, record.Key, false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, novelInstallScript("2.0.0"))
	}))
	defer server.Close()
	updated, err := lib.InstallTypedURL(ctx, plugin.TypeNovelJS, server.URL+"/novel.js")
	if err != nil || updated == nil || !strings.Contains(updated.Body, `version = "2.0.0"`) || updated.Enabled {
		t.Fatalf("disabled URL update = %+v, %v", updated, err)
	}
	// A same-key manga script cannot silently convert or remove the provider.
	comic := `class WrongKind extends ComicSource { name = "Wrong"; key = "durable_novel"; version = "3.0.0" }`
	if _, err := lib.InstallText(ctx, comic, "test"); !errors.Is(err, plugin.ErrWrongUse) {
		t.Fatalf("a conflicting manga install returned %v, want ErrWrongUse", err)
	}
	if _, err := lib.InstallTypedText(ctx, plugin.TypeNovelJS, "invalid javascript !!!", "test"); err == nil {
		t.Fatal("a malformed update was accepted")
	}
	// A fresh engine restores only the library's durable records; it must still
	// see a disabled novel provider.
	reopened, err := core.Open(core.Options{DataDir: root, AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored := New(reopened)
	failed, err := restored.Restore(ctx)
	if err != nil || len(failed) != 0 {
		t.Fatalf("restore = %v, %v", failed, err)
	}
	records := restored.Sources()
	if len(records) != 1 || records[0].Type != plugin.TypeNovelJS || records[0].Version != "1.0.0" || records[0].Enabled || records[0].Origin != server.URL+"/novel.js" {
		t.Fatalf("restored records = %+v", records)
	}
	if _, err := reopened.NovelSearch(ctx, record.Key, "still off", 1); !errors.Is(err, plugin.ErrDisabled) {
		t.Fatalf("a disabled provider answered search: %v", err)
	}
	if err := restored.SetEnabled(ctx, record.Key, true); err != nil {
		t.Fatal(err)
	}
	// Core deliberately does not execute disabled JS merely to refresh its
	// metadata. Activation must now expose the new version as well as code.
	if got := restored.Sources()[0].Version; got != "2.0.0" {
		t.Fatalf("activation kept old version metadata: %s", got)
	}
	page, err := reopened.NovelSearch(ctx, record.Key, "restored", 1)
	if err != nil || page == nil || len(page.Items) != 1 || page.Items[0].Title != "2.0.0" {
		t.Fatalf("restored novel search = %+v, %v", page, err)
	}
	chapter, err := reopened.NovelChapter(ctx, record.Key, "n1", "ch1")
	if err != nil || chapter == nil || !strings.Contains(chapter.HTML, "n1/ch1") {
		t.Fatalf("restored chapter = %+v, %v", chapter, err)
	}
	// Uninstalling forgets the record, so the next restore cannot bring it back.
	if err := restored.Remove(ctx, record.Key); err != nil {
		t.Fatal(err)
	}
	if len(restored.Sources()) != 0 {
		t.Fatalf("the removed source is still installed: %+v", restored.Sources())
	}
	again, err := core.Open(core.Options{DataDir: root, AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	third := New(again)
	if failed, err := third.Restore(ctx); err != nil || len(failed) != 0 {
		t.Fatalf("restore after removal = %v, %v", failed, err)
	}
	if len(third.Sources()) != 0 {
		t.Fatalf("an uninstalled novel source came back: %+v", third.Sources())
	}
}
