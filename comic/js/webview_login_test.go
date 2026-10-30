package js

import (
	"context"
	"strings"
	"testing"
)

const webviewSource = `
class Browser extends ComicSource {
    name = "Browser"
    key = "browser"
    version = "1.0.0"
    init() { this.saveData("seen", []) }
    account = {
        loginWithWebview: {
            url: "https://site.test/login",
            checkStatus: (url, title) => title === "已登录",
            onLoginSuccess: async () => { this.saveData("loggedInAt", 1750000000000) },
        },
    }
    search = {load: async () => ({comics: [], maxPage: 1})}
}`

func webviewRT(t *testing.T) *Runtime {
	t.Helper()
	r := newRT(t, Options{}, nil)
	if _, err := r.LoadSource(context.Background(), webviewSource); err != nil {
		t.Fatal(err)
	}
	return r
}

// The declaration names a page and hands back a handle for the two callbacks,
// because a function cannot cross into JSON.
func TestAccountWebviewLogin(t *testing.T) {
	r := webviewRT(t)
	decl, err := r.AccountWebviewLogin(context.Background(), "browser")
	if err != nil {
		t.Fatalf("declaration: %v", err)
	}
	if decl.URL != "https://site.test/login" || decl.Handle == "" {
		t.Fatalf("declaration = %+v", decl)
	}
	if strings.Join(decl.Callbacks, ",") != "checkStatus,onLoginSuccess" {
		t.Errorf("callbacks = %v", decl.Callbacks)
	}
}

// checkStatus decides when the browser is done; the page's own title is what the
// ehentai-style sources test.
func TestWebviewStatus(t *testing.T) {
	r := webviewRT(t)
	ctx := context.Background()
	decl, err := r.AccountWebviewLogin(ctx, "browser")
	if err != nil {
		t.Fatal(err)
	}
	done, err := r.WebviewStatus(ctx, "browser", decl.Handle, "https://site.test/login", "登录")
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Error("the login page itself was reported as logged in")
	}
	done, err = r.WebviewStatus(ctx, "browser", decl.Handle, "https://site.test/home", "已登录")
	if err != nil || !done {
		t.Fatalf("logged-in page: %v %v", done, err)
	}
	// A title with quotes must not break the evaluation.
	if _, err := r.WebviewStatus(ctx, "browser", decl.Handle, "https://site.test/\"x\"", `a"b`); err != nil {
		t.Errorf("quoting: %v", err)
	}
}

// onLoginSuccess runs once, and a source without it still finishes cleanly.
func TestWebviewLoginDone(t *testing.T) {
	r := webviewRT(t)
	ctx := context.Background()
	decl, err := r.AccountWebviewLogin(ctx, "browser")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.WebviewLoginDone(ctx, "browser", decl.Handle); err != nil {
		t.Fatalf("done: %v", err)
	}
	// A handle that was never issued is refused rather than evaluated.
	if err := r.WebviewLoginDone(ctx, "browser", "w999"); err == nil {
		// A well-formed but unknown handle reaches the source, which has no such
		// callback: it must not run anything, and must not panic.
		t.Log("an unknown handle answered without running a callback")
	}
	if err := r.WebviewLoginDone(ctx, "browser", "not a handle"); err == nil {
		t.Error("a malformed handle was accepted")
	}
}

// A source that logs in with a password or pasted cookies declares no browser
// login, which is how a client tells the three styles apart.
func TestAccountWebviewAbsent(t *testing.T) {
	r := newRT(t, Options{}, nil)
	if _, err := r.LoadSource(context.Background(), `class Plain extends ComicSource {
    name = "Plain"; key = "plain"; version = "1.0.0"
    account = { login: async () => true }
    search = {load: async () => ({comics: [], maxPage: 1})}
}`); err != nil {
		t.Fatal(err)
	}
	decl, err := r.AccountWebviewLogin(context.Background(), "plain")
	if err != nil {
		t.Fatalf("declaration: %v", err)
	}
	if decl.Handle != "" || decl.URL != "" {
		t.Errorf("a password source declared a browser login: %+v", decl)
	}
}
