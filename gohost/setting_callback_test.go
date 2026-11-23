package gohost

import (
	"context"
	"strings"
	"testing"
)

// Nine official sources declare a callback setting — jm's domain refresh, wnacg's
// domain refresh, a cache wipe — and a callback is a function, so nothing can be
// carried across the boundary by value. The source runs it instead, which is what
// this reaches.
func TestSettingCallbackRunsInTheSource(t *testing.T) {
	c := openTest(t)
	ctx := context.Background()
	const callbackComic = `class Cb extends ComicSource {
  name = 'Cb'; key = 'cb'; version = '1'
  settings = {
    refresh: {title: 'refresh', type: 'callback', callback: () => 'refreshed'},
    fails: {title: 'fails', type: 'callback', callback: () => { throw 'no network' }},
    counts: {title: 'counts', type: 'callback', callback: async () => {
      let n = Number(this.loadData('n') || '0') + 1
      this.saveData('n', String(n))
      return 'run ' + n
    }},
    void: {title: 'void', type: 'callback', callback: () => { this.saveData('v', 'yes') }},
  }
  search = {load: async () => ({comics: [], maxPage: 1})}
}`
	if _, err := c.Install(ctx, callbackComic, "file_cb", ""); err != nil {
		t.Fatal(err)
	}

	got, err := c.CallSettingCallback(ctx, "cb", "refresh")
	if err != nil || got != "refreshed" {
		t.Fatalf("callback = %q, %v", got, err)
	}
	// A source that refuses answers in its own words.
	if _, err := c.CallSettingCallback(ctx, "cb", "fails"); err == nil {
		t.Error("a throwing callback must be an error")
	} else if !strings.Contains(err.Error(), "no network") {
		t.Errorf("error = %v, want the source's own text", err)
	}
	// Each press runs again rather than answering from a first result.
	if got, _ := c.CallSettingCallback(ctx, "cb", "counts"); got != "run 1" {
		t.Errorf("first run = %q", got)
	}
	if got, _ := c.CallSettingCallback(ctx, "cb", "counts"); got != "run 2" {
		t.Errorf("second run = %q", got)
	}
	// A setting without a callback is reported, not silently ignored.
	if _, err := c.CallSettingCallback(ctx, "cb", "missing"); err == nil {
		t.Error("a missing callback must be an error")
	}
	// A callback with no return value has nothing to say. The JSON literal "null"
	// is the transport's word for that, not the source's, and the panel would
	// print it as if it were.
	if got, err := c.CallSettingCallback(ctx, "cb", "void"); err != nil || got != "" {
		t.Errorf("void callback = %q, %v; want no message", got, err)
	}
}
