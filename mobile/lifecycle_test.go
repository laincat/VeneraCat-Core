package mobile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/host"
)

// These are the calls the app makes after a source is installed: update it, turn
// it off, delete it. None of them had a test, and a JSON shape that drifts here
// breaks the app with no error anywhere near the cause.
func TestPluginLifecycle(t *testing.T) {
	h := &fakeHost{data: map[string]string{}, routes: map[string]string{}}
	c := newCore(t, h)

	if _, err := c.Install(0, "comic", comic, "file", "", ""); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Update in place: same key, new version, and the body is the new one.
	updated := strings.Replace(comic, `version = "1.0.0"`, `version = "2.0.0"`, 1)
	rec, err := c.Replace(0, "mini", "comic", updated, "file", "", "")
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	var record pluginRecord
	if err := json.Unmarshal([]byte(rec), &record); err != nil {
		t.Fatalf("replace returned %q: %v", rec, err)
	}
	if record.Key != "mini" || record.Version != "2.0.0" {
		t.Fatalf("record = %+v", record)
	}

	// Turning a source off must be visible in the list the app renders.
	if err := c.SetEnabled("mini", false); err != nil {
		t.Fatalf("setEnabled: %v", err)
	}
	list, err := c.Plugins()
	if err != nil {
		t.Fatalf("plugins: %v", err)
	}
	if !strings.Contains(list, `"key":"mini"`) || !strings.Contains(list, `"enabled":false`) {
		t.Fatalf("plugins = %s", list)
	}

	// Removing it must leave nothing behind, including its saved data.
	if err := c.Remove("mini"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if after, err := c.Plugins(); err != nil || strings.Contains(after, `"key":"mini"`) {
		t.Fatalf("after remove, plugins = %s", list)
	}
}

// Updating a source that is not installed is an error, not a silent install: the
// app calls replace only when it believes the source is there, so a missing one
// means its state is stale.
func TestReplaceUnknownPluginFails(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}})
	if _, err := c.Replace(0, "ghost", "comic", comic, "file", "", ""); err == nil {
		t.Fatal("replacing an unknown key succeeded")
	}
}

// The list handed to the app never carries script bodies: they are large, and a
// record list is for showing names and versions.
func TestPluginsOmitsBodies(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}})
	if _, err := c.Install(0, "comic", comic, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	if list, err := c.Plugins(); err != nil || strings.Contains(list, "class Mini") {
		t.Fatalf("the plugin list carried a script body: %s", list)
	}
}

// pluginRecord is the subset of a record these tests care about.
type pluginRecord struct {
	Key     string `json:"key"`
	Version string `json:"version"`
	Enabled bool   `json:"enabled"`
}

// A client that runs the challenge in its own browser needs the whole rule.
// Sending only the markers would let it pass on a blank page, and dropping the
// query rule would make an edge challenge wait for a cookie that never comes.
func TestChallengeWireCarriesTheWholeRule(t *testing.T) {
	h := &verifyHost{}
	b := &hostBridge{h: h}
	c := &host.Challenge{
		Kind: host.ChallengeEdge, URL: "https://api.example.test/manga", Partition: "p",
		Done: host.DoneRule{AbsentMarkers: []string{"challenge-assets"}, SameHost: true,
			RequiredQueryParams: []string{"powblock", "pbchal"}},
	}
	clear, err := b.Verify(context.Background(), c)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if clear.UserAgent != "WebView/9" || clear.Query["powblock"] != "n" || clear.Query["pbchal"] != "t" {
		t.Fatalf("clearance = %+v", clear)
	}
	payload := h.lastVerify
	for _, want := range []string{`"sameHost":true`, `"requiredQueryParams":["powblock","pbchal"]`, `"absentMarkers":["challenge-assets"]`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("challenge JSON lacks %s: %s", want, payload)
		}
	}
}

// verifyHost answers the bridge's Verify with a clearance carrying parameters,
// which is what an edge challenge hands back.
type verifyHost struct {
	fakeHost
	lastVerify string
}

func (h *verifyHost) Verify(in string) (string, error) {
	h.lastVerify = in
	return `{"userAgent":"WebView/9","query":{"powblock":"n","pbchal":"t"}}`, nil
}
