// Package mobile is the gomobile surface of VeneraCat Core for Swift (iOS)
// and other foreign callers.
//
// gomobile only exports basic types, so every call takes and returns JSON
// text. One Core value owns a JavaScript runtime and a plugin registry. The
// client implements Host to provide HTTP, Cloudflare verification, storage
// and logging; Core never opens connections itself. A video play result with
// "sniff" is resolved by the client's own WebView.
//
// Build for Apple platforms (needs Xcode, so it runs on macOS):
//
//	go install golang.org/x/mobile/cmd/gomobile@latest
//	gomobile init
//	gomobile bind -target=ios,iossimulator,macos -o VeneraCatCore.xcframework ./mobile
package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/laincat/veneracat-core/backup"
	"github.com/laincat/veneracat-core/comic/js"
	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/plugin"
	"github.com/laincat/veneracat-core/subscription"
	"github.com/laincat/veneracat-core/video"
)

// Version is the Core API level.
const Version = js.CoreVersion

// Host is implemented by the client. Methods are called from Core goroutines
// and may block; each must return a JSON document (see the types below) or
// an error.
type Host interface {
	// HTTP sends a request: {"id","method","url","headers":{k:[v]},
	// "bodyBase64","cookies":bool,"partition"} and returns {"status",
	// "headers":{k:[v]},"bodyBase64","finalUrl"}. The client must apply its
	// own timeout; Core gives up waiting after the call deadline.
	HTTP(requestJSON string) (string, error)
	// CancelHTTP tells the client that Core no longer waits for request id
	// (the call was cancelled or timed out). The client should abort it.
	CancelHTTP(id int64)
	// Verify shows a challenge {"kind","url","partition","absentMarkers",
	// "requiredCookies"} in a WebView and returns {"userAgent"} after the
	// user passed it. Return an error containing "dismissed" when the user
	// gives up.
	Verify(challengeJSON string) (string, error)
	// Storage for source data: Load returns the JSON value or "" when absent.
	LoadData(key, dataKey string) (string, error)
	SaveData(key, dataKey, valueJSON string) error
	DeleteData(key, dataKey string) error
	LoadSetting(key, settingKey string) (string, error)
	IsLogged(key string) bool
	// Log receives script and Core log lines.
	Log(level, title, content string)

	// Cookies. The client keeps one cookie jar per partition, the same jar
	// HTTP uses when "cookies" is true and Verify fills after a challenge.
	// A cookie is {"name","value","domain","path","expires","maxAge",
	// "secure","httpOnly"}: expires is Unix milliseconds, maxAge seconds,
	// both 0 when absent. A domain with a leading dot also matches
	// subdomains; without one the cookie is host-only.
	//
	// LoadCookies returns a JSON array of the cookies the jar would send to
	// url. SaveCookies stores a JSON array received for url. DeleteCookies
	// removes every cookie that matches url.
	LoadCookies(partition, url string) (string, error)
	SaveCookies(partition, url, cookiesJSON string) error
	DeleteCookies(partition, url string) error

	// Source-driven UI. Methods that wait for the user block until they
	// answer; Core never calls them on the thread that runs scripts, so the
	// client may wait for its main thread. A result of "" means the user
	// closed the UI without choosing.
	//
	// ShowMessage shows a short toast.
	ShowMessage(message string)
	// ShowDialog shows title and content with the buttons in actionsJSON,
	// [{"text","style"}] where style is "text", "filled" or "danger". It
	// returns {"index":n} for the tapped button; Core then runs that
	// button's script callback.
	ShowDialog(title, content, actionsJSON string) (string, error)
	// ShowInputDialog asks for one line of text and returns {"text":"..."}.
	// image is an optional image URL (for example a captcha). errorMessage
	// is "" the first time; when the script rejects the text, Core asks
	// again with its message.
	ShowInputDialog(title, image, errorMessage string) (string, error)
	// ShowSelectDialog lets the user pick one of optionsJSON (an array of
	// strings) and returns {"index":n}. initialIndex is -1 for none.
	ShowSelectDialog(title, optionsJSON string, initialIndex int64) (string, error)
	// LaunchURL opens url outside the app.
	LaunchURL(url string)
	// ShowLoading shows a loading indicator tagged with id until
	// CancelLoading(id). When the user dismisses it, the client calls
	// Core.LoadingDismissed(id).
	ShowLoading(id int64)
	CancelLoading(id int64)

	// Clipboard text.
	WriteClipboard(text string) error
	ReadClipboard() (string, error)
}

// config is the JSON passed to New:
// {"appVersion","locale","platform","defaultUserAgent","memoryLimitBytes"}.
type config struct {
	AppVersion       string `json:"appVersion"`
	Locale           string `json:"locale"`
	Platform         string `json:"platform"`
	DefaultUserAgent string `json:"defaultUserAgent"`
	MemoryLimitBytes int64  `json:"memoryLimitBytes"`
}

// Core is one profile's engine.
type Core struct {
	host       Host
	bridge     *hostBridge
	appVersion string
	rt         *js.Runtime
	reg        *plugin.Registry

	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
	nextID  int64
}

// New creates a Core.
func New(configJSON string, h Host) (_ *Core, err error) {
	defer guard(&err)
	if h == nil {
		return nil, errors.New("mobile: host is required")
	}
	var cfg config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("mobile: config: %w", err)
	}
	bridge := &hostBridge{h: h, loading: map[int64]func(){}}
	rt, err := js.New(js.Options{MemoryLimit: cfg.MemoryLimitBytes}, js.Services{
		Storage:   bridge,
		Env:       js.Environment{Locale: cfg.Locale, Platform: cfg.Platform, AppVersion: cfg.AppVersion, DefaultUA: cfg.DefaultUserAgent},
		Log:       h.Log,
		Transport: bridge,
		Verifier:  bridge,
		Cookies:   cookieBridge{bridge},
		UI:        uiBridge{bridge},
		Clipboard: clipboardBridge{bridge},
	})
	if err != nil {
		return nil, err
	}
	return &Core{host: h, bridge: bridge, appVersion: cfg.AppVersion, rt: rt, reg: plugin.New(rt, bridge), cancels: map[int64]context.CancelFunc{}}, nil
}

// Close releases the engine.
func (c *Core) Close() (err error) {
	defer guard(&err)
	return c.rt.Close()
}

// NewCall reserves a call ID the client can pass to Cancel. Methods that take
// a callID stop when it is cancelled. Pass 0 for calls that are not
// cancellable.
func (c *Core) NewCall() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

// Cancel stops a running call.
func (c *Core) Cancel(callID int64) {
	c.mu.Lock()
	cancel := c.cancels[callID]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// LoadingDismissed tells Core the user dismissed the loading indicator id
// (see Host.ShowLoading). Core runs the script's onCancel callback.
func (c *Core) LoadingDismissed(id int64) {
	c.bridge.loadingDismissed(id)
}

func (c *Core) ctx(callID int64) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	if callID == 0 {
		return ctx, cancel
	}
	c.mu.Lock()
	c.cancels[callID] = cancel
	c.mu.Unlock()
	return ctx, func() {
		cancel()
		c.mu.Lock()
		delete(c.cancels, callID)
		c.mu.Unlock()
	}
}

func encode(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// --- plugins ---------------------------------------------------------------

// Install installs a plugin: type is "comic", "video-js" or "kazumi-rule".
func (c *Core) Install(callID int64, typ, body, origin, indexURL, sha256 string) (_ string, err error) {
	defer guard(&err)
	ctx, done := c.ctx(callID)
	defer done()
	rec, err := c.reg.Install(ctx, plugin.Type(typ), body, origin, indexURL, sha256)
	if err != nil {
		return "", err
	}
	return encode(rec)
}

// Replace updates an installed plugin; the old version stays on failure.
func (c *Core) Replace(callID int64, key, typ, body, origin, indexURL, sha256 string) (_ string, err error) {
	defer guard(&err)
	ctx, done := c.ctx(callID)
	defer done()
	rec, err := c.reg.Replace(ctx, key, plugin.Type(typ), body, origin, indexURL, sha256)
	if err != nil {
		return "", err
	}
	return encode(rec)
}

// EnableBuiltin installs a built-in provider such as "builtin_hongguo".
func (c *Core) EnableBuiltin(key string) (_ string, err error) {
	defer guard(&err)
	rec, err := c.reg.EnableBuiltin(key)
	if err != nil {
		return "", err
	}
	return encode(rec)
}

// Restore re-activates persisted records (a JSON array of records) and
// returns {key: error} for the ones that failed.
func (c *Core) Restore(recordsJSON string) (_ string, err error) {
	defer guard(&err)
	var recs []plugin.Record
	if err := json.Unmarshal([]byte(recordsJSON), &recs); err != nil {
		return "", err
	}
	failed := c.reg.Restore(context.Background(), recs)
	out := map[string]string{}
	for k, e := range failed {
		out[k] = e.Error()
	}
	return encode(out)
}

// SetEnabled turns a plugin on or off.
func (c *Core) SetEnabled(key string, on bool) (err error) {
	defer guard(&err)
	return c.reg.SetEnabled(context.Background(), key, on)
}

// Remove uninstalls a plugin.
func (c *Core) Remove(key string) (err error) {
	defer guard(&err)
	return c.reg.Remove(context.Background(), key)
}

// Plugins lists installed plugins as JSON records, without script bodies.
func (c *Core) Plugins() (_ string, err error) {
	defer guard(&err)
	list := c.reg.List()
	for i := range list {
		list[i].Body = ""
	}
	return encode(list)
}

// Records lists installed plugins with bodies, for the client to persist.
func (c *Core) Records() (_ string, err error) {
	defer guard(&err)
	return encode(c.reg.List())
}

// --- comic sources ---------------------------------------------------------

// CallComic calls ComicSource.sources[key].<path>(args...) where argsJSON is
// a JSON array, and returns the JSON result.
func (c *Core) CallComic(callID int64, key, path, argsJSON string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	var args []json.RawMessage
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("mobile: args must be a JSON array: %w", err)
		}
	}
	vals := make([]any, len(args))
	for i, a := range args {
		vals[i] = a
	}
	ctx, done := c.ctx(callID)
	defer done()
	out, err := rt.Call(ctx, key, path, vals...)
	return string(out), err
}

// SearchOptions returns a comic source's search.optionList as JSON:
// [{"label","type","values":["key",...],"labels":["text",...],"default"}].
// search.load takes one chosen value per group, so a client needs each
// group's default before it can search.
func (c *Core) SearchOptions(key string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	groups, err := rt.SearchOptions(context.Background(), key)
	if err != nil {
		return "", err
	}
	if groups == nil {
		return "[]", nil
	}
	return encode(groups)
}

// HasComic reports whether a comic source defines path (e.g. "search.load").
// An unknown source or an invalid path is false. It returns a plain bool
// because gomobile exports (bool, error) with an awkward Swift signature.
func (c *Core) HasComic(key, path string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	rt, err := c.reg.Comic(key)
	if err != nil {
		return false
	}
	ok, err = rt.Has(context.Background(), key, path)
	return ok && err == nil
}

// --- video -------------------------------------------------------------------

func (c *Core) video(key string) (video.Provider, error) { return c.reg.Video(key) }

// VideoSearch returns a video.Page as JSON.
func (c *Core) VideoSearch(callID int64, key, keyword, cursor string) (_ string, err error) {
	defer guard(&err)
	p, err := c.video(key)
	if err != nil {
		return "", err
	}
	ctx, done := c.ctx(callID)
	defer done()
	page, err := p.Search(ctx, keyword, cursor)
	if err != nil {
		return "", err
	}
	return encode(page)
}

// VideoCategories lists browsable sections, or "[]".
func (c *Core) VideoCategories(key string) (_ string, err error) {
	defer guard(&err)
	p, err := c.video(key)
	if err != nil {
		return "", err
	}
	if e, ok := p.(video.Explorer); ok {
		return encode(e.Categories())
	}
	return "[]", nil
}

// VideoExplore pages through a category.
func (c *Core) VideoExplore(callID int64, key, category, cursor string) (_ string, err error) {
	defer guard(&err)
	p, err := c.video(key)
	if err != nil {
		return "", err
	}
	e, ok := p.(video.Explorer)
	if !ok {
		return "", errors.New("mobile: this video plugin has no catalogue")
	}
	ctx, done := c.ctx(callID)
	defer done()
	page, err := e.Explore(ctx, category, cursor)
	if err != nil {
		return "", err
	}
	return encode(page)
}

// VideoDetails returns video.Details as JSON.
func (c *Core) VideoDetails(callID int64, key, id string) (_ string, err error) {
	defer guard(&err)
	p, err := c.video(key)
	if err != nil {
		return "", err
	}
	ctx, done := c.ctx(callID)
	defer done()
	d, err := p.Details(ctx, id)
	if err != nil {
		return "", err
	}
	return encode(d)
}

// VideoPlay returns video.Play as JSON. When it contains "sniff" the client
// resolves the media URL with its WebView.
func (c *Core) VideoPlay(callID int64, key, itemID, lineID, episodeID string) (_ string, err error) {
	defer guard(&err)
	p, err := c.video(key)
	if err != nil {
		return "", err
	}
	ctx, done := c.ctx(callID)
	defer done()
	play, err := p.Play(ctx, itemID, lineID, episodeID)
	if err != nil {
		return "", err
	}
	return encode(play)
}

// --- subscriptions and backups --------------------------------------------------

// ParseComicIndex parses an index.json document fetched from indexURL.
func (c *Core) ParseComicIndex(data, indexURL string) (_ string, err error) {
	defer guard(&err)
	idx, err := subscription.ParseComic([]byte(data), indexURL)
	if err != nil {
		return "", err
	}
	return encode(idx)
}

// CompareComicIndex parses an index.json document fetched from indexURL and
// returns its entries with a status against the installed comic sources:
// {"rows":[{...entry, "status", "installedVersion"}], "problems":[...]}.
// status is "new", "update",
// "installed", "other" (installed from another index) or "too-new" (needs
// a newer app). Versions use the official comparison rules.
func (c *Core) CompareComicIndex(data, indexURL string) (_ string, err error) {
	defer guard(&err)
	idx, err := subscription.ParseComic([]byte(data), indexURL)
	if err != nil {
		return "", err
	}
	var installed []subscription.Installed
	for _, r := range c.reg.List() {
		if r.Type == plugin.TypeComic {
			installed = append(installed, subscription.Installed{Key: r.Key, Version: r.Version, IndexURL: r.IndexURL})
		}
	}
	return encode(struct {
		Rows     []subscription.Row     `json:"rows"`
		Problems []subscription.Problem `json:"problems,omitempty"`
	}{idx.Compare(indexURL, c.appVersion, installed), idx.Problems})
}

// ParseVideoIndex parses a vcvideo.json document fetched from indexURL.
func (c *Core) ParseVideoIndex(data, indexURL string) (_ string, err error) {
	defer guard(&err)
	idx, err := subscription.Parse([]byte(data), indexURL)
	if err != nil {
		return "", err
	}
	return encode(idx)
}

// ReadBackup reads a .venera file at path and returns the preview content.
// knownKeysJSON is a JSON array of installed source keys.
func (c *Core) ReadBackup(path, tempDir, knownKeysJSON string) (_ string, err error) {
	defer guard(&err)
	content, err := readBackup(path, tempDir, knownKeysJSON)
	if err != nil {
		return "", err
	}
	return encode(content)
}

// PlanBackup builds a restore plan. selectionJSON mirrors backup.Selection
// with lower-case keys; the plan includes cookies only if selected.
func (c *Core) PlanBackup(path, tempDir, knownKeysJSON, selectionJSON string) (_ string, err error) {
	defer guard(&err)
	content, err := readBackup(path, tempDir, knownKeysJSON)
	if err != nil {
		return "", err
	}
	var sel struct {
		History, Favorites, ImageFavorites, Settings, SearchHistory, Sources, SourceData, Cookies bool
		Folders                                                                                   []string
	}
	if err := json.Unmarshal([]byte(selectionJSON), &sel); err != nil {
		return "", err
	}
	plan, err := content.Plan(backup.Selection{History: sel.History, Favorites: sel.Favorites, ImageFavorites: sel.ImageFavorites,
		Settings: sel.Settings, SearchHistory: sel.SearchHistory, Sources: sel.Sources, SourceData: sel.SourceData,
		Cookies: sel.Cookies, Folders: sel.Folders})
	if err != nil {
		return "", err
	}
	return encode(plan)
}

// --- host bridge ------------------------------------------------------------------

// hostBridge adapts the JSON Host to Core's Go interfaces.
type hostBridge struct {
	h      Host
	nextID atomic.Int64

	loadingMu   sync.Mutex
	loading     map[int64]func()
	nextLoading int64
}

type wireRequest struct {
	ID         int64       `json:"id"`
	Method     string      `json:"method"`
	URL        string      `json:"url"`
	Headers    http.Header `json:"headers,omitempty"`
	BodyBase64 string      `json:"bodyBase64,omitempty"`
	Cookies    bool        `json:"cookies"`
	Partition  string      `json:"partition,omitempty"`
}

type wireResponse struct {
	Status     int         `json:"status"`
	Headers    http.Header `json:"headers"`
	BodyBase64 string      `json:"bodyBase64"`
	FinalURL   string      `json:"finalUrl"`
}

func (b *hostBridge) Do(ctx context.Context, r *host.Request) (*host.Response, error) {
	id := b.nextID.Add(1)
	req := wireRequest{ID: id, Method: r.Method, URL: r.URL, Headers: r.Header, Cookies: r.Cookies, Partition: r.Partition}
	if len(r.Body) > 0 {
		req.BodyBase64 = base64.StdEncoding.EncodeToString(r.Body)
	}
	in, _ := json.Marshal(req)
	type result struct {
		out string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- result{"", fmt.Errorf("mobile: host HTTP panicked: %v", p)}
			}
		}()
		out, err := b.h.HTTP(string(in))
		ch <- result{out, err}
	}()
	var res result
	select {
	case res = <-ch:
	case <-ctx.Done():
		// The goroutine above ends when the client returns; ask it to abort
		// so it returns soon.
		go b.h.CancelHTTP(id)
		return nil, ctx.Err()
	}
	if res.err != nil {
		return nil, res.err
	}
	var w wireResponse
	if err := json.Unmarshal([]byte(res.out), &w); err != nil {
		return nil, fmt.Errorf("mobile: host HTTP response: %w", err)
	}
	body, err := base64.StdEncoding.DecodeString(w.BodyBase64)
	if err != nil {
		return nil, fmt.Errorf("mobile: host HTTP body: %w", err)
	}
	return &host.Response{Status: w.Status, Header: w.Headers, Body: body, FinalURL: w.FinalURL}, nil
}

func (b *hostBridge) Verify(ctx context.Context, c *host.Challenge) (*host.Clearance, error) {
	in, _ := json.Marshal(map[string]any{"kind": c.Kind, "url": c.URL, "partition": c.Partition,
		"absentMarkers": c.Done.AbsentMarkers, "requiredCookies": c.Done.RequiredCookies})
	type result struct {
		out string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		out, err := b.h.Verify(string(in))
		ch <- result{out, err}
	}()
	var res result
	select {
	case res = <-ch:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out, err := res.out, res.err
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "dismissed") {
			return nil, host.ErrChallengeDismissed
		}
		return nil, err
	}
	var w struct {
		UserAgent string `json:"userAgent"`
	}
	if err := json.Unmarshal([]byte(out), &w); err != nil {
		return nil, err
	}
	return &host.Clearance{UserAgent: w.UserAgent}, nil
}

func (b *hostBridge) LoadData(key, dataKey string) (json.RawMessage, bool, error) {
	s, err := b.h.LoadData(key, dataKey)
	if err != nil || s == "" {
		return nil, false, err
	}
	return json.RawMessage(s), true, nil
}
func (b *hostBridge) SaveData(key, dataKey string, v json.RawMessage) error {
	return b.h.SaveData(key, dataKey, string(v))
}
func (b *hostBridge) DeleteData(key, dataKey string) error { return b.h.DeleteData(key, dataKey) }
func (b *hostBridge) LoadSetting(key, settingKey string) (json.RawMessage, bool, error) {
	s, err := b.h.LoadSetting(key, settingKey)
	if err != nil || s == "" {
		return nil, false, err
	}
	return json.RawMessage(s), true, nil
}
func (b *hostBridge) IsLogged(key string) bool { return b.h.IsLogged(key) }

func readBackup(path, tempDir, knownKeysJSON string) (*backup.Content, error) {
	var known []string
	if knownKeysJSON != "" {
		if err := json.Unmarshal([]byte(knownKeysJSON), &known); err != nil {
			return nil, fmt.Errorf("mobile: knownKeys must be a JSON array: %w", err)
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	a, err := backup.Open(f, st.Size(), backup.Limits{})
	if err != nil {
		return nil, err
	}
	return a.Read(context.Background(), backup.Options{KnownKeys: known, TempDir: tempDir})
}

// guard turns a panic into an error. gomobile does not recover panics, so an
// unexpected one would terminate the app; every exported method defers it.
func guard(err *error) {
	if p := recover(); p != nil {
		*err = fmt.Errorf("mobile: internal error: %v", p)
	}
}
