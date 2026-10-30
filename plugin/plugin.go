// Package plugin is the single entry point clients use to manage sources:
// comic sources, JavaScript video plugins, Kazumi rules and built-in video
// providers share one key namespace and one install/enable/remove flow.
//
// The registry keeps metadata and script text in memory; persistence is the
// client's job (it stores Record values and calls Restore at start-up).
// Nothing a plugin contains runs before Install, Replace, SetEnabled(true) or
// Restore of an enabled record.
package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/laincat/veneracat-core/attribution"
	"github.com/laincat/veneracat-core/comic/js"
	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/video"
	"github.com/laincat/veneracat-core/video/builtin/hongguo"
	"github.com/laincat/veneracat-core/video/kazumi"
)

// Type of a plugin.
type Type string

const (
	TypeComic   Type = "comic"       // legacy ComicSource script
	TypeVideoJS Type = "video-js"    // VideoSource script
	TypeKazumi  Type = "kazumi-rule" // Kazumi rule JSON
	TypeBuiltin Type = "builtin"     // provider compiled into Core
)

func (t Type) script() bool { return t == TypeComic || t == TypeVideoJS }

// Record is what the client persists for an installed plugin.
type Record struct {
	Type    Type   `json:"type"`
	Key     string `json:"key"`
	Name    string `json:"name"`
	Version string `json:"version"`
	// Body is the script or rule text; empty for built-ins.
	Body string `json:"body,omitempty"`
	// SHA256 of Body, lower-case hex.
	SHA256 string `json:"sha256,omitempty"`
	// Origin is where Body came from (subscription entry URL, file, backup).
	Origin string `json:"origin,omitempty"`
	// IndexURL is the subscription index the plugin was installed from.
	IndexURL string `json:"indexUrl,omitempty"`
	Enabled  bool   `json:"enabled"`
	// Attribution is the plugin's own declaration, with missing parts taken
	// from its subscription entry (IndexAttribution).
	Attribution attribution.Info `json:"attribution"`
	// IndexAttribution is what the subscription entry said, kept so an
	// update of the script can be merged again.
	IndexAttribution attribution.Info `json:"indexAttribution,omitempty"`
	// Warnings about optional metadata (for example an invalid author URL).
	Warnings []string `json:"warnings,omitempty"`
}

// MissingAttribution reports what the UI should flag on a plugin.
func (r Record) MissingAttribution() (author, homepage bool) { return r.Attribution.Missing() }

// Builtin describes a provider compiled into Core.
type Builtin struct {
	Key, Name, Version string
	Attribution        attribution.Info
	New                func(host.Transport) video.Provider
}

// Builtins lists the built-in providers. They are installed only when the
// user enables them.
var Builtins = []Builtin{
	{Key: hongguo.Key, Name: "红果短剧", Version: "0.1.0", Attribution: hongguo.Attribution,
		New: func(t host.Transport) video.Provider { return hongguo.New(t) }},
}

func builtin(key string) (Builtin, bool) {
	for _, b := range Builtins {
		if b.Key == key {
			return b, true
		}
	}
	return Builtin{}, false
}

// Errors.
var (
	ErrExists   = errors.New("plugin: key already installed")
	ErrNotFound = errors.New("plugin: not installed")
	ErrDisabled = errors.New("plugin: disabled")
	ErrWrongUse = errors.New("plugin: not this kind of plugin")
	ErrChecksum = errors.New("plugin: content does not match its sha256")
	ErrKey      = errors.New("plugin: the script declares a different key")
)

// Registry owns the installed plugins of one profile. Operations on the
// same registry are serialised: installs, updates and toggles run one at a
// time, which keeps the engine state and the records consistent.
type Registry struct {
	js        *js.Runtime
	transport host.Transport

	op sync.Mutex // serialises mutating operations

	mu      sync.Mutex // guards records and video
	records map[string]*Record
	video   map[string]video.Provider
}

// New creates a registry. rt runs comic and JavaScript video plugins; t is
// the transport for Kazumi rules and built-ins (wrap it with
// challenge.NewTransport for Cloudflare handling).
func New(rt *js.Runtime, t host.Transport) *Registry {
	return &Registry{js: rt, transport: t, records: map[string]*Record{}, video: map[string]video.Provider{}}
}

func digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// rollbackTimeout bounds restoring a previous version after a failed update.
const rollbackTimeout = 30 * time.Second

// activation is a loaded plugin not yet published in the registry.
type activation struct {
	key, name, version string
	attribution        attribution.Info
	warnings           []string
	provider           video.Provider // nil for comic sources
	loadedScript       bool           // the JS runtime holds key
}

// apply copies what the loaded body declares into rec, merging the
// attribution with the subscription entry's.
func (a *activation) apply(rec *Record) {
	rec.Name, rec.Version = a.name, a.version
	rec.Attribution = attribution.Merge(a.attribution, rec.IndexAttribution)
	rec.Warnings = a.warnings
}

// activate loads body without touching the registry maps. A JS script whose
// type does not match typ is unloaded again.
func (r *Registry) activate(ctx context.Context, typ Type, body string) (*activation, error) {
	switch typ {
	case TypeKazumi:
		rule, err := kazumi.Parse([]byte(body))
		if err != nil {
			return nil, err
		}
		p := kazumi.New(rule, r.transport)
		info := p.Info()
		return &activation{key: info.Key, name: info.Name, version: info.Version, attribution: info.Attribution, provider: p}, nil
	case TypeComic, TypeVideoJS:
		if r.js == nil {
			return nil, errors.New("plugin: no JavaScript runtime")
		}
		meta, err := r.js.LoadSource(ctx, body)
		if err != nil {
			return nil, err
		}
		a := &activation{key: meta.Key, name: meta.Name, version: meta.Version, attribution: meta.Attribution,
			warnings: meta.Warnings, loadedScript: true}
		got := TypeComic
		if meta.Kind == js.KindVideo {
			got = TypeVideoJS
		}
		if got != typ {
			r.js.Unload(ctx, meta.Key)
			return nil, fmt.Errorf("%w: script is a %s plugin, not %s", ErrWrongUse, got, typ)
		}
		if got == TypeVideoJS {
			vp, err := r.js.VideoProvider(meta.Key)
			if err != nil {
				r.js.Unload(ctx, meta.Key)
				return nil, err
			}
			a.provider = vp
		}
		return a, nil
	case TypeBuiltin:
		return nil, fmt.Errorf("%w: use EnableBuiltin", ErrWrongUse)
	}
	return nil, fmt.Errorf("plugin: unknown type %q", typ)
}

// deactivate releases what activate loaded.
func (r *Registry) deactivate(ctx context.Context, a *activation) {
	if a != nil && a.loadedScript {
		r.js.Unload(ctx, a.key)
	}
}

func (r *Registry) taken(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.records[key]
	return ok
}

// publish stores rec and its provider.
func (r *Registry) publish(rec *Record, p video.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[rec.Key] = rec
	if p != nil {
		r.video[rec.Key] = p
	} else {
		delete(r.video, rec.Key)
	}
}

// Install validates and activates a plugin. wantSHA256, when not empty,
// must match body. A key already in use is ErrExists; use Replace to update.
func (r *Registry) Install(ctx context.Context, typ Type, body, origin, indexURL, wantSHA256 string) (*Record, error) {
	r.op.Lock()
	defer r.op.Unlock()
	return r.install(ctx, typ, body, origin, indexURL, wantSHA256, attribution.Info{})
}

func (r *Registry) install(ctx context.Context, typ Type, body, origin, indexURL, want string, indexAttr attribution.Info) (*Record, error) {
	sum := digest(body)
	if want != "" && want != sum {
		return nil, ErrChecksum
	}
	// A JS script's key is only known after it ran its constructor in an
	// isolated engine; LoadSource rejects keys the runtime already holds, and
	// we reject keys held by Kazumi rules or built-ins below.
	a, err := r.activate(ctx, typ, body)
	if err != nil {
		return nil, err
	}
	if r.taken(a.key) {
		// LoadSource refuses keys the runtime already holds, so a collision
		// here is with a Kazumi rule or built-in, and the freshly loaded
		// script (if any) is ours to unload.
		r.deactivate(ctx, a)
		return nil, fmt.Errorf("%w: %s", ErrExists, a.key)
	}
	rec := &Record{Type: typ, Key: a.key, Body: body, SHA256: sum, Origin: origin, IndexURL: indexURL,
		Enabled: true, IndexAttribution: indexAttr}
	a.apply(rec)
	r.publish(rec, a.provider)
	return clone(rec), nil
}

// Replace installs a new version over an existing plugin with the same key
// and type. The previous version stays active if the new one fails, and a
// disabled plugin stays disabled.
func (r *Registry) Replace(ctx context.Context, key string, typ Type, body, origin, indexURL, wantSHA256 string) (*Record, error) {
	r.op.Lock()
	defer r.op.Unlock()
	r.mu.Lock()
	old, ok := r.records[key]
	var prev Record
	if ok {
		prev = *old
	}
	r.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	if prev.Type != typ {
		return nil, fmt.Errorf("%w: %s is a %s plugin", ErrWrongUse, key, prev.Type)
	}
	sum := digest(body)
	if wantSHA256 != "" && wantSHA256 != sum {
		return nil, ErrChecksum
	}
	next := Record{Type: typ, Key: key, Body: body, SHA256: sum, Origin: origin, IndexURL: indexURL, Enabled: prev.Enabled,
		IndexAttribution: prev.IndexAttribution, Attribution: prev.Attribution}
	if !prev.Enabled {
		// Validate without running anything: the new body is checked when the
		// user enables the plugin. Kazumi rules can be parsed safely.
		if typ == TypeKazumi {
			a, err := r.activate(ctx, typ, body)
			if err != nil {
				return nil, err
			}
			if a.key != key {
				return nil, fmt.Errorf("%w: %s became %s", ErrKey, key, a.key)
			}
			a.apply(&next)
		} else {
			if _, err := js.ClassName(body); err != nil {
				return nil, err
			}
			next.Name, next.Version = prev.Name, prev.Version
		}
		r.publish(&next, nil)
		return clone(&next), nil
	}
	if typ.script() {
		r.js.Unload(ctx, key)
	}
	a, err := r.activate(ctx, typ, body)
	if err == nil && a.key != key {
		r.deactivate(ctx, a)
		err = fmt.Errorf("%w: %s became %s", ErrKey, key, a.key)
	}
	if err != nil {
		// Bring the previous version back with a fresh context: ctx may be
		// what failed.
		if typ.script() {
			rctx, cancel := context.WithTimeout(context.Background(), rollbackTimeout)
			pa, rerr := r.activate(rctx, typ, prev.Body)
			cancel()
			if rerr != nil {
				prev.Enabled = false
				r.publish(&prev, nil)
				return nil, fmt.Errorf("%w (and the previous version could not be restored, it is now disabled: %v)", err, rerr)
			}
			r.publish(&prev, pa.provider)
		}
		return nil, err
	}
	a.apply(&next)
	r.publish(&next, a.provider)
	return clone(&next), nil
}

// EnableBuiltin installs a built-in provider.
func (r *Registry) EnableBuiltin(key string) (*Record, error) {
	r.op.Lock()
	defer r.op.Unlock()
	b, ok := builtin(key)
	if !ok {
		return nil, fmt.Errorf("plugin: no built-in %q", key)
	}
	if r.taken(key) {
		return nil, fmt.Errorf("%w: %s", ErrExists, key)
	}
	rec := &Record{Type: TypeBuiltin, Key: b.Key, Name: b.Name, Version: b.Version, Origin: "builtin", Enabled: true,
		Attribution: b.Attribution}
	r.publish(rec, b.New(r.transport))
	return clone(rec), nil
}

// Restore re-activates persisted records at start-up. Disabled records are
// registered without running anything. Records that fail to load are
// returned with their error and kept, disabled, so the user can see and
// remove them; they are never silently dropped.
func (r *Registry) Restore(ctx context.Context, recs []Record) map[string]error {
	r.op.Lock()
	defer r.op.Unlock()
	failed := map[string]error{}
	for _, rec := range recs {
		rec := rec
		if r.taken(rec.Key) {
			failed[rec.Key] = fmt.Errorf("%w: %s", ErrExists, rec.Key)
			continue
		}
		if rec.Type != TypeBuiltin && rec.SHA256 != "" && digest(rec.Body) != rec.SHA256 {
			failed[rec.Key] = ErrChecksum
			rec.Enabled = false
			r.publish(&rec, nil)
			continue
		}
		if !rec.Enabled {
			r.publish(&rec, nil)
			continue
		}
		var err error
		if rec.Type == TypeBuiltin {
			b, ok := builtin(rec.Key)
			if !ok {
				err = fmt.Errorf("plugin: no built-in %q", rec.Key)
			} else {
				r.publish(&rec, b.New(r.transport))
				continue
			}
		} else {
			var a *activation
			a, err = r.activate(ctx, rec.Type, rec.Body)
			if err == nil && a.key != rec.Key {
				r.deactivate(ctx, a)
				err = fmt.Errorf("%w: %s became %s", ErrKey, rec.Key, a.key)
			}
			if err == nil {
				a.apply(&rec)
				r.publish(&rec, a.provider)
				continue
			}
		}
		failed[rec.Key] = err
		rec.Enabled = false
		r.publish(&rec, nil)
	}
	return failed
}

// SetEnabled turns a plugin on or off without removing it. A disabled
// script is unloaded from the engine; enabling loads it again and checks
// that it still declares the same key.
func (r *Registry) SetEnabled(ctx context.Context, key string, on bool) error {
	r.op.Lock()
	defer r.op.Unlock()
	r.mu.Lock()
	cur, ok := r.records[key]
	var rec Record
	if ok {
		rec = *cur
	}
	r.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if rec.Enabled == on {
		return nil
	}
	if !on {
		if rec.Type.script() {
			r.js.Unload(ctx, key)
		}
		rec.Enabled = false
		r.publish(&rec, nil)
		return nil
	}
	var p video.Provider
	switch rec.Type {
	case TypeBuiltin:
		b, ok := builtin(key)
		if !ok {
			return fmt.Errorf("plugin: no built-in %q", key)
		}
		p = b.New(r.transport)
	default:
		a, err := r.activate(ctx, rec.Type, rec.Body)
		if err != nil {
			return err
		}
		if a.key != key {
			r.deactivate(ctx, a)
			return fmt.Errorf("%w: %s became %s", ErrKey, key, a.key)
		}
		a.apply(&rec)
		p = a.provider
	}
	rec.Enabled = true
	r.publish(&rec, p)
	return nil
}

// Remove uninstalls a plugin.
func (r *Registry) Remove(ctx context.Context, key string) error {
	r.op.Lock()
	defer r.op.Unlock()
	r.mu.Lock()
	rec, ok := r.records[key]
	var typ Type
	var enabled bool
	if ok {
		typ, enabled = rec.Type, rec.Enabled
		delete(r.records, key)
		delete(r.video, key)
	}
	r.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if typ.script() && enabled {
		r.js.Unload(ctx, key)
	}
	return nil
}

// List returns installed plugins sorted by key.
func (r *Registry) List() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, 0, len(r.records))
	for _, rec := range r.records {
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Get returns one record.
func (r *Registry) Get(key string) (Record, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[key]
	if !ok {
		return Record{}, false
	}
	return *rec, true
}

// Video returns the provider of an enabled video plugin.
func (r *Registry) Video(key string) (video.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[key]
	switch {
	case !ok:
		return nil, ErrNotFound
	case rec.Type == TypeComic:
		return nil, ErrWrongUse
	case !rec.Enabled:
		return nil, ErrDisabled
	}
	p, ok := r.video[key]
	if !ok || p == nil {
		return nil, ErrDisabled
	}
	return wrap(p), nil
}

// Comic checks that key is an enabled comic source and returns the runtime
// to call it with.
func (r *Registry) Comic(key string) (*js.Runtime, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[key]
	switch {
	case !ok:
		return nil, ErrNotFound
	case rec.Type != TypeComic:
		return nil, ErrWrongUse
	case !rec.Enabled:
		return nil, ErrDisabled
	}
	return r.js, nil
}

func clone(r *Record) *Record { c := *r; return &c }
