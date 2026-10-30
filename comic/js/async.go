package js

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MoeForge/moejs"
	moengine "github.com/MoeForge/moejs/engine"
	"github.com/laincat/veneracat-core/comic/hostapi"
)

// --- delay ------------------------------------------------------------------

// delay returns a promise that resolves after the given milliseconds. A
// Close cancels pending delays.
func (e *engine) delay(msg map[string]any) (moejs.Value, error) {
	ms := num(msg["time"], 0)
	if ms < 0 {
		ms = 0
	}
	p, settle := e.loop.NewPromise()
	go func() {
		t := time.NewTimer(time.Duration(ms * float64(time.Millisecond)))
		defer t.Stop()
		select {
		case <-t.C:
		case <-e.ctx.Done():
		}
		settle(func(*moejs.Runtime) (moejs.Value, error) { return moejs.Undefined(), nil })
	}()
	return p, nil
}

// --- http ---------------------------------------------------------------------

// http performs a request through the client transport and resolves with
// {status, headers, body, error}. Network failures never reject: like the
// official host they come back in the error field, and the init.js helper
// throws them.
func (e *engine) http(msg map[string]any) (moejs.Value, error) {
	hm := hostapi.HTTPMessage{
		Method: str(msg["http_method"]),
		URL:    str(msg["url"]),
		Data:   msg["data"],
		Bytes:  msg["bytes"] == true,
	}
	if h, ok := msg["headers"].(map[string]any); ok {
		hm.Headers = h
	}
	if x, ok := msg["extra"].(map[string]any); ok {
		hm.Extra = x
	}
	key := e.partitionKey()
	ctx := e.workCtx()
	p, settle := e.loop.NewPromise()
	go func() {
		res := hostapi.DoHTTP(ctx, e.mgr.transport, key, hm, e.mgr.svc.Env.DefaultUA)
		settle(func(rt *moejs.Runtime) (moejs.Value, error) { return httpValue(rt, res) })
	}()
	return p, nil
}

func httpValue(rt *moejs.Runtime, res hostapi.HTTPResult) (moejs.Value, error) {
	out := map[string]any{"status": nil, "headers": map[string]any{}, "body": nil, "error": nil}
	if res.Status != nil {
		out["status"] = int64(*res.Status)
	}
	h := map[string]any{}
	for k, v := range res.Headers {
		h[k] = v
	}
	out["headers"] = h
	if res.Body != nil {
		out["body"] = res.Body
	}
	if res.Error != nil {
		out["error"] = *res.Error
	}
	return rt.FromGo(out)
}

// partitionKey is the key hostapi turns into this engine's cookie and
// clearance partition ("comic:" + key). Engines without a source use keys
// containing ':', which no source key can (keys are [A-Za-z0-9_]+), so they
// never share a partition with an installed source.
func (e *engine) partitionKey() string {
	if e.key != "" {
		return e.key
	}
	return ":" + e.partition
}

// --- cookie ---------------------------------------------------------------------

func (e *engine) cookie(realm *moejs.Realm, msg map[string]any) (moejs.Value, error) {
	if e.mgr.svc.Cookies == nil {
		return moejs.Undefined(), errors.New("cookie storage is not available")
	}
	cm := hostapi.CookieMessage{Function: str(msg["function"]), URL: str(msg["url"])}
	if list, ok := msg["cookies"].([]any); ok {
		for _, c := range list {
			if m, ok := c.(map[string]any); ok {
				cm.Cookies = append(cm.Cookies, m)
			}
		}
	}
	out, err := hostapi.DoCookie(e.mgr.svc.Cookies, e.partitionKey(), cm)
	if err != nil {
		return moejs.Undefined(), err
	}
	if list, ok := out.([]map[string]any); ok {
		arr := make([]any, len(list))
		for i, m := range list {
			arr[i] = m
		}
		return realm.FromGo(arr)
	}
	return toValue(realm, out)
}

// --- compute --------------------------------------------------------------------

// compute evaluates `(function)` in this runtime on a microtask and calls it
// with args. The official host documents an "engine pool" but runs the
// function in the same engine; so do we.
func (e *engine) compute(msg map[string]any) (moejs.Value, error) {
	src, ok := msg["function"].(string)
	if !ok {
		return moejs.Undefined(), errors.New("Function must be a string")
	}
	var args []any
	if a, ok := msg["args"]; ok && a != nil {
		list, ok := a.([]any)
		if !ok {
			return moejs.Undefined(), errors.New("Args must be a list")
		}
		args = list
	}
	p, settle := e.loop.NewPromise()
	settle(func(rt *moejs.Runtime) (moejs.Value, error) {
		script, err := moejs.CompileScript("<compute>", "("+src+")")
		if err != nil {
			return moejs.Undefined(), err
		}
		fn, err := rt.RunScript(script)
		if err != nil {
			return moejs.Undefined(), err
		}
		vals := make([]moejs.Value, len(args))
		for i, a := range args {
			if vals[i], err = rt.FromGo(a); err != nil {
				return moejs.Undefined(), err
			}
		}
		res, err := rt.CallFunction(fn, moejs.Undefined(), vals...)
		if errors.Is(err, moejs.ErrNotCallable) {
			return moejs.Undefined(), errors.New("The provided code does not evaluate to a function.")
		}
		return res, err
	})
	return p, nil
}

// --- UI and clipboard ---------------------------------------------------------

// UIHandler shows source-driven UI. Methods that wait for the user block
// their goroutine; the runtime calls them off the event loop. Callbacks
// passed to the handler run the script function on the loop and return its
// awaited JSON result.
type UIHandler interface {
	ShowMessage(message string)
	ShowDialog(title, content string, actions []DialogAction) error // returns when closed
	LaunchURL(url string)
	ShowLoading(onCancel func()) (id int)
	CancelLoading(id int)
	// ShowInputDialog returns the entered text, or nil when cancelled.
	// validate, when non-nil, returns an error message or "" for valid input.
	ShowInputDialog(title string, image string, validate func(text string) string) (*string, error)
	// ShowSelectDialog returns the chosen index, or nil when cancelled.
	ShowSelectDialog(title string, options []string, initialIndex *int) (*int, error)
}

// DialogAction is a dialog button.
type DialogAction struct {
	Text  string
	Style string // text | filled | danger
	// Invoke runs the script callback; it returns after any promise settles.
	Invoke func() error
}

// Clipboard is the system clipboard.
type Clipboard interface {
	SetText(text string) error
	Text() (string, error)
}

var errNoUI = errors.New("No JavaScript UI handler provided")

// ui serves the UI messages. The client's handler is never called on the
// event loop: a handler that invokes a script callback synchronously (for
// example onCancel inside CancelLoading) would otherwise wait for the loop
// it is blocking. Methods with no result are fire-and-forget; showLoading
// waits for its id off the loop.
func (e *engine) ui(msg map[string]any) (moejs.Value, error) {
	h := e.mgr.svc.UI
	if h == nil {
		return moejs.Undefined(), errNoUI
	}
	switch fn := str(msg["function"]); fn {
	case "showMessage":
		if m := str(msg["message"]); m != "" {
			go h.ShowMessage(m)
		}
		return moejs.Undefined(), nil
	case "launchUrl":
		if u := str(msg["url"]); u != "" {
			go h.LaunchURL(u)
		}
		return moejs.Undefined(), nil
	case "cancelLoading":
		if id, ok := msg["id"].(int64); ok {
			go h.CancelLoading(int(id))
		}
		return moejs.Undefined(), nil
	case "showLoading":
		var onCancel func()
		if f, ok := jsFunc(msg["onCancel"]); ok {
			onCancel = func() { e.post(f, nil) }
		}
		// The legacy API returns the id synchronously. Wait for the handler
		// off the loop, bounded so a stuck handler cannot freeze the engine.
		ch := make(chan int, 1)
		go func() { ch <- h.ShowLoading(onCancel) }()
		select {
		case id := <-ch:
			return moejs.Int(int64(id)), nil
		case <-time.After(5 * time.Second):
			return moejs.Undefined(), errors.New("UI handler did not return a loading id")
		case <-e.ctx.Done():
			return moejs.Undefined(), ErrClosed
		}
	case "showDialog":
		var actions []DialogAction
		if list, ok := msg["actions"].([]any); ok {
			for _, a := range list {
				m, ok := a.(map[string]any)
				if !ok {
					continue
				}
				f, ok := jsFunc(m["callback"])
				if !ok {
					continue
				}
				style := str(m["style"])
				if style == "" {
					style = "text"
				}
				actions = append(actions, DialogAction{Text: str(m["text"]), Style: style,
					Invoke: func() error { return e.callback(f, nil) }})
			}
		}
		title, content := str(msg["title"]), str(msg["content"])
		return e.async(func() (any, error) { return nil, h.ShowDialog(title, content, actions) })
	case "showInputDialog":
		title, ok := msg["title"].(string)
		if !ok {
			return moejs.Null(), nil
		}
		var validate func(string) string
		if f, ok := jsFunc(msg["validator"]); ok {
			validate = func(text string) string {
				var out string
				_ = e.callbackResult(f, []any{text}, func(rt *moejs.Runtime, v moejs.Value) {
					if !v.IsNullish() {
						out = v.String()
					}
				})
				return out
			}
		}
		image := str(msg["image"])
		return e.async(func() (any, error) {
			s, err := h.ShowInputDialog(title, image, validate)
			if s == nil {
				return nil, err
			}
			return *s, err
		})
	case "showSelectDialog":
		title, ok := msg["title"].(string)
		if !ok {
			return moejs.Null(), nil
		}
		opts, ok := msg["options"].([]any)
		if !ok {
			return moejs.Null(), nil
		}
		var options []string
		for _, o := range opts {
			if s, ok := o.(string); ok {
				options = append(options, s)
			}
		}
		var initial *int
		if v, ok := msg["initialIndex"].(int64); ok {
			i := int(v)
			initial = &i
		}
		return e.async(func() (any, error) {
			i, err := h.ShowSelectDialog(title, options, initial)
			if i == nil {
				return nil, err
			}
			return int64(*i), err
		})
	default:
		return moejs.Undefined(), fmt.Errorf("No JavaScript UI handler provided for function: %s", fn)
	}
}

func (e *engine) clipboard(method string, msg map[string]any) (moejs.Value, error) {
	c := e.mgr.svc.Clipboard
	if c == nil {
		return moejs.Undefined(), errors.New("No JavaScript UI handler provided for method: " + method)
	}
	if method == "setClipboard" {
		text := str(msg["text"])
		return e.async(func() (any, error) { return nil, c.SetText(text) })
	}
	return e.async(func() (any, error) {
		s, err := c.Text()
		return s, err
	})
}

// async runs work off the loop and returns a promise of its result.
func (e *engine) async(work func() (any, error)) (moejs.Value, error) {
	p, settle := e.loop.NewPromise()
	go func() {
		v, err := work()
		settle(func(rt *moejs.Runtime) (moejs.Value, error) {
			if err != nil {
				return moejs.Undefined(), err
			}
			if v == nil {
				return moejs.Null(), nil
			}
			return rt.FromGo(v)
		})
	}()
	return p, nil
}

// jsFunc recognises a function exported by ToGoStrict.
func jsFunc(v any) (*moengine.Object, bool) {
	o, ok := v.(*moengine.Object)
	return o, ok && o != nil
}

// post schedules a script callback on the loop without waiting for it.
func (e *engine) post(f *moengine.Object, args []any) {
	e.loop.RunOnLoop(func(rt *moejs.Runtime) {
		if vals, err := goValues(rt, args); err == nil {
			_, _ = rt.CallFunction(moengine.ObjectValue(f), moejs.Undefined(), vals...)
		}
	})
}

// callback calls a script function and waits for it, including a returned
// promise. It must not be called on the loop goroutine; UI handlers run on
// their own goroutines (see ui).
func (e *engine) callback(f *moengine.Object, args []any) error {
	return e.callbackResult(f, args, nil)
}

func goValues(rt *moejs.Runtime, args []any) ([]moejs.Value, error) {
	vals := make([]moejs.Value, len(args))
	for i, a := range args {
		v, err := rt.FromGo(a)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	return vals, nil
}

func (e *engine) callbackResult(f *moengine.Object, args []any, use func(*moejs.Runtime, moejs.Value)) error {
	done := make(chan error, 1)
	ok := e.loop.RunOnLoop(func(rt *moejs.Runtime) {
		vals, err := goValues(rt, args)
		if err != nil {
			done <- err
			return
		}
		res, err := rt.CallFunction(moengine.ObjectValue(f), moejs.Undefined(), vals...)
		if err != nil {
			done <- err
			return
		}
		settle(rt, res, func(raw json.RawMessage, err error) {
			if err == nil && use != nil {
				v, perr := rt.ParseJSON(raw)
				if perr == nil {
					use(rt, v)
				}
			}
			done <- err
		})
	})
	if !ok {
		return ErrClosed
	}
	select {
	case err := <-done:
		return err
	case <-e.ctx.Done():
		return ErrClosed
	}
}
