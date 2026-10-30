package mobile

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const uiSource = `
class U extends ComicSource {
    name = "U"; key = "u"; version = "1.0.0"
    cookies = async () => {
        Network.setCookies("https://u.test/", [{name: "sid", value: "abc"}, {name: "lang", value: "zh", domain: ".u.test", path: "/"}])
        const got = await Network.getCookies("https://u.test/")
        Network.deleteCookies("https://u.test/")
        const after = await Network.getCookies("https://u.test/")
        return {names: got.map(c => c.name).sort(), domain: got.find(c => c.name == "lang").domain, after: after.length}
    }
    dialog = async () => {
        let hit = "none"
        await UI.showDialog("T", "C", [{text: "No", callback: () => { hit = "no" }}, {text: "Yes", style: "filled", callback: () => { hit = "yes" }}])
        return hit
    }
    input = async () => UI.showInputDialog("Code", (t) => t.length < 3 ? "too short" : null)
    select = async () => UI.showSelectDialog("Pick", ["a", "b", "c"], 2)
    closed = async () => [await UI.showInputDialog("Code"), await UI.showSelectDialog("Pick", ["a"])]
    misc = async () => {
        UI.showMessage("hi")
        UI.launchUrl("https://u.test/login")
        await setClipboard("copied")
        const id = UI.showLoading(() => { globalThis.cancelled = true })
        UI.cancelLoading(id)
        return getClipboard()
    }
    loading = () => { globalThis.cancelled = false; return UI.showLoading(() => { globalThis.cancelled = true }) }
    wasCancelled = () => globalThis.cancelled
}
`

func callU(t *testing.T, c *Core, path string) string {
	t.Helper()
	out, err := c.CallComic(0, "u", path, "[]")
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

// waitFor polls until the asynchronous UI calls Core makes have reached
// the host.
func waitFor(t *testing.T, h *fakeHost, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range h.eventList() {
			if e == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("host never saw %q; events %q", want, h.eventList())
}

func TestHostCookiesUIAndClipboard(t *testing.T) {
	h := &fakeHost{data: map[string]string{}}
	c := newCore(t, h)
	if _, err := c.Install(0, "comic", uiSource, "file", "", ""); err != nil {
		t.Fatal(err)
	}

	// Cookies go to the source's own partition and come back as cookie maps.
	if got := callU(t, c, "cookies"); got != `{"names":["lang","sid"],"domain":".u.test","after":0}` {
		t.Errorf("cookies = %s", got)
	}
	waitFor(t, h, "saveCookies comic:u https://u.test/")
	waitFor(t, h, "deleteCookies comic:u")

	// The tapped button's callback runs in the script.
	h.answers = []string{`{"index":1}`}
	if got := callU(t, c, "dialog"); got != `"yes"` {
		t.Errorf("dialog = %s", got)
	}
	waitFor(t, h, `dialog T [{"text":"No","style":"text"},{"text":"Yes","style":"filled"}]`)

	// A rejected input is asked again with the validator's message.
	h.answers = []string{`{"text":"ab"}`, `{"text":"abcd"}`}
	if got := callU(t, c, "input"); got != `"abcd"` {
		t.Errorf("input = %s", got)
	}
	waitFor(t, h, "input Code []")
	waitFor(t, h, "input Code [too short]")

	h.answers = []string{`{"index":0}`}
	if got := callU(t, c, "select"); got != `0` {
		t.Errorf("select = %s", got)
	}
	waitFor(t, h, `select Pick ["a","b","c"] 2`)

	// "" means the user closed the UI: the script sees null.
	h.answers = nil
	if got := callU(t, c, "closed"); got != `[null,null]` {
		t.Errorf("closed = %s", got)
	}

	if got := callU(t, c, "misc"); got != `"copied"` {
		t.Errorf("clipboard = %s", got)
	}
	for _, want := range []string{"message hi", "launch https://u.test/login", "loading 1", "cancelLoading 1"} {
		waitFor(t, h, want)
	}

	// Dismissing a loading indicator runs onCancel once.
	id := callU(t, c, "loading")
	waitFor(t, h, "loading "+id)
	var n int64
	if err := json.Unmarshal([]byte(id), &n); err != nil {
		t.Fatal(err)
	}
	c.LoadingDismissed(n)
	c.LoadingDismissed(n)
	deadline := time.Now().Add(2 * time.Second)
	for callU(t, c, "wasCancelled") != "true" {
		if time.Now().After(deadline) {
			t.Fatal("onCancel did not run")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHostUIBadAnswersAreErrors(t *testing.T) {
	h := &fakeHost{data: map[string]string{}}
	c := newCore(t, h)
	if _, err := c.Install(0, "comic", uiSource, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{`{"index":7}`, `{"index":-1}`, `not json`} {
		h.answers = []string{a}
		if _, err := c.CallComic(0, "u", "select", "[]"); err == nil {
			t.Errorf("answer %s: want an error", a)
		} else if !strings.Contains(err.Error(), "UI result") {
			t.Errorf("answer %s: %v", a, err)
		}
	}
}
