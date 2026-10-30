package magpieplugin

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/MoeForge/moejs"
)

// Request is one HTTP request a package asked for. The client sends it: Core
// opens no connections, and the package never sees the client's transport.
type Request struct {
	Method string
	URL    string
	// Header maps canonical keys to values in the order given.
	Header map[string][]string
	Body   []byte
}

// Response is what the client got back. A non-2xx status is not an error:
// packages inspect it themselves.
type Response struct {
	Status     int
	StatusText string
	Header     map[string][]string
	Body       []byte
	URL        string
}

// Streamer is a Host that can also hand a reply over in chunks. A client that
// implements it lets a package read a streaming response as it arrives; one
// that does not still works, with replies that arrive whole.
type Streamer interface {
	// DoStream sends req and calls onChunk for each piece of the body. It
	// returns when the body ends, or with an error; a handler error stops it.
	DoStream(ctx context.Context, req *Request, onChunk func([]byte) error) (*Response, error)
}

// Host is what a client must provide for a package that uses the network.
// Do must honour ctx cancellation; a cancelled context should come back as an
// error, which rejects the package's fetch.
type Host interface {
	Do(ctx context.Context, req *Request) (*Response, error)
}

// prelude installs the Web APIs a provider package expects. MoeJS gives a
// runtime Intl, timers and promises but no fetch, Headers, Response or URL;
// a package that signs in uses all four. The implementations are small on
// purpose: they cover what provider packages call, not the whole Web platform.
const prelude = `(() => {
  class Headers {
    constructor(init) {
      this._m = new Map()
      if (!init) return
      if (init instanceof Headers) { for (const [k, v] of init._m) this._m.set(k, v); return }
      if (Array.isArray(init)) { for (const [k, v] of init) this.set(k, v); return }
      for (const k of Object.keys(init)) this.set(k, init[k])
    }
    set(k, v) { this._m.set(String(k).toLowerCase(), String(v)) }
    append(k, v) { this.set(k, v) }
    get(k) { const v = this._m.get(String(k).toLowerCase()); return v === undefined ? null : v }
    has(k) { return this._m.has(String(k).toLowerCase()) }
    delete(k) { this._m.delete(String(k).toLowerCase()) }
    forEach(fn, self) { for (const [k, v] of this._m) fn.call(self, v, k, this) }
    entries() { return this._m.entries() }
    keys() { return this._m.keys() }
    values() { return this._m.values() }
    [Symbol.iterator]() { return this._m.entries() }
  }
  const hasTextDecoder = typeof globalThis.TextDecoder === "function"
  class MagpieTextDecoder {
    constructor(label) { this.encoding = label || "utf-8"; this.fatal = false }
    decode(input) {
      if (input === undefined || input === null) return ""
      if (typeof input === "string") return input
      if (input instanceof ArrayBuffer) input = new Uint8Array(input)
      let out = ""
      for (let i = 0; i < input.length; i++) out += String.fromCharCode(input[i])
      return decodeURIComponent(escape(out))
    }
  }
  class ReadableStream {
    constructor() { this._c = []; this._w = []; this._done = false; this._err = null }
    _push(text) { if (this._done) return; this._c.push(text); this._wake() }
    _close() { this._done = true; this._wake() }
    _fail(e) { this._err = e; this._done = true; this._wake() }
    _wake() { while (this._w.length && (this._c.length || this._done || this._err)) this._w.shift()() }
    getReader() {
      const self = this
      const read = async () => {
        if (self._err) throw self._err
        if (self._c.length) return { value: self._c.shift(), done: false }
        if (self._done) return { value: undefined, done: true }
        await new Promise((r) => self._w.push(r))
        return read()
      }
      return {
        read,
        async cancel() { self._done = true; self._wake() },
        releaseLock() {},
      }
    }
    [Symbol.asyncIterator]() {
      const reader = this.getReader()
      return {
        async next() {
          const r = await reader.read()
          return r.done ? { value: undefined, done: true } : { value: r.value, done: false }
        },
      }
    }
  }
  class Response {
    constructor(r, stream) {
      this.status = r.status
      this.statusText = r.statusText || ""
      this.ok = r.status >= 200 && r.status < 300
      this.url = r.url || ""
      this.headers = new Headers(r.headers)
      this.bodyUsed = false
      this._body = r.body === undefined || r.body === null ? "" : r.body
      this.body = stream || null
    }
    async text() {
      this.bodyUsed = true
      if (this.body) {
        // Chunks are bytes, as the Web Streams API gives them; decode once so
        // a multi-byte character split across two chunks still comes out right.
        const parts = []
        let total = 0
        for await (const chunk of this.body) {
          if (typeof chunk === "string") { parts.push(chunk); continue }
          parts.push(chunk)
          total += chunk.length
        }
        if (parts.length && typeof parts[0] === "string") return parts.join("")
        const all = new Uint8Array(total)
        let at = 0
        for (const part of parts) { all.set(part, at); at += part.length }
        return new TextDecoder().decode(all)
      }
      return this._body
    }
    async json() { this.bodyUsed = true; return JSON.parse(await this.text()) }
    async arrayBuffer() {
      this.bodyUsed = true
      const s = this._body
      const out = new ArrayBuffer(s.length)
      const v = new Uint8Array(out)
      for (let i = 0; i < s.length; i++) v[i] = s.charCodeAt(i) & 0xff
      return out
    }
    clone() { return new Response({ status: this.status, statusText: this.statusText, url: this.url, headers: this.headers, body: this._body }) }
  }
  class URL {
    constructor(input, base) {
      let text = String(input)
      if (base !== undefined) {
        const b = new URL(String(base))
        if (!/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(text)) {
          if (text.startsWith("/")) {
            text = b.protocol + "//" + b.host + text
          } else {
            const path = b.pathname.replace(/\/[^/]*$/, "/")
            text = b.protocol + "//" + b.host + path + text
          }
        }
      }
      const p = __parseURL(text)
      this.href = p.href
      this.protocol = p.protocol
      this.host = p.host
      this.hostname = p.hostname
      this.port = p.port
      this.pathname = p.pathname
      this.search = p.search
      this.hash = p.hash
      this.username = p.username
      this.password = p.password
      this.origin = p.origin
    }
    toString() { return this.href }
  }
  class AbortSignal {
    constructor() { this.aborted = false; this._l = [] }
    addEventListener(name, fn) { if (name === "abort") this._l.push(fn) }
    removeEventListener(name, fn) { this._l = this._l.filter((x) => x !== fn) }
    throwIfAborted() { if (this.aborted) throw new Error("aborted") }
    static timeout(ms) {
      const s = new AbortSignal()
      const t = setTimeout(() => { s.aborted = true; for (const fn of s._l) fn() }, ms)
      if (t && typeof t.unref === "function") t.unref()
      return s
    }
    static abort() { const s = new AbortSignal(); s.aborted = true; return s }
  }
  class AbortController {
    constructor() { this.signal = new AbortSignal() }
    abort() { this.signal.aborted = true; for (const fn of this.signal._l) fn() }
  }
  globalThis.AbortSignal = AbortSignal
  globalThis.AbortController = AbortController
  const streams = new Map()
  globalThis.__magpieStreams = streams
  const encoder = new TextEncoder()
  const push = (id, text) => { const s = streams.get(id); if (s) s._push(encoder.encode(text)) }
  const close = (id) => { const s = streams.get(id); if (s) s._close() }
  const fail = (id, message) => {
    const s = streams.get(id)
    if (s) { streams.delete(id); s._fail(new Error(message)) }
  }
  if (!hasTextDecoder) globalThis.TextDecoder = MagpieTextDecoder
  globalThis.ReadableStream = ReadableStream
  globalThis.Headers = Headers
  globalThis.Response = Response
  globalThis.URL = URL
  // Request is the other half of fetch that packages actually use: they build
  // one to read its url, headers and body before deciding where to send it.
  class Request {
    constructor(input, init) {
      const opts = init || {}
      const base = input instanceof Request ? input : null
      this.url = base ? base.url
        : (typeof input === "string" ? input : (input instanceof URL ? input.href : String(input?.url ?? input)))
      this.method = String(opts.method || (base ? base.method : "GET")).toUpperCase()
      this.headers = new Headers(opts.headers !== undefined ? opts.headers : (base ? base.headers : undefined))
      this.body = opts.body !== undefined ? opts.body : (base ? base.body : null)
    }
    clone() { return new Request(this.url, { method: this.method, headers: this.headers, body: this.body }) }
  }
  globalThis.Request = Request
  globalThis.fetch = async (input, init) => {
    const opts = init || {}
    const target = typeof input === "string" ? input : (input instanceof URL ? input.href : String(input.url ?? input))
    const headers = new Headers(opts.headers)
    const pairs = []
    for (const [k, v] of headers) pairs.push([k, v])
    let body = opts.body
    if (body === undefined || body === null) body = ""
    else if (typeof body !== "string") body = String(body)
    const id = __hostBegin()
    const signal = opts.signal
    const onAbort = signal ? () => __hostAbort(id) : null
    if (onAbort) {
      if (signal.aborted) onAbort()
      else signal.addEventListener("abort", onAbort)
    }
    try {
      const method = String(opts.method || "GET").toUpperCase()
      if (__hostCanStream()) {
        const stream = new ReadableStream()
        __magpieStreams.set(id, stream)
        const head = await __hostFetchStream(id, target, method, pairs, body)
        if (head.error) { __magpieStreams.delete(id); throw new Error(head.error) }
        return new Response(head, stream)
      }
      const r = await __hostFetch(id, target, method, pairs, body)
      if (r.error) throw new Error(r.error)
      return new Response(r)
    } finally {
      if (onAbort) signal.removeEventListener("abort", onAbort)
    }
  }
  return { push, close, fail }
})()`

// UseHost installs the network bridge and the Web APIs a package expects.
// Every fetch goes to h, which is the client's transport; nothing here opens
// a connection.
func (in *Instance) UseHost(h Host) error {
	if h == nil {
		return fmt.Errorf("magpieplugin: nil host")
	}
	in.host = h
	if err := in.SetFunction("__parseURL", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		return in.rt.FromGo(parseURL(moejs.Arg(args, 0).String()))
	}); err != nil {
		return err
	}
	if err := in.SetFunction("__hostBegin", 0, in.hostBegin); err != nil {
		return err
	}
	if err := in.SetFunction("__hostAbort", 1, in.hostAbort); err != nil {
		return err
	}
	if err := in.SetFunction("__hostFetch", 5, in.hostFetch); err != nil {
		return err
	}
	if err := in.SetFunction("__hostCanStream", 0, func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
		_, ok := h.(Streamer)
		return moejs.Bool(ok), nil
	}); err != nil {
		return err
	}
	if err := in.SetFunction("__hostFetchStream", 5, in.hostFetchStream); err != nil {
		return err
	}
	script, err := moejs.CompileScript("__magpie-web__.js", prelude)
	if err != nil {
		return err
	}
	handlers, err := in.rt.RunScript(script)
	if err != nil {
		return err
	}
	// Handlers a streaming reply calls; Go reaches them through the loop.
	for name, target := range map[string]*moejs.Value{
		"push": &in.streamPush, "close": &in.streamClose, "fail": &in.streamFail,
	} {
		value, err := in.rt.Get(handlers, name)
		if err != nil {
			return err
		}
		*target = value
	}
	return nil
}

// hostFetchStream hands the reply over as it arrives: the head settles the
// fetch, then each chunk is pushed into the package's ReadableStream. A reply
// that fails after its head is a stream error, which the package sees while it
// reads rather than as a rejected fetch.
func (in *Instance) hostFetchStream(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
	streamer, ok := in.host.(Streamer)
	if !ok {
		return moejs.Undefined(), fmt.Errorf("magpieplugin: the host cannot stream")
	}
	id := int64(numberArg(args, 0))
	req := &Request{Method: moejs.Arg(args, 2).String(), URL: moejs.Arg(args, 1).String(), Header: map[string][]string{}}
	if req.Method == "" {
		req.Method = "GET"
	}
	if raw, err := in.rt.ToGo(moejs.Arg(args, 3)); err == nil {
		if pairs, ok := raw.([]any); ok {
			for _, pair := range pairs {
				kv, ok := pair.([]any)
				if !ok || len(kv) != 2 {
					continue
				}
				name, _ := kv[0].(string)
				value, _ := kv[1].(string)
				req.Header[name] = append(req.Header[name], value)
			}
		}
	}
	if body := moejs.Arg(args, 4).String(); body != "" {
		req.Body = []byte(body)
	}
	promise, settle := in.loop.NewPromise()
	done := in.loop.Hold()
	ctx, cancel := context.WithCancel(in.ctx)
	in.mu.Lock()
	in.pending[id] = cancel
	in.mu.Unlock()
	go func() {
		defer done(nil)
		defer func() {
			cancel()
			in.mu.Lock()
			delete(in.pending, id)
			in.mu.Unlock()
		}()
		onChunk := func(chunk []byte) error {
			text := string(chunk)
			var pushErr error
			posted := in.loop.RunOnLoop(func(rt *moejs.Runtime) {
				if _, err := rt.CallFunction(in.streamPush, moejs.Undefined(), moejs.Int(id), moejs.String(text)); err != nil {
					pushErr = err
				}
			})
			if !posted {
				return fmt.Errorf("magpieplugin: the instance is closed")
			}
			return pushErr
		}
		head, err := streamer.DoStream(ctx, req, onChunk)
		// The stream ends (or fails) on the loop, after the head settled.
		in.loop.RunOnLoop(func(rt *moejs.Runtime) {
			if err != nil {
				_, _ = rt.CallFunction(in.streamFail, moejs.Undefined(), moejs.Int(id), moejs.String(err.Error()))
				return
			}
			_, _ = rt.CallFunction(in.streamClose, moejs.Undefined(), moejs.Int(id))
		})
		settle(func(rt *moejs.Runtime) (moejs.Value, error) {
			if err != nil && head == nil {
				return rt.FromGo(map[string]any{"error": err.Error()})
			}
			headers := make([][2]string, 0, len(head.Header))
			for k, vs := range head.Header {
				for _, v := range vs {
					headers = append(headers, [2]string{k, v})
				}
			}
			return rt.FromGo(map[string]any{
				"status": head.Status, "statusText": head.StatusText,
				"headers": headers, "url": head.URL, "stream": true,
			})
		})
	}()
	return promise, nil
}

// hostFetch answers a package's fetch. The Go work runs outside the loop and
// settles the promise it returned, so a slow request never blocks the plugin's
// own timers and a cancelled context rejects rather than hangs.
// hostBegin reserves an id for one fetch, so an abort can name the request it
// cancels without the package seeing anything of the transport.
func (in *Instance) hostBegin(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
	id := in.nextID()
	in.mu.Lock()
	in.pending[id] = func() {}
	in.mu.Unlock()
	return moejs.Int(id), nil
}

// hostAbort cancels one fetch. A package that aborts a request expects its
// promise to reject; the host's Do sees a cancelled context.
func (in *Instance) hostAbort(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
	id := int64(numberArg(args, 0))
	in.mu.Lock()
	cancel := in.pending[id]
	delete(in.pending, id)
	in.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return moejs.Undefined(), nil
}

func (in *Instance) hostFetch(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
	id := int64(numberArg(args, 0))
	target := moejs.Arg(args, 1).String()
	method := moejs.Arg(args, 2).String()
	if method == "" {
		method = "GET"
	}
	req := &Request{Method: method, URL: target, Header: map[string][]string{}}
	// The prelude sends [[name, value], …]; read it as plain Go values.
	if raw, err := in.rt.ToGo(moejs.Arg(args, 3)); err == nil {
		if pairs, ok := raw.([]any); ok {
			for _, pair := range pairs {
				kv, ok := pair.([]any)
				if !ok || len(kv) != 2 {
					continue
				}
				name, _ := kv[0].(string)
				value, _ := kv[1].(string)
				req.Header[name] = append(req.Header[name], value)
			}
		}
	}
	if body := moejs.Arg(args, 4).String(); body != "" {
		req.Body = []byte(body)
	}
	promise, settle := in.loop.NewPromise()
	// A promise settled from another goroutine keeps the loop alive only if
	// the hold is taken before the loop can find nothing left to run.
	done := in.loop.Hold()
	ctx, cancel := context.WithCancel(in.ctx)
	in.mu.Lock()
	in.pending[id] = cancel
	in.mu.Unlock()
	go func() {
		defer done(nil)
		defer func() {
			cancel()
			in.mu.Lock()
			delete(in.pending, id)
			in.mu.Unlock()
		}()
		resp, err := in.host.Do(ctx, req)
		settle(func(rt *moejs.Runtime) (moejs.Value, error) {
			if err != nil {
				return rt.FromGo(map[string]any{"error": err.Error()})
			}
			headers := make([][2]string, 0, len(resp.Header))
			for k, vs := range resp.Header {
				for _, v := range vs {
					headers = append(headers, [2]string{k, v})
				}
			}
			return rt.FromGo(map[string]any{
				"status":     resp.Status,
				"statusText": resp.StatusText,
				"headers":    headers,
				"body":       string(resp.Body),
				"url":        resp.URL,
			})
		})
	}()
	return promise, nil
}

// numberArg reads a JavaScript number argument.
func numberArg(args []moejs.Value, i int) float64 {
	if n, ok := moejs.Arg(args, i).IsInt32(); ok {
		return float64(n)
	}
	// Larger ids arrive as doubles; both are exact well below 2^53.
	if moejs.Arg(args, i).IsNumber() {
		return moejs.Arg(args, i).AsNumber()
	}
	return 0
}

func parseURL(raw string) map[string]any {
	u, err := url.Parse(raw)
	if err != nil {
		u = &url.URL{}
	}
	port := u.Port()
	hostname := u.Hostname()
	host := hostname
	if port != "" {
		host = hostname + ":" + port
	}
	origin := ""
	if u.Scheme != "" {
		origin = u.Scheme + "://" + host
	}
	search, hash := "", ""
	if u.RawQuery != "" || strings.Contains(raw, "?") {
		search = "?" + u.RawQuery
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		hash = "#" + u.Fragment
	}
	password := ""
	if u.User != nil {
		password, _ = u.User.Password()
	}
	username := ""
	if u.User != nil {
		username = u.User.Username()
	}
	// WHATWG URL keeps at least the root path, and a package may compare
	// pathname to "/" to accept a bare host.
	pathname := u.Path
	if pathname == "" {
		pathname = "/"
	}
	return map[string]any{
		"href": u.String(), "protocol": u.Scheme + ":", "host": host, "hostname": hostname,
		"port": port, "pathname": pathname, "search": search, "hash": hash,
		"username": username, "password": password, "origin": origin,
	}
}

// AuthStore is the client's sign-in storage for provider packages. Core never
// keeps credentials itself: the client writes them where it keeps secrets
// (Keychain on Apple), and hands them back by provider id.
type AuthStore interface {
	// Get returns the stored sign-in for provider, or nil when there is none.
	Get(ctx context.Context, provider string) (map[string]any, error)
	// Set replaces the stored sign-in for provider.
	Set(ctx context.Context, provider string, value map[string]any) error
}

// clientPrelude exposes `client.auth.get` and `client.auth.set` to a package in
// the shape Magpie's plugins call: both take {path: {id}, body} and answer a
// promise.
const clientPrelude = `(() => {
  globalThis.__magpieClient = {
    auth: {
      async get(input) { return __authGet(String(input?.path?.id ?? "")) },
      async set(input) { await __authSet(String(input?.path?.id ?? ""), input?.body ?? {}); return {} },
    },
  }
})()`

// UseClient installs the sign-in bridge. A package may read the account it is
// running for and save a refreshed one; it cannot reach another provider's
// sign-in, because the id it names is checked against the instance's own.
func (in *Instance) UseClient(provider string, store AuthStore) error {
	if store == nil {
		return fmt.Errorf("magpieplugin: nil auth store")
	}
	in.provider, in.auth = provider, store
	// The client's store is local, so it answers on the loop: no goroutine to
	// race with the loop finding nothing left to run.
	if err := in.SetFunction("__authGet", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		// The id is checked, not ignored: answering another provider's name with
		// this provider's sign-in would hand a package the wrong credential while
		// looking like it worked.
		if moejs.Arg(args, 0).String() != provider {
			return moejs.Null(), nil
		}
		value, err := store.Get(in.ctx, provider)
		if err != nil {
			return moejs.Undefined(), err
		}
		if value == nil {
			return moejs.Null(), nil
		}
		return in.rt.FromGo(value)
	}); err != nil {
		return err
	}
	if err := in.SetFunction("__authSet", 2, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		id := moejs.Arg(args, 0).String()
		if id != provider {
			return moejs.Undefined(), fmt.Errorf("magpieplugin: %s cannot write %q's sign-in", provider, id)
		}
		raw, err := in.rt.ToGo(moejs.Arg(args, 1))
		if err != nil {
			return moejs.Undefined(), err
		}
		value, _ := raw.(map[string]any)
		if err := store.Set(in.ctx, provider, value); err != nil {
			return moejs.Undefined(), err
		}
		return moejs.Undefined(), nil
	}); err != nil {
		return err
	}
	script, err := moejs.CompileScript("__magpie-client__.js", clientPrelude)
	if err != nil {
		return err
	}
	_, err = in.rt.RunScript(script)
	return err
}
