package gohost

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

const accountComic = `class Acc extends ComicSource {
  name = 'Acc'; key = 'acc'; version = '1'
  account = {
    login: async (user, pwd) => {
      if (user === 'right' && pwd === 'right') { return 'ok' }
      throw 'Invalid account or password'
    },
    logout: async () => 'ok',
  }
  search = { load: async () => ({comics: [], maxPage: 1}) }
  comic = { loadInfo: async () => ({title: 't', cover: '', tags: {}}) }
}`

// A source that logs in with cookies declares no password login at all.
const cookieComic = `class Ckc extends ComicSource {
  name = 'Ckc'; key = 'ckc'; version = '1'
  account = {
    loginWithCookies: {
      fields: ['ipb_member_id', 'ipb_pass_hash'],
      validate: async (values) => values.length === 2 && values[0] !== '',
    },
  }
}`

const webviewComic = `class Wv extends ComicSource {
  name = 'Wv'; key = 'wv'; version = '1'
  account = {
    loginWithWebview: {url: 'https://example.invalid/login', checkStatus: (url, title) => false},
  }
}`

const noAccountComic = `class Plain extends ComicSource {
  name = 'Plain'; key = 'plain'; version = '1'
  search = { load: async () => ({comics: [], maxPage: 1}) }
}`

// A source that declares account.login needs an account; one that does not must
// not be shown a login form. Fifteen official sources declare one, and until now
// the interface could not tell them apart.
func TestAccountSupportIsReadFromTheSource(t *testing.T) {
	c := openTest(t)
	ctx := context.Background()
	if _, err := c.Install(ctx, accountComic, "file_acc", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Install(ctx, noAccountComic, "file_plain", ""); err != nil {
		t.Fatal(err)
	}
	if got := c.AccountSupport(ctx, "acc"); !got.Password || !got.Logout {
		t.Errorf("acc: %+v, want a password login and a logout", got)
	}
	if got := c.AccountSupport(ctx, "plain"); got.Password || got.Cookies || got.Webview || got.Logout {
		t.Errorf("plain: %+v, want no login", got)
	}
}

// What the source answers is passed through, and what it throws is kept as its
// own text: the reason usually names the field it did not like.
func TestLoginAccountReportsTheSource(t *testing.T) {
	c := openTest(t)
	ctx := context.Background()
	if _, err := c.Install(ctx, accountComic, "file_acc", ""); err != nil {
		t.Fatal(err)
	}
	got, err := c.LoginAccount(ctx, "acc", "right", "right")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if got != "ok" {
		t.Errorf("answer = %q, want ok", got)
	}
	if _, err := c.LoginAccount(ctx, "acc", "wrong", "wrong"); err == nil {
		t.Error("a refused login must be an error")
	} else if !strings.Contains(err.Error(), "Invalid account or password") {
		t.Errorf("error = %v, want the source's own text", err)
	}
	if err := c.LogoutAccount(ctx, "acc"); err != nil {
		t.Errorf("logout: %v", err)
	}
}

// openTest builds a Core over a temporary data directory.
func openTest(t *testing.T) *Core {
	t.Helper()
	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "d"), AppVersion: "1.6.3", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// The three styles are told apart: ehentai has no password login at all, only a
// webview and a cookie form, and a panel that only knew about account.login
// would show it as "no account needed".
func TestAccountStylesAreToldApart(t *testing.T) {
	c := openTest(t)
	ctx := context.Background()
	if _, err := c.Install(ctx, cookieComic, "file_ckc", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Install(ctx, webviewComic, "file_wv", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Install(ctx, accountComic, "file_acc2", ""); err != nil {
		t.Fatal(err)
	}
	if got := c.AccountSupport(ctx, "ckc"); !got.Cookies || got.Password || got.Webview {
		t.Errorf("cookie source: %+v", got)
	}
	if got := c.AccountSupport(ctx, "wv"); !got.Webview || got.Password || got.Cookies {
		t.Errorf("webview source: %+v", got)
	}
	if got := c.AccountSupport(ctx, "acc"); !got.Password || got.Cookies || got.Webview {
		t.Errorf("password source: %+v", got)
	}

	fields, err := c.AccountCookieFields(ctx, "ckc")
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields[0] != "ipb_member_id" {
		t.Errorf("fields = %v", fields)
	}
	// The source decides: it looks at the values and answers whether they work.
	if ok, err := c.LoginWithCookies(ctx, "ckc", []string{"", ""}); err != nil || ok {
		t.Errorf("empty cookies = %v, %v; want false, nil", ok, err)
	}
	if ok, err := c.LoginWithCookies(ctx, "ckc", []string{"1", "2"}); err != nil || !ok {
		t.Errorf("filled cookies = %v, %v; want true, nil", ok, err)
	}
}
