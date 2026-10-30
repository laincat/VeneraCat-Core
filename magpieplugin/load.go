// Loader compiles a Magpie provider package for MoeJS. It resolves only
// package-relative modules: a Node built-in or npm dependency fails here
// instead of resolving to an empty stub, so a package the host cannot run
// says so before any hook is called.
package magpieplugin

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/eventloop"
)

// DefaultMemoryLimit bounds one plugin instance. Providers hold model lists
// and streamed replies, so the limit is generous but finite.
const DefaultMemoryLimit int64 = 64 << 20

// Options configure one instance.
type Options struct {
	// MemoryLimit is the JavaScript heap budget; zero means
	// DefaultMemoryLimit, negative means unlimited.
	MemoryLimit int64
	// DisableDynamicCode refuses eval and Function constructors, which a
	// provider package has no reason to need. Off by default because a
	// package that uses them would otherwise fail at an unpredictable
	// point; the report's caller decides.
	DisableDynamicCode bool
	// Locale is the Intl locale, "" for en-US.
	Locale string
}

// Loader holds the compiled module graph of one package. It is immutable and
// safe to share: each account gets its own Instance.
type Loader struct {
	bundle *Bundle
	entry  *moejs.Module
}

// Load compiles the package's entry and, through Link, every module it
// reaches. A module that is not in the snapshot, including a Node built-in or
// an npm package, is an error: nothing is resolved outside the package.
func Load(b *Bundle) (*Loader, error) {
	entries, err := b.Entries()
	if err != nil {
		return nil, err
	}
	// Compiling every entry candidate keeps the reported failures to the
	// ones that matter, and the entry order is the host's.
	var compiled []*moejs.Module
	for _, name := range entries {
		src, err := b.Read(name)
		if err != nil {
			return nil, err
		}
		if isTypeScript(name) {
			return nil, fmt.Errorf("%w: %s is TypeScript, which this runtime does not parse", ErrUnsupportedSource, name)
		}
		m, err := moejs.Compile(name, string(src))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		compiled = append(compiled, m)
	}
	// The last candidate that links cleanly is the entry; the first one that
	// does is what Magpie loads, so stop there.
	mods := map[string]*moejs.Module{entries[0]: compiled[0]}
	entry, err := moejs.Link(compiled[0], func(ref moejs.Referrer, spec string) (*moejs.Module, error) {
		from := entries[0]
		if ref != nil {
			from = ref.Name()
		}
		name, err := b.Resolve(from, spec)
		if err != nil {
			return nil, err
		}
		if m, ok := mods[name]; ok {
			return m, nil
		}
		src, err := b.Read(name)
		if err != nil {
			return nil, err
		}
		if isTypeScript(name) {
			return nil, fmt.Errorf("%w: %s is TypeScript, which this runtime does not parse", ErrUnsupportedSource, name)
		}
		m, err := moejs.Compile(name, string(src))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		mods[name] = m
		return m, nil
	})
	if err != nil {
		return nil, err
	}
	return &Loader{bundle: b, entry: entry}, nil
}

// awaitHelper turns a hook that may be async into one answer Go can read: it
// awaits the package's function and hands the settled value (or what it threw)
// to a host callback. Magpie's hooks return promises, and MoeJS gives Go the
// promise object rather than its value.
const awaitHelper = `(async (fn, arg, done) => {
  try {
    done(await fn(arg))
  } catch (e) {
    done({ magpieError: true, message: (e && (e.stack || e.message)) || String(e) })
  }
})`

// Instance runs one account's copy of a package. Its own runtime means one
// plugin's globals, timers and errors never reach another's.
type Instance struct {
	loader *Loader
	rt     *moejs.Runtime
	// loop runs the promises a provider hook awaits. Magpie's hooks are
	// async and settle on the host's tasks, so a call is not complete until
	// the loop has nothing left to run.
	loop   *eventloop.Loop
	settle moejs.Value

	// ctx ends the work a package started; cancelling it rejects its fetches.
	ctx      context.Context
	cancel   context.CancelFunc
	host     Host
	provider string
	auth     AuthStore

	// mu guards pending and id: one entry per fetch in flight, so an abort
	// reaches the request it names and no other.
	mu      sync.Mutex
	pending map[int64]context.CancelFunc
	id      int64

	// The prelude's stream handlers, captured once so a reply can be pushed
	// into the package's ReadableStream.
	streamPush, streamClose, streamFail moejs.Value

	// bridgeInstalled says the plugin ABI hooks are reachable.
	bridgeInstalled bool

	// pluginOptions are the user's switches for each provider's plugin, keyed
	// "provider|option".
	optionsMu     sync.Mutex
	pluginOptions map[string]bool

	// One reply at a time per instance: chatPipes maps a handle to the reader
	// that turns the package's text into events.
	chatMu    sync.Mutex
	chatPipes map[int64]*replyBuffer
	chatNext  int64
}

// nextID returns a fetch id. Ids only have to be unique within an instance.
func (in *Instance) nextID() int64 {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.id++
	return in.id
}

// NewInstance creates the runtime and evaluates the package's top level.
// Nothing network-bound happens here; the caller must first provide the
// globals the package expects, such as fetch and client.auth.
func (l *Loader) NewInstance(opts Options) (*Instance, error) {
	limit := opts.MemoryLimit
	if limit == 0 {
		limit = DefaultMemoryLimit
	}
	if limit < 0 {
		limit = 0
	}
	rt := moejs.NewRuntime(moejs.Options{
		MemoryLimit:        limit,
		DisableDynamicCode: opts.DisableDynamicCode,
		Locale:             opts.Locale,
	})
	if err := rt.Load(l.entry); err != nil {
		return nil, err
	}
	script, err := moejs.CompileScript("__magpie-await__.js", awaitHelper)
	if err != nil {
		return nil, err
	}
	settle, err := rt.RunScript(script)
	if err != nil {
		return nil, err
	}
	loop, err := eventloop.New(rt, eventloop.Options{})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Instance{
		loader: l, rt: rt, loop: loop, settle: settle,
		ctx: ctx, cancel: cancel, pending: map[int64]context.CancelFunc{},
	}, nil
}

// SetGlobal defines a value the package reads, such as `console`.
func (in *Instance) SetGlobal(name string, value any) error {
	return in.rt.SetGlobal(name, value)
}

// SetFunction defines a host function the package calls, such as `fetch`.
func (in *Instance) SetFunction(name string, length int, fn moejs.NativeFunc) error {
	return in.rt.SetGlobal(name, in.rt.Function(name, length, fn))
}

// Exports asks whether the package exports name, without calling anything.
func (in *Instance) Exports(name string) bool {
	_, ok := in.rt.Export(name)
	return ok
}

// Call calls an exported function with one argument, converting Go values to
// JavaScript and the result back. Async results are awaited by the runtime.
// The argument is passed as the plugin's `input` object, which is how a
// Magpie plugin receives its client.
func (in *Instance) Call(export string, arg any) (any, error) {
	fn, ok := in.rt.Export(export)
	if !ok {
		return nil, fmt.Errorf("magpieplugin: %s exports no %s", in.loader.bundle.Manifest().Name, export)
	}
	argValue := moejs.Undefined()
	if arg != nil {
		converted, err := in.rt.FromGo(arg)
		if err != nil {
			return nil, err
		}
		argValue = converted
	}
	return in.callValue(export, fn, argValue)
}

// callValue is Call's body, for callers that already hold a JavaScript
// function: the plugin bridge below reaches functions it defined itself.
func (in *Instance) callValue(export string, fn moejs.Value, argValue moejs.Value) (any, error) {
	var result moejs.Value
	var thrown error
	var failed bool
	done := in.rt.Function("done", 2, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		if len(args) > 0 {
			failure, convErr := in.rt.ToGo(args[0])
			if convErr == nil {
				if m, ok := failure.(map[string]any); ok && m["magpieError"] == true {
					thrown = fmt.Errorf("magpieplugin: %s threw: %v", export, m["message"])
					failed = true
					return moejs.Undefined(), nil
				}
			}
		}
		if len(args) > 0 {
			result = args[0]
		}
		return moejs.Undefined(), nil
	})
	// Run, not Call, so an async hook's awaited work completes before the
	// answer is read; the loop returns when nothing keeps it alive.
	runErr := in.loop.Run(func(rt *moejs.Runtime) error {
		_, callErr := rt.CallFunction(in.settle, moejs.Undefined(), fn, argValue, done)
		return callErr
	})
	if runErr != nil {
		return nil, runErr
	}
	if failed {
		return nil, thrown
	}
	if result.IsUndefined() {
		return nil, nil
	}
	out, err := in.rt.ToGo(result)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Interrupt stops running code: a package that loops forever must not hold
// the caller. The plugin stays unusable afterwards, so callers create a new
// instance for the next attempt.
func (in *Instance) Interrupt(reason any) { in.rt.Interrupt(reason) }

// Close ends the instance: running fetches are cancelled and the runtime is
// stopped. The instance must not be used afterwards.
func (in *Instance) Close() {
	if in.cancel != nil {
		in.cancel()
	}
	in.loop.Terminate()
}

// isTypeScript reports whether a path would need a TypeScript compiler. A
// package that ships one cannot load here, and saying so beats a syntax error
// from a parser reading type annotations as JavaScript.
func isTypeScript(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".ts", ".tsx", ".mts", ".cts":
		return true
	}
	return false
}
