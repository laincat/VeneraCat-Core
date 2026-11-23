package gohost

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Does a source that pages with cursors reach its second page?
//
// The desktop client asks search.loadNext for a source that declares only that
// entry, handing back the token the previous page returned. A source like that
// can answer its first page and still fail on the second if the token is
// dropped or the wrong shape is passed, so the check reads two pages and
// expects different comics.
func TestLiveCursorSearchPages(t *testing.T) {
	script := os.Getenv("NK_LIVE_JS")
	query := os.Getenv("NK_LIVE_QUERY")
	if script == "" || query == "" {
		t.Skip("set NK_LIVE_JS and NK_LIVE_QUERY to run against a real source")
	}
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "1.6.3", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	rec, err := c.Install(ctx, string(body), script, "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Has(ctx, rec.Key, "search.loadNext") || c.Has(ctx, rec.Key, "search.load") {
		t.Skipf("%s does not page by cursor alone", rec.Key)
	}
	options, err := c.DefaultOptions(ctx, rec.Key)
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.Search(ctx, rec.Key, query, options, 1)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first.Comics) == 0 || first.Next == "" {
		t.Skipf("page 1 has %d comics and cursor %q", len(first.Comics), first.Next)
	}
	second, err := c.Search(ctx, rec.Key, query, options, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	seen := make(map[string]bool, len(first.Comics))
	for _, comic := range first.Comics {
		seen[comic.ID] = true
	}
	shared := 0
	for _, comic := range second.Comics {
		if seen[comic.ID] {
			shared++
		}
	}
	if len(second.Comics) == 0 {
		t.Fatalf("page 2 is empty; cursor was %q", first.Next)
	}
	if shared == len(second.Comics) {
		t.Fatalf("page 2 repeats page 1 (%d comics); the cursor was not advanced", shared)
	}
	t.Logf("%s: page 1 %d comics, page 2 %d comics, %d shared", rec.Key, len(first.Comics), len(second.Comics), shared)
}
