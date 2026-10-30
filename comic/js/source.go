package js

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/laincat/veneracat-core/attribution"
	"github.com/laincat/veneracat-core/comic/version"
)

// Kind is the plugin family of a script.
type Kind string

const (
	// KindComic is a legacy ComicSource script (official API).
	KindComic Kind = "comic"
	// KindVideo is a VeneraCat VideoSource script.
	KindVideo Kind = "video"
)

func (k Kind) base() string {
	if k == KindVideo {
		return "VideoSource"
	}
	return "ComicSource"
}

// registry is the JavaScript expression of the kind's source table.
func (k Kind) registry() string { return k.base() + ".sources" }

// Meta describes a loaded source.
type Meta struct {
	Kind    Kind   `json:"kind"`
	Name    string `json:"name"`
	Key     string `json:"key"`
	Version string `json:"version"`
	// MinAppVersion is minAppVersion for comic sources and minCoreVersion
	// for video sources.
	MinAppVersion string `json:"minAppVersion,omitempty"`
	URL           string `json:"url,omitempty"`
	// Attribution comes from the class fields author and homepage. Both are
	// optional; official sources declare neither.
	Attribution attribution.Info `json:"attribution"`
	// Warnings are problems with optional metadata that did not block the
	// load (for example an author URL that is not http(s)).
	Warnings []string `json:"warnings,omitempty"`
}

// LoadError is a source that cannot be installed.
type LoadError struct{ Reason string }

func (e *LoadError) Error() string { return "js: invalid source: " + e.Reason }

var (
	keyPattern   = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	classPattern = regexp.MustCompile(`^class\s+([A-Za-z_$][A-Za-z0-9_$]*)\s+extends\s+(ComicSource|VideoSource)\b`)
)

// ClassName finds the source class: the first line that starts with
// "class " and contains "extends ComicSource", as the official parser does,
// or "extends VideoSource" for video plugins.
func ClassName(source string) (string, error) {
	name, _, err := classOf(source)
	return name, err
}

func classOf(source string) (string, Kind, error) {
	for _, line := range splitLines(source) {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "class ") {
			continue
		}
		if !strings.Contains(t, "extends ComicSource") && !strings.Contains(t, "extends VideoSource") {
			return "", "", &LoadError{"the first class must extend ComicSource or VideoSource"}
		}
		m := classPattern.FindStringSubmatch(t)
		if m == nil {
			return "", "", &LoadError{"cannot read the class name"}
		}
		if m[2] == "VideoSource" {
			return m[1], KindVideo, nil
		}
		return m[1], KindComic, nil
	}
	return "", "", &LoadError{"no class extends ComicSource or VideoSource"}
}

// LoadSource evaluates a ComicSource or VideoSource script in a fresh engine,
// validates its metadata, registers it under <Base>.sources[key] and runs
// init(). Nothing in the script runs before this call.
//
// The script first runs in an engine bound to no source: its constructor
// can reach neither the cookies nor the storage of any installed source.
// Only after the key is validated is that engine bound to the key; init()
// then runs with the source's own storage. Comic and video keys share one
// namespace, so a key identifies one plugin.
func (r *Runtime) LoadSource(ctx context.Context, source string) (Meta, error) {
	r.loadMu.Lock()
	defer r.loadMu.Unlock()
	if r.isClosed() {
		return Meta{}, ErrClosed
	}
	class, kind, err := classOf(source)
	if err != nil {
		return Meta{}, err
	}
	e, err := r.newEngine("", "pending", newScratchStorage())
	if err != nil {
		return Meta{}, err
	}
	e.kind = kind
	keep := false
	defer func() {
		if !keep {
			e.close()
		}
	}()
	minField := "minAppVersion"
	if kind == KindVideo {
		minField = "minCoreVersion"
	}
	// The script runs inside a function so its top-level declarations stay
	// private, as with the official loader.
	wrapped := "(function(){\n" + strings.ReplaceAll(source, "\r\n", "\n") +
		"\n;globalThis.__vcPending = new " + class + "();})();" +
		"(function(s){return {name:s.name,key:s.key,version:s.version,minAppVersion:s." + minField + ",url:s.url," +
		"author:s.author===undefined?null:s.author,homepage:s.homepage===undefined?null:s.homepage};})(globalThis.__vcPending)"
	raw, err := evalOn(ctx, e, wrapped)
	if err != nil {
		return Meta{}, fmt.Errorf("js: source failed to evaluate: %w", err)
	}
	var m struct {
		Name, Key, Version, MinAppVersion, URL any
		Author                                 json.RawMessage
		Homepage                               json.RawMessage
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return Meta{}, err
	}
	meta := Meta{Kind: kind, Name: anyText(m.Name), Key: anyText(m.Key), Version: anyText(m.Version),
		MinAppVersion: anyText(m.MinAppVersion), URL: anyText(m.URL)}
	meta.Attribution, meta.Warnings = attribution.ParseJSON(m.Author, m.Homepage)
	if err := r.validate(meta); err != nil {
		return Meta{}, err
	}
	// Bind the engine to its key. From here on messages use the source's
	// partition and storage; writes made while loading are kept.
	scratch := e.storage.(*scratchStorage)
	if err := scratch.flush(r.svc.Storage, meta.Key); err != nil {
		return Meta{}, err
	}
	e.key, e.partition, e.storage = meta.Key, "", r.svc.Storage
	reg := "(function(k){" + kind.registry() + "[k]=globalThis.__vcPending;delete globalThis.__vcPending;" +
		"var s=" + kind.registry() + "[k];return typeof s.init==='function';})(" + strconv.Quote(meta.Key) + ")"
	hasInit, err := evalOn(ctx, e, reg)
	if err != nil {
		return Meta{}, err
	}
	if string(hasInit) == "true" {
		if _, err := callOn(ctx, e, meta.Key, "init"); err != nil {
			return Meta{}, fmt.Errorf("js: %s init failed: %w", meta.Key, err)
		}
	}
	r.smu.Lock()
	if r.closed {
		r.smu.Unlock()
		return Meta{}, ErrClosed
	}
	if _, dup := r.sources[meta.Key]; dup {
		r.smu.Unlock()
		return Meta{}, &LoadError{fmt.Sprintf("key(%s) already exists", meta.Key)}
	}
	r.sources[meta.Key], r.engines[meta.Key] = meta, e
	r.smu.Unlock()
	keep = true
	return meta, nil
}

func (r *Runtime) validate(m Meta) error {
	switch {
	case m.Name == "":
		return &LoadError{"name is required"}
	case m.Key == "":
		return &LoadError{"key is required"}
	case m.Version == "":
		return &LoadError{"version is required"}
	case !keyPattern.MatchString(m.Key):
		return &LoadError{fmt.Sprintf("key %q is invalid", m.Key)}
	}
	r.smu.Lock()
	_, dup := r.sources[m.Key]
	r.smu.Unlock()
	if dup {
		return &LoadError{fmt.Sprintf("key(%s) already exists", m.Key)}
	}
	if m.MinAppVersion != "" {
		have, field := r.svc.Env.AppVersion, "minAppVersion"
		if m.Kind == KindVideo {
			have, field = CoreVersion, "minCoreVersion"
		}
		have = strings.SplitN(have, "-", 2)[0]
		if version.Newer(m.MinAppVersion, have) {
			return &LoadError{fmt.Sprintf("%s %s is required", field, m.MinAppVersion)}
		}
	}
	return nil
}

// Unload removes a source and closes its engine.
func (r *Runtime) Unload(_ context.Context, key string) {
	r.smu.Lock()
	e := r.engines[key]
	delete(r.engines, key)
	delete(r.sources, key)
	r.smu.Unlock()
	if e != nil {
		e.close()
	}
}

// Sources lists loaded sources.
func (r *Runtime) Sources() []Meta {
	r.smu.Lock()
	defer r.smu.Unlock()
	out := make([]Meta, 0, len(r.sources))
	for _, m := range r.sources {
		out = append(out, m)
	}
	return out
}

// ErrNoFunction means the source does not define the requested function.
var ErrNoFunction = errors.New("js: source does not define this function")

var pathPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(\.[A-Za-z_$][A-Za-z0-9_$]*|\[[0-9]+\])*$`)

// SearchOptionGroup is one group of a source's search.optionList.
//
// The official client passes one chosen value per group to search.load, so
// searching at all needs each group's default: a source that reads
// options[0] receives undefined otherwise, and builds a broken URL.
type SearchOptionGroup struct {
	Label string `json:"label"`
	// Type is "select" or "multi-select"; empty means "select".
	Type string `json:"type,omitempty"`
	// Values are the option keys in order, Labels their display text.
	Values []string `json:"values"`
	Labels []string `json:"labels"`
	// Default is the key of the group's default option, empty for the first.
	Default string `json:"default,omitempty"`
	// DefaultValues is what a multi-select group starts with, in the source's
	// order. The official client hands a multi-select group's value to the
	// source as a JSON array string, so a client that sends one value where an
	// array belongs breaks the source's own JSON.parse.
	DefaultValues []string `json:"defaultValues,omitempty"`
}

// SourceSetting is one entry of a source's settings declaration: the fields
// the official client renders as a select, a switch, an input or a button.
//
// Values and defaults are kept as JSON: a source may declare numbers or
// booleans, and a client must store what it read.
type SourceSetting struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	// Type is "select", "switch", "input" or "callback".
	Type string `json:"type"`
	// Default is the value the source wants when nothing is stored.
	Default json.RawMessage `json:"default,omitempty"`
	// Options are the choices of a select, in the source's order.
	Options []SettingOption `json:"options,omitempty"`
	// Validator is a regular expression an input's text must match.
	Validator string `json:"validator,omitempty"`
	// ButtonText is what a callback setting wants its button to say; empty when
	// the source leaves it to the client.
	ButtonText string `json:"buttonText,omitempty"`
}

// SettingOption is one choice of a select.
type SettingOption struct {
	Value json.RawMessage `json:"value"`
	Text  string          `json:"text,omitempty"`
}

// Settings reads <Base>.sources[key].settings. Reading it evaluates the
// property, so a source that computes its settings in a getter is answered
// too; a source without settings returns an empty list.
func (r *Runtime) Settings(ctx context.Context, key string) ([]SourceSetting, error) {
	e, err := r.engine(key)
	if err != nil {
		return nil, err
	}
	// Functions cannot cross the boundary, so only the fields a client draws
	// are read; a callback setting keeps its title and nothing else.
	src := "(function(s){var o=" + e.kind.registry() + "[" + strconv.Quote(key) + "];" +
		"var st=o&&o.settings;if(!st)return [];" +
		"var out=[];for(var k in st){if(!Object.prototype.hasOwnProperty.call(st,k))continue;" +
		"var d=st[k]||{};var opts=[];" +
		"(d.options||[]).forEach(function(opt){if(!opt||typeof opt!=='object')return;" +
		"opts.push({value:opt.value===undefined?null:opt.value," +
		"text:opt.text===undefined?'':String(opt.text)});});" +
		"out.push({key:String(k),title:d.title===undefined?'':String(d.title)," +
		"type:d.type===undefined?'':String(d.type)," +
		"default:d['default']===undefined?null:d['default'],options:opts," +
		"validator:d.validator===undefined?'':String(d.validator)," +
		"buttonText:d.buttonText===undefined?'':String(d.buttonText)});}" +
		"return out;})()"
	raw, err := evalOn(ctx, e, src)
	if err != nil {
		return nil, err
	}
	var out []SourceSetting
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("js: settings: %w", err)
	}
	return out, nil
}

// SearchOptions reads <Base>.sources[key].search.optionList. A source without
// it returns an empty list.
func (r *Runtime) SearchOptions(ctx context.Context, key string) ([]SearchOptionGroup, error) {
	e, err := r.engine(key)
	if err != nil {
		return nil, err
	}
	src := "(function(s){var o=" + e.kind.registry() + "[" + strconv.Quote(key) + "];" +
		"var l=o&&o.search&&o.search.optionList;if(!l)return null;" +
		"return l.map(function(g){var keys=[],labels=[];" +
		"(g.options||[]).forEach(function(opt){if(typeof opt!=='string'||!opt)return;" +
		"var i=opt.indexOf('-');if(i<0){keys.push(opt);labels.push(opt);return;}" +
		"keys.push(opt.slice(0,i));labels.push(opt.slice(i+1));});" +
		"var dv=[];if(Array.isArray(g['default'])){g['default'].forEach(function(v){dv.push(String(v))});}" +
		"return {label:g.label===undefined?'':String(g.label),type:g.type===undefined?'':String(g.type)," +
		"values:keys,labels:labels,default:g['default']==null||Array.isArray(g['default'])?'':String(g['default'])," +
		"defaultValues:dv};});})()"
	raw, err := evalOn(ctx, e, src)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var out []SearchOptionGroup
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("js: search.optionList: %w", err)
	}
	for i := range out {
		if out[i].Type == "multi-select" {
			// A multi-select group keeps its list; the single Default stays
			// empty so a client cannot mistake the two shapes.
			continue
		}
		if out[i].Default == "" && len(out[i].Values) > 0 {
			out[i].Default = out[i].Values[0]
		}
	}
	return out, nil
}

// ExplorePage is one entry of a source's explore list, the sections a source
// offers to browse without searching.
type ExplorePage struct {
	Title string `json:"title"`
	// Type is "singlePageWithMultiPart", "multiPartPage", "multiPageComicList"
	// or "mixed".
	Type string `json:"type"`
}

// ExplorePages reads <Base>.sources[key].explore. A source without it
// returns an empty list.
func (r *Runtime) ExplorePages(ctx context.Context, key string) ([]ExplorePage, error) {
	e, err := r.engine(key)
	if err != nil {
		return nil, err
	}
	src := "(function(s){var o=" + e.kind.registry() + "[" + strconv.Quote(key) + "];" +
		"var l=o&&o.explore;if(!l||!l.length)return null;" +
		"return Array.prototype.map.call(l,function(p){return {title:p.title==null?'':String(p.title)," +
		"type:p.type==null?'':String(p.type)};});})()"
	raw, err := evalOn(ctx, e, src)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var out []ExplorePage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("js: explore: %w", err)
	}
	return out, nil
}

// Has reports whether <Base>.sources[key].<path> is defined.
func (r *Runtime) Has(ctx context.Context, key, path string) (bool, error) {
	if !pathPattern.MatchString(path) {
		return false, fmt.Errorf("js: invalid path %q", path)
	}
	e, err := r.engine(key)
	if err != nil {
		return false, err
	}
	out, err := evalOn(ctx, e, "(function(){try{var v="+e.kind.registry()+"["+strconv.Quote(key)+"]."+path+
		";return v!==null&&v!==undefined;}catch(e){return false;}})()")
	return string(out) == "true", err
}

// Call invokes <Base>.sources[key].<path>(args...) with `this` bound to the
// object that holds the function, awaits a promise result and returns the
// result as JSON. Arguments are passed as JSON text, so json.Number and
// json.RawMessage keep their exact digits. path is a dotted property path
// such as "search.load" or "explore[0].load".
func (r *Runtime) Call(ctx context.Context, key, path string, args ...any) (json.RawMessage, error) {
	e, err := r.engine(key)
	if err != nil {
		return nil, err
	}
	return callOn(ctx, e, key, path, args...)
}

func callOn(ctx context.Context, e *engine, key, path string, args ...any) (json.RawMessage, error) {
	if !pathPattern.MatchString(path) {
		return nil, fmt.Errorf("js: invalid path %q", path)
	}
	argJSON, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("js: arguments: %w", err)
	}
	if args == nil {
		argJSON = []byte("[]")
	}
	if !json.Valid(argJSON) {
		return nil, errors.New("js: arguments are not valid JSON")
	}
	owner, name := path, ""
	if i := strings.LastIndexAny(path, ".["); i >= 0 {
		owner, name = path[:i], path[i:]
	} else {
		owner, name = "", "."+path
	}
	holder := e.kind.registry() + "[" + strconv.Quote(key) + "]"
	if owner != "" {
		holder += "." + owner
	}
	src := "(function(h){if(h===undefined||h===null)throw new Error('" + errNoFn + "');var f=h" + name +
		";if(typeof f!=='function')throw new Error('" + errNoFn + "');return f.apply(h,globalThis.__vcArgs);})(" + holder + ")"
	out, err := e.run(ctx, func(rt *moejs.Runtime) (moejs.Value, error) {
		v, err := rt.ParseJSON(argJSON)
		if err != nil {
			return moejs.Undefined(), err
		}
		if err := rt.SetGlobal("__vcArgs", v); err != nil {
			return moejs.Undefined(), err
		}
		script, err := moejs.CompileScript("<call "+key+"."+path+">", src)
		if err != nil {
			return moejs.Undefined(), err
		}
		return rt.RunScript(script)
	})
	if err != nil && strings.Contains(err.Error(), errNoFn) {
		return nil, ErrNoFunction
	}
	return out, err
}

const errNoFn = "__vc_no_function__"

func anyText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

// CoreVersion is the VeneraCat Core API level video plugins compare their
// minCoreVersion against.
const CoreVersion = "1.0.0"

// AccountLoginFields reads the cookie names a source asks for when it logs in
// with cookies: <Base>.sources[key].account.loginWithCookies.fields.
//
// The list is data, not a function, so Call cannot reach it; the script reads it
// and hands back strings. A source without that style returns nothing.
func (r *Runtime) AccountLoginFields(ctx context.Context, key string) ([]string, error) {
	e, err := r.engine(key)
	if err != nil {
		return nil, err
	}
	src := "(function(o){" +
		"var c=o&&o.account&&o.account.loginWithCookies;" +
		"if(!c)return null;" +
		"var out=[];(c.fields||[]).forEach(function(f){if(f===null||f===undefined)return;" +
		"out.push(String(f));});" +
		"return out;})(" + e.kind.registry() + "[" + strconv.Quote(key) + "])"
	raw, err := evalOn(ctx, e, src)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" || len(raw) == 0 {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("js: account fields: %w", err)
	}
	return out, nil
}

// Translations reads the UI strings a source declares, keyed by JSON field.
//
// A source writes `title: "domain"` and supplies the readable text in a
// `translation` table per locale; without it the interface shows the raw key.
// The client's own locale is tried first, then its language, then English, then
// Chinese, so a source that only ships some locales still reads.
func (r *Runtime) Translations(ctx context.Context, key string) (map[string]string, error) {
	e, err := r.engine(key)
	if err != nil {
		return nil, err
	}
	locale := r.svc.Env.Locale
	src := "(function(o,loc){" +
		"var t=o&&o.translation;if(!t)return null;" +
		"var base=String(loc||'').split(/[_-]/)[0];" +
		// The exact locale, then its language however it is spelled in the table
		// (zh_TW finds zh_CN), then English and Chinese.
		"var order=[loc,base];" +
		"for(var k in t){if(!Object.prototype.hasOwnProperty.call(t,k))continue;" +
		"if(base&&String(k).split(/[_-]/)[0]===base&&order.indexOf(k)<0)order.push(k);}" +
		"order=order.concat(['en','zh_CN','zh']);" +
		"for(var i=0;i<order.length;i++){var k=order[i];if(!k)continue;" +
		"var m=t[k];if(!m||typeof m!=='object')continue;" +
		"var out={};for(var p in m){if(!Object.prototype.hasOwnProperty.call(m,p))continue;" +
		"var v=m[p];if(v===null||v===undefined)continue;out[p]=String(v);}" +
		"return out;}" +
		"return null;})(" + e.kind.registry() + "[" + strconv.Quote(key) + "]," + strconv.Quote(locale) + ")"
	raw, err := evalOn(ctx, e, src)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" || len(raw) == 0 {
		return nil, nil
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("js: translation: %w", err)
	}
	return out, nil
}

// Value reads <Base>.sources[key].<path> as data, without calling it.
//
// Some of a source's declarations are data rather than functions and still
// decide behaviour — favorites.multiFolder says whether a folder id is expected
// at all — and Call cannot reach those. The value comes back as JSON text;
// undefined and null both read as "null".
func (r *Runtime) Value(ctx context.Context, key, path string) (json.RawMessage, error) {
	if !pathPattern.MatchString(path) {
		return nil, fmt.Errorf("js: invalid path %q", path)
	}
	e, err := r.engine(key)
	if err != nil {
		return nil, err
	}
	out, err := evalOn(ctx, e, "(function(){try{var v="+e.kind.registry()+"["+strconv.Quote(key)+"]."+path+
		";return v===undefined?null:v;}catch(e){return null;}})()")
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
