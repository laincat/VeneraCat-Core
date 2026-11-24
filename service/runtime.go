package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/MoeForge/moejs"
	"github.com/laincat/veneracat-core/host"
)

// FetchRequest describes an HTTP request issued by a service plugin via
// fetch(url, init).
type FetchRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    string
}

// FetchResponse is the HTTP response returned to a service plugin calling
// fetch(url, init). Non-2xx status codes are returned in Status/OK rather than
// as Go errors so plugins can inspect 401/403/404 responses cleanly.
type FetchResponse struct {
	Status  int
	OK      bool
	Body    string
	Headers map[string]string
}

// Host is what a service plugin may reach. Every callback is optional: a
// service that asks for a capability the host does not implement gets an
// error at call time rather than a missing function at load time.
//
// The host decides what a service may do; Grants decides what it may ask for.
// A service that calls something it did not declare gets a clear refusal.
type Host struct {
	// Fetch performs one GET HTTP request and returns the response body. It is
	// only reachable when the network permission is granted.
	Fetch func(ctx context.Context, url string) (string, error)
	// FetchRequest performs one HTTP request carrying method, headers and body,
	// returning status, body and response headers without treating non-2xx
	// status codes as errors. It backs fetch(url, init) and may also satisfy
	// single-argument fetch(url) when Fetch is nil.
	FetchRequest func(ctx context.Context, req FetchRequest) (*FetchResponse, error)
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

	// gate serialises calls. A MoeJS runtime is single-goroutine, and a
	// service may be called from several places at once.
	gate   chan struct{}
	active context.Context
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
	return &Runtime{host: host, ctx: ctx, cancel: cancel, granted: allowed, gate: make(chan struct{}, 1)}
}

func (r *Runtime) lock(ctx context.Context) error {
	select {
	case r.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-r.gate
			return err
		}
		if err := r.ctx.Err(); err != nil {
			<-r.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
}

func (r *Runtime) bind(ctx context.Context, rt *moejs.Runtime) func() {
	active, cancel := context.WithCancel(ctx)
	stopLifetime := context.AfterFunc(r.ctx, cancel)
	if r.ctx.Err() != nil {
		cancel()
	}
	r.active = active
	interrupted := make(chan struct{})
	stopInterrupt := context.AfterFunc(active, func() {
		rt.Interrupt(active.Err())
		close(interrupted)
	})
	return func() {
		if !stopInterrupt() {
			<-interrupted
			rt.ClearInterrupt()
		}
		stopLifetime()
		cancel()
		r.active = nil
	}
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
	if err := r.lock(ctx); err != nil {
		return err
	}
	defer func() { <-r.gate }()
	defer r.bind(ctx, rt)()
	if err := r.installGlobals(rt, m, grants); err != nil {
		return err
	}
	if err := rt.Load(mod); err != nil {
		return fmt.Errorf("service: %s does not load: %w", m.ID, err)
	}
	if err := r.active.Err(); err != nil {
		return err
	}
	r.mod, r.rt, r.man = mod, rt, m
	// The interrupt handle is published separately: a supervisor stops a busy
	// call from another goroutine, and Call holds the gate for the whole call.
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
		rawURL := argString(realm, args, 0)
		if !grants.Allows(PermissionNetwork) {
			return moejs.Undefined(), refusal(m, PermissionNetwork, "use the network")
		}
		hasInit := len(args) >= 2 && !args[1].IsUndefined() && !args[1].IsNull()
		if !hasInit {
			if r.host.Fetch != nil {
				body, err := r.host.Fetch(r.active, rawURL)
				if err != nil {
					return moejs.Undefined(), err
				}
				return moejs.String(body), nil
			}
			if r.host.FetchRequest != nil {
				resp, err := r.host.FetchRequest(r.active, FetchRequest{Method: http.MethodGet, URL: rawURL})
				if err != nil {
					return moejs.Undefined(), err
				}
				if resp == nil {
					return moejs.Undefined(), errors.New("service: host returned nil fetch response")
				}
				if resp.Status < 200 || resp.Status > 299 {
					return moejs.Undefined(), fmt.Errorf("service: %s returned HTTP %d", rawURL, resp.Status)
				}
				return moejs.String(resp.Body), nil
			}
			return moejs.Undefined(), fmt.Errorf("service: this host has no network access")
		}

		req, err := parseFetchInit(realm, args[1])
		if err != nil {
			return moejs.Undefined(), err
		}
		req.URL = rawURL
		if r.host.FetchRequest == nil {
			if r.host.Fetch == nil {
				return moejs.Undefined(), fmt.Errorf("service: this host has no network access")
			}
			if req.Method != http.MethodGet || len(req.Headers) > 0 || req.Body != "" {
				return moejs.Undefined(), errors.New("service: this host does not support custom fetch method, headers or body")
			}
			body, err := r.host.Fetch(r.active, rawURL)
			if err != nil {
				return moejs.Undefined(), err
			}
			return buildFetchResponseValue(realm, &FetchResponse{
				Status:  http.StatusOK,
				OK:      true,
				Body:    body,
				Headers: map[string]string{},
			})
		}
		resp, err := r.host.FetchRequest(r.active, req)
		if err != nil {
			return moejs.Undefined(), err
		}
		if resp == nil {
			return moejs.Undefined(), errors.New("service: host returned nil fetch response")
		}
		return buildFetchResponseValue(realm, resp)
	})
	get := moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		key := argString(realm, args, 0)
		if !grants.Allows(PermissionStorage) {
			return moejs.Undefined(), refusal(m, PermissionStorage, "use storage")
		}
		if r.host.StorageGet == nil {
			return moejs.Undefined(), fmt.Errorf("service: this host has no storage")
		}
		value, ok, err := r.host.StorageGet(r.active, key)
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
			return moejs.Undefined(), refusal(m, PermissionStorage, "use storage")
		}
		if r.host.StorageSet == nil {
			return moejs.Undefined(), fmt.Errorf("service: this host has no storage")
		}
		if err := r.host.StorageSet(r.active, key, value); err != nil {
			return moejs.Undefined(), err
		}
		return moejs.Undefined(), nil
	})
	secret := moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		key := argString(realm, args, 0)
		if !grants.Allows(PermissionSecrets) {
			return moejs.Undefined(), refusal(m, PermissionSecrets, "read secrets")
		}
		if r.host.Secret == nil {
			return moejs.Undefined(), fmt.Errorf("service: this host has no account values")
		}
		value, err := r.host.Secret(r.active, key)
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
	if err := r.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { <-r.gate }()
	if r.mod == nil {
		return nil, errors.New("service: no service is loaded")
	}
	if r.rt == nil {
		return nil, errors.New("service: the runtime is gone")
	}
	defer r.bind(ctx, r.rt)()
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
	if reason := r.active.Err(); reason != nil {
		return nil, reason
	}
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
	r.cancel()
	r.gate <- struct{}{}
	defer func() { <-r.gate }()
	r.running.Store(nil)
	r.mod, r.rt = nil, nil
}

// refusal explains a denied capability. "It did not ask" and "the host did not
// grant it" are different facts, and telling a service the first when its
// manifest asked for the permission sends whoever reads the log looking in the
// wrong place.
func refusal(m *Manifest, p Permission, what string) error {
	for _, requested := range m.Permissions {
		if requested == p {
			return fmt.Errorf("service: %s may not %s: this host did not grant the %q permission", m.ID, what, p)
		}
	}
	return fmt.Errorf("service: %s may not %s: it did not ask for the %q permission", m.ID, what, p)
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

func normalizeFetchMethod(raw string) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(raw))
	if method == "" {
		return http.MethodGet, nil
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead:
		return method, nil
	default:
		return "", fmt.Errorf("service: unsupported fetch method %q", method)
	}
}

func parseFetchInit(realm *moejs.Realm, v moejs.Value) (FetchRequest, error) {
	raw, err := realm.ToGoStrict(v)
	if err != nil {
		return FetchRequest{}, fmt.Errorf("service: invalid fetch init: %w", err)
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return FetchRequest{}, errors.New("service: fetch init must be an object")
	}
	req := FetchRequest{Method: http.MethodGet}
	if rawMethod, exists := fields["method"]; exists && rawMethod != nil {
		methodText, ok := rawMethod.(string)
		if !ok {
			return FetchRequest{}, errors.New("service: fetch init.method must be a string")
		}
		method, err := normalizeFetchMethod(methodText)
		if err != nil {
			return FetchRequest{}, err
		}
		req.Method = method
	}
	if rawHeaders, exists := fields["headers"]; exists && rawHeaders != nil {
		headerMap, ok := rawHeaders.(map[string]any)
		if !ok {
			return FetchRequest{}, errors.New("service: fetch init.headers must be an object")
		}
		if len(headerMap) > 0 {
			req.Headers = make(map[string]string, len(headerMap))
			for k, rawVal := range headerMap {
				val, ok := rawVal.(string)
				if !ok {
					return FetchRequest{}, fmt.Errorf("service: fetch header %q value must be a string", k)
				}
				if strings.EqualFold(strings.TrimSpace(k), "cookie") {
					return FetchRequest{}, errors.New("service: fetch init.headers may not set Cookie")
				}
				if err := host.ValidHeader(k, val); err != nil {
					return FetchRequest{}, fmt.Errorf("service: %w", err)
				}
				req.Headers[k] = val
			}
		}
	}
	if rawBody, exists := fields["body"]; exists && rawBody != nil {
		bodyText, ok := rawBody.(string)
		if !ok {
			return FetchRequest{}, errors.New("service: fetch init.body must be a string")
		}
		req.Body = bodyText
	}
	return req, nil
}

func buildFetchResponseValue(realm *moejs.Realm, resp *FetchResponse) (moejs.Value, error) {
	headers := make(map[string]any, len(resp.Headers))
	for k, v := range resp.Headers {
		headers[strings.ToLower(k)] = v
	}
	return realm.FromGo(map[string]any{
		"status":  resp.Status,
		"ok":      resp.Status >= 200 && resp.Status <= 299,
		"body":    resp.Body,
		"headers": headers,
	})
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

// Method is one thing a service says it can do.
type Method struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
}

// Describe asks the service what it offers.
//
// It calls the optional `describe` export, which returns
// {"methods":[{"name":"sync","title":"同步"}]}. A service that does not export
// it offers nothing to press, and that is not an error: many services are used
// by other code rather than by a button.
func (r *Runtime) Describe(ctx context.Context) ([]Method, error) {
	if err := r.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { <-r.gate }()
	if r.mod == nil || r.rt == nil {
		return nil, errors.New("service: no service is loaded")
	}
	defer r.bind(ctx, r.rt)()
	hook, err := r.mod.Hook("describe")
	if err != nil {
		return nil, nil
	}
	result, err := r.rt.Call(hook)
	if reason := r.active.Err(); reason != nil {
		return nil, reason
	}
	if err != nil {
		return nil, fmt.Errorf("service: %s failed to describe itself: %w", r.man.ID, err)
	}
	if state, value, ok := moejs.PromiseResult(result); ok {
		switch state {
		case moejs.PromiseFulfilled:
			result = value
		case moejs.PromiseRejected:
			return nil, fmt.Errorf("service: %s failed to describe itself: %w", r.man.ID, rejectionText(r.rt, value))
		default:
			return nil, fmt.Errorf("service: %s returned a promise that has not settled; async handlers that wait are not supported yet", r.man.ID)
		}
	}
	raw, err := r.rt.ToGo(result)
	if err != nil {
		return nil, fmt.Errorf("service: %s described itself with a value that is not JSON: %w", r.man.ID, err)
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("service: %s describe() must return an object with a methods list", r.man.ID)
	}
	list, ok := fields["methods"].([]any)
	if !ok {
		return nil, fmt.Errorf("service: %s describe() has no methods list", r.man.ID)
	}
	out := make([]Method, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		title, _ := entry["title"].(string)
		out = append(out, Method{Name: name, Title: title})
	}
	return out, nil
}
