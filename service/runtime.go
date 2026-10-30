package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/MoeForge/moejs"
)

// Host is what a service plugin may reach. Every callback is optional: a
// service that asks for a capability the host does not implement gets an
// error at call time rather than a missing function at load time.
//
// The host decides what a service may do; Grants decides what it may ask for.
// A service that calls something it did not declare gets a clear refusal.
type Host struct {
	// Fetch performs one HTTP request and returns the response body. It is
	// only reachable when the network permission is granted.
	Fetch func(ctx context.Context, url string) (string, error)
	// StorageGet reads one stored value. It reports false when the key is
	// unknown.
	StorageGet func(ctx context.Context, key string) (string, bool, error)
	// StorageSet writes one stored value.
	StorageSet func(ctx context.Context, key, value string) error
	// Secret reads one account value the user entered for this service.
	Secret func(ctx context.Context, key string) (string, error)
	// Log receives the service's own log lines.
	Log func(level, message string)
}

// Runtime runs one service plugin.
//
// A service is a JavaScript module that exports a handle function:
//
//	export function handle(method, payload) {
//	  if (method === 'ping') return { ok: true };
//	  return null;
//	}
//
// payload is the raw JSON text of the request, and the return value is
// serialised as JSON. The module reaches the host through the globals fetch,
// storageGet, storageSet, secret and log; each of them refuses clearly when
// the matching permission was not granted.
type Runtime struct {
	host Host
	rt   *moejs.Runtime
	mod  *moejs.Module
	man  *Manifest
	// granted is what the host allows on top of the manifest's request.
	granted []Permission

	// mu serialises calls. A MoeJS runtime is single-goroutine, and a
	// service may be called from several places at once.
	mu sync.Mutex
	// ctx ends the host work this runtime started; it is cancelled by Close.
	ctx    context.Context
	cancel context.CancelFunc
	// running is the live runtime, published for Interrupt.
	running atomic.Pointer[moejs.Runtime]
}

// NewRuntime creates a runtime for one service. The caller closes it.
// allowed lists the permissions the host grants; a permission the service did
// not ask for is not in force, and one it asked for but the host did not grant
// stays refused.
func NewRuntime(host Host, allowed ...Permission) *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{host: host, ctx: ctx, cancel: cancel, granted: allowed}
}

// Load compiles and loads the service module. The manifest is kept for the
// permission checks the host applies on every call.
func (r *Runtime) Load(ctx context.Context, m *Manifest, source string) error {
	if m == nil {
		return errors.New("service: no manifest")
	}
	grants, err := Grant(m, r.granted)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	mod, err := moejs.Compile("service:"+m.ID, source)
	if err != nil {
		return fmt.Errorf("service: %s does not compile: %w", m.ID, err)
	}
	rt := moejs.NewRuntime(moejs.Options{})
	if err := r.installGlobals(rt, m, grants); err != nil {
		return err
	}
	if err := rt.Load(mod); err != nil {
		return fmt.Errorf("service: %s does not load: %w", m.ID, err)
	}
	r.mu.Lock()
	r.mod, r.rt, r.man = mod, rt, m
	r.mu.Unlock()
	// The interrupt handle is published separately: a supervisor stops a busy
	// call from another goroutine, and Call holds mu for the whole call.
	r.running.Store(rt)
	return nil
}

// Interrupt stops whatever the runtime is doing. It is safe to call from
// another goroutine while Call is running, which is the point: a script that
// never returns cannot be stopped by a context.
func (r *Runtime) Interrupt(reason any) {
	if rt := r.running.Load(); rt != nil {
		rt.Interrupt(reason)
	}
}

// installGlobals puts the host functions in the runtime. They are always
// defined: a service that uses one it did not declare gets an error naming the
// permission, instead of "x is not defined", which reads like a bug in the
// service rather than a decision by the host.
func (r *Runtime) installGlobals(rt *moejs.Runtime, m *Manifest, grants Grants) error {
	fetch := moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		url := argString(realm, args, 0)
		if !grants.Allows(PermissionNetwork) {
			return moejs.Undefined(), fmt.Errorf("service: %s may not use the network: it did not ask for the %q permission", m.ID, PermissionNetwork)
		}
		if r.host.Fetch == nil {
			return moejs.Undefined(), fmt.Errorf("service: this host has no network access")
		}
		body, err := r.host.Fetch(r.ctx, url)
		if err != nil {
			return moejs.Undefined(), err
		}
		return moejs.String(body), nil
	})
	get := moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		key := argString(realm, args, 0)
		if !grants.Allows(PermissionStorage) {
			return moejs.Undefined(), fmt.Errorf("service: %s may not use storage: it did not ask for the %q permission", m.ID, PermissionStorage)
		}
		if r.host.StorageGet == nil {
			return moejs.Undefined(), fmt.Errorf("service: this host has no storage")
		}
		value, ok, err := r.host.StorageGet(r.ctx, key)
		if err != nil {
			return moejs.Undefined(), err
		}
		if !ok {
			return moejs.Null(), nil
		}
		return moejs.String(value), nil
	})
	set := moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		key := argString(realm, args, 0)
		value := argString(realm, args, 1)
		if !grants.Allows(PermissionStorage) {
			return moejs.Undefined(), fmt.Errorf("service: %s may not use storage: it did not ask for the %q permission", m.ID, PermissionStorage)
		}
		if r.host.StorageSet == nil {
			return moejs.Undefined(), fmt.Errorf("service: this host has no storage")
		}
		if err := r.host.StorageSet(r.ctx, key, value); err != nil {
			return moejs.Undefined(), err
		}
		return moejs.Undefined(), nil
	})
	secret := moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		key := argString(realm, args, 0)
		if !grants.Allows(PermissionSecrets) {
			return moejs.Undefined(), fmt.Errorf("service: %s may not read secrets: it did not ask for the %q permission", m.ID, PermissionSecrets)
		}
		if r.host.Secret == nil {
			return moejs.Undefined(), fmt.Errorf("service: this host has no account values")
		}
		value, err := r.host.Secret(r.ctx, key)
		if err != nil {
			return moejs.Undefined(), err
		}
		return moejs.String(value), nil
	})
	log := moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		if r.host.Log != nil {
			r.host.Log(argString(realm, args, 0), argString(realm, args, 1))
		}
		return moejs.Undefined(), nil
	})
	for name, fn := range map[string]moejs.NativeFunc{
		"fetch":      fetch,
		"storageGet": get,
		"storageSet": set,
		"secret":     secret,
		"log":        log,
	} {
		if err := rt.SetGlobal(name, fn); err != nil {
			return fmt.Errorf("service: cannot install %q: %w", name, err)
		}
	}
	return nil
}

// Call runs one method of the service. The result is the JSON text of what
// the handler returned.
func (r *Runtime) Call(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mod == nil {
		return nil, errors.New("service: no service is loaded")
	}
	if r.rt == nil {
		return nil, errors.New("service: the runtime is gone")
	}
	hook, err := r.mod.Hook("handle")
	if err != nil {
		return nil, fmt.Errorf("service: %s exports no handle function: %w", r.man.ID, err)
	}
	methodValue, err := r.rt.FromGo(method)
	if err != nil {
		return nil, err
	}
	payloadValue, err := r.rt.FromGo(string(payload))
	if err != nil {
		return nil, err
	}
	result, err := r.rt.Call(hook, methodValue, payloadValue)
	if err != nil {
		return nil, fmt.Errorf("service: %s failed on %q: %w", r.man.ID, method, err)
	}
	// An async handler returns a promise instead of a value. One that settled
	// during the call is read as its result; one still waiting is refused,
	// because silently turning it into {} would look like an empty answer.
	switch state, value, ok := moejs.PromiseResult(result); {
	case ok && state == moejs.PromiseFulfilled:
		result = value
	case ok && state == moejs.PromiseRejected:
		return nil, fmt.Errorf("service: %s failed on %q: %w", r.man.ID, method, rejectionText(r.rt, value))
	case ok:
		return nil, fmt.Errorf("service: %s returned a promise that has not settled; async handlers that wait are not supported yet, write a synchronous handler", r.man.ID)
	}
	raw, err := r.rt.ToGo(result)
	if err != nil {
		return nil, fmt.Errorf("service: %s returned a value that is not JSON: %w", r.man.ID, err)
	}
	if raw == nil {
		return json.RawMessage("null"), nil
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("service: %s returned a value that is not JSON: %w", r.man.ID, err)
	}
	return out, nil
}

// Close ends the runtime and cancels the host work it started.
func (r *Runtime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancel()
	r.running.Store(nil)
	r.mod, r.rt = nil, nil
}

// argString reads one string argument, or "" when it is missing.
func argString(realm *moejs.Realm, args []moejs.Value, index int) string {
	if index >= len(args) {
		return ""
	}
	raw, err := realm.ToGoStrict(args[index])
	if err != nil {
		return ""
	}
	text, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// rejectionText renders why a promise was rejected: the message of an Error,
// or the value itself when it is a string.
func rejectionText(rt *moejs.Runtime, v moejs.Value) error {
	if v.IsObject() {
		if message, err := rt.Get(v, "message"); err == nil {
			if raw, err := rt.ToGo(message); err == nil {
				if text, ok := raw.(string); ok && text != "" {
					return errors.New(text)
				}
			}
		}
	}
	if raw, err := rt.ToGo(v); err == nil {
		if text, ok := raw.(string); ok && text != "" {
			return errors.New(text)
		}
	}
	return errors.New("the service rejected without a message")
}
