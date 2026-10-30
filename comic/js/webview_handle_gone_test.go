package js

import (
	"context"
	"errors"
	"testing"
)

// A handle belongs to the runtime that handed it out. Replacing the source
// builds a new runtime, so the old handle's callbacks are gone. A client that
// keeps polling it — a login window left open while the user updates the
// source — currently gets `false` and `nil`: the window spins forever with no
// way to tell "the source says not yet" from "this handle no longer exists".
//
// This test pins the behaviour the client would need to tell them apart.
func TestHandleFromReplacedSourceIsReported(t *testing.T) {
	r := newRT(t, Options{}, nil)
	ctx := context.Background()
	if _, err := r.LoadSource(ctx, webviewSource); err != nil {
		t.Fatal(err)
	}
	decl, err := r.AccountWebviewLogin(ctx, "browser")
	if err != nil || decl.Handle == "" {
		t.Fatalf("declaration = %+v, %v", decl, err)
	}

	// The user updates the source while the login window is open.
	replaced := `
class Browser extends ComicSource {
    name = "Browser"
    key = "browser"
    version = "2.0.0"
    account = {
        loginWithWebview: {
            url: "https://site.test/login",
            checkStatus: (url, title) => title === "已登录",
            onLoginSuccess: async () => {},
        },
    }
    search = {load: async () => ({comics: [], maxPage: 1})}
}`
	r.Unload(ctx, "browser")
	if _, err := r.LoadSource(ctx, replaced); err != nil {
		t.Fatal(err)
	}

	done, statusErr := r.WebviewStatus(ctx, "browser", decl.Handle, "https://site.test/home", "已登录")
	loginErr := r.WebviewLoginDone(ctx, "browser", decl.Handle)
	t.Logf("stale handle: status=%v err=%v; done err=%v", done, statusErr, loginErr)
	if !errors.Is(statusErr, ErrWebviewHandleGone) {
		t.Errorf("status error = %v, want ErrWebviewHandleGone", statusErr)
	}
	if !errors.Is(loginErr, ErrWebviewHandleGone) {
		t.Errorf("done error = %v, want ErrWebviewHandleGone", loginErr)
	}
}

// A source that writes loginWithWebview with no callbacks at all is still a
// valid login: the cookies alone complete it. Its handle must keep answering
// false/nil rather than looking like one from a source that no longer exists.
func TestHandleWithoutCallbacksStaysQuiet(t *testing.T) {
	r := newRT(t, Options{}, nil)
	ctx := context.Background()
	const bare = `
class Bare extends ComicSource {
    name = "Bare"
    key = "bare"
    version = "1.0.0"
    account = {loginWithWebview: {url: "https://site.test/login"}}
    search = {load: async () => ({comics: [], maxPage: 1})}
}`
	if _, err := r.LoadSource(ctx, bare); err != nil {
		t.Fatal(err)
	}
	decl, err := r.AccountWebviewLogin(ctx, "bare")
	if err != nil || decl.Handle == "" {
		t.Fatalf("declaration = %+v, %v", decl, err)
	}
	done, statusErr := r.WebviewStatus(ctx, "bare", decl.Handle, "https://site.test/home", "title")
	if statusErr != nil || done {
		t.Errorf("status = %v, %v; want false, nil", done, statusErr)
	}
	if err := r.WebviewLoginDone(ctx, "bare", decl.Handle); err != nil {
		t.Errorf("done = %v; want nil", err)
	}
}
