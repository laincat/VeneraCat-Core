package backup

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// The plan is a wire format: both clients decode it with their own model
// types. Renaming or dropping a field here breaks a restore silently, so the
// key set is pinned by a test rather than by comments.
func TestPlanJSONContract(t *testing.T) {
	c, err := open(t, officialBackup(t)).Read(context.Background(), Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Plan(DefaultSelection())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"history", "folders", "imageFavorites", "sources", "pending"} {
		if _, ok := top[key]; !ok {
			t.Fatalf("plan lost %q; keys are %v", key, sortedKeys(top))
		}
	}

	first := func(name string) map[string]json.RawMessage {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(top[name], &rows); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(rows) == 0 {
			t.Fatalf("%s is empty; the contract test needs a row", name)
		}
		return rows[0]
	}

	history := first("history")
	for _, key := range []string{"sourceKey", "id", "type", "title", "time", "ep", "page"} {
		if _, ok := history[key]; !ok {
			t.Fatalf("history row lost %q; keys are %v", key, sortedKeys(history))
		}
	}
	var timeMillis int64
	if err := json.Unmarshal(history["time"], &timeMillis); err != nil {
		t.Fatalf("history.time is not an integer: %s", history["time"])
	}

	folder := first("folders")
	for _, key := range []string{"name", "items"} {
		if _, ok := folder[key]; !ok {
			t.Fatalf("folder lost %q; keys are %v", key, sortedKeys(folder))
		}
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(folder["items"], &items); err != nil || len(items) == 0 {
		t.Fatalf("folder items: %v", err)
	}
	for _, key := range []string{"sourceKey", "id", "name"} {
		if _, ok := items[0][key]; !ok {
			t.Fatalf("favourite lost %q; keys are %v", key, sortedKeys(items[0]))
		}
	}

	picture := first("imageFavorites")
	for _, key := range []string{"id", "sourceKey", "title", "episodes", "other"} {
		if _, ok := picture[key]; !ok {
			t.Fatalf("kept picture lost %q; keys are %v", key, sortedKeys(picture))
		}
	}
	// A kept picture has no url or page of its own; pages live inside episodes.
	for _, key := range []string{"url", "page"} {
		if _, ok := picture[key]; ok {
			t.Fatalf("kept picture gained %q; it is not part of the format", key)
		}
	}
	// episodes and other are the official app's own JSON and are passed through
	// untouched: the official client nests pages one level down, as
	// episodes[].imageFavorites[].{page,imageKey,eid,ep}. This package must not
	// rewrite that, so only the outer type is pinned here.
	var episodes []json.RawMessage
	if err := json.Unmarshal(picture["episodes"], &episodes); err != nil || len(episodes) == 0 {
		t.Fatalf("episodes is not a non-empty array: %v (%s)", err, picture["episodes"])
	}

	source := first("sources")
	if _, ok := source["key"]; !ok {
		t.Fatalf("source lost \"key\"; keys are %v", sortedKeys(source))
	}
	var pending map[string]json.RawMessage
	if err := json.Unmarshal(top["pending"], &pending); err != nil {
		t.Fatalf("pending is not an object: %s", top["pending"])
	}
}

func sortedKeys(m map[string]json.RawMessage) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
