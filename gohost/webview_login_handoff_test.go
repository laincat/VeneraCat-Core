package gohost

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// ehentai's onLoginSuccess copies the forum's cookies onto exhentai, and it reads
// them back through Network.getCookies. That only works if the client has already
// put the browser's cookies into the source's jar before running the callback —
// which is the order the login window uses and the order this test pins.
func TestWebviewLoginHandoffCopiesSourceCookies(t *testing.T) {
	dir := os.Getenv("NK_OFFICIAL_SRC")
	if dir == "" {
		t.Skip("set NK_OFFICIAL_SRC to the official source directory to run this")
	}
	body, err := os.ReadFile(filepath.Join(dir, "ehentai.js"))
	if err != nil {
		t.Skipf("no ehentai.js under %s: %v", dir, err)
	}
	tmp := t.TempDir()
	c, err := Open(Options{DataDir: tmp, AppVersion: "1.6.3", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	rec, err := c.Install(ctx, string(body), "file", "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	decl, err := c.AccountWebviewLogin(ctx, rec.Key)
	if err != nil {
		t.Fatalf("declaration: %v", err)
	}
	if decl.URL == "" || decl.Handle == "" {
		t.Skip("this source declares no browser login")
	}

	// What the browser would have earned on the forum login page.
	c.jar.Set(ComicPartition(rec.Key), "https://forums.e-hentai.org/index.php?act=Login", []*http.Cookie{
		{Name: "ipb_member_id", Value: "42", Domain: "forums.e-hentai.org"},
		{Name: "ipb_pass_hash", Value: "hash", Domain: "forums.e-hentai.org"},
	})

	if err := c.WebviewLoginDone(ctx, rec.Key, decl.Handle); err != nil {
		t.Fatalf("onLoginSuccess: %v", err)
	}
	got := c.jar.Get(ComicPartition(rec.Key), "https://exhentai.org/")
	names := map[string]string{}
	for _, one := range got {
		names[one.Name] = one.Value
	}
	if names["ipb_member_id"] != "42" || names["ipb_pass_hash"] != "hash" {
		t.Fatalf("exhentai has %v; the source's own callback did not copy the forum cookies", names)
	}
}

// And the reverse: running the callback before the client stored anything leaves
// exhentai without a login, which is why the order in the login window matters.
func TestWebviewLoginHandoffNeedsTheCookiesFirst(t *testing.T) {
	dir := os.Getenv("NK_OFFICIAL_SRC")
	if dir == "" {
		t.Skip("set NK_OFFICIAL_SRC to the official source directory to run this")
	}
	body, err := os.ReadFile(filepath.Join(dir, "ehentai.js"))
	if err != nil {
		t.Skipf("no ehentai.js under %s: %v", dir, err)
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
	decl, err := c.AccountWebviewLogin(ctx, rec.Key)
	if err != nil || decl.Handle == "" {
		t.Skip("this source declares no browser login")
	}
	if err := c.WebviewLoginDone(ctx, rec.Key, decl.Handle); err != nil {
		t.Fatalf("onLoginSuccess: %v", err)
	}
	if got := c.jar.Get(ComicPartition(rec.Key), "https://exhentai.org/"); len(got) != 0 {
		t.Fatalf("exhentai has %d cookie(s) without anything stored first", len(got))
	}
}
