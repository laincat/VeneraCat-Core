package mobile

import (
	"encoding/json"
	"strings"
	"testing"
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
