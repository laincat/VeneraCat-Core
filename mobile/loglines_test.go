package mobile

import (
	"encoding/json"
	"strings"
	"testing"
)

// A phone's console is not somewhere a user can look, so the client needs the
// lines themselves — with the source they came from.
func TestLogLinesCarryTheSource(t *testing.T) {
	h := &fakeHost{data: map[string]string{}, routes: map[string]string{}}
	c := newCore(t, h)
	if _, err := c.Install(0, "comic", `class Talk extends ComicSource {
  name = 'Talk'; key = 'talk'; version = '1'
  search = {load: async () => { console.log('hello from talk'); return {comics: [], maxPage: 1} }}
}`, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CallComic(0, "talk", "search.load", `["x",[],1]`); err != nil {
		t.Fatal(err)
	}
	raw, err := c.LogLines()
	if err != nil {
		t.Fatal(err)
	}
	var lines []LogEntry
	if err := json.Unmarshal([]byte(raw), &lines); err != nil {
		t.Fatalf("log lines are not JSON: %v (%s)", err, raw)
	}
	var found *LogEntry
	for i := range lines {
		if lines[i].Key == "talk" && strings.Contains(lines[i].Content, "hello from talk") {
			found = &lines[i]
		}
	}
	if found == nil {
		t.Fatalf("the line did not arrive with its source: %s", raw)
	}
	if found.Time == 0 {
		t.Error("the line has no timestamp")
	}
	c.ClearLogLines()
	if raw, _ := c.LogLines(); raw != "[]" {
		t.Errorf("after clearing: %s", raw)
	}
}

// The buffer is bounded, and what survives is the newest.
func TestLogLinesAreBounded(t *testing.T) {
	h := &fakeHost{data: map[string]string{}, routes: map[string]string{}}
	c := newCore(t, h)
	if _, err := c.Install(0, "comic", `class Loud extends ComicSource {
  name = 'Loud'; key = 'loud'; version = '1'
  search = {load: async () => { for (let i = 0; i < 500; i++) console.log('line ' + i); return {comics: [], maxPage: 1} }}
}`, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CallComic(0, "loud", "search.load", `["x",[],1]`); err != nil {
		t.Fatal(err)
	}
	raw, _ := c.LogLines()
	var lines []LogEntry
	if err := json.Unmarshal([]byte(raw), &lines); err != nil {
		t.Fatal(err)
	}
	if len(lines) != maxLogEntries {
		t.Fatalf("kept %d lines, want %d", len(lines), maxLogEntries)
	}
	if last := lines[len(lines)-1].Content; !strings.Contains(last, "line 499") {
		t.Errorf("the newest line was dropped: %q", last)
	}
}

// A source that fails without printing anything must still leave a line: that is
// the case where a user opens the panel, and on a site returning 403 the source
// itself has nothing to say.
func TestFailedCallsLeaveALine(t *testing.T) {
	h := &fakeHost{data: map[string]string{}, routes: map[string]string{}}
	c := newCore(t, h)
	if _, err := c.Install(0, "comic", `class Broken extends ComicSource {
  name = 'Broken'; key = 'broken'; version = '1'
  search = {load: async () => { throw new Error('the site answered with a login page') }}
}`, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CallComic(0, "broken", "search.load", `["x",[],1]`); err == nil {
		t.Fatal("the call was supposed to fail")
	}
	raw, err := c.LogLines()
	if err != nil {
		t.Fatal(err)
	}
	var lines []LogEntry
	if err := json.Unmarshal([]byte(raw), &lines); err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 {
		t.Fatal("a failed call left no line behind")
	}
	last := lines[len(lines)-1]
	if last.Key != "broken" || !strings.Contains(last.Content, "search.load") {
		t.Errorf("the line does not name the source and the call: %+v", last)
	}
	if !strings.Contains(last.Content, "login page") {
		t.Errorf("the line lost the cause: %q", last.Content)
	}
}
