// Package js runs VeneraCat NovelSource plugins on MoeJS.
//
// Each loaded source runs in its own isolated JavaScript engine backed by
// comic/js.Runtime, sharing the host Transport, CookieJar, Storage and
// Verifier interfaces provided by the client.
package js

import (
	"context"
	"fmt"

	comicjs "github.com/laincat/veneracat-core/comic/js"
)

// Options configures a novel JavaScript runtime.
type Options = comicjs.Options

// Services are the host capabilities provided by the client.
type Services = comicjs.Services

// Meta describes a loaded novel source.
type Meta = comicjs.Meta

// ScriptError is a value thrown by a plugin or rejected by a Promise.
type ScriptError = comicjs.ScriptError

// Runtime manages isolated JavaScript engines for NovelSource plugins.
type Runtime struct {
	rt *comicjs.Runtime
}

// New creates a standalone novel JavaScript runtime.
func New(opts Options, svc Services) (*Runtime, error) {
	rt, err := comicjs.New(opts, svc)
	if err != nil {
		return nil, err
	}
	return &Runtime{rt: rt}, nil
}

// JSRuntime returns the underlying shared JavaScript runtime.
func (r *Runtime) JSRuntime() *comicjs.Runtime { return r.rt }

// LoadSource evaluates a NovelSource script in a fresh isolated engine.
func (r *Runtime) LoadSource(ctx context.Context, source string) (Meta, error) {
	return r.rt.LoadSourceChecked(ctx, source, "", func(m comicjs.Meta) error {
		if m.Kind != comicjs.KindNovel {
			return fmt.Errorf("novel-js: script is a %s source, not novel", m.Kind)
		}
		return nil
	})
}

// Provider returns a novel.Provider for a loaded NovelSource plugin.
func (r *Runtime) Provider(key string) (*Provider, error) {
	return NewProvider(r.rt, key)
}

// Unload removes a source and closes its engine.
func (r *Runtime) Unload(ctx context.Context, key string) {
	r.rt.Unload(ctx, key)
}

// Close terminates every engine in the runtime.
func (r *Runtime) Close() error {
	return r.rt.Close()
}
