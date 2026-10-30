package mobile

import (
	"encoding/json"
	"testing"
)

// A source's browser login has to cross the language boundary as data: the page
// and a handle for the two callbacks. The callbacks cannot be JSON, so the handle
// names them inside the source's runtime.
const webviewComic = `
class Login extends ComicSource {
    name = "Login"; key = "login"; version = "1.0.0"
    init() { this.saveData("done", false) }
    account = {
        loginWithWebview: {
            url: "https://site.test/login",
            checkStatus: (url, title) => title === "已登录",
            onLoginSuccess: async () => { this.saveData("done", true) },
        },
    }
    search = { load: async () => ({comics: [], maxPage: 1}) }
}
`

func TestAccountWebviewLoginJSON(t *testing.T) {
	h := &fakeHost{routes: map[string]string{}, data: map[string]string{}}
	c := newCore(t, h)
	if _, err := c.Install(0, "comic", webviewComic, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	raw, err := c.AccountWebviewLogin("login")
	if err != nil {
		t.Fatalf("declaration: %v", err)
	}
	var decl struct {
		URL     string   `json:"url"`
		Handle  string   `json:"handle"`
		Callbks []string `json:"callbacks"`
	}
	if err := json.Unmarshal([]byte(raw), &decl); err != nil {
		t.Fatalf("declaration JSON %q: %v", raw, err)
	}
	if decl.URL != "https://site.test/login" || decl.Handle == "" {
		t.Fatalf("declaration = %+v", decl)
	}
	if len(decl.Callbks) != 2 {
		t.Errorf("callbacks = %v, want checkStatus and onLoginSuccess", decl.Callbks)
	}

	// The source's own checkStatus decides when the page means "logged in".
	status := func(title string) bool {
		t.Helper()
		raw, err := c.WebviewStatus(c.NewCall(), "login", decl.Handle, "https://site.test/home", title)
		if err != nil {
			t.Fatalf("status %q: %v", title, err)
		}
		var out struct {
			Done bool `json:"done"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("status JSON %q: %v", raw, err)
		}
		return out.Done
	}
	if status("登录") {
		t.Error("the login page was reported as logged in")
	}
	if !status("已登录") {
		t.Error("the logged-in page was not reported as logged in")
	}

	// onLoginSuccess runs after the client stored the cookies.
	if err := c.WebviewLoginDone(c.NewCall(), "login", decl.Handle); err != nil {
		t.Fatalf("done: %v", err)
	}
	if got := h.data["login/done"]; got != "true" {
		t.Fatalf("onLoginSuccess did not run: %q", got)
	}
}

// A source that logs in another way declares no browser login, which is how a
// client tells the three styles apart.
func TestAccountWebviewLoginAbsent(t *testing.T) {
	c := newCore(t, &fakeHost{routes: map[string]string{}, data: map[string]string{}})
	if _, err := c.Install(0, "comic", comic, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	raw, err := c.AccountWebviewLogin("mini")
	if err != nil {
		t.Fatalf("declaration: %v", err)
	}
	var decl map[string]any
	if err := json.Unmarshal([]byte(raw), &decl); err != nil {
		t.Fatalf("declaration JSON %q: %v", raw, err)
	}
	if len(decl) != 0 {
		t.Errorf("a password source declared a browser login: %v", decl)
	}
}

// A malformed handle is refused rather than evaluated.
func TestWebviewStatusRejectsBadHandle(t *testing.T) {
	c := newCore(t, &fakeHost{routes: map[string]string{}, data: map[string]string{}})
	if _, err := c.Install(0, "comic", webviewComic, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WebviewStatus(c.NewCall(), "login", "not a handle", "u", "t"); err == nil {
		t.Error("a malformed handle was accepted")
	}
	if err := c.WebviewLoginDone(c.NewCall(), "login", "w999"); err == nil {
		t.Log("an unknown handle answered without running a callback")
	}
}

// A callback setting is a function in the source: the client names the setting
// and reports what came back, which is the only thing it can do with it.
const callbackComic = `
class Cb extends ComicSource {
    name = "Cb"; key = "cb"; version = "1.0.0"
    settings = {
        refresh: {
            title: "Refresh Domain List",
            type: "callback",
            buttonText: "Refresh",
            callback: () => "domains refreshed",
        },
    }
    search = { load: async () => ({comics: [], maxPage: 1}) }
}
`

// A callback written without a return value, which is what a source that only
// refreshes a domain list does.
const voidCallbackComic = `
class Cb extends ComicSource {
    name = "Cb2"; key = "cb2"; version = "1.0.0"
    settings = {
        refresh: {
            title: "Refresh Domain List",
            type: "callback",
            buttonText: "Refresh",
            callback: () => { this.saveData("refreshed", true) },
        },
    }
    search = { load: async () => ({comics: [], maxPage: 1}) }
}
`

func TestCallSettingCallback(t *testing.T) {
	c := newCore(t, &fakeHost{routes: map[string]string{}, data: map[string]string{}})
	if _, err := c.Install(0, "comic", callbackComic, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	// The button's own text travels with the setting.
	raw, err := c.Settings("cb")
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	var list []struct {
		Key        string `json:"key"`
		Type       string `json:"type"`
		ButtonText string `json:"buttonText"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("settings JSON %q: %v", raw, err)
	}
	if len(list) != 1 || list[0].Type != "callback" || list[0].ButtonText != "Refresh" {
		t.Fatalf("settings = %+v", list)
	}

	answer, err := c.CallSettingCallback(c.NewCall(), "cb", "refresh")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if answer != "domains refreshed" {
		t.Errorf("answer = %q", answer)
	}

	// Naming a setting with no callback is an error, not a silent success.
	if _, err := c.CallSettingCallback(c.NewCall(), "cb", "missing"); err == nil {
		t.Error("a setting without a callback answered")
	}

	// A callback with no return value answers with nothing to show. The literal
	// "null" is a JSON artifact, and a client that prints it tells the user the
	// source said "null".
	if _, err := c.Install(0, "comic", voidCallbackComic, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	void, err := c.CallSettingCallback(c.NewCall(), "cb2", "refresh")
	if err != nil {
		t.Fatalf("void callback: %v", err)
	}
	if void != "" {
		t.Errorf("a callback with no return value answered %q", void)
	}
}
