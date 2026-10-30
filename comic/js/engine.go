package js

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/eventloop"
	"github.com/laincat/veneracat-core/comic/html"
	"github.com/laincat/veneracat-core/comic/image"
)

// engine is one JavaScript runtime bound to one source key. Everything a
// script sends through sendMessage is attributed to that key, whatever the
// message says, so sources cannot reach each other's cookies, clearance or
// storage. MoeJS runtimes are single-goroutine: the engine lives on its own
// event loop and all access is posted there.
type engine struct {
	mgr *Runtime
	// key is the source this engine serves. "" marks the global engine
	// (Runtime.Eval) and a loading engine before validation; both get an
	// isolated partition and no source storage.
	key       string
	partition string
	kind      Kind
	storage   SourceStorage

	rt    *moejs.Runtime
	loop  *eventloop.Loop
	html  *html.Store
	image *image.Store

	// ctx ends when the engine closes and cancels its host work.
	ctx    context.Context
	cancel context.CancelFunc
	// callCtx is the context of the call running on the loop, read and
	// written on the loop only. Host work a script starts (http, delay)
	// stops when that call is cancelled, as well as when the engine closes.
	callCtx context.Context

	mu     sync.Mutex // serialises run and guards closed
	closed bool
}

// newEngine creates an engine and runs the preludes on it.
func (r *Runtime) newEngine(key, partition string, storage SourceStorage) (*engine, error) {
	e := &engine{mgr: r, key: key, partition: partition, storage: storage,
		html: html.NewStore(), image: image.NewStore(image.Limits{})}
	e.ctx, e.cancel = context.WithCancel(r.ctx)
	e.rt = moejs.NewRuntime(moejs.Options{MemoryLimit: r.opts.MemoryLimit})
	loop, err := eventloop.New(e.rt, eventloop.Options{
		// A script error in a timer callback must not stop the loop.
		OnError: func(err error) { r.svc.Log("error", "JS "+e.label(), err.Error()) },
	})
	if err != nil {
		return nil, err
	}
	e.loop = loop
	if err := e.rt.SetGlobal("sendMessage", moejs.NativeFunc(e.sendMessage)); err != nil {
		return nil, err
	}
	if err := e.rt.SetGlobal("appVersion", r.svc.Env.AppVersion); err != nil {
		return nil, err
	}
	for _, p := range r.preludes {
		if _, err := e.rt.RunScript(p); err != nil {
			return nil, fmt.Errorf("js: prelude: %w", err)
		}
	}
	if err := loop.Start(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *engine) label() string {
	if e.key == "" {
		return e.partition
	}
	return e.key
}

type outcome struct {
	val json.RawMessage
	err error
}

// run executes fn on the loop, awaits a promise result and converts it.
func (e *engine) run(ctx context.Context, fn func(*moejs.Runtime) (moejs.Value, error)) (json.RawMessage, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, ErrClosed
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.mgr.opts.CallTimeout)
		defer cancel()
	}
	callCtx, endCall := mergeDone(ctx, e.ctx)
	// Ending the call releases host work it started that is still waiting
	// (an http request nobody awaits any more), and the context's timers.
	defer endCall()
	done := make(chan outcome, 1)
	finish := func(v json.RawMessage, err error) {
		select {
		case done <- outcome{v, err}:
		default:
		}
	}
	posted := e.loop.RunOnLoop(func(rt *moejs.Runtime) {
		rt.ClearInterrupt()
		rt.ResetAllocation()
		e.callCtx = callCtx
		v, err := fn(rt)
		if err != nil {
			finish(nil, err)
			return
		}
		settle(rt, v, finish)
	})
	if !posted {
		return nil, ErrClosed
	}
	select {
	case o := <-done:
		if isInterrupt(o.err) {
			// Memory limits and other interrupts stop the loop too.
			if err := e.restart(); err != nil {
				return nil, err
			}
		}
		return o.val, o.err
	case <-ctx.Done():
		cause := context.Cause(ctx)
		e.loop.Interrupt(cause)
		if err := e.restart(); err != nil {
			return nil, err
		}
		return nil, cause
	case <-e.ctx.Done():
		return nil, ErrClosed
	}
}

// restart waits for a stopped or stopping loop to end, clears the pending
// interrupt and starts it again. An interrupt always ends a MoeJS event
// loop; queued timers and tasks survive the restart.
func (e *engine) restart() error {
	_ = e.loop.Stop()
	e.rt.ClearInterrupt()
	if err := e.loop.Start(); err != nil {
		e.closed = true
		return fmt.Errorf("js: engine could not restart after interrupt: %w", err)
	}
	return nil
}

// close terminates the engine. Host work is cancelled first so a blocked
// call returns quickly instead of waiting for its timeout.
func (e *engine) close() {
	e.cancel()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	e.loop.Terminate()
}

func isInterrupt(err error) bool {
	var ie *moejs.InterruptedError
	return errors.As(err, &ie)
}

// workCtx is the context for host work started now: the running call's,
// or the engine's when no call is running (timers firing later).
func (e *engine) workCtx() context.Context {
	if e.callCtx != nil {
		return e.callCtx
	}
	return e.ctx
}

// mergeDone returns a context that ends when either a or b ends, keeping a's
// deadline and values, and a function that ends it and releases its hooks.
func mergeDone(a, b context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(a)
	stop := context.AfterFunc(b, func() { cancel(context.Cause(b)) })
	return ctx, func() {
		stop()
		cancel(context.Canceled)
	}
}

// settle converts v, awaiting it first when it is a promise.
func settle(rt *moejs.Runtime, v moejs.Value, finish func(json.RawMessage, error)) {
	if state, result, ok := moejs.PromiseResult(v); ok {
		switch state {
		case moejs.PromiseFulfilled:
			v = result
		case moejs.PromiseRejected:
			finish(nil, jsError(rt, result))
			return
		default:
			then, err := rt.Get(v, "then")
			if err != nil {
				finish(nil, err)
				return
			}
			onOK := rt.Function("", 1, func(_ *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
				finish(toJSON(rt, moejs.Arg(a, 0)))
				return moejs.Undefined(), nil
			})
			onErr := rt.Function("", 1, func(_ *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
				finish(nil, jsError(rt, moejs.Arg(a, 0)))
				return moejs.Undefined(), nil
			})
			if _, err := rt.CallFunction(then, v, onOK, onErr); err != nil {
				finish(nil, err)
			}
			return
		}
	}
	finish(toJSON(rt, v))
}

// toJSON serialises a script value for Go. It matches what the official
// app receives from its JavaScript bridge: a Map becomes an object whose
// entries keep insertion order (sources return tags and chapters as Maps,
// and chapter order is reading order), and a BigInt becomes an exact
// integer literal. Plain JSON.stringify would drop both.
func toJSON(rt *moejs.Runtime, v moejs.Value) (json.RawMessage, error) {
	if v.IsUndefined() {
		return json.RawMessage("null"), nil
	}
	fn, err := rt.RunScript(serializer())
	if err != nil {
		return nil, err
	}
	s, err := rt.CallFunction(fn, moejs.Undefined(), v)
	if err != nil {
		return nil, err
	}
	if s.IsUndefined() {
		// JSON.stringify returns undefined for functions and symbols.
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(s.String()), nil
}

// serializerSource stringifies with a replacer that swaps each Map and
// BigInt for a placeholder string, then substitutes the placeholders with
// their JSON text. Map keys are converted with String(), as the official
// bridge does. The placeholder carries a per-call nonce so script strings
// cannot forge one.
const serializerSource = `(function(root){
  const nonce = Math.random().toString(36).slice(2) + Date.now().toString(36);
  const tag = "\u0000vc" + nonce + ":";
  const raw = [];
  const seen = new Set();
  function ser(v) {
    const text = JSON.stringify(v, function(k, x) {
      if (typeof x === "bigint") { raw.push(x.toString()); return tag + (raw.length - 1); }
      if (x instanceof Map) {
        if (seen.has(x)) throw new TypeError("Converting circular structure to JSON");
        seen.add(x);
        const parts = [];
        for (const [mk, mv] of x) {
          const t = ser(mv);
          if (t !== undefined) parts.push(JSON.stringify(String(mk)) + ":" + t);
        }
        seen.delete(x);
        raw.push("{" + parts.join(",") + "}");
        return tag + (raw.length - 1);
      }
      return x;
    });
    if (text === undefined) return undefined;
    const quoted = JSON.stringify(tag).slice(0, -1);
    let out = "", i = 0;
    for (;;) {
      const j = text.indexOf(quoted, i);
      if (j < 0) { out += text.slice(i); break; }
      const end = text.indexOf('"', j + quoted.length);
      out += text.slice(i, j) + raw[Number(text.slice(j + quoted.length, end))];
      i = end + 1;
    }
    return out;
  }
  return ser(root);
})`

var serializer = sync.OnceValue(func() *moejs.Script {
	s, err := moejs.CompileScript("<to-json>", serializerSource)
	if err != nil {
		panic("js: serializer does not compile: " + err.Error())
	}
	return s
})

// ScriptError is a value a script threw or a promise rejected with.
type ScriptError struct {
	Message string
	Stack   string
}

func (e *ScriptError) Error() string { return "js: " + e.Message }

func jsError(rt *moejs.Runtime, v moejs.Value) error {
	msg := v.String()
	if v.IsObject() {
		if m, err := rt.Get(v, "message"); err == nil && !m.IsUndefined() {
			msg = m.String()
		}
	}
	e := &ScriptError{Message: msg}
	if v.IsObject() {
		if s, err := rt.Get(v, "stack"); err == nil && !s.IsUndefined() {
			e.Stack = s.String()
		}
	}
	return e
}
