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

func videoInstallScript(version string) string {
	return fmt.Sprintf(`class DurableVideo extends VideoSource {
 name = "Durable video"; key = "durable_video"; version = %q
 search = {load: async () => ({videos: [{id: "clip", title: %q}], maxPage: 1})}
 video = {loadPlay: async () => ({url: "https://fixture.invalid/clip.mp4", format: "mp4"})}
}`, version, version)
}

func TestVideoFileInstallURLUpdateAndRestartPreserveTypeAndDisabledState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	c, err := core.Open(core.Options{DataDir: root, AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lib := New(c)
	path := filepath.Join(t.TempDir(), "video.js")
	if err := os.WriteFile(path, []byte(videoInstallScript("1.0.0")), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := lib.InstallFile(ctx, plugin.TypeVideoJS, path)
	if err != nil || record == nil || record.Type != plugin.TypeVideoJS {
		t.Fatalf("file installation = %+v, %v", record, err)
	}
	if len(lib.ComicSources()) != 0 {
		t.Fatal("the manga picker includes the video provider")
	}
	if err := lib.SetEnabled(ctx, record.Key, false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, videoInstallScript("2.0.0"))
	}))
	defer server.Close()
	updated, err := lib.InstallTypedURL(ctx, plugin.TypeVideoJS, server.URL+"/video.js")
	if err != nil || updated == nil || !strings.Contains(updated.Body, `version = "2.0.0"`) || updated.Enabled {
		t.Fatalf("disabled URL update = %+v, %v", updated, err)
	}
	// A same-key manga script cannot silently convert or remove the provider.
	comic := `class WrongKind extends ComicSource { name = "Wrong"; key = "durable_video"; version = "3.0.0" }`
	if _, err := lib.InstallText(ctx, comic, "test"); !errors.Is(err, plugin.ErrWrongUse) {
		t.Fatalf("a conflicting manga install returned %v, want ErrWrongUse", err)
	}
	if _, err := lib.InstallTypedText(ctx, plugin.TypeVideoJS, "invalid javascript !!!", "test"); err == nil {
		t.Fatal("a malformed update was accepted")
	}
	// A fresh engine restores only the library's durable records, not objects
	// kept alive by the installer; it must still see a disabled video provider.
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
	if len(records) != 1 || records[0].Type != plugin.TypeVideoJS || records[0].Version != "1.0.0" || records[0].Enabled || records[0].Origin != server.URL+"/video.js" {
		t.Fatalf("restored records = %+v", records)
	}
	if _, err := reopened.VideoSearch(ctx, record.Key, "clip", ""); !errors.Is(err, plugin.ErrDisabled) {
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
	page, err := reopened.VideoSearch(ctx, record.Key, "clip", "")
	if err != nil || page == nil || len(page.Items) != 1 || page.Items[0].Title != "2.0.0" {
		t.Fatalf("restored video search = %+v, %v", page, err)
	}
	play, err := reopened.VideoPlay(ctx, record.Key, "clip", "line", "ep")
	if err != nil || play == nil || play.URL != "https://fixture.invalid/clip.mp4" {
		t.Fatalf("restored play declaration = %+v, %v", play, err)
	}
}

func TestSourceFileLimitDoesNotInstallPartialInput(t *testing.T) {
	c, err := core.Open(core.Options{DataDir: t.TempDir(), AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lib := New(c)
	path := filepath.Join(t.TempDir(), "too-large.js")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", MaxScriptBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.InstallFile(context.Background(), plugin.TypeVideoJS, path); err == nil {
		t.Fatal("an over-budget file was accepted")
	}
	if len(lib.Sources()) != 0 {
		t.Fatal("a failed file read changed the installed set")
	}
}
