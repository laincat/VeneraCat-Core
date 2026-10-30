// Package js runs legacy ComicSource and VeneraCat VideoSource scripts on
// MoeJS.
//
// A Runtime manages one JavaScript engine per installed source plus a
// global engine for Eval. Engines never share globals, so a source cannot
// read another source's objects, and every host message a script sends is
// attributed to the engine's own source key (see engine.go and
// docs/JS-ISOLATION.md).
package js

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/MoeForge/moejs"
	"github.com/laincat/veneracat-core/challenge"
	"github.com/laincat/veneracat-core/host"
)

//go:embed init.js
var defaultInit string

//go:embed video-prelude.js
var videoPrelude string

// ErrClosed is returned after Close.
var ErrClosed = errors.New("js: runtime closed")

// Runtime manages the engines of one profile.
type Runtime struct {
	opts Options
	svc  Services
	// preludes are compiled once and run in every new engine.
	preludes []*moejs.Script

	// ctx ends at Close and cancels every engine's host work.
	ctx    context.Context
	cancel context.CancelFunc
	// transport is svc.Transport wrapped for Cloudflare handling.
	transport host.Transport

	global *engine

	// loadMu serialises LoadSource so two installs cannot interleave their
	// validate-and-register steps.
	loadMu sync.Mutex

	smu     sync.Mutex // guards sources, engines and closed
	sources map[string]Meta
	engines map[string]*engine
	closed  bool
}

// New creates a runtime.
func New(opts Options, svc Services) (*Runtime, error) {
	opts = opts.withDefaults()
	if svc.Log == nil {
		svc.Log = func(string, string, string) {}
	}
	r := &Runtime{opts: opts, svc: svc, sources: map[string]Meta{}, engines: map[string]*engine{}}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	// Challenge handling is always on: without a verifier the wrapper still
	// recognises Cloudflare pages and reports them as ErrChallengeRequired,
	// which scripts see as "CloudflareException: <url>".
	r.transport = svc.Transport
	if svc.Transport != nil {
		r.transport = challenge.NewTransport(svc.Transport, svc.Verifier)
	}
	init, err := moejs.CompileScript("<init>", opts.InitScript)
	if err != nil {
		return nil, fmt.Errorf("js: init script: %w", err)
	}
	r.preludes = append(r.preludes, init)
	// The VideoSource prelude builds on init.js globals. Custom init scripts
	// (tests) that do not define them skip it.
	if opts.InitScript == defaultInit {
		vp, err := moejs.CompileScript("<video-prelude>", videoPrelude)
		if err != nil {
			return nil, fmt.Errorf("js: video prelude: %w", err)
		}
		r.preludes = append(r.preludes, vp)
	}
	r.global, err = r.newEngine("", "global", nil)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Eval runs src on the global engine, which belongs to no source, and
// returns its completion value as JSON. A Promise result is awaited.
func (r *Runtime) Eval(ctx context.Context, src string) (json.RawMessage, error) {
	if r.isClosed() {
		return nil, ErrClosed
	}
	return evalOn(ctx, r.global, src)
}

func evalOn(ctx context.Context, e *engine, src string) (json.RawMessage, error) {
	script, err := moejs.CompileScript("<eval>", src)
	if err != nil {
		return nil, err
	}
	return e.run(ctx, func(rt *moejs.Runtime) (moejs.Value, error) { return rt.RunScript(script) })
}

func (r *Runtime) isClosed() bool {
	r.smu.Lock()
	defer r.smu.Unlock()
	return r.closed
}

func (r *Runtime) engine(key string) (*engine, error) {
	r.smu.Lock()
	defer r.smu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	e, ok := r.engines[key]
	if !ok {
		return nil, fmt.Errorf("js: no source %q", key)
	}
	return e, nil
}

// Close terminates every engine. It is safe to call more than once.
func (r *Runtime) Close() error {
	r.cancel()
	r.smu.Lock()
	if r.closed {
		r.smu.Unlock()
		return nil
	}
	r.closed = true
	engines := make([]*engine, 0, len(r.engines))
	for _, e := range r.engines {
		engines = append(engines, e)
	}
	r.engines = map[string]*engine{}
	r.smu.Unlock()
	for _, e := range engines {
		e.close()
	}
	r.global.close()
	return nil
}
