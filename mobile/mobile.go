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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/laincat/veneracat-core/backup"
	"github.com/laincat/veneracat-core/comic/image"
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
	// NewCall reserves cancellation before Swift queues the blocking call.
	// Keeping the context closes the cancel-before-registration race.
	reserved       map[int64]context.Context
	lifetime       context.Context
	cancelLifetime context.CancelFunc
	closed         bool
	nextID         int64

	logMu sync.Mutex
	logs  []LogEntry
}

// LogEntry is one line a source printed. Core keeps the last few so a client can
// show why a source misbehaved; the same lines go to the host's Log, which on a
// phone usually means a console nobody can open.
type LogEntry struct {
	Key     string `json:"key"`
	Level   string `json:"level"`
	Title   string `json:"title"`
	Content string `json:"content"`
	// Time is Unix milliseconds: a client formats it in the user's locale.
	Time int64 `json:"time"`
}

// maxLogEntries matches the desktop client's buffer.
const maxLogEntries = 400

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
	c := &Core{host: h, bridge: bridge, appVersion: cfg.AppVersion, cancels: map[int64]context.CancelFunc{}}
	rt, err := js.New(js.Options{MemoryLimit: cfg.MemoryLimitBytes}, js.Services{
		Storage:   bridge,
		Env:       js.Environment{Locale: cfg.Locale, Platform: cfg.Platform, AppVersion: cfg.AppVersion, DefaultUA: cfg.DefaultUserAgent},
		Log:       h.Log,
		LogSource: c.appendLog,
		Transport: bridge,
		Verifier:  bridge,
		Cookies:   cookieBridge{bridge},
		UI:        uiBridge{bridge},
		Clipboard: clipboardBridge{bridge},
	})
	if err != nil {
		return nil, err
	}
	c.rt = rt
	c.reg = plugin.New(rt, bridge)
	return c, nil
}

// appendLog keeps the newest maxLogEntries lines and forwards the line to the
// host, so a client that already logs to a console keeps doing so.
func (c *Core) appendLog(key, level, title, content string) {
	c.logMu.Lock()
	c.logs = append(c.logs, LogEntry{Key: key, Level: level, Title: title, Content: content,
		Time: time.Now().UnixMilli()})
	if len(c.logs) > maxLogEntries {
		c.logs = append(c.logs[:0], c.logs[len(c.logs)-maxLogEntries:]...)
	}
	c.logMu.Unlock()
	if c.host != nil {
		c.host.Log(level, title, content)
	}
}

// LogLines returns what the sources have printed, oldest first, as JSON.
func (c *Core) LogLines() (_ string, err error) {
	defer guard(&err)
	c.logMu.Lock()
	// An empty buffer is [] rather than null: a client decodes this into an
	// array, and null is not one.
	out := make([]LogEntry, 0, len(c.logs))
	out = append(out, c.logs...)
	c.logMu.Unlock()
	return encode(out)
}

// ClearLogLines empties the buffer.
func (c *Core) ClearLogLines() {
	c.logMu.Lock()
	c.logs = nil
	c.logMu.Unlock()
}

// Close releases the engine.
func (c *Core) Close() (err error) {
	defer guard(&err)
	c.mu.Lock()
	c.closed = true
	if c.cancelLifetime != nil {
		c.cancelLifetime()
	}
	for _, cancel := range c.cancels {
		cancel()
	}
	clear(c.cancels)
	clear(c.reserved)
	c.mu.Unlock()
	return c.rt.Close()
}

// NewCall reserves a call ID the client can pass to Cancel. Methods that take
// a callID stop when it is cancelled. Pass 0 for calls that are not
// cancellable.
func (c *Core) NewCall() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	if c.reserved == nil {
		c.reserved = make(map[int64]context.Context)
	}
	if c.cancels == nil {
		c.cancels = make(map[int64]context.CancelFunc)
	}
	if c.lifetime == nil {
		c.lifetime, c.cancelLifetime = context.WithCancel(context.Background())
	}
	ctx, cancel := context.WithCancel(c.lifetime)
	if c.closed {
		cancel()
	}
	c.reserved[c.nextID] = ctx
	c.cancels[c.nextID] = cancel
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
	c.mu.Lock()
	if c.lifetime == nil {
		c.lifetime, c.cancelLifetime = context.WithCancel(context.Background())
	}
	parent := c.reserved[callID]
	if parent == nil {
		parent = c.lifetime
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	if c.closed {
		cancel()
	}
	if callID == 0 {
		c.mu.Unlock()
		return ctx, cancel
	}
	reservation := c.cancels[callID]
	c.cancels[callID] = cancel
	c.mu.Unlock()
	return ctx, func() {
		cancel()
		if reservation != nil {
			reservation()
		}
	}
}

// endCall releases the reservation and the derived context of one call.
//
// Every method that takes a callID must defer it, including the paths that
// return before the call starts: Swift reserves an id before it queues the
// blocking call, so a request for an unknown source would otherwise leave one
// entry in each map for the life of the session.
func (c *Core) endCall(callID int64) {
	if callID == 0 {
		return
	}
	c.mu.Lock()
	cancel := c.cancels[callID]
	delete(c.cancels, callID)
	delete(c.reserved, callID)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
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
	defer c.endCall(callID)
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
	defer c.endCall(callID)
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

// ExportBackup builds a .venera archive from the installed comic sources and the
// search history the client passes, and returns it base64-encoded.
//
// Base64 rather than []byte on purpose: every other method here crosses the
// bridge as a string, so the Swift side's binding shape is known. A []byte
// return would be a new shape for the bindings, and nothing on the build machine
// here can confirm how gomobile imports it.
//
// The client owns the search history, so it is an argument rather than something
// Core reads; sources come from the registry. Cookies are never included: they
// are per-source credentials and an export is a file people mail to themselves.
func (c *Core) ExportBackup(searchHistoryJSON, sourceDataJSON string) (_ string, err error) {
	defer guard(&err)
	// The host owns each source's stored data, so it arrives as
	// {"<source key>": {"<name>": <value>}}. Core cannot enumerate it itself.
	data := map[string]map[string]json.RawMessage{}
	if sourceDataJSON != "" {
		if err := json.Unmarshal([]byte(sourceDataJSON), &data); err != nil {
			return "", fmt.Errorf("mobile: source data must be an object of objects: %w", err)
		}
	}
	var history []string
	if searchHistoryJSON != "" {
		if err := json.Unmarshal([]byte(searchHistoryJSON), &history); err != nil {
			return "", fmt.Errorf("mobile: search history must be a JSON array of strings: %w", err)
		}
	}
	sources := make([]backup.WriteSource, 0, len(c.reg.List()))
	for _, record := range c.reg.List() {
		if record.Type != plugin.TypeComic || record.Body == "" {
			continue
		}
		sources = append(sources, backup.WriteSource{
			Name: record.Key + ".js",
			Body: []byte(record.Body),
			Data: data[record.Key],
		})
	}
	var buf bytes.Buffer
	if err := backup.Write(&buf, backup.WriteOptions{
		Sources:       sources,
		Settings:      map[string]json.RawMessage{},
		SearchHistory: history,
	}); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// --- comic sources ---------------------------------------------------------

// CallComic calls ComicSource.sources[key].<path>(args...) where argsJSON is
// a JSON array, and returns the JSON result.
func (c *Core) CallComic(callID int64, key, path, argsJSON string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	rt, err := c.reg.Comic(key)
	if err != nil {
		c.appendLog(key, "error", "Core", path+": "+err.Error())
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
	if err != nil {
		// The screen shows this error where it happened; the line is what tells a
		// later reader why that screen complained, which is the whole reason the
		// log panel exists. The desktop client records the same thing.
		c.appendLog(key, "error", "Core", path+": "+err.Error())
	}
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

// Settings returns a source's settings declaration as JSON:
// [{"key","title","type","default","options":[{"value","text"}],"validator"}],
// where type is "select", "switch", "input" or "callback". Values and
// defaults are JSON, because a source may declare numbers or booleans. A
// source without settings answers "[]".
func (c *Core) Settings(key string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	list, err := rt.Settings(context.Background(), key)
	if err != nil {
		return "", err
	}
	if list == nil {
		return "[]", nil
	}
	return encode(list)
}

// ExplorePages returns a comic source's browses as JSON:
// [{"title","type"}], where type is "singlePageWithMultiPart",
// "multiPartPage", "multiPageComicList" or "mixed". A source without any
// answers "[]".
func (c *Core) ExplorePages(key string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	pages, err := rt.ExplorePages(context.Background(), key)
	if err != nil {
		return "", err
	}
	if pages == nil {
		return "[]", nil
	}
	return encode(pages)
}

// ComicExplore loads one of a source's browse pages and returns
// {"sections":[{"title","comics":[...]}],"maxPage":n,"next":"token"}.
//
// cursor is the token the previous page returned ("" for the first). A browse
// may page by number or by cursor; both are handled in the core, so a client
// does not have to know which kind it is looking at.
func (c *Core) ComicExplore(callID int64, key string, index, page int, cursor string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	rt, err := c.reg.Comic(key)
	if err != nil {
		c.appendLog(key, "error", "Core", fmt.Sprintf("explore[%d]: %s", index, err))
		return "", err
	}
	ctx, done := c.ctx(callID)
	defer done()
	res, err := rt.Explore(ctx, key, index, page, cursor)
	if err != nil {
		c.appendLog(key, "error", "Core", fmt.Sprintf("explore[%d]: %s", index, err))
		return "", err
	}
	type section struct {
		Title  string            `json:"title"`
		Comics []json.RawMessage `json:"comics"`
	}
	out := struct {
		Sections []section `json:"sections"`
		MaxPage  int       `json:"maxPage"`
		Next     string    `json:"next"`
	}{MaxPage: res.MaxPage, Next: res.Next, Sections: make([]section, 0, len(res.Sections))}
	for _, s := range res.Sections {
		if len(s.Comics) == 0 {
			continue
		}
		out.Sections = append(out.Sections, section{Title: s.Title, Comics: s.Comics})
	}
	return encode(out)
}

// ClickTag runs comic.onClickTag(namespace, tag) and returns what the source
// means by the tap: {"action":"search","keyword":..} or
// {"action":"category","param":..}. An empty action means the source has no
// opinion, which is not an error.
//
// Sources put their own meaning on a tag: copy_manga searches "作者:<name>",
// manhuagui maps 类型 to a category parameter. A source that cannot handle the
// tag throws, and that message is the most useful thing to show.
func (c *Core) ClickTag(callID int64, key, namespace, tag string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	ctx, done := c.ctx(callID)
	defer done()
	ok, err := rt.Has(ctx, key, "comic.onClickTag")
	if err != nil {
		return "", err
	}
	if !ok {
		return encode(TagAction{})
	}
	raw, err := rt.Call(ctx, key, "comic.onClickTag", namespace, tag)
	if err != nil {
		c.appendLog(key, "error", "Core", "comic.onClickTag: "+err.Error())
		return "", err
	}
	var f struct {
		Action  string          `json:"action"`
		Keyword json.RawMessage `json:"keyword"`
		Param   json.RawMessage `json:"param"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", fmt.Errorf("mobile: onClickTag: %w", err)
	}
	return encode(TagAction{
		Action:  strings.ToLower(strings.TrimSpace(f.Action)),
		Keyword: textOf(f.Keyword),
		Param:   textOf(f.Param),
	})
}

// CallSettingCallback runs a setting the source declares as `type: "callback"`.
//
// The callback is a function inside the source, so nothing about it can cross by
// value: the client names the setting and the source runs it. What it answers is
// reported back as text, which is the whole point of such a button.
func (c *Core) CallSettingCallback(callID int64, key, settingKey string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	ctx, done := c.ctx(callID)
	defer done()
	path := "settings." + settingKey + ".callback"
	ok, err := rt.Has(ctx, key, path)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("mobile: %s declares no callback", settingKey)
	}
	raw, err := rt.Call(ctx, key, path)
	if err != nil {
		c.appendLog(key, "error", "Core", path+": "+err.Error())
		return "", err
	}
	var answer any
	if json.Unmarshal(raw, &answer) == nil {
		if answer == nil {
			// A callback that returns nothing carries no message. Handing back the
			// literal "null" would put that word on screen as if the source had
			// said it; an empty answer is what the clients already show as "done".
			return "", nil
		}
		if text, ok := answer.(string); ok {
			return text, nil
		}
	}
	return strings.TrimSpace(string(raw)), nil
}

// WebviewLoginDecl is what a source declares for a login on its own page, as
// JSON: {"url":"…","handle":"w1"}.
//
// The two callbacks cannot cross into JSON, so the handle names them inside the
// source's runtime; the client passes it back to WebviewStatus and
// WebviewLoginDone. An empty object means the source logs in another way.
func (c *Core) AccountWebviewLogin(key string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	ctx, done := c.ctx(0)
	defer done()
	decl, err := rt.AccountWebviewLogin(ctx, key)
	if err != nil {
		c.appendLog(key, "error", "Core", "account.loginWithWebview: "+err.Error())
		return "", err
	}
	// A source that logs in another way answers an empty object: the client
	// draws "not supported" from that, not from an empty URL.
	if decl.Handle == "" {
		return "{}", nil
	}
	return encode(decl)
}

// WebviewStatus runs the source's checkStatus for the page the browser is on.
// A source without that callback answers false: the client keeps waiting
// rather than deciding the login finished on its own.
func (c *Core) WebviewStatus(callID int64, key, handle, pageURL, title string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	ctx, done := c.ctx(callID)
	defer done()
	done2, err := rt.WebviewStatus(ctx, key, handle, pageURL, title)
	if err != nil {
		return "", err
	}
	// A JSON object rather than a bare bool: gomobile turns a Go bool plus an
	// error into a BOOL that Swift imports as `throws -> Bool`, and that shape
	// differs from every other method in this file.
	return encode(struct {
		Done bool `json:"done"`
	}{done2})
}

// WebviewLoginDone runs the source's onLoginSuccess after the browser stored its
// cookies. A source without that callback finishes normally.
func (c *Core) WebviewLoginDone(callID int64, key, handle string) (err error) {
	defer guard(&err)
	defer c.endCall(callID)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return err
	}
	ctx, done := c.ctx(callID)
	defer done()
	if err := rt.WebviewLoginDone(ctx, key, handle); err != nil {
		c.appendLog(key, "error", "Core", "account.loginWithWebview: "+err.Error())
		return err
	}
	return nil
}

// TagAction is what a source means by tapping one of its tags.
type TagAction struct {
	// Action is "search" or "category"; empty means the source has no opinion.
	Action string `json:"action"`
	// Keyword is the search to run, for action "search".
	Keyword string `json:"keyword"`
	// Param is the category parameter, for action "category".
	Param string `json:"param"`
}

// CommentSupport reports which comment functions a source declares, as JSON:
// {"load":bool,"send":bool,"like":bool}.
//
// They live on the source's `comic` object: looking for a top-level
// loadComments reports "keeps no comments" for every source that has them.
func (c *Core) CommentSupport(key string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	has := func(path string) bool {
		ok, err := rt.Has(ctx, key, path)
		return err == nil && ok
	}
	return encode(CommentSupport{
		Load: has("comic.loadComments"),
		Send: has("comic.sendComment"),
		Like: has("comic.likeComment"),
	})
}

// ComicComments loads one page of a comic's comment thread:
// {"comments":[{id,userName,avatar,content,time,replyCount,score,isLiked}],"maxPage":n}
//
// replyTo reads the replies to one comment; empty reads the thread itself. The
// empty identifiers cross as null, which is what a source tests for.
func (c *Core) ComicComments(callID int64, key, comicID, subID string, page int, replyTo string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	ctx, done := c.ctx(callID)
	defer done()
	raw, err := rt.Call(ctx, key, "comic.loadComments", comicID, nullable(subID), page, nullable(replyTo))
	if err != nil {
		c.appendLog(key, "error", "Core", "comic.loadComments: "+err.Error())
		return "", err
	}
	var f struct {
		Comments []struct {
			ID         json.RawMessage `json:"id"`
			UserName   json.RawMessage `json:"userName"`
			Avatar     json.RawMessage `json:"avatar"`
			Content    json.RawMessage `json:"content"`
			Time       json.RawMessage `json:"time"`
			ReplyCount json.RawMessage `json:"replyCount"`
			Score      json.RawMessage `json:"score"`
			IsLiked    bool            `json:"isLiked"`
		} `json:"comments"`
		MaxPage json.Number `json:"maxPage"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", fmt.Errorf("mobile: loadComments: %w", err)
	}
	out := ComicCommentPage{Comments: make([]ComicComment, 0, len(f.Comments))}
	if n, err := f.MaxPage.Int64(); err == nil {
		out.MaxPage = int(n)
	}
	for _, one := range f.Comments {
		out.Comments = append(out.Comments, ComicComment{
			ID: textOf(one.ID), UserName: textOf(one.UserName), Avatar: textOf(one.Avatar),
			Content: textOf(one.Content), Time: textOf(one.Time),
			ReplyCount: intOf(one.ReplyCount), Score: textOf(one.Score), IsLiked: one.IsLiked,
		})
	}
	return encode(out)
}

// SendComicComment posts a comment; any answer from the source means success.
func (c *Core) SendComicComment(callID int64, key, comicID, subID, content, replyTo string) (err error) {
	defer guard(&err)
	defer c.endCall(callID)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return err
	}
	ctx, done := c.ctx(callID)
	defer done()
	if _, err := rt.Call(ctx, key, "comic.sendComment", comicID, nullable(subID), content, nullable(replyTo)); err != nil {
		c.appendLog(key, "error", "Core", "comic.sendComment: "+err.Error())
		return err
	}
	return nil
}

// LikeComicComment votes on a comment. isLike false takes the vote back: it is
// the same call with the other flag, not a separate function.
func (c *Core) LikeComicComment(callID int64, key, comicID, subID, commentID string, isLike bool) (err error) {
	defer guard(&err)
	defer c.endCall(callID)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return err
	}
	ctx, done := c.ctx(callID)
	defer done()
	if _, err := rt.Call(ctx, key, "comic.likeComment", comicID, nullable(subID), commentID, isLike); err != nil {
		c.appendLog(key, "error", "Core", "comic.likeComment: "+err.Error())
		return err
	}
	return nil
}

// nullable keeps a missing identifier out of the call: a source tests for null
// and treats an empty string as a real, empty value.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CommentSupport is what a source declares about comments.
type CommentSupport struct {
	Load bool `json:"load"`
	Send bool `json:"send"`
	Like bool `json:"like"`
}

// ComicComment is one comment as the client receives it.
type ComicComment struct {
	ID       string `json:"id"`
	UserName string `json:"userName"`
	Avatar   string `json:"avatar"`
	Content  string `json:"content"`
	Time     string `json:"time"`
	// ReplyCount is how many replies the source reports, 0 when it does not say.
	ReplyCount int    `json:"replyCount"`
	Score      string `json:"score"`
	IsLiked    bool   `json:"isLiked"`
}

// ComicCommentPage is one page of a thread.
type ComicCommentPage struct {
	Comments []ComicComment `json:"comments"`
	MaxPage  int            `json:"maxPage"`
}

// textOf reads a field a source may write as a string or a number.
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// intOf reads a count a source may write as a string.
func intOf(raw json.RawMessage) int {
	n := 0
	seen := false
	for _, r := range textOf(raw) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		seen = true
	}
	if !seen {
		return 0
	}
	return n
}

// ImageConfig returns how a source wants one image fetched, as JSON:
// {"url","method","headers","data","handle","callbacks":["onResponse",
// "modifyImage","onLoadFailed"]}.
//
// A source may answer with callbacks instead of plain bytes: onResponse
// decrypts or rewrites the body, modifyImage names a pixel-buffer script,
// onLoadFailed offers another mirror. Functions cannot cross into JSON, so
// `handle` names them inside the source's runtime and the two methods below
// call them back. A host that ignores `handle` behaves as it did before —
// it still gets the address and the headers.
//
// thumbnail asks comic.onThumbnailLoad instead of comic.onImageLoad.
func (c *Core) ImageConfig(key, url, comic, chapter string, thumbnail bool) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	cfg, err := rt.ImageConfig(context.Background(), key, url, comic, chapter, thumbnail)
	if err != nil {
		return "", err
	}
	return encode(cfg)
}

// ApplyImageResponse runs the config's onResponse over a fetched body. body
// and the result are base64, so a binary file survives gomobile. A source
// with no such callback answers ErrNoImageCallback, which a caller may read
// as "keep the body".
func (c *Core) ApplyImageResponse(key, handle, bodyB64 string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	body, err := base64.StdEncoding.DecodeString(bodyB64)
	if err != nil {
		return "", fmt.Errorf("mobile: body must be base64: %w", err)
	}
	out, err := rt.ApplyImageResponse(context.Background(), key, handle, body)
	if err != nil {
		if errors.Is(err, js.ErrNoImageCallback) {
			return "", ErrNoImageCallback
		}
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// ImageFallback calls the config's onLoadFailed and returns the next config,
// in the same shape as ImageConfig.
func (c *Core) ImageFallback(key, handle string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	cfg, err := rt.ImageFallback(context.Background(), key, handle)
	if err != nil {
		if errors.Is(err, js.ErrNoImageCallback) {
			return "", ErrNoImageCallback
		}
		return "", err
	}
	return encode(cfg)
}

// ErrModifyImage reports a modifyImage script that produced no image.
var ErrModifyImage = errors.New("core: modifyImage did not return an image")

// ModifyImage runs a source's pixel-buffer script over one decoded image and
// answers as JSON: {"width","height","rgba"} with the pixels base64.
//
// The script takes an Image and returns another; Core moves RGBA only, so the
// caller decodes the fetched bytes and encodes the result. Passing pixels as
// base64 keeps a binary buffer intact across gomobile.
func (c *Core) ModifyImage(key, script string, width, height int, rgbaB64 string) (_ string, err error) {
	defer guard(&err)
	if script == "" {
		return "", ErrModifyImage
	}
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	pix, err := base64.StdEncoding.DecodeString(rgbaB64)
	if err != nil {
		return "", fmt.Errorf("mobile: pixels must be base64: %w", err)
	}
	out, err := rt.ModifyImage(context.Background(), script,
		image.Bitmap{W: width, H: height, Pix: pix})
	if err != nil {
		if errors.Is(err, js.ErrModifyImage) {
			return "", ErrModifyImage
		}
		return "", err
	}
	return encode(struct {
		Width  int    `json:"width"`
		Height int    `json:"height"`
		RGBA   string `json:"rgba"`
	}{out.W, out.H, base64.StdEncoding.EncodeToString(out.Pix)})
}

// Category returns a source's category page as JSON:
// {"title","parts":[{"name","type","itemType","categories","params",
// "groupParam","randomNumber"}],"options":[...]}.
//
// A source without one answers "{}", which the interface shows as no category
// page. `options` has the same shape as SearchOptions, so a client reuses its
// filter code.
func (c *Core) Category(key string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	cat, err := rt.Category(context.Background(), key)
	if err != nil {
		return "", err
	}
	if cat.Title == "" && len(cat.Parts) == 0 {
		return "{}", nil
	}
	return encode(cat)
}

// CategoryComics loads one page of a category page.
//
// category is the chosen value (not the dimension's name), param is what that
// value maps to — a per-value parameter, the part's groupParam, or the value
// itself — and options holds one value per group of the category's options.
func (c *Core) CategoryComics(callID int64, key, category, param, optionsJSON string, page int) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	ctx, done := c.ctx(callID)
	defer done()
	var options []string
	if optionsJSON != "" {
		if err := json.Unmarshal([]byte(optionsJSON), &options); err != nil {
			return "", fmt.Errorf("mobile: options must be a JSON array of strings: %w", err)
		}
	}
	raw, err := rt.Call(ctx, key, "categoryComics.load", category, param, options, max(1, page))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// HasComic reports whether a comic source defines path (e.g. "search.load").
// An unknown source or an invalid path is false. It returns a plain bool
// because gomobile exports (bool, error) with an awkward Swift signature.
// ComicValue reads a declaration that is data rather than a function, as JSON.
//
// favorites.multiFolder decides whether a folder id is expected at all, and
// CallComic invokes functions, so it cannot reach a value.
func (c *Core) ComicValue(key, path string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	raw, err := rt.Value(context.Background(), key, path)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return "null", nil
	}
	return string(raw), nil
}

// LinkDomains returns the host names a source claims for `comic.link`, as JSON:
// ["nhentai.net"]. `comic.link` is an object, so it is read as a value; calling
// it answers "source does not define this function".
func (c *Core) LinkDomains(key string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	raw, err := rt.Value(context.Background(), key, "comic.link")
	if err != nil {
		return "", err
	}
	var f struct {
		Domains []string `json:"domains"`
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "[]", nil
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return "[]", nil
	}
	if f.Domains == nil {
		return "[]", nil
	}
	return encode(f.Domains)
}

// LinkToID asks a source to read a comic id out of one of its own links. A link
// it does not recognise answers "" rather than an error: several sources are
// asked about the same address.
func (c *Core) LinkToID(callID int64, key, raw string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	ctx, done := c.ctx(callID)
	defer done()
	ok, err := rt.Has(ctx, key, "comic.link.linkToId")
	if err != nil || !ok {
		return "", nil
	}
	out, err := rt.Call(ctx, key, "comic.link.linkToId", raw)
	if err != nil {
		c.appendLog(key, "error", "Core", "comic.link.linkToId: "+err.Error())
		return "", err
	}
	id := textOf(out)
	return id, nil
}

// OpenLink asks every installed comic source that claims the address's host
// whether it is one of its own comics, and returns
// {"key":"nhentai","id":"691529"}; empty fields when none recognises it.
//
// Pasting a link is what a reader does when an address arrives in a chat, and
// searching for the URL as a keyword finds nothing.
func (c *Core) OpenLink(callID int64, raw string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	host := linkHost(raw)
	if host == "" {
		return encode(LinkTarget{})
	}
	for _, record := range c.reg.List() {
		if string(record.Type) != "comic" || record.Body == "" {
			continue
		}
		domains, err := c.LinkDomains(record.Key)
		if err != nil {
			continue
		}
		var list []string
		if json.Unmarshal([]byte(domains), &list) != nil || !claimsHost(list, host) {
			continue
		}
		id, err := c.LinkToID(callID, record.Key, raw)
		if err != nil || id == "" {
			continue
		}
		return encode(LinkTarget{Key: record.Key, ID: id})
	}
	// Nothing claimed it: an empty answer, not an error — the client then treats
	// the text as a keyword.
	return encode(LinkTarget{})
}

// LinkTarget is the source and comic a pasted address belongs to.
type LinkTarget struct {
	Key string `json:"key"`
	ID  string `json:"id"`
}

// linkHost reads the host of an address with or without a scheme.
func linkHost(raw string) string {
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

// claimsHost ignores a "www." prefix on either side: a link that arrives with it
// is the same site.
func claimsHost(domains []string, host string) bool {
	trim := func(s string) string { return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "www.") }
	want := trim(host)
	for _, d := range domains {
		if trim(d) == want {
			return true
		}
	}
	return false
}

// Translations returns the UI strings a source declares for this client's
// locale as a JSON object of key to text; "{}" when it declares none.
//
// A source writes a key as a setting's title and supplies the readable text in
// its translation table, so without this the interface shows "domain".
func (c *Core) Translations(key string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	got, err := rt.Translations(context.Background(), key)
	if err != nil {
		return "", err
	}
	if len(got) == 0 {
		return "{}", nil
	}
	return encode(got)
}

// AccountCookieFields lists the cookie names a source asks for when it logs in
// with cookies, as a JSON array of strings; "[]" when it does not offer that
// style.
//
// The list is data, not a function, so CallComic cannot reach it. The rest of a
// source login needs no new entry: HasComic reports which styles exist and
// CallComic runs account.login or account.loginWithCookies.validate.
func (c *Core) AccountCookieFields(key string) (_ string, err error) {
	defer guard(&err)
	rt, err := c.reg.Comic(key)
	if err != nil {
		return "", err
	}
	fields, err := rt.AccountLoginFields(context.Background(), key)
	if err != nil {
		return "", err
	}
	if fields == nil {
		return "[]", nil
	}
	return encode(fields)
}

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
	defer c.endCall(callID)
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
	defer c.endCall(callID)
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
	defer c.endCall(callID)
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
	defer c.endCall(callID)
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

// NormalizeURL fills in a missing scheme for a scheme-relative URL, such as
// "//images.example.com/1.jpg". A source may hand back an image address
// without its scheme; the client asks Core so both clients apply the same
// rule.
//
// The scheme is taken from base when base has one, and https otherwise. Only
// http and https are ever produced: a file or a script address is refused by
// the caller, not rewritten here.
func (c *Core) NormalizeURL(raw, base string) string {
	return host.NormalizeURL(raw, base)
}

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
	if len(plan.Cookies) > 0 {
		var installed []string
		if err := json.Unmarshal([]byte(knownKeysJSON), &installed); err != nil {
			return "", err
		}
		plan.ResolveCookieTargets(installed)
	}
	return encode(plan)
}

// --- host bridge ------------------------------------------------------------------

// hostBridge adapts the JSON Host to Core's Go interfaces.
// All bridges share the client's HTTP task registry. IDs must not repeat when
// comic sources and several Magpie accounts use the same Host concurrently.
var nextHTTPRequestID atomic.Int64

// nextHostRequestID is the counter shared by the whole-request and chunked
// bridges: one task table on the client side means one number space.
func nextHostRequestID() int64 { return nextHTTPRequestID.Add(1) }

type hostBridge struct {
	h Host

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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := nextHostRequestID()
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
// ErrNoImageCallback reports an image config without that callback.
var ErrNoImageCallback = errors.New("core: image config has no such callback")

func guard(err *error) {
	if p := recover(); p != nil {
		*err = fmt.Errorf("mobile: internal error: %v", p)
	}
}
