package library

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	core "github.com/laincat/veneracat-core/gohost"
	"github.com/laincat/veneracat-core/plugin"
)

func fixtureSource(key, version string) string {
	return fmt.Sprintf("class Source extends ComicSource { name='fixture'; key=%q; version=%q; }", key, version)
}

func TestExpectedIdentityDoesNotTouchAnotherSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c, err := core.Open(core.Options{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lib := New(c)
	body := fixtureSource("other", "1")
	if _, err := lib.InstallText(ctx, body, "original"); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.InstallText(ctx, fixtureSource("expected", "1"), "expected-original"); err != nil {
		t.Fatal(err)
	}
	if err := c.Store().SaveData("other", "account", []byte("123")); err != nil {
		t.Fatal(err)
	}
	if err := c.Jar().Import(core.ComicPartition("other"), []*http.Cookie{{Name: "sid", Value: "keep", Domain: "example.test"}}); err != nil {
		t.Fatal(err)
	}
	bad := "class Wrong extends ComicSource { name='wrong'; key='other'; version='2'; constructor(){super();this.saveData('account', 999)} init(){this.saveData('account', 999)} }"
	for _, enabled := range []bool{true, false} {
		for _, key := range []string{"other", "expected"} {
			if err := lib.SetEnabled(ctx, key, enabled); err != nil {
				t.Fatal(err)
			}
		}
		before, err := os.ReadFile(filepath.Join(root, "plugins.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, expected := range []string{"expected", "missing"} {
			if _, err := lib.InstallExpecting(ctx, expected, plugin.TypeComic, bad, "collection", ""); !errors.Is(err, plugin.ErrKey) {
				t.Fatalf("%s: wanted identity failure, got %v", expected, err)
			}
		}
		after, err := os.ReadFile(filepath.Join(root, "plugins.json"))
		if err != nil || string(after) != string(before) {
			t.Fatalf("records changed: %v", err)
		}
		rec, ok := c.Record("other")
		if !ok || rec.Body != body || rec.Enabled != enabled {
			t.Fatalf("original source changed: %+v", rec)
		}
		data, _, err := c.Store().LoadData("other", "account")
		if err != nil || string(data) != "123" {
			t.Fatalf("original source data changed: %s %v", data, err)
		}
		if got := c.Jar().Header(core.ComicPartition("other"), "https://example.test"); got != "sid=keep" {
			t.Fatalf("original source cookies changed: %s", got)
		}
	}
}

func TestConcurrentInstallsAndSourceWritesSurviveReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c, err := core.Open(core.Options{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lib := New(c)
	var workers sync.WaitGroup
	for index := range 8 {
		workers.Go(func() {
			key := fmt.Sprintf("fixture_%d", index)
			if _, err := lib.InstallExpecting(ctx, key, plugin.TypeComic, fixtureSource(key, "1"), "collection", ""); err != nil {
				t.Error(err)
				return
			}
			if err := c.Store().SaveData("shared", "data_"+key, []byte("9007199254740993")); err != nil {
				t.Error(err)
			}
			_, _, _ = c.Store().LoadData("shared", "data_"+key)
			_ = lib.Sources()
		})
	}
	workers.Wait()
	reopened, err := core.Open(core.Options{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	other := New(reopened)
	failures, err := other.Restore(ctx)
	if err != nil || len(failures) > 0 || len(other.Sources()) != 8 {
		t.Fatalf("restored %d sources: %v %v", len(other.Sources()), failures, err)
	}
	values, err := reopened.Store().AllData("shared")
	if err != nil || len(values) != 8 {
		t.Fatalf("restored %d values: %v", len(values), err)
	}
}
