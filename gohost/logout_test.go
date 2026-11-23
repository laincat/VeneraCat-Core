package gohost

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Logging out is the source's own function: ehentai deletes its cookies and
// nhentai its stored token. A client that only cleared a flag would leave the
// session alive, so this checks that calling account.logout actually empties what
// the source asked to delete.
func TestLogoutClearsWhatTheSourceDeletes(t *testing.T) {
	dir := os.Getenv("NK_OFFICIAL_SRC")
	if dir == "" {
		t.Skip("set NK_OFFICIAL_SRC to the official source directory to run this")
	}

	t.Run("ehentai deletes its cookies", func(t *testing.T) {
		body, err := os.ReadFile(filepath.Join(dir, "ehentai.js"))
		if err != nil {
			t.Skipf("no ehentai.js: %v", err)
		}
		c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.3", Locale: "zh_CN"})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx := context.Background()
		rec, err := c.Install(ctx, string(body), "file", "")
		if err != nil {
			t.Fatalf("install: %v", err)
		}
		part := ComicPartition(rec.Key)
		c.jar.Set(part, "https://e-hentai.org/", []*http.Cookie{
			{Name: "ipb_member_id", Value: "42", Domain: ".e-hentai.org"},
		})
		if got := c.jar.Get(part, "https://e-hentai.org/"); len(got) != 1 {
			t.Fatalf("setup: jar has %d cookie(s)", len(got))
		}
		if err := c.LogoutAccount(ctx, rec.Key); err != nil {
			t.Fatalf("logout: %v", err)
		}
		if got := c.jar.Get(part, "https://e-hentai.org/"); len(got) != 0 {
			t.Fatalf("after logout the jar still has %d cookie(s)", len(got))
		}
	})

	t.Run("nhentai deletes its token", func(t *testing.T) {
		body, err := os.ReadFile(filepath.Join(dir, "nhentai.js"))
		if err != nil {
			t.Skipf("no nhentai.js: %v", err)
		}
		c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.3", Locale: "zh_CN"})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx := context.Background()
		rec, err := c.Install(ctx, string(body), "file", "")
		if err != nil {
			t.Fatalf("install: %v", err)
		}
		if err := c.Store().SaveData(rec.Key, "accessToken", json.RawMessage(`"token"`)); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := c.LogoutAccount(ctx, rec.Key); err != nil {
			t.Fatalf("logout: %v", err)
		}
		raw, ok, err := c.Store().LoadData(rec.Key, "accessToken")
		if err != nil {
			t.Fatal(err)
		}
		if ok && len(raw) > 0 && string(raw) != "null" {
			t.Fatalf("after logout the token is still stored: %s", raw)
		}
	})
}

// And the client must do only what the source asked: Komiic's logout deletes its
// token and nothing else, so a cookie left in the partition stays. A client that
// cleared everything would look right here and be wrong for this source.
func TestLogoutDoesNotDeleteMoreThanTheSourceAsked(t *testing.T) {
	dir := os.Getenv("NK_OFFICIAL_SRC")
	if dir == "" {
		t.Skip("set NK_OFFICIAL_SRC to the official source directory to run this")
	}
	body, err := os.ReadFile(filepath.Join(dir, "komiic.js"))
	if err != nil {
		t.Skipf("no komiic.js: %v", err)
	}
	c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.3", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	rec, err := c.Install(ctx, string(body), "file", "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := c.Store().SaveData(rec.Key, "token", json.RawMessage(`"token"`)); err != nil {
		t.Fatalf("setup data: %v", err)
	}
	part := ComicPartition(rec.Key)
	c.jar.Set(part, "https://komiic.com/", []*http.Cookie{
		{Name: "session", Value: "kept", Domain: "komiic.com"},
	})
	if err := c.LogoutAccount(ctx, rec.Key); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if got := c.jar.Get(part, "https://komiic.com/"); len(got) != 1 || got[0].Value != "kept" {
		t.Fatalf("the client deleted a cookie this source did not ask to delete: %v", got)
	}
}
