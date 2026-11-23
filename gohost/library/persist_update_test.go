package library

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/laincat/veneracat-core/gohost"
	"github.com/laincat/veneracat-core/plugin"
	"github.com/laincat/veneracat-core/subscription"
)

func TestFailedRollbackDisablesUnpersistedReplacement(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c, err := core.Open(core.Options{DataDir: root, AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lib := New(c)
	old := strings.Replace(firstPersisted, "search =", `init() { if (this.loadData("fail_restore")) { throw "old init unavailable" } }
 search =`, 1)
	if _, err := lib.InstallText(ctx, old, "old.js"); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "plugins.json")
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Store().SaveData("first_source", "fail_restore", json.RawMessage(`true`)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(manifest+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(firstPersisted, "1.0.0", "2.0.0", 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(updated)) }))
	defer server.Close()
	rec, err := lib.InstallEntry(ctx, subscription.Entry{Key: "first_source", URL: server.URL}, "")
	if err == nil || !strings.Contains(err.Error(), "previous source could not be restored") || rec != nil {
		t.Fatalf("record=%+v error=%v", rec, err)
	}
	if records := c.Records(); len(records) != 1 || records[0].Enabled {
		t.Fatal("unpersisted replacement remains enabled")
	}
	after, err := os.ReadFile(manifest)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed rollback changed persisted source")
	}
}

func TestUpdateWriteFailureRestoresPreviousSource(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "enabled"
		if !enabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			c, err := core.Open(core.Options{DataDir: root, AppVersion: "1.6.3"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			lib := New(c)
			if _, err = lib.InstallTypedText(ctx, plugin.TypeComic, firstPersisted, "old.js"); err != nil {
				t.Fatal(err)
			}
			if !enabled {
				if err = lib.SetEnabled(ctx, "first_source", false); err != nil {
					t.Fatal(err)
				}
			}
			if err = c.Store().SaveData("first_source", "account", json.RawMessage(`{"token":"fixture"}`)); err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse("https://source.invalid/")
			c.Jar().SetURL(core.ComicPartition("first_source"), u, []*http.Cookie{{Name: "fixture", Value: "preserved"}})
			before := c.Records()[0]
			manifest := filepath.Join(root, "plugins.json")
			diskBefore, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			// A directory at the task-owned temp-file path deterministically rejects
			// the next atomic write, without relying on chmod or platform privileges.
			blocked := manifest + ".tmp"
			if err = os.Mkdir(blocked, 0700); err != nil {
				t.Fatal(err)
			}
			updated := strings.ReplaceAll(strings.ReplaceAll(firstPersisted, "1.0.0", "2.0.0"), "maxPage: 1", "maxPage: 2")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(updated)) }))
			defer server.Close()
			entry := subscription.Entry{Key: "first_source", URL: server.URL + "/updated.js"}
			rec, err := lib.InstallEntry(ctx, entry, "https://index.invalid/index.json")
			if err == nil {
				t.Fatal("update ignored failed persistence")
			}
			if rec != nil {
				t.Errorf("failed update returned an installed new record: %+v", rec)
			}
			after := c.Records()[0]
			if after.Body != before.Body || after.SHA256 != before.SHA256 || after.Origin != before.Origin || after.Enabled != before.Enabled {
				t.Errorf("failed update left runtime on new record: version=%s origin=%s sha=%s", after.Version, after.Origin, after.SHA256)
			}
			if enabled {
				raw, callErr := c.Call(ctx, "first_source", "search.load", "query", []any{}, 1)
				if callErr != nil {
					t.Fatal(callErr)
				}
				var result struct {
					MaxPage int `json:"maxPage"`
				}
				if err = json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				if result.MaxPage != 1 {
					t.Errorf("failed update still executes new code: maxPage=%d", result.MaxPage)
				}
			}
			diskAfter, _ := os.ReadFile(manifest)
			if !bytes.Equal(diskBefore, diskAfter) {
				t.Error("failed update changed manifest")
			}
			account, ok, err := c.Store().LoadData("first_source", "account")
			if err != nil || !ok || string(account) != `{"token":"fixture"}` {
				t.Errorf("rollback lost account data: %s %v", account, err)
			}
			if got := c.Jar().Header(core.ComicPartition("first_source"), u.String()); got != "fixture=preserved" {
				t.Errorf("rollback lost cookies: %q", got)
			}
			if err = os.Remove(blocked); err != nil {
				t.Fatal(err)
			}
			if _, err = lib.InstallEntry(ctx, entry, "https://index.invalid/index.json"); err != nil {
				t.Fatalf("retry: %v", err)
			}
			var saved []plugin.Record
			body, _ := os.ReadFile(manifest)
			if err = json.Unmarshal(body, &saved); err != nil {
				t.Fatal(err)
			}
			if len(saved) != 1 || saved[0].Body != updated || saved[0].Enabled != enabled {
				t.Fatal("retry did not persist source and enabled state")
			}
		})
	}
}
