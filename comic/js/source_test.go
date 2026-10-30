package js

import (
	"context"
	"testing"
)

// A source may log in with cookies instead of a password: it lists the cookie
// names it wants and validates them itself. ehentai, lanraragi and mycomic
// declare that style, and the list is data, so it needs its own reader.
func TestAccountLoginFields(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class Ck extends ComicSource {
  name='Ck'; key='ck'; version='1'
  account = {
    loginWithCookies: {
      fields: ['ipb_member_id', 'ipb_pass_hash'],
      validate: async (values) => values.length === 2,
    },
  }
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	got, err := r.AccountLoginFields(context.Background(), "ck")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "ipb_member_id" || got[1] != "ipb_pass_hash" {
		t.Fatalf("fields = %v", got)
	}

	// A source that logs in another way reports no fields rather than an error.
	plain := `class P extends ComicSource {
  name='P'; key='p'; version='1'
  account = {login: async () => 'ok'}
}`
	if _, err := r.LoadSource(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	if fields, err := r.AccountLoginFields(context.Background(), "p"); err != nil || fields != nil {
		t.Errorf("plain = %v, %v; want none", fields, err)
	}
}

// A source writes a key as a setting's title and supplies the readable text in
// its translation table; without reading it the interface shows "domain".
func TestTranslationsFollowTheClientLocale(t *testing.T) {
	src := `class Tr extends ComicSource {
  name='Tr'; key='tr'; version='1'
  translation = {
    'zh_CN': {domain: '域名', ehevent: '触发事件'},
    'en': {domain: 'Domain'},
  }
  settings = {domain: {title: 'domain', type: 'switch', default: false}}
}`
	for _, tc := range []struct{ locale, want string }{
		{"zh_CN", "域名"},
		{"zh_TW", "域名"}, // the language part matches when the region does not
		{"en_US", "Domain"},
		{"ja_JP", "Domain"}, // falls back to English
	} {
		r := newRT(t, Options{}, nil)
		r.svc.Env.Locale = tc.locale
		if _, err := r.LoadSource(context.Background(), src); err != nil {
			t.Fatal(err)
		}
		got, err := r.Translations(context.Background(), "tr")
		if err != nil {
			t.Fatal(err)
		}
		if got["domain"] != tc.want {
			t.Errorf("locale %s: domain = %q, want %q", tc.locale, got["domain"], tc.want)
		}
	}

	// A source without a table reports nothing rather than an error.
	r := newRT(t, Options{}, nil)
	if _, err := r.LoadSource(context.Background(), `class NoTr extends ComicSource {
  name='NoTr'; key='notr'; version='1'
  settings = {a: {title: 'a', type: 'switch', default: false}}
}`); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Translations(context.Background(), "notr"); err != nil || got != nil {
		t.Errorf("no table = %v, %v", got, err)
	}
}

// Some declarations are data and still decide behaviour: favorites.multiFolder
// says whether a folder id is expected at all. Call cannot reach a value, so it
// has its own reader.
func TestValueReadsDataNotFunctions(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class D extends ComicSource {
  name='D'; key='d'; version='1'
  favorites = {multiFolder: true, loadComics: async () => ({comics: [], maxPage: 1})}
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	raw, err := r.Value(context.Background(), "d", "favorites.multiFolder")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "true" {
		t.Errorf("multiFolder = %s, want true", raw)
	}
	// A path that is absent reads as null rather than failing.
	raw, err = r.Value(context.Background(), "d", "favorites.loadFolders")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "null" {
		t.Errorf("absent path = %s, want null", raw)
	}
}
