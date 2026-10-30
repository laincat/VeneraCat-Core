// The plugin a provider package exports, reached without the client writing
// JavaScript.
//
// A Magpie provider package exports a function — default, or any named export —
// which Magpie calls with an `input` object and which answers with
// `{auth: {provider, methods, loader, usage, options}}`. A client that wants a
// sign-in would otherwise have to reproduce that ABI in its own language; this
// bridge keeps the ABI in one place, next to the runtime that already knows how
// to await it.
package magpieplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/MoeForge/moejs"
)

// pluginGlue builds the bridge inside the package's runtime. It is written as
// a script because the ABI is JavaScript: the factory takes `{client}` and its
// methods hand back promises.
const pluginGlue = `globalThis.__magpie = (() => {
  const pending = new Map()
  let seq = 0
  let pluginPromise = null

  // Magpie calls the default export when it is a function, and otherwise every
  // exported function in the module's own order. __magpieExport is a host
  // function: a script cannot see the module namespace itself.
  async function factory() {
    const names = globalThis.__magpieExportNames || []
    const order = ["default"].concat(names.filter((n) => n !== "default"))
    for (const name of order) {
      const v = globalThis.__magpieExport(name)
      if (typeof v === "function") return v
      if (v && typeof v === "object" && typeof v.server === "function") return v.server
    }
    return null
  }

  function input() {
    return {
      client: globalThis.__magpieClient,
      directory: "/",
      worktree: "/",
      project: { id: "magpie", worktree: "/", time: { created: 0 } },
      serverUrl: "http://127.0.0.1/",
      // A plugin's own switches. A key it did not declare reads as false, the
      // same answer Magpie gives, so a package written for either host behaves
      // the same.
      magpie: {
        option(provider, key) {
          const value = __magpieOption(String(provider ?? ""), String(key ?? ""))
          return value === null ? false : value
        },
        options(provider) {
          const seen = __magpieOptions(String(provider ?? ""))
          return seen === null ? {} : seen
        },
      },
    }
  }

  function plugin() {
    if (!pluginPromise) {
      pluginPromise = (async () => {
        const fn = await factory()
        if (!fn) throw new Error("this package exports no plugin function")
        return await fn(input())
      })()
    }
    return pluginPromise
  }

  async function auth() {
    const p = await plugin()
    const a = p && p.auth
    if (!a || typeof a !== "object") return null
    const methods = Array.isArray(a.methods) ? a.methods : []
    const options = Array.isArray(a.options) ? a.options : []
    return {
      provider: typeof a.provider === "string" ? a.provider : "",
      methods: methods.map((m, index) => ({
        index,
        label: typeof m?.label === "string" ? m.label : "",
        kind: typeof m?.kind === "string" ? m.kind : (typeof m?.type === "string" ? m.type : ""),
        hint: typeof m?.hint === "string" ? m.hint : "",
        // A key-based method asks for its fields with prompts; a client that
        // cannot see them has nowhere to type the key.
        prompts: (Array.isArray(m?.prompts) ? m.prompts : []).map((p) => ({
          key: typeof p?.key === "string" ? p.key : "",
          type: typeof p?.type === "string" ? p.type : "text",
          message: typeof p?.message === "string" ? p.message : "",
          placeholder: typeof p?.placeholder === "string" ? p.placeholder : "",
        })),
      })),
      options: options.map((o) => ({
        key: typeof o?.key === "string" ? o.key : "",
        label: typeof o?.label === "string" ? o.label : "",
        description: typeof o?.description === "string" ? o.description : "",
        confirm: typeof o?.confirm === "string" ? o.confirm : "",
        default: o?.default === true,
      })),
      hasUsage: typeof a.usage === "function",
      hasLoader: typeof a.loader === "function",
    }
  }

  // A sign-in method hands back {url, callback}: the page to show, and the call
  // that waits for the user. The callback stays here, because it is a closure.
  async function authorize(arg) {
    const index = arg?.index ?? 0
    const inputs = arg?.inputs || {}
    const p = await plugin()
    const methods = Array.isArray(p?.auth?.methods) ? p.auth.methods : []
    const method = methods[index]
    if (!method || typeof method.authorize !== "function") {
      throw new Error("this package has no such sign-in method")
    }
    const started = await method.authorize(inputs)
    // An API key is settled the moment it is given: there is nothing to wait
    // for, so it goes back as a result rather than as a handle nobody can poll.
    if (started && started.type === "success") {
      return { id: 0, immediate: started }
    }
    const id = ++seq
    pending.set(id, started)
    const code = started?.userCode ?? started?.user_code ?? started?.code ?? ""
    return {
      id,
      url: typeof started?.url === "string" ? started.url : "",
      userCode: typeof code === "string" ? code : "",
      instructions: typeof started?.instructions === "string" ? started.instructions : "",
      expiresIn: typeof started?.expiresIn === "number" ? started.expiresIn
        : typeof started?.expires_in === "number" ? started.expires_in : 0,
    }
  }

  async function poll(arg) {
    const id = arg?.id
    const started = pending.get(id)
    if (!started) throw new Error("no sign-in is waiting under that id")
    if (typeof started.callback !== "function") throw new Error("this sign-in has no callback")
    const result = await started.callback()
    if (result && result.type === "success") pending.delete(id)
    return result == null ? null : result
  }

  async function cancel(arg) {
    const id = arg?.id
    const started = pending.get(id)
    pending.delete(id)
    if (started && typeof started.cancel === "function") await started.cancel()
    return true
  }

  let chatOptions = null

  // The package prepares the request shape: its loader refreshes the sign-in
  // and returns the fetch that carries it. Core builds the body and reads the
  // reply, so a package's vendor quirks stay in the package.
  async function chatPrepare(arg) {
    const p = await plugin()
    const a = p?.auth
    if (!a || typeof a.loader !== "function") throw new Error("this package declares no loader")
    const provider = typeof a.provider === "string" ? a.provider : ""
    const getAuth = async () => globalThis.__magpieClient.auth.get({ path: { id: provider } })
    const options = await a.loader(getAuth, { models: {} })
    if (!options || typeof options.fetch !== "function" || !options.baseURL) {
      throw new Error("the package loader returned no usable request entry")
    }
    chatOptions = options
    // A package may name its dialect and its path: an Anthropic-shaped vendor
    // answers on /messages in the Messages dialect, and the host should not
    // have to guess from the URL.
    const dialect = typeof options.dialect === "string" && options.dialect ? options.dialect : "chat"
    return { baseURL: String(options.baseURL), dialect }
  }

  async function chatRun(arg) {
    const options = chatOptions
    if (!options) throw new Error("chat is not prepared yet")
    const handle = arg?.handle
    const dialect = typeof options.dialect === "string" && options.dialect ? options.dialect : "chat"
    const path = typeof options.path === "string" && options.path
      ? options.path
      : (dialect === "messages" ? "/messages" : dialect === "responses" ? "/responses" : "/chat/completions")
    // Headers the package declared come first, so a vendor that wants its own
    // name for the key (x-api-key) gets it without the host knowing the vendor.
    const headers = Object.assign({ "Content-Type": "application/json", Accept: "text/event-stream" },
                                  options.headers && typeof options.headers === "object" ? options.headers : {})
    const response = await options.fetch(arg?.url || (options.baseURL + path), {
      method: "POST",
      headers,
      body: arg?.body ?? "",
    })
    if (!response.ok) {
      let text = ""
      try { text = await response.text() } catch (e) {}
      return { status: response.status, ok: false, body: text.slice(0, 200) }
    }
    if (!response.body) {
      // A client that cannot stream hands the reply over whole; the reader
      // treats it the same, so the events do not depend on that capability.
      const whole = await response.text()
      if (whole) __magpieChatPush(String(handle), whole)
      return { status: response.status, ok: true }
    }
    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    for (;;) {
      const { value, done } = await reader.read()
      if (done) break
      __magpieChatPush(String(handle), decoder.decode(value, { stream: true }))
    }
    const rest = decoder.decode()
    if (rest) __magpieChatPush(String(handle), rest)
    return { status: response.status, ok: true }
  }

  // A vendor that lists its models over HTTP lets a client offer the real ones
  // instead of a catalogue baked into this host. A vendor that does not is said
  // plainly rather than answered with an invented list.
  async function modelsRun(arg) {
    const options = chatOptions
    if (!options) throw new Error("chat is not prepared yet")
    const url = arg?.url || (options.baseURL + "/models")
    const response = await options.fetch(url, { method: "GET", headers: { Accept: "application/json" } })
    if (!response.ok) {
      let text = ""
      try { text = await response.text() } catch (e) {}
      return { status: response.status, ok: false, body: text.slice(0, 200) }
    }
    return { status: response.status, ok: true, body: await response.json() }
  }

  // A plugin may press its vendor's daily check-in itself. Magpie keeps the
  // answer's shape small and fixed, so a client shows one card for any vendor.
  async function checkin(arg) {
    const p = await plugin()
    const a = p?.auth
    if (typeof a?.checkin !== "function") throw new Error("this package declares no check-in")
    const provider = typeof a.provider === "string" ? a.provider : ""
    const getAuth = async () => globalThis.__magpieClient.auth.get({ path: { id: provider } })
    return await a.checkin(getAuth, arg?.provider ?? provider)
  }

  async function usage(arg) {
    const p = await plugin()
    if (typeof p?.auth?.usage !== "function") return null
    return await p.auth.usage(arg?.provider ?? "")
  }

  return { plugin, auth, authorize, poll, cancel, usage, checkin, chatPrepare, chatRun, modelsRun }
})()
`

var errNoPlugin = errors.New("magpieplugin: this package exports no plugin function")

// InstallPluginBridge makes the package's plugin reachable as JSON hooks. It
// must run after the globals the package expects (fetch, client.auth) exist.
func (in *Instance) InstallPluginBridge() error {
	if in.bridgeInstalled {
		return nil
	}
	names, err := in.exportNames()
	if err != nil {
		return err
	}
	// The glue reads the module namespace through this host function: a script
	// cannot reach it directly.
	if err := in.SetFunction("__magpieExport", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		if len(args) == 0 {
			return moejs.Undefined(), nil
		}
		value, ok := in.rt.Export(args[0].String())
		if !ok {
			return moejs.Undefined(), nil
		}
		return value, nil
	}); err != nil {
		return err
	}
	if err := in.SetGlobal("__magpieExportNames", names); err != nil {
		return err
	}
	if err := in.SetFunction("__magpieOption", 2, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		provider := moejs.Arg(args, 0).String()
		key := moejs.Arg(args, 1).String()
		in.optionsMu.Lock()
		value, ok := in.pluginOptions[provider+"|"+key]
		in.optionsMu.Unlock()
		if !ok {
			return moejs.Null(), nil
		}
		return in.rt.FromGo(value)
	}); err != nil {
		return err
	}
	if err := in.SetFunction("__magpieOptions", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		provider := moejs.Arg(args, 0).String()
		in.optionsMu.Lock()
		out := map[string]any{}
		for name, value := range in.pluginOptions {
			if strings.HasPrefix(name, provider+"|") {
				out[strings.TrimPrefix(name, provider+"|")] = value
			}
		}
		in.optionsMu.Unlock()
		return in.rt.FromGo(out)
	}); err != nil {
		return err
	}
	// A reply arrives in pieces: the package pushes decoded text, and the
	// reader below turns it into events while the loop is still running.
	if err := in.SetFunction("__magpieChatPush", 2, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		handle, err := strconv.ParseInt(moejs.Arg(args, 0).String(), 10, 64)
		if err != nil {
			return moejs.Undefined(), fmt.Errorf("magpieplugin: reply handle %q: %w", moejs.Arg(args, 0).String(), err)
		}
		text := moejs.Arg(args, 1).String()
		in.chatMu.Lock()
		writer := in.chatPipes[handle]
		in.chatMu.Unlock()
		if writer == nil {
			return moejs.Undefined(), fmt.Errorf("magpieplugin: no reply is open under %d", handle)
		}
		if _, err := writer.Write([]byte(text)); err != nil {
			return moejs.Undefined(), err
		}
		return moejs.Undefined(), nil
	}); err != nil {
		return err
	}
	script, err := moejs.CompileScript("__magpie-plugin__.js", pluginGlue)
	if err != nil {
		return err
	}
	if _, err := in.rt.RunScript(script); err != nil {
		return err
	}
	in.bridgeInstalled = true
	return nil
}

// exportNames lists the module's exports in declaration order, which is the
// order Magpie tries them in.
func (in *Instance) exportNames() ([]string, error) {
	module := in.rt.Module()
	if module == nil {
		return nil, errors.New("magpieplugin: the package has no module")
	}
	return module.Exports(), nil
}

// hook calls one of the bridge's functions with a JSON argument and returns
// its answer as a Go value.
func (in *Instance) hook(name string, argJSON string) (any, error) {
	if !in.bridgeInstalled {
		return nil, errors.New("magpieplugin: the plugin bridge is not installed")
	}
	script, err := moejs.CompileScript("__magpie-hook__.js", "globalThis.__magpie."+name)
	if err != nil {
		return nil, err
	}
	fn, err := in.rt.RunScript(script)
	if err != nil {
		return nil, err
	}
	arg := moejs.Undefined()
	if strings.TrimSpace(argJSON) != "" {
		parsed, err := in.rt.ParseJSONString(argJSON)
		if err != nil {
			return nil, fmt.Errorf("magpieplugin: %s argument: %w", name, err)
		}
		arg = parsed
	}
	return in.callValue(name, fn, arg)
}

// SetPluginOptions hands the plugin its own switches: key "provider|option" to
// the value the user chose. Magpie keeps these per provider and lets the package
// read them through `input.magpie.option`.
func (in *Instance) SetPluginOptions(options map[string]bool) {
	in.optionsMu.Lock()
	defer in.optionsMu.Unlock()
	in.pluginOptions = options
}

// PluginAuth reports what the package's plugin offers: its provider id, its
// sign-in methods and its own options. A package without a plugin answers nil.
func (in *Instance) PluginAuth() (map[string]any, error) {
	answer, err := in.hook("auth", "")
	if err != nil {
		return nil, err
	}
	if answer == nil {
		return nil, nil
	}
	out, ok := answer.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("magpieplugin: auth answered %T, not an object", answer)
	}
	return out, nil
}

// Authorize starts one sign-in method and returns the page to show the user.
func (in *Instance) Authorize(methodIndex int, inputs map[string]any) (map[string]any, error) {
	arg, err := json.Marshal(map[string]any{"index": methodIndex, "inputs": inputs})
	if err != nil {
		return nil, err
	}
	answer, err := in.hook("authorize", string(arg))
	if err != nil {
		return nil, err
	}
	out, ok := answer.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("magpieplugin: authorize answered %T, not an object", answer)
	}
	return out, nil
}

// Poll waits once for the user to finish signing in.
func (in *Instance) Poll(id int64) (map[string]any, error) {
	answer, err := in.hook("poll", fmt.Sprintf(`{"id":%d}`, id))
	if err != nil {
		return nil, err
	}
	if answer == nil {
		return nil, nil
	}
	out, ok := answer.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("magpieplugin: poll answered %T, not an object", answer)
	}
	return out, nil
}

// CancelSignIn forgets a pending sign-in.
func (in *Instance) CancelSignIn(id int64) error {
	_, err := in.hook("cancel", fmt.Sprintf(`{"id":%d}`, id))
	return err
}

// Usage runs the package's usage hook, when it has one.
func (in *Instance) Usage(provider string) (map[string]any, error) {
	arg, err := json.Marshal(map[string]any{"provider": provider})
	if err != nil {
		return nil, err
	}
	answer, err := in.hook("usage", string(arg))
	if err != nil {
		return nil, err
	}
	if answer == nil {
		return nil, nil
	}
	out, ok := answer.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("magpieplugin: usage answered %T, not an object", answer)
	}
	return out, nil
}
