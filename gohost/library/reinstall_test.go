package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/laincat/veneracat-core/gohost"
	"github.com/laincat/veneracat-core/plugin"
	"github.com/laincat/veneracat-core/subscription"
)

func TestEnabledSourcesUpdateFromFileAndURLThenRestore(t *testing.T) {
	for _, kind := range []plugin.Type{plugin.TypeComic, plugin.TypeVideoJS} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			c, err := core.Open(core.Options{DataDir: root, AppVersion: "1.6.3"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			lib := New(c)
			body := func(n int) string {
				if kind == plugin.TypeVideoJS {
					return videoInstallScript(fmt.Sprintf("%d.0.0", n))
				}
				return strings.ReplaceAll(strings.ReplaceAll(firstPersisted, "1.0.0", fmt.Sprintf("%d.0.0", n)), "maxPage: 1", fmt.Sprintf("maxPage: %d", n))
			}
			path := filepath.Join(t.TempDir(), "source.js")
			if err = os.WriteFile(path, []byte(body(1)), 0600); err != nil {
				t.Fatal(err)
			}
			first, err := lib.InstallFile(ctx, kind, path)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, []byte(body(2)), 0600); err != nil {
				t.Fatal(err)
			}
			second, err := lib.InstallFile(ctx, kind, path)
			if err != nil || second == nil || second.Version != "2.0.0" || !second.Enabled {
				t.Fatalf("enabled file update=%+v error=%v", second, err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body(3)) }))
			defer server.Close()
			third, err := lib.InstallTypedURL(ctx, kind, server.URL+"/source.js")
			if err != nil || third == nil || third.Version != "3.0.0" || !third.Enabled {
				t.Fatalf("enabled URL update=%+v error=%v", third, err)
			}
			// A second runtime must execute the persisted update, not the original
			// version or an object left alive in the installer's engine.
			reopened, err := core.Open(core.Options{DataDir: root, AppVersion: "1.6.3"})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			restored := New(reopened)
			failed, err := restored.Restore(ctx)
			if err != nil || len(failed) != 0 {
				t.Fatalf("restore=%v error=%v", failed, err)
			}
			recs := restored.Sources()
			if len(recs) != 1 || recs[0].Key != first.Key || recs[0].Type != kind || recs[0].Version != "3.0.0" || !recs[0].Enabled {
				t.Fatalf("saved source=%+v", recs)
			}
			if kind == plugin.TypeVideoJS {
				page, err := reopened.VideoSearch(ctx, first.Key, "clip", "")
				if err != nil || len(page.Items) != 1 || page.Items[0].Title != "3.0.0" {
					t.Fatalf("restored video result=%+v error=%v", page, err)
				}
			} else {
				raw, err := reopened.Call(ctx, first.Key, "search.load", "query", []any{}, 1)
				if err != nil {
					t.Fatal(err)
				}
				var result struct {
					MaxPage int `json:"maxPage"`
				}
				if err = json.Unmarshal(raw, &result); err != nil || result.MaxPage != 3 {
					t.Fatalf("restored comic result=%s error=%v", raw, err)
				}
			}
		})
	}
}

func TestIndexCannotRedirectAnInstallToAnotherSource(t *testing.T) {
	ctx := context.Background()
	c, err := core.Open(core.Options{DataDir: t.TempDir(), AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lib := New(c)
	if _, err = lib.InstallText(ctx, firstPersisted, "original.js"); err != nil {
		t.Fatal(err)
	}
	before, err := c.Store().Records()
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(firstPersisted, "1.0.0", "2.0.0", 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer server.Close()
	if _, err = lib.InstallEntry(ctx, subscription.Entry{Key: "different_catalog_entry", URL: server.URL}, "https://index.invalid/index.json"); err == nil {
		t.Error("index entry selected another installed source for replacement")
	}
	recs := c.Records()
	if len(recs) != 1 || recs[0].Body != firstPersisted || recs[0].Origin != "original.js" {
		t.Error("index mismatch replaced the original source")
	}
	after, err := c.Store().Records()
	if err != nil || string(before) != string(after) {
		t.Error("index mismatch changed persisted sources")
	}
}

func TestForgedDuplicateMessageDoesNotUpdateInstalledSource(t *testing.T) {
	ctx := context.Background()
	c, err := core.Open(core.Options{DataDir: t.TempDir(), AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lib := New(c)
	if _, err = lib.InstallText(ctx, firstPersisted, "original.js"); err != nil {
		t.Fatal(err)
	}
	spoof := `class Evil extends ComicSource { constructor(){ super(); throw new Error("plugin: key already installed: first_source") } }`
	if _, err = lib.InstallText(ctx, spoof, "evil.js"); err == nil {
		t.Fatal("spoofed exception was accepted")
	}
	recs := c.Records()
	if len(recs) != 1 || recs[0].Body != firstPersisted || recs[0].Origin != "original.js" {
		t.Fatal("exception text selected an update target")
	}
}
