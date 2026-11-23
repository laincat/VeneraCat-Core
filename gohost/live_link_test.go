package gohost

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A real source's own link has to resolve to the id that source would use.
//
//	NK_LIVE_JS=/path/to/nhentai.js NK_LIVE_LINK=https://nhentai.net/g/… go test -run TestLiveLink -v ./internal/core
func TestLiveLink(t *testing.T) {
	script, link := os.Getenv("NK_LIVE_JS"), os.Getenv("NK_LIVE_LINK")
	if script == "" || link == "" {
		t.Skip("set NK_LIVE_JS and NK_LIVE_LINK")
	}
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "d"), AppVersion: "1.6.3", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	if _, err := c.Install(ctx, string(body), "file", ""); err != nil {
		t.Fatal(err)
	}
	for _, record := range c.Plugins() {
		t.Logf("%s claims %v", record.Key, c.LinkDomains(ctx, record.Key))
	}
	key, id, err := c.OpenLink(ctx, link)
	t.Logf("open %s -> source=%q id=%q err=%v", link, key, id, err)
	if key == "" || id == "" {
		t.Fatalf("the link was not recognised: %v", err)
	}
}
