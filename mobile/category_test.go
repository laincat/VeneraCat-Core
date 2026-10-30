package mobile

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/backup"
)

const categoryComic = `class Cat extends ComicSource {
  name = "Cat"
  key = "cat"
  version = "1.0.0"
  category = {title: "Cat", parts: [
    {name: "Language", type: "fixed", categories: ["Chinese", "English"],
     itemType: "category", groupParam: "language"},
    {name: "Tags", type: "random", categories: ["a", "b", "c"],
     itemType: "search", randomNumber: 2},
  ]}
  categoryComics = {
    optionList: [{label: "排序", type: "select", options: ["date-最近", "hot-热门"]}],
    load: async (category, param, options, page) => ({
      comics: [{id: category + "/" + param + "/" + options[0] + "/" + page, title: "t", cover: ""}],
      maxPage: 5,
    }),
  }
}`

// The app reads a category page and then loads it; both cross the bridge as
// JSON, so a shape that drifts breaks the interface with no error to show.
func TestCategoryAcrossTheBridge(t *testing.T) {
	h := &fakeHost{data: map[string]string{}, routes: map[string]string{}}
	c := newCore(t, h)
	if _, err := c.Install(0, "comic", categoryComic, "file", "", ""); err != nil {
		t.Fatalf("install: %v", err)
	}

	raw, err := c.Category("cat")
	if err != nil {
		t.Fatalf("category: %v", err)
	}
	var page struct {
		Title string `json:"title"`
		Parts []struct {
			Name         string   `json:"name"`
			Type         string   `json:"type"`
			ItemType     string   `json:"itemType"`
			Categories   []string `json:"categories"`
			GroupParam   string   `json:"groupParam"`
			RandomNumber int      `json:"randomNumber"`
		} `json:"parts"`
		Options []struct {
			Default string `json:"default"`
		} `json:"options"`
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("category returned %q: %v", raw, err)
	}
	if page.Title != "Cat" || len(page.Parts) != 2 {
		t.Fatalf("page = %s", raw)
	}
	if page.Parts[0].GroupParam != "language" || len(page.Parts[0].Categories) != 2 {
		t.Errorf("part 0 = %+v", page.Parts[0])
	}
	if page.Parts[1].Type != "random" || page.Parts[1].RandomNumber != 2 {
		t.Errorf("part 1 = %+v", page.Parts[1])
	}
	// An option without a default starts at its first value, never empty.
	if len(page.Options) != 1 || page.Options[0].Default != "date" {
		t.Errorf("options = %+v", page.Options)
	}

	// The value, its parameter, the chosen options and the page all reach the
	// source in that order.
	raw, err = c.CategoryComics(0, "cat", "Chinese", "language", `["hot"]`, 2)
	if err != nil {
		t.Fatalf("categoryComics: %v", err)
	}
	var list struct {
		Comics []struct {
			ID string `json:"id"`
		} `json:"comics"`
		MaxPage int `json:"maxPage"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("categoryComics returned %q: %v", raw, err)
	}
	if len(list.Comics) != 1 || list.Comics[0].ID != "Chinese/language/hot/2" {
		t.Errorf("comics = %+v", list.Comics)
	}
	if list.MaxPage != 5 {
		t.Errorf("maxPage = %d", list.MaxPage)
	}

	// Bad options are reported rather than silently dropped.
	if _, err := c.CategoryComics(0, "cat", "Chinese", "language", `{"a":1}`, 1); err == nil {
		t.Error("expected an error for options that are not an array")
	}

	// A source without a category page answers "{}" rather than failing.
	if _, err := c.Install(0, "comic", comic, "file", "", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got, err := c.Category("mini"); err != nil || got != "{}" {
		t.Errorf("no category page = %q, %v", got, err)
	}
}

// A source that logs in with cookies asks for a list of cookie names. That list
// is data, so it crosses the bridge by its own entry; the rest of a login runs
// through HasComic and CallComic.
func TestAccountCookieFieldsAcrossTheBridge(t *testing.T) {
	h := &fakeHost{data: map[string]string{}, routes: map[string]string{}}
	c := newCore(t, h)
	const cookieComic = `class Ck extends ComicSource {
  name = "Ck"
  key = "ck"
  version = "1.0.0"
  account = {
    loginWithCookies: {
      fields: ["ipb_member_id", "ipb_pass_hash"],
      validate: async (values) => values.length === 2 && values[0] !== "",
    },
    logout: async () => "ok",
  }
}`
	if _, err := c.Install(0, "comic", cookieComic, "file", "", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	raw, err := c.AccountCookieFields("ck")
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	var fields []string
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("fields returned %q: %v", raw, err)
	}
	if len(fields) != 2 || fields[0] != "ipb_member_id" {
		t.Errorf("fields = %v", fields)
	}

	// The styles are asked for by path, as the other optional members are.
	if !c.HasComic("ck", "account.loginWithCookies.validate") {
		t.Error("cookie login not detected")
	}
	if c.HasComic("ck", "account.login") {
		t.Error("a cookie-only source must not report a password login")
	}
	if !c.HasComic("ck", "account.logout") {
		t.Error("logout not detected")
	}

	// The source answers for itself: empty values are refused, filled ones pass.
	out, err := c.CallComic(0, "ck", "account.loginWithCookies.validate", `[["",""]]`)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if strings.TrimSpace(out) != "false" {
		t.Errorf("empty values = %s, want false", out)
	}
	out, err = c.CallComic(0, "ck", "account.loginWithCookies.validate", `[["1","2"]]`)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if strings.TrimSpace(out) != "true" {
		t.Errorf("filled values = %s, want true", out)
	}

	// A source without that style answers "[]" rather than failing.
	if _, err := c.Install(0, "comic", comic, "file", "", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got, err := c.AccountCookieFields("mini"); err != nil || got != "[]" {
		t.Errorf("plain source = %q, %v", got, err)
	}
}

// A source declares a key and supplies the readable text per locale; both cross
// the bridge, because the interface cannot invent the text.
func TestTranslationsAcrossTheBridge(t *testing.T) {
	h := &fakeHost{data: map[string]string{}, routes: map[string]string{}}
	c := newCore(t, h)
	const trComic = `class Tr extends ComicSource {
  name = "Tr"
  key = "tr"
  version = "1.0.0"
  translation = {'zh_CN': {domain: '域名'}, 'en': {domain: 'Domain'}}
  settings = {domain: {title: 'domain', type: 'switch', default: false}}
}`
	if _, err := c.Install(0, "comic", trComic, "file", "", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	raw, err := c.Translations("tr")
	if err != nil {
		t.Fatalf("translations: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("translations returned %q: %v", raw, err)
	}
	if got["domain"] == "" {
		t.Errorf("domain = %q", got["domain"])
	}

	// A source without a table answers "{}" rather than failing.
	if _, err := c.Install(0, "comic", comic, "file", "", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	if out, err := c.Translations("mini"); err != nil || out != "{}" {
		t.Errorf("plain source = %q, %v", out, err)
	}
}

// A declaration that is data rather than a function: favorites.multiFolder says
// whether a folder id is expected at all. CallComic invokes, ComicValue reads.
func TestComicValueAcrossTheBridge(t *testing.T) {
	h := &fakeHost{data: map[string]string{}, routes: map[string]string{}}
	c := newCore(t, h)
	const favComic = `class Fv extends ComicSource {
  name = "Fv"
  key = "fv"
  version = "1.0.0"
  favorites = {multiFolder: true, loadComics: async () => ({comics: [], maxPage: 1})}
}`
	if _, err := c.Install(0, "comic", favComic, "file", "", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	got, err := c.ComicValue("fv", "favorites.multiFolder")
	if err != nil {
		t.Fatalf("value: %v", err)
	}
	if got != "true" {
		t.Errorf("multiFolder = %s, want true", got)
	}
	// An absent path reads as null rather than failing.
	got, err = c.ComicValue("fv", "favorites.loadFolders")
	if err != nil {
		t.Fatalf("value: %v", err)
	}
	if got != "null" {
		t.Errorf("absent path = %s, want null", got)
	}
}

// The client cannot build a .venera archive itself — the sources live in Core's
// registry — so Core writes the bytes and the client saves them.
func TestExportBackupAcrossTheBridge(t *testing.T) {
	h := &fakeHost{data: map[string]string{}, routes: map[string]string{}}
	c := newCore(t, h)
	if _, err := c.Install(0, "comic", categoryComic, "file", "", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	encoded, err := c.ExportBackup(`["one piece","进击的巨人"]`, `{"cat":{"token":"abc"}}`)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if encoded == "" {
		t.Fatal("the export is empty")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("export is not base64: %v", err)
	}
	archive, err := backup.Open(bytes.NewReader(data), int64(len(data)), backup.Limits{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	content, err := archive.Read(context.Background(), backup.Options{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(content.Sources) != 1 || content.Sources[0].Key != "cat" {
		t.Fatalf("sources = %+v", content.Sources)
	}
	if content.Sources[0].Script == "" {
		t.Error("the script did not travel")
	}
	if !content.Sources[0].HasData || !strings.Contains(string(content.Sources[0].Data), `"token":"abc"`) {
		t.Errorf("source data did not travel: %s", content.Sources[0].Data)
	}
	if len(content.SearchHistory) != 2 || content.SearchHistory[0] != "one piece" {
		t.Errorf("history = %v", content.SearchHistory)
	}
	// Cookies are the one thing that must never be in an export.
	if archive.Has(backup.MemberCookies) {
		t.Error("the export contains cookies")
	}
	// Bad history is reported rather than silently dropped.
	if _, err := c.ExportBackup(`{"not":"an array"}`, ""); err == nil {
		t.Error("expected an error for history that is not an array")
	}
	if _, err := c.ExportBackup("", `["not an object"]`); err == nil {
		t.Error("expected an error for source data that is not an object")
	}
}
