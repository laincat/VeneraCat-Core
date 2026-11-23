// Package gohost assembles an optional Go host: one JavaScript
// runtime and one plugin registry, with the network, storage, cookies,
// WebView verification and dialogs the client provides.
package gohost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // registered so a modifyImage script can read gif bytes
	_ "image/jpeg" // and jpeg, which is what most pages serve
	"image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/laincat/veneracat-core/comic/hostapi"
	coreimage "github.com/laincat/veneracat-core/comic/image"
	"github.com/laincat/veneracat-core/comic/js"
	"github.com/laincat/veneracat-core/gohost/cookies"
	"github.com/laincat/veneracat-core/gohost/store"
	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/plugin"
)

// Options configure Open.
type Options struct {
	// DataDir holds sources, settings and plugin records.
	DataDir  string
	Locale   string
	Platform string
	// AppVersion is what sources see and what index entries are compared
	// with (minAppVersion).
	AppVersion string
	// UserAgent is the default request agent.
	UserAgent   string
	MemoryLimit int64
	// UI, Clipboard and Verifier are the client's services. A nil Verifier
	// means challenges fail instead of opening a browser.
	UI        js.UIHandler
	Clipboard js.Clipboard
	Verifier  host.Verifier
	// VerifierFactory builds the verifier once the cookie jar exists; it is
	// used when Verifier is nil.
	VerifierFactory func(jar *cookies.Jar) host.Verifier
	// LogSink receives script log lines tagged with their source key. When nil
	// they go to stderr only, which a user who started the app from a launcher
	// never sees.
	LogSink func(key, level, title, content string)
}

// Core is one profile: its runtime, registry and stores.
// LogLine is one line a source printed, tagged with the source it came from.
// Core keeps the last few so a user can see why a source misbehaved: the same
// lines go to stderr, which nobody who started the app from a launcher sees.
type LogLine struct {
	Key     string
	Level   string
	Title   string
	Content string
	Time    time.Time
}

// maxLogLines bounds what is kept; a source that logs in a loop must not grow
// the process without limit.
const maxLogLines = 400

// MaxLogLines is what the panel tells the user the buffer holds.
func MaxLogLines() int { return maxLogLines }

type Core struct {
	rt    *js.Runtime
	reg   *plugin.Registry
	store *store.Store
	jar   *cookies.Jar
	logMu sync.Mutex
	logs  []LogLine
	tr    *Transport
	opts  Options

	// cursors holds the paging cursor that led to each page of a source's
	// search, for a source that pages by cursor rather than by page number.
	mu      sync.Mutex
	cursors map[string]map[int]string
}

// Open assembles Core. Close it when the app quits.
func Open(opts Options) (*Core, error) {
	if opts.DataDir == "" {
		return nil, errors.New("core: DataDir is required")
	}
	if opts.AppVersion == "" {
		opts.AppVersion = "0.1.0"
	}
	if opts.Locale == "" {
		opts.Locale = "zh_CN"
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}
	if opts.MemoryLimit <= 0 {
		opts.MemoryLimit = 256 << 20
	}
	st, err := store.Open(opts.DataDir)
	if err != nil {
		return nil, err
	}
	jar, err := cookies.Open(filepath.Join(opts.DataDir, "cookies.json"))
	if err != nil {
		return nil, err
	}
	tr := NewTransport(jar, opts.UserAgent, 40*time.Second)
	verifier := opts.Verifier
	if verifier == nil && opts.VerifierFactory != nil {
		verifier = opts.VerifierFactory(jar)
	}
	// The sink needs the Core it writes into, so the Core exists first and the
	// runtime is attached to it below.
	c := &Core{store: st, jar: jar, tr: tr, opts: opts, cursors: map[string]map[int]string{}}
	rt, err := js.New(js.Options{MemoryLimit: opts.MemoryLimit}, js.Services{
		Storage: st,
		Env:     js.Environment{Locale: opts.Locale, Platform: opts.Platform, AppVersion: opts.AppVersion, DefaultUA: opts.UserAgent},
		Log:     logLine,
		LogSource: func(key, level, title, content string) {
			c.appendLog(key, level, title, content)
			if opts.LogSink != nil {
				opts.LogSink(key, level, title, content)
			}
			logLine(level, title, content)
		},
		Transport: tr,
		Verifier:  verifier,
		Cookies:   jar,
		UI:        opts.UI,
		Clipboard: opts.Clipboard,
	})
	if err != nil {
		return nil, err
	}
	c.rt = rt
	c.reg = plugin.New(rt, tr)
	return c, nil
}

// appendLog keeps the newest maxLogLines lines.
func (c *Core) appendLog(key, level, title, content string) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	c.logs = append(c.logs, LogLine{Key: key, Level: level, Title: title, Content: content, Time: time.Now()})
	if len(c.logs) > maxLogLines {
		c.logs = append(c.logs[:0], c.logs[len(c.logs)-maxLogLines:]...)
	}
}

// LogLines returns what the sources have printed, oldest first. The slice is a
// copy: the runtime appends from its own goroutines.
func (c *Core) LogLines() []LogLine {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	return append([]LogLine(nil), c.logs...)
}

// ClearLogs empties the buffer.
func (c *Core) ClearLogs() {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	c.logs = nil
}

// Close releases the runtime.
func (c *Core) Close() error { return c.rt.Close() }

// DataDir is the directory the app keeps its data in.
func (c *Core) DataDir() string { return c.opts.DataDir }

// Store, Jar and Transport are the app's own handles to the same services.
func (c *Core) Store() *store.Store { return c.store }
func (c *Core) Jar() *cookies.Jar   { return c.jar }

// ComicPartition is the cookie partition a comic source uses.
//
// Every request and every challenge names it, so anything else that writes
// cookies — a backup restore, a test — has to name it the same way. A restore
// that used the bare key stored cookies nothing would ever send.
func ComicPartition(key string) string { return hostapi.Partition(key) }
func (c *Core) Transport() *Transport  { return c.tr }
func (c *Core) Runtime() *js.Runtime   { return c.rt }

// WebviewHandleGone reports a login handle whose source was unloaded or
// replaced. The client must stop waiting: nothing will ever answer for it.
func WebviewHandleGone(err error) bool { return errors.Is(err, js.ErrWebviewHandleGone) }

// logLine writes script and Core log lines to stderr, prefixed so they are
// easy to filter in a terminal.
func logLine(level, title, content string) {
	if content == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", level, title, content)
}

// --- plugins ---

// Install installs a comic source script.
func (c *Core) Install(ctx context.Context, body, origin, indexURL string) (*plugin.Record, error) {
	return c.InstallTyped(ctx, plugin.TypeComic, body, origin, indexURL)
}

// InstallTyped installs a plugin whose kind the caller already knows. The
// client always knows it — it asks the user which file they picked — while a
// seed builder only has the file, so it says so here rather than guessing in
// the interface.
func (c *Core) InstallTyped(ctx context.Context, typ plugin.Type, body, origin, indexURL string) (*plugin.Record, error) {
	return c.reg.Install(ctx, typ, body, origin, indexURL, "")
}

// Replace updates an installed source.
func (c *Core) Replace(ctx context.Context, key, body, origin, indexURL string) (*plugin.Record, error) {
	return c.ReplaceTyped(ctx, key, plugin.TypeComic, body, origin, indexURL)
}

// ReplaceTyped keeps the installed provider's kind explicit when updating it.
// The shared registry enforces same-type replacement and rollback on failure.
func (c *Core) ReplaceTyped(ctx context.Context, key string, typ plugin.Type, body, origin, indexURL string) (*plugin.Record, error) {
	return c.reg.Replace(ctx, key, typ, body, origin, indexURL, "")
}

// Records lists installed plugins with bodies, for persistence.
func (c *Core) Records() []plugin.Record { return c.reg.List() }

// Plugins lists installed plugins without bodies.
func (c *Core) Plugins() []plugin.Record {
	list := c.reg.List()
	for i := range list {
		list[i].Body = ""
	}
	return list
}

// Restore re-activates the saved records and returns the ones that failed.
func (c *Core) Restore(ctx context.Context, recs []plugin.Record) map[string]error {
	return c.reg.Restore(ctx, recs)
}

// Remove uninstalls a plugin and forgets what it stored.
func (c *Core) Remove(ctx context.Context, key string) error {
	if err := c.RemoveRecord(ctx, key); err != nil {
		return err
	}
	return c.ClearSource(key)
}

func (c *Core) RemoveRecord(ctx context.Context, key string) error { return c.reg.Remove(ctx, key) }

func (c *Core) ClearSource(key string) error {
	return errors.Join(c.jar.Clear(ComicPartition(key)), c.store.RemoveSource(key))
}

// SetEnabled turns a plugin on or off.
func (c *Core) SetEnabled(ctx context.Context, key string, on bool) error {
	return c.reg.SetEnabled(ctx, key, on)
}

// Has reports whether a comic source defines path, such as "search.load".
func (c *Core) Has(ctx context.Context, key, path string) bool {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return false
	}
	ok, err := rt.Has(ctx, key, path)
	return err == nil && ok
}

// Call invokes ComicSource.<path> and returns the JSON result.
//
// A failure is also recorded in the log buffer with its source: the UI shows the
// error where it happened, and the line is what tells a later reader why the
// window they just closed was complaining.
func (c *Core) Call(ctx context.Context, key, path string, args ...any) (json.RawMessage, error) {
	rt, err := c.reg.Comic(key)
	if err != nil {
		c.appendLog(key, "error", "Core", path+": "+err.Error())
		return nil, err
	}
	out, err := rt.Call(ctx, key, path, args...)
	if err != nil {
		c.appendLog(key, "error", "Core", path+": "+err.Error())
	}
	return out, err
}

// --- comic data ---

// Comic is one comic in a list.
type Comic struct {
	ID          string
	Title       string
	Subtitle    string
	Cover       string
	Tags        []string
	Description string
	// FavoriteID is the source's own handle on this comic's favorite entry.
	// Six official sources take it as the fourth argument of
	// favorites.addOrDelFavorite, and wnacg builds its delete URL from it: with
	// no handle the removal asks for "del-id-undefined" and quietly does
	// nothing.
	FavoriteID string
}

// Page is one page of search results.
type Page struct {
	Comics  []Comic
	MaxPage int
	Next    string
}

type ExplorePage = js.ExplorePage

func (c *Core) ExplorePages(ctx context.Context, key string) ([]ExplorePage, error) {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return nil, err
	}
	return rt.ExplorePages(ctx, key)
}

// ExploreSection is a titled group of comics in one browse.
type ExploreSection struct {
	Title  string
	Comics []Comic
}

// ExploreResult is what one browse returned.
type ExploreResult struct {
	Sections []ExploreSection
	// MaxPage is for "multiPageComicList" browses; 0 when unknown.
	MaxPage int
	// Next is the cursor for a browse that pages by token instead of by number.
	// Empty means "no more", which is what a source returns at the end.
	Next string
}

// Explore loads one browse. page is the page number for numbered browses;
// cursor is the token a cursor browse handed back last time.
//
// The shapes and the load/loadNext choice live in Core (comic/js), so both
// clients read a browse the same way; this only maps each comic into the shape
// the interface draws.
func (c *Core) Explore(ctx context.Context, key string, index, page int, cursor string) (ExploreResult, error) {
	rt, err := c.reg.Comic(key)
	if err != nil {
		c.appendLog(key, "error", "Core", "explore: "+err.Error())
		return ExploreResult{}, err
	}
	res, err := rt.Explore(ctx, key, index, page, cursor)
	if err != nil {
		c.appendLog(key, "error", "Core", fmt.Sprintf("explore[%d]: %s", index, err))
		return ExploreResult{}, err
	}
	out := ExploreResult{MaxPage: res.MaxPage, Next: res.Next}
	for _, section := range res.Sections {
		sec := ExploreSection{Title: section.Title}
		for _, raw := range section.Comics {
			if comic := parseComic(raw); comic.ID != "" {
				sec.Comics = append(sec.Comics, comic)
			}
		}
		if len(sec.Comics) > 0 {
			out.Sections = append(out.Sections, sec)
		}
	}
	return out, nil
}

type SearchOptionGroup = js.SearchOptionGroup
type SourceSetting = js.SourceSetting
type SettingOption = js.SettingOption

func (c *Core) Settings(ctx context.Context, key string) ([]SourceSetting, error) {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return nil, err
	}
	return rt.Settings(ctx, key)
}

func (c *Core) SearchOptions(ctx context.Context, key string) ([]SearchOptionGroup, error) {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return nil, err
	}
	return rt.SearchOptions(ctx, key)
}

func (c *Core) LoadSourceSetting(key, setting string) (json.RawMessage, bool, error) {
	return c.store.LoadSetting(key, setting)
}

func (c *Core) SaveSourceSetting(key, setting string, value json.RawMessage) error {
	return c.store.SaveSetting(key, setting, value)
}

// DefaultOptions is one value per search group: the group's default, which
// search.load expects.
func (c *Core) DefaultOptions(ctx context.Context, key string) ([]string, error) {
	groups, err := c.SearchOptions(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if g.IsMultiSelect() {
			// A multi-select group's value is a JSON array of the chosen keys,
			// which is what the source parses out of options[i].
			chosen := g.DefaultValues
			if len(chosen) == 0 {
				chosen = g.Values
			}
			encoded, err := json.Marshal(chosen)
			if err != nil {
				return nil, err
			}
			out = append(out, string(encoded))
			continue
		}
		out = append(out, g.Default)
	}
	return out, nil
}

// Search cursors, per source and page. A source that pages by cursor hands one
// back with each page; the interface asks for a page number, so the cursor that
// led to each page is kept and replayed. A cursor belongs to one search, so a
// new search (page 1) clears what the previous one left.
func (c *Core) searchCursor(key string, page int) any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if page <= 1 {
		delete(c.cursors, key)
		return nil
	}
	if v, ok := c.cursors[key][page]; ok {
		return v
	}
	return nil
}

func (c *Core) rememberSearchCursor(key string, page int, next string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if page <= 1 || c.cursors[key] == nil {
		c.cursors[key] = map[int]string{}
	}
	c.cursors[key][page+1] = next
}

// Search runs a search against one source.
//
// A source pages its results in one of two ways, and the official client
// supports both:
//
//   - search.load(keyword, options, page) takes a page number;
//   - search.loadNext(keyword, options, next) takes whatever cursor the
//     previous page returned in `next`.
//
// A source that declares both is asked for search.load, because a page number
// is what the interface offers. A source that declares only search.loadNext is
// asked for that instead: calling search.load there would report "source does
// not define this function" for a source that works in the official client.
// options holds one chosen value per group of SearchOptions, usually
// DefaultOptions.
func (c *Core) Search(ctx context.Context, key, keyword string, options []string, page int) (Page, error) {
	if options == nil {
		options = []string{}
	}
	if c.Has(ctx, key, "search.load") {
		raw, err := c.Call(ctx, key, "search.load", keyword, options, page)
		if err != nil {
			return Page{}, err
		}
		return parsePage(raw)
	}
	if c.Has(ctx, key, "search.loadNext") {
		// The cursor is the source's own; the first page is when it has none.
		var cursor any
		if page > 1 {
			cursor = c.searchCursor(key, page)
		}
		raw, err := c.Call(ctx, key, "search.loadNext", keyword, options, cursor)
		if err != nil {
			return Page{}, err
		}
		out, err := parsePage(raw)
		if err != nil {
			return Page{}, err
		}
		c.rememberSearchCursor(key, page, out.Next)
		return out, nil
	}
	return Page{}, fmt.Errorf("core: %s offers no search entry point", key)
}

// Chapter is one chapter of a comic.
type Chapter struct {
	ID    string
	Title string
	// Group is the chapter group's name, empty for a flat chapter list.
	Group string
}

// TagGroup is one tag group, such as 作者 → [..].
type TagGroup struct {
	Name   string
	Values []string
}

// Details is comic.loadInfo's result.
type Details struct {
	Title       string
	Subtitle    string
	Cover       string
	Description string
	Tags        []TagGroup
	// Chapters keep the source's order, which is reading order.
	Chapters []Chapter
	// FavoriteID is the source's handle on this comic's favorite entry, when
	// loadInfo reports one. Removal needs it on some sources.
	FavoriteID string
	// SubID is the source's secondary identifier for this comic, which some
	// comment APIs need to place a thread under the right work.
	SubID string
}

// Details runs comic.loadInfo(id).
func (c *Core) Details(ctx context.Context, key, comicID string) (Details, error) {
	raw, err := c.Call(ctx, key, "comic.loadInfo", comicID)
	if err != nil {
		return Details{}, err
	}
	return parseDetails(raw)
}

// ChapterImages runs comic.loadEp(comicID, chapterID); chapterID may be "".
func (c *Core) ChapterImages(ctx context.Context, key, comicID, chapterID string) ([]string, error) {
	var arg any
	if chapterID != "" {
		arg = chapterID
	}
	raw, err := c.Call(ctx, key, "comic.loadEp", comicID, arg)
	if err != nil {
		return nil, err
	}
	var out struct {
		Images []string `json:"images"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("core: loadEp: %w", err)
	}
	return out.Images, nil
}

// ImageRequest is how to fetch one image, from onImageLoad/onThumbnailLoad.
type ImageRequest struct {
	URL     string
	Method  string
	Headers map[string]string
	Body    []byte
	baseURL string
	// Handle names the config's callbacks inside the source's runtime, and
	// Callbacks says which of onResponse/onLoadFailed it carries. A source
	// that only names an address leaves both empty.
	Handle    string
	Callbacks []string
	// ModifyImage is the source's pixel-buffer script, when it supplied one.
	// It is a string of JavaScript, run by Core over the decoded image.
	ModifyImage string
}

// HasCallback reports whether the config carries name.
func (r ImageRequest) HasCallback(name string) bool {
	for _, c := range r.Callbacks {
		if c == name {
			return true
		}
	}
	return false
}

// ImageRequest asks the source how to fetch an image. thumbnail asks
// onThumbnailLoad instead of onImageLoad, for covers. Without either hook the
// URL is fetched as it is.
func (c *Core) ImageRequest(ctx context.Context, key, url, comicID, chapterID string, thumbnail bool) (ImageRequest, error) {
	req := ImageRequest{URL: url, Method: "GET", baseURL: url}
	rt, err := c.reg.Comic(key)
	if err != nil {
		return req, err
	}
	// Core keeps the config's callbacks and reports a handle for them; a
	// source without a hook reports ErrNoImageCallback and the address is
	// fetched as it is.
	cfg, err := rt.ImageConfig(ctx, key, url, comicID, chapterID, thumbnail)
	if errors.Is(err, js.ErrNoImageCallback) {
		return req, nil
	}
	if err != nil {
		return req, err
	}
	if cfg.URL == "" && cfg.Headers == nil && cfg.Data == "" && cfg.Handle == "" {
		return req, nil // the source returned nothing useful
	}
	if cfg.URL != "" {
		req.URL = cfg.URL
	}
	if cfg.Method != "" {
		req.Method = strings.ToUpper(cfg.Method)
	}
	req.Headers = cfg.Headers
	if cfg.Data != "" {
		req.Body = []byte(cfg.Data)
	}
	req.Handle = cfg.Handle
	req.Callbacks = cfg.Callbacks
	if thumbnail {
		// The official client documents modifyImage and onLoadFailed as ignored
		// for a cover. onResponse stays: a scrambled cover still has to be made
		// readable.
		kept := req.Callbacks[:0]
		for _, name := range req.Callbacks {
			if name != "onLoadFailed" {
				kept = append(kept, name)
			}
		}
		req.Callbacks = kept
	} else {
		req.ModifyImage = cfg.ModifyImage
	}
	return req, nil
}

type CategoryPart = js.CategoryPart
type RankingOption = js.RankingOption
type Ranking = js.Ranking
type Category = js.Category

func (c *Core) CategoryPage(ctx context.Context, key string) (Category, error) {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return Category{}, err
	}
	return rt.Category(ctx, key)
}

// RankingComics loads one page of a source's ranking page. A source without one
// fails here rather than answering an empty list, so the interface can say so.
func (c *Core) RankingComics(ctx context.Context, key, option string, page int) (Page, error) {
	if page < 1 {
		page = 1
	}
	raw, err := c.Call(ctx, key, "categoryComics.ranking.load", option, page)
	if err != nil {
		return Page{}, err
	}
	return parsePage(raw)
}

// CategoryComics loads one page of a source's category page.
//
// category is the part's name as the source declared it, param the parameter
// that value maps to, and options one chosen value per group of the category's
// optionList.
func (c *Core) CategoryComics(ctx context.Context, key, category, param string, options []string, page int) (Page, error) {
	if options == nil {
		options = []string{}
	}
	if page < 1 {
		page = 1
	}
	raw, err := c.Call(ctx, key, "categoryComics.load", category, param, options, page)
	if err != nil {
		return Page{}, err
	}
	return parsePage(raw)
}

// FavoritesSupport describes how a source keeps favorites on its own side.
//
// Eleven official sources declare a favorites block, and none of it was
// reachable: the client only had a local shelf, so a comic kept on the site was
// invisible here.
type FavoritesSupport struct {
	// Supported is false when the source keeps no favorites of its own.
	Supported bool
	// MultiFolder means the source files favorites into folders and can report
	// them; a single-folder source takes no folder id at all.
	MultiFolder bool
	// Folders means the source can also list its folders and say which of them
	// already hold a comic.
	Folders bool
	// CanLoad means the source can list the comics in a folder. ehentai declares
	// favorites but not this: it offers folders and a toggle, no list, so an
	// entry that would show nothing must not appear.
	CanLoad bool
}

// FavoritesSupport reads how a source keeps favorites.
func (c *Core) FavoritesSupport(ctx context.Context, key string) FavoritesSupport {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return FavoritesSupport{}
	}
	has := func(path string) bool {
		ok, err := rt.Has(ctx, key, path)
		return err == nil && ok
	}
	out := FavoritesSupport{
		Supported: has("favorites.loadComics") || has("favorites.addOrDelFavorite"),
		Folders:   has("favorites.loadFolders"),
		CanLoad:   has("favorites.loadComics"),
	}
	if out.Supported {
		// multiFolder is data, not a function: Call invokes, Value reads.
		raw, err := rt.Value(ctx, key, "favorites.multiFolder")
		if err == nil {
			var multi bool
			if json.Unmarshal(raw, &multi) == nil {
				out.MultiFolder = multi
			}
		}
		out.Folders = out.Folders && out.MultiFolder
	}
	return out
}

// FavoriteFolders lists a source's folders and, when comicID is not empty, which
// of them already hold that comic.
func (c *Core) FavoriteFolders(ctx context.Context, key, comicID string) (map[string]string, []string, error) {
	var arg any
	if comicID != "" {
		arg = comicID
	} else {
		arg = nil
	}
	raw, err := c.Call(ctx, key, "favorites.loadFolders", arg)
	if err != nil {
		return nil, nil, err
	}
	var envelope struct {
		Folders   map[string]string `json:"folders"`
		Favorited []string          `json:"favorited"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, nil, fmt.Errorf("core: favorites.loadFolders: %w", err)
	}
	return envelope.Folders, envelope.Favorited, nil
}

// FavoriteComics loads one page of a folder. A single-folder source takes no
// folder id, so pass an empty one.
func (c *Core) FavoriteComics(ctx context.Context, key, folder string, page int) (Page, error) {
	if page < 1 {
		page = 1
	}
	var arg any
	if folder != "" {
		arg = folder
	} else {
		arg = nil
	}
	raw, err := c.Call(ctx, key, "favorites.loadComics", page, arg)
	if err != nil {
		return Page{}, err
	}
	return parsePage(raw)
}

// SetFavorite adds or removes a comic on the source's side.
func (c *Core) SetFavorite(ctx context.Context, key, comicID, folder, favoriteID string, adding bool) error {
	folderArg := any(nil)
	if folder != "" {
		folderArg = folder
	}
	// Sources that do not use the handle take three arguments and ignore the
	// fourth; sources that do use it (wnacg for the delete) need it, so it is
	// always passed rather than appended only when known.
	favoriteArg := any(nil)
	if favoriteID != "" {
		favoriteArg = favoriteID
	}
	_, err := c.Call(ctx, key, "favorites.addOrDelFavorite", comicID, folderArg, adding, favoriteArg)
	return err
}

// CallSettingCallback runs a setting's own callback, e.g. jm's refreshDomains
// or wnacg's domain refresh.
//
// Nine official sources declare one. A callback is a function in the source, so
// nothing can be carried across the boundary by value — but the source itself can
// run it, which is what this does. A source that changed its mind between the
// declaration and the press answers with its own error.
func (c *Core) CallSettingCallback(ctx context.Context, key, settingKey string) (string, error) {
	raw, err := c.Call(ctx, key, "settings."+settingKey+".callback")
	if err != nil {
		return "", err
	}
	var out any
	if json.Unmarshal(raw, &out) == nil {
		if out == nil {
			// A callback that returns nothing carries no message. Passing the
			// JSON literal "null" on would show that word as if the source had
			// said it; the panel already has "已完成" for an empty answer.
			return "", nil
		}
		if text, ok := out.(string); ok {
			return text, nil
		}
	}
	return strings.TrimSpace(string(raw)), nil
}

// Translations reads the UI strings a source declares for this client's locale.
//
// A source writes `title: "domain"` and supplies the readable text in its
// translation table; without it the settings panel shows the raw key.
func (c *Core) Translations(ctx context.Context, key string) (map[string]string, error) {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return nil, err
	}
	return rt.Translations(ctx, key)
}

// AccountStyles says how a source logs in. Three styles exist in the official
// sources: a password (account.login), a host webview
// (account.loginWithWebview) and a set of cookies the user pastes in
// (account.loginWithCookies).
type AccountStyles struct {
	Password bool
	Webview  bool
	Cookies  bool
	Logout   bool
}

// AccountSupport reads which login styles a source declares.
//
// Without this the interface could not tell a source that needs an account from
// one that does not, and fifteen official sources were unreachable past their
// public pages. The styles matter: ehentai has no password login at all, only a
// webview and a cookie form.
func (c *Core) AccountSupport(ctx context.Context, key string) AccountStyles {
	var out AccountStyles
	rt, err := c.reg.Comic(key)
	if err != nil {
		return out
	}
	has := func(path string) bool {
		ok, err := rt.Has(ctx, key, path)
		return err == nil && ok
	}
	out.Password = has("account.login")
	out.Webview = has("account.loginWithWebview")
	out.Cookies = has("account.loginWithCookies.validate")
	out.Logout = has("account.logout")
	return out
}

// AccountCookieFields lists the cookie names a source wants for its cookie
// login; empty when it does not offer that style.
func (c *Core) AccountCookieFields(ctx context.Context, key string) ([]string, error) {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return nil, err
	}
	return rt.AccountLoginFields(ctx, key)
}

// LoginWithCookies hands the pasted cookie values to the source's own validate,
// which sets them and answers whether they work. A false answer is not an
// error: the source looked and found them unusable.
func (c *Core) LoginWithCookies(ctx context.Context, key string, values []string) (bool, error) {
	if values == nil {
		values = []string{}
	}
	raw, err := c.Call(ctx, key, "account.loginWithCookies.validate", values)
	if err != nil {
		return false, err
	}
	var ok bool
	if err := json.Unmarshal(raw, &ok); err != nil {
		return false, fmt.Errorf("core: account.loginWithCookies.validate: %w", err)
	}
	return ok, nil
}

// LoginAccount calls the source's own account.login and returns what it said.
// A source that refuses answers with its own text, which the interface shows
// verbatim: the reason usually names the field it did not like.
func (c *Core) LoginAccount(ctx context.Context, key, account, password string) (string, error) {
	raw, err := c.Call(ctx, key, "account.login", account, password)
	if err != nil {
		return "", err
	}
	// A source answers "ok" or some short phrase; anything else is passed on as
	// JSON text rather than dropped. A login that answers nothing is no message,
	// not the word "null".
	var out any
	if json.Unmarshal(raw, &out) == nil {
		if out == nil {
			return "", nil
		}
		if text, ok := out.(string); ok {
			return text, nil
		}
	}
	return strings.TrimSpace(string(raw)), nil
}

// LogoutAccount calls account.logout when the source declares one.
func (c *Core) LogoutAccount(ctx context.Context, key string) error {
	_, err := c.Call(ctx, key, "account.logout")
	return err
}

// ErrNoTagAction reports a source that does not say what a tapped tag means.
var ErrNoTagAction = errors.New("core: source does not handle tag taps")

// TagAction is what a source wants done when a tag is tapped: run a search for
// Keyword, or open the category page whose Param belongs to this source.
type TagAction struct {
	// Action is "search" or "category".
	Action  string
	Keyword string
	// Param is only set for "category"; a source may leave it null.
	Param string
}

// LinkDomains lists the host names a source claims for `comic.link`, such as
// nhentai.net. A source without a link declaration has none.
func (c *Core) LinkDomains(ctx context.Context, key string) []string {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return nil
	}
	// `comic.link` is an object, not a function: reading it is a property read,
	// and calling it would answer "source does not define this function".
	raw, err := rt.Value(ctx, key, "comic.link")
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var f struct {
		Domains []string `json:"domains"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return f.Domains
}

// LinkToID asks a source to read a comic id out of one of its own links.
//
// A source answers null for a link it does not recognise, which is an ordinary
// "no" rather than a failure: several sources are asked about the same address.
func (c *Core) LinkToID(ctx context.Context, key, raw string) (string, error) {
	if !c.Has(ctx, key, "comic.link.linkToId") {
		return "", nil
	}
	out, err := c.Call(ctx, key, "comic.link.linkToId", raw)
	if err != nil {
		return "", err
	}
	var id flexString
	if json.Unmarshal(out, &id) != nil {
		return "", nil
	}
	return strings.TrimSpace(string(id)), nil
}

// OpenLink asks every source that claims a host whether a pasted address is one
// of its own comics, and returns the source key and comic id that recognised it.
//
// Eight sources declare `comic.link`: pasting their address is what a reader does
// when a link arrives in a chat, and searching for the URL as a keyword finds
// nothing. The first source that answers wins; the rest are not asked.
func (c *Core) OpenLink(ctx context.Context, raw string) (key, id string, err error) {
	host := hostOf(raw)
	if host == "" {
		return "", "", nil
	}
	for _, record := range c.reg.List() {
		if record.Type != "comic" || !record.Enabled {
			continue
		}
		claimed := c.LinkDomains(ctx, record.Key)
		if !claims(claimed, host) {
			continue
		}
		found, err := c.LinkToID(ctx, record.Key, raw)
		if err != nil || found == "" {
			continue
		}
		return record.Key, found, nil
	}
	return "", "", nil
}

// hostOf reads the host of an address the user pasted, with or without a scheme.
func hostOf(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" || strings.ContainsAny(text, " 	") {
		return ""
	}
	if !strings.Contains(text, "://") {
		if !strings.Contains(text, ".") {
			return ""
		}
		text = "https://" + text
	}
	u, err := url.Parse(text)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// claims reports whether a source lists host, ignoring a "www." prefix on either
// side: a link that arrives with it is the same site.
func claims(domains []string, host string) bool {
	trim := func(s string) string { return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "www.") }
	want := trim(host)
	for _, d := range domains {
		if trim(d) == want {
			return true
		}
	}
	return false
}

type WebviewLogin = js.WebviewLogin

func (c *Core) AccountWebviewLogin(ctx context.Context, key string) (WebviewLogin, error) {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return WebviewLogin{}, err
	}
	return rt.AccountWebviewLogin(ctx, key)
}

// WebviewStatus runs the source's checkStatus for the page the browser is on.
func (c *Core) WebviewStatus(ctx context.Context, key, handle, pageURL, title string) (bool, error) {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return false, err
	}
	return rt.WebviewStatus(ctx, key, handle, pageURL, title)
}

// WebviewLoginDone runs the source's onLoginSuccess, when it wrote one.
func (c *Core) WebviewLoginDone(ctx context.Context, key, handle string) error {
	rt, err := c.reg.Comic(key)
	if err != nil {
		return err
	}
	return rt.WebviewLoginDone(ctx, key, handle)
}

// ClickTag runs comic.onClickTag(namespace, tag).
//
// Sources put their own meaning on a tag: `copy_manga` searches "作者:<name>",
// `manhuagui` maps 类型 to a category parameter. A source that cannot handle
// the tag throws, and that message is the most useful thing to show.
func (c *Core) ClickTag(ctx context.Context, key, namespace, tag string) (TagAction, error) {
	if !c.Has(ctx, key, "comic.onClickTag") {
		return TagAction{}, ErrNoTagAction
	}
	raw, err := c.Call(ctx, key, "comic.onClickTag", namespace, tag)
	if err != nil {
		return TagAction{}, err
	}
	var out struct {
		Action  string `json:"action"`
		Keyword string `json:"keyword"`
		Param   any    `json:"param"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return TagAction{}, fmt.Errorf("core: onClickTag: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(out.Action))
	if action == "" {
		return TagAction{}, ErrNoTagAction
	}
	param := ""
	if out.Param != nil {
		param = fmt.Sprint(out.Param)
	}
	return TagAction{Action: action, Keyword: out.Keyword, Param: param}, nil
}

// AppVersion is what sources see and what index entries are compared with.
func (c *Core) AppVersion() string { return c.opts.AppVersion }

// FetchThumbnail is FetchImage through a source's cover hook.
func (c *Core) FetchThumbnail(ctx context.Context, key, rawURL, comicID string) ([]byte, string, error) {
	req, err := c.ImageRequest(ctx, key, rawURL, comicID, "", true)
	if err != nil {
		return nil, "", err
	}
	return c.doImage(ctx, key, req)
}

// MaxImageBytes bounds one image.
const MaxImageBytes = 32 << 20

// FetchImage downloads one image through the source's rules and returns its
// bytes and content type.
func (c *Core) FetchImage(ctx context.Context, key, rawURL, comicID, chapterID string) ([]byte, string, error) {
	req, err := c.ImageRequest(ctx, key, rawURL, comicID, chapterID, false)
	if err != nil {
		return nil, "", err
	}
	return c.doImage(ctx, key, req)
}

// DoImageRequest fetches an image a source has already described, the way
// FetchImage would after asking the source. A caller that needs to inspect the
// resolved address first — the reader page, which must not fetch anything but
// http(s) — resolves with ImageRequest and then calls this.
func (c *Core) DoImageRequest(ctx context.Context, key string, req ImageRequest) ([]byte, string, error) {
	return c.doImage(ctx, key, req)
}

func (c *Core) doImage(ctx context.Context, key string, req ImageRequest) ([]byte, string, error) {
	// A config may name several mirrors and a body transform. Both live in the
	// source's runtime, so a failure asks for the next mirror and a success
	// hands the body to onResponse. The hop count bounds a source that keeps
	// answering with another failing address.
	const maxHops = 4
	var lastErr error
	for hop := 0; hop < maxHops; hop++ {
		data, ctype, err := c.fetchImageOnce(ctx, key, req)
		if err == nil && looksLikeHTML(data) && req.HasCallback("onLoadFailed") {
			// A mirror can answer 200 with an error page. The official client
			// treats that as a failed load because decoding it fails, and asks
			// the source for the next mirror; here the fetch itself succeeded,
			// so the body is what has to say so.
			err = errors.New("core: image response is an HTML page")
		}
		if err == nil {
			// Source transforms are part of this load attempt. Their errors
			// must reach the same bounded fallback loop as transport errors.
			data, ctype, err = c.applyImageTransforms(ctx, key, req, data, ctype)
			if err == nil {
				return data, ctype, nil
			}
		}
		lastErr = err
		if req.Handle == "" || !req.HasCallback("onLoadFailed") {
			return nil, "", err
		}
		rt, rerr := c.reg.Comic(key)
		if rerr != nil {
			return nil, "", err
		}
		next, ferr := rt.ImageFallback(ctx, key, req.Handle)
		if ferr != nil || next.URL == "" {
			return nil, "", err
		}
		// The callback returned a replacement config, not a patch. Omitted
		// fields use fresh-request defaults; in particular, neither the old
		// POST body nor its pixel script belongs to the next mirror.
		req = ImageRequest{
			URL: next.URL, Method: "GET", Headers: next.Headers, Body: []byte(next.Data),
			Handle: next.Handle, Callbacks: next.Callbacks, ModifyImage: next.ModifyImage, baseURL: c.imageURL(key, req),
		}
		if next.Method != "" {
			req.Method = strings.ToUpper(next.Method)
		}
	}
	return nil, "", lastErr
}

// applyImageTransforms handles one response. Mirror selection stays in doImage
// so response and pixel failures follow the source's onLoadFailed contract.
func (c *Core) applyImageTransforms(ctx context.Context, key string, req ImageRequest, data []byte, ctype string) ([]byte, string, error) {
	if req.Handle != "" && req.HasCallback("onResponse") {
		rt, err := c.reg.Comic(key)
		if err != nil {
			return nil, "", err
		}
		got, err := rt.ApplyImageResponse(ctx, key, req.Handle, data)
		if err != nil && !errors.Is(err, js.ErrNoImageCallback) {
			return nil, "", err
		}
		if err == nil {
			// The source's bytes need not match the server's content type.
			data, ctype = got, ""
		}
	}
	if req.ModifyImage == "" {
		return data, ctype, nil
	}
	fixed, err := c.modifyImage(ctx, key, req.ModifyImage, data)
	if errors.Is(err, errUndecodable) {
		// Preserve the existing pass-through for formats this Go build cannot
		// decode (such as AVIF) but the native reader may still display.
		return data, ctype, nil
	}
	if err != nil {
		return nil, "", err
	}
	return fixed, "image/png", nil
}

// looksLikeHTML reports whether a body is a web page rather than an image. A
// mirror that is out of quota answers 200 with an error page, and the source is
// the only one that knows another mirror.
func looksLikeHTML(data []byte) bool {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	head = bytes.TrimLeft(head, " \t\r\n\ufeff")
	lower := bytes.ToLower(head)
	for _, prefix := range []string{"<!doctype html", "<html", "<?xml", "<head", "<body"} {
		if bytes.HasPrefix(lower, []byte(prefix)) {
			return true
		}
	}
	return false
}

// fetchImageOnce performs one hop of a config: one address, one request.
func (c *Core) imageURL(key string, req ImageRequest) string {
	base := req.baseURL
	if host.ValidateURL(base) != nil {
		base = c.tr.SchemeOf(ComicPartition(key)) + "://"
	}
	return host.NormalizeURL(req.URL, base)
}

func (c *Core) fetchImageOnce(ctx context.Context, key string, req ImageRequest) ([]byte, string, error) {
	// A source may hand back an image address without its scheme, such as
	// "//images.example.com/1.jpg". RFC 3986 says such an address inherits
	// the scheme of the page, so the scheme this source last used is the one
	// to use. The result still has to be an http(s) URL: nothing else gets
	// through.
	req.URL = c.imageURL(key, req)
	if err := host.ValidateURL(req.URL); err != nil {
		// An empty or relative address here means the source returned no image
		// URL at all, which the reader reports only as "not http(s)". Naming the
		// address in the log is what makes that actionable.
		c.appendLog(key, "error", "Core", "image URL is not http(s): "+strconv.Quote(req.URL))
		return nil, "", errors.New("core: image URL is not http(s)")
	}
	header := http.Header{}
	for k, v := range req.Headers {
		header.Set(k, v)
	}
	resp, err := c.tr.Do(ctx, &host.Request{Method: req.Method, URL: req.URL, Header: header, Body: req.Body,
		Cookies: true, Partition: ComicPartition(key)})
	if err != nil {
		return nil, "", err
	}
	if resp.Status < 200 || resp.Status > 299 {
		return nil, "", fmt.Errorf("core: image HTTP %d", resp.Status)
	}
	if len(resp.Body) > MaxImageBytes {
		return nil, "", ErrTooLarge
	}
	ctype := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(ctype, ';'); i >= 0 {
		ctype = ctype[:i]
	}
	return resp.Body, strings.TrimSpace(ctype), nil
}

// --- parsing ---

func parsePage(raw json.RawMessage) (Page, error) {
	var envelope struct {
		Comics  json.RawMessage `json:"comics"`
		MaxPage json.Number     `json:"maxPage"`
		Next    string          `json:"next"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Page{}, fmt.Errorf("core: search.load: %w", err)
	}
	var items []json.RawMessage
	if len(envelope.Comics) > 0 {
		// One malformed comic must not hide the rest of the page.
		_ = json.Unmarshal(mustArray(envelope.Comics), &items)
	}
	p := Page{Next: envelope.Next}
	if n, err := envelope.MaxPage.Int64(); err == nil {
		p.MaxPage = int(n)
	}
	seen := map[string]bool{}
	for _, item := range items {
		c := parseComic(item)
		if c.ID == "" || seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		p.Comics = append(p.Comics, c)
	}
	return p, nil
}

func parseComic(raw json.RawMessage) Comic {
	var c Comic
	var f struct {
		ID          flexString `json:"id"`
		Title       flexString `json:"title"`
		Subtitle    flexString `json:"subtitle"`
		SubTitle    flexString `json:"subTitle"`
		Cover       flexString `json:"cover"`
		Tags        []string   `json:"tags"`
		Description flexString `json:"description"`
		FavoriteID  flexString `json:"favoriteId"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return Comic{}
	}
	c.FavoriteID = string(f.FavoriteID)
	c.ID, c.Title, c.Cover, c.Description = string(f.ID), string(f.Title), string(f.Cover), string(f.Description)
	c.Subtitle = string(f.Subtitle)
	if c.Subtitle == "" {
		c.Subtitle = string(f.SubTitle)
	}
	c.Tags = f.Tags
	return c
}

func parseDetails(raw json.RawMessage) (Details, error) {
	var f struct {
		Title       flexString      `json:"title"`
		Subtitle    flexString      `json:"subtitle"`
		SubTitle    flexString      `json:"subTitle"`
		Cover       flexString      `json:"cover"`
		Description flexString      `json:"description"`
		Tags        json.RawMessage `json:"tags"`
		Chapters    json.RawMessage `json:"chapters"`
		FavoriteID  flexString      `json:"favoriteId"`
		SubID       flexString      `json:"subId"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return Details{}, fmt.Errorf("core: comic.loadInfo: %w", err)
	}
	d := Details{Title: string(f.Title), Cover: string(f.Cover), Description: string(f.Description),
		FavoriteID: string(f.FavoriteID), SubID: string(f.SubID)}
	d.Subtitle = string(f.Subtitle)
	if d.Subtitle == "" {
		d.Subtitle = string(f.SubTitle)
	}
	for _, field := range objectFields(f.Tags) {
		var values []string
		if err := json.Unmarshal(field.raw, &values); err == nil {
			d.Tags = append(d.Tags, TagGroup{Name: field.name, Values: values})
		}
	}
	// Chapters are either a flat {id: title} or grouped
	// {group: {id: title}}; the source's key order is reading order.
	for _, field := range objectFields(f.Chapters) {
		var nested []rawField
		if len(field.raw) > 0 && field.raw[0] == '{' {
			nested = objectFields(field.raw)
		}
		if len(nested) == 0 {
			d.Chapters = append(d.Chapters, Chapter{ID: field.name, Title: field.text()})
			continue
		}
		for _, n := range nested {
			d.Chapters = append(d.Chapters, Chapter{ID: n.name, Title: n.text(), Group: field.name})
		}
	}
	return d, nil
}

type rawField struct {
	name string
	raw  json.RawMessage
}

// text is the field's value as text: a string, or a number as written.
func (f rawField) text() string {
	var s string
	if err := json.Unmarshal(f.raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(f.raw), `"`)
}

// objectFields decodes a JSON object in document order, so key order
// survives (chapters depend on it).
func objectFields(raw json.RawMessage) []rawField {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil
	}
	var out []rawField
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return out
		}
		key, _ := keyTok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return out
		}
		out = append(out, rawField{name: key, raw: value})
	}
	return out
}

func mustArray(raw json.RawMessage) json.RawMessage {
	if len(raw) > 0 && raw[0] == '[' {
		return raw
	}
	return json.RawMessage("[]")
}

// flexString accepts a JSON string or number.
type flexString string

func (f *flexString) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*f = flexString(n.String())
		return nil
	}
	*f = ""
	return nil
}

const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// maxModifyPixels bounds a bitmap handed to a source's modifyImage script.
// Decoding a very large image just to reorder it costs far more memory than
// the reader will ever show, so a bigger one is passed through untouched.
const maxModifyPixels = 64 << 20

// errUndecodable marks a body this build cannot decode, which is not the
// source's mistake.
var errUndecodable = errors.New("core: image format not supported")

// modifyImage runs a source's pixel-buffer script and returns PNG bytes.
//
// The script works on RGBA and Core only moves pixels; decoding the fetched
// bytes and encoding the result happen here, which is what the comic/image
// package leaves to the client.
func (c *Core) modifyImage(ctx context.Context, key, script string, data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errUndecodable
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 || int64(w)*int64(h) > maxModifyPixels {
		return nil, fmt.Errorf("core: modifyImage: %dx%d is out of range", w, h)
	}
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, a := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			i := (y*w + x) * 4
			pix[i], pix[i+1], pix[i+2], pix[i+3] = byte(r>>8), byte(g>>8), byte(b>>8), byte(a>>8)
		}
	}
	rt, err := c.reg.Comic(key)
	if err != nil {
		return nil, err
	}
	out, err := rt.ModifyImage(ctx, script, coreimage.Bitmap{W: w, H: h, Pix: pix})
	if err != nil {
		return nil, err
	}
	rgba := &image.RGBA{Pix: out.Pix, Stride: out.W * 4, Rect: image.Rect(0, 0, out.W, out.H)}
	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		return nil, fmt.Errorf("core: modifyImage: %w", err)
	}
	return buf.Bytes(), nil
}
