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
		"var i=opt.indexOf('-');if(i<=0){keys.push(opt);labels.push(opt);return;}" +
		"keys.push(opt.slice(0,i));labels.push(opt.slice(i+1));});" +
		"return {label:g.label===undefined?'':String(g.label),type:g.type===undefined?'':String(g.type)," +
		"values:keys,labels:labels,default:g['default']==null?'':String(g['default'])};});})()"
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
		if out[i].Default == "" && len(out[i].Values) > 0 {
			out[i].Default = out[i].Values[0]
		}
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
