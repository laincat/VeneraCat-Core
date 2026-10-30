# Magpie plugin system (iOS)

English | [简体中文](AI-SERVICE.md)

The current design follows [standard APIs and plugin boundaries](AI-CORE-BOUNDARY.en.md): Core includes OpenAI/Anthropic standard API infrastructure; Magpie plugins supply other providers and extensions such as routing, usage and content masking. iOS runs independently in-app; desktop initially connects to an installed local Magpie.

The material below preserves earlier design and implementation records. Statements such as hooks/auth not existing describe earlier stages, not the current overall state. Provider hooks, authorisation, requests/streaming and fake-service tests exist. Full standard direct-call entry points, generic extension points and real-account iOS acceptance remain incomplete. The package is `magpieplugin`, separate from the comic `plugin.Registry`.

## User goal

Run the provider plugins Magpie uses directly on iOS, reusing their login,
refresh, model listing and request adaptation. Implement selected Magpie built-in
providers as plugins with the same contract. No desktop gateway or Magpie/Bun
side process is a prerequisite.

Provider plugins, routing and protocol conversion are different layers: providers
handle accounts and vendor requests, routing chooses an account/model, and
conversion maps application messages to the target API. Supporting plugins does
not automatically reproduce Magpie's gateway.

## Original package audit

Pinned source:
[magpie-community/plugins@c590a740](https://github.com/magpie-community/plugins/tree/c590a7405792c1149e554bc0e58f8cb44a1f3da3).
Five manifests and entry modules were inspected, together with Copilot's two
relative modules. All five manifests declare MIT and no dependencies; this does
not mean they have no runtime dependencies.

| Package | Dependencies observed | Implication for iOS |
|---|---|---|
| Copilot | Relative endpoints/usage modules, fetch, Headers, URL, timers, device-code login | First compatibility candidate; not execution-tested, and service acceptance of this app needs verification |
| Factory | node:http, node:fs, Buffer, Web Streams, relative modules | Trace call paths and supply required adapters; loading an entry alone is insufficient |
| WorkBuddy | node:crypto, node:fs, node:os/path, node:zlib, TransformStream | Controlled crypto/compression bridges are possible; desktop file access cannot be assumed |
| Qoder | node:crypto, node:child_process, fs/promises, os/path/url, ReadableStream | Subprocess paths need replacement or disabling; incompatible paths exist |
| Grok | spawn/execFile, fs, os/path, crypto, local CLI discovery | CLI-dependent paths cannot run unchanged on iOS |

An import alone does not prove the entire package unusable: some dependencies
serve optional features and require call-path analysis. Empty stubs must not
pretend success. No real accounts, CLI installation, scripts or credentials from
other apps were used in this audit.

## Independent system and compatibility boundary

Use the name Magpie plugin system and code package `magpieplugin`. Read original
Magpie provider packages and manage installation records, versions, accounts and
runtime instances independently of comic keys, cookies and storage. Protocol
conversion and routing belong to the system's AI service; they are not features
implicitly supplied by every provider plugin.

Full compatibility is a target, not an achieved result. Verify pinned original
plugins for loading, authorization, refresh, model discovery, streaming and
cancellation. Report unavailable iOS CLI/subprocess capabilities explicitly;
do not hide them behind empty implementations. The initial scope is provider
plugins, not all Magpie pages, agents, Pi extensions or desktop features.

## Candidate runtime layers

| Layer | Responsibility |
|---|---|
| Package entries/modules | Read main/exports from package.json and retain relative modules; define an actual build strategy for TypeScript |
| Provider hooks | First validate auth.provider, methods/authorize/callback, loader/getAuth, client.auth.set; then config, provider.models, chat.headers and usage |
| Web compatibility APIs | fetch/Headers/Request/Response, URL, timers, AbortSignal, encoding and streams, based on actual package needs |
| Platform services | URLSession streams/cancellation, ASWebAuthenticationSession or device-code UI, Keychain and allowed crypto operations |
| AI routing/protocol | Unified requests/events, protocol codecs, capability checks and later routing/retries |

Core may handle protocols, module/hook adapters and testable scheduling; clients
still own networking, UI and credentials. Scope credentials to a plugin/account,
separate from comic cookies, source settings and ordinary logs. getAuth sees only
the current account and client.auth.set cannot write another account. Provider
scripts necessarily use tokens to make requests; isolation means their own
accounts only, not that scripts never see credentials. Content plugins must not
directly obtain provider credentials.

The execution engine is not settled. First check existing MoeJS module, Promise
and asynchronous host support; JavaScriptCore is a comparison option. Neither
automatically provides Node/Bun or the full Web API. Compatibility bridges use
client capabilities without opening connections or spawning processes in Core.
Archive format remains separate from the runtime contract: zip/7z and online npm
installation have not been chosen.

## Offline compatibility first, then real services

1. Use self-authored plugins to pin hook input/output, async calls, account scope
   and error propagation.
2. Test the pinned original Copilot modules against fake HTTP: device-code
   authorization, polling cancellation, credential writes, refresh and custom
   fetch. Keep real tokens out of tests.
3. Add streaming requests and discovery, then validate on iOS with the user's own
   account. Claim provider usability only after that verification.
4. Extend adapters for Factory and other packages; report CLI/desktop-file paths
   individually, then implement selected built-ins against the same contract.
   Add routing based on structured errors, usage and model capabilities; never
   splice another provider's generation after output has begun.

Check distribution-channel requirements for downloaded code and feature updates
before publishing. Engine compatibility does not establish App Store eligibility.

## Static compatibility analysis (implemented)

`magpieplugin.Analyze` walks the module graph from the entry through `import`,
`export ... from`, side-effect imports, dynamic `import()` and `require()`, and
reports:

- `Modules`: the package files reachable from the entry, sorted;
- `Externals`: what lies outside, split into `node` (built-ins needing a host
  adapter), `bare` (npm packages) and `url`;
- `Missing`: specifiers that look like package files but are absent.

Comments are stripped before scanning. Real packages use JSDoc type references
such as `@type {import('@opencode-ai/plugin').Plugin}`, which are not runtime
dependencies; scanning them reported a dependency the package never loads.
Regular-expression literals are not modelled, as noted in the source.

Against the pinned original package (static read only, no network, no execution):

```
Copilot  entry=index.mjs
         modules=[endpoints.mjs index.mjs usage.mjs]
         node=[]  bare=[]  missing=[]
```

Copilot depends only on Web-level capabilities (relative modules, `fetch`,
`Headers`, `URL`, timers), which makes it the first candidate for execution
testing. This is a static result: device-code authorization, credential writes,
refresh and streaming have not run. The `node:` dependencies of Qoder, Grok,
WorkBuddy and Factory appear in `Externals` instead of being silently ignored.

A static scan cannot see `eval`, assembled specifiers or paths chosen at run
time, so it is a list of what is missing, not proof of compatibility.

## Loading and execution (implemented)

`magpieplugin.Load` compiles the snapshot for MoeJS and `Link` resolves only
package-relative modules: a Node built-in or npm dependency fails there with
`ErrUnsupportedModule` instead of quietly becoming a stub. `NewInstance` gives
each account its own runtime (its own globals and errors), and `Call` invokes an
exported function with the plugin's `input`.

Magpie hooks are async, and MoeJS hands Go the promise object rather than its
value, so `Call` awaits it through a small built-in script and receives the
settled value in a callback. The call runs on an `eventloop`, which completes the
awaited work before the answer is read. A thrown value becomes a Go error rather
than a successful empty result.

Against the pinned original Copilot package (files read only; no network, no
account):

```
Load     3 modules compiled and linked
Instance top level evaluated, exports CopilotAuthPlugin = true
```

This shows the original package compiles, links, evaluates and exports its entry
under our engine. Authorization, device-code polling, credential writes, refresh
and real requests have not run, so the provider still cannot be called usable.

## Network and Web API bridge (implemented)

`Instance.UseHost(h)` takes the client's `Host.Do(ctx, *Request)`; Core still
opens no connection. It installs the five basics packages actually use — `fetch`,
`Request`, `Headers`, `Response` and `URL`:

- `fetch` hands the request to the host, runs it off the loop and settles the
  promise afterwards, so a slow request never blocks the package's own timers;
  a cancelled context rejects the promise.
- `Request` exists because a real package used it: Cloudflare's plugin builds one
  to read its own url and headers before deciding where to send the request. A
  package that reaches for it and finds it missing fails with `ReferenceError`,
  which reads like a broken package rather than a missing API.
- Header names are lower-cased, as the Web API does. `Response` offers
  `status`, `ok`, `headers`, `json()` and `text()`. Bodies cross as text, so
  integers such as `9007199254740993` are not rounded through a float.
- `URL` is parsed by Go's `net/url` and mapped to the Web fields, root path
  included.

Driving the pinned Copilot package through its real sign-in against a fake
service (no network, no account):

```
authorize  → POST /login/device/code → verification_uri + user_code
callback   → polls /login/oauth/access_token, two pendings, then the token
result     → { type: "success", refresh: "gho_test_token" }
enterprise → requests go to company.ghe.com, verification page too
refusal    → { type: "failed" } after a single poll
```

So this provider's device-code sign-in runs in our engine, which is more than
"it loads".

`Instance.UseClient(provider, store)` adds the credential step: a package reads
and writes the sign-in of **its own provider** through `client.auth.get` and
`client.auth.set`. Reading another id answers `null` — answering with its own
sign-in looked like success and handed the package the wrong credential — and
writing another id is refused. The store belongs to the
client (Keychain on Apple); Core keeps no credentials. Because it is local
storage, it answers on the loop; only the network goes through an async promise.

The same Copilot package then exercises the refresh path against the fake
service:

```
loader(getAuth)      returns a baseURL and defines a fetch
options.fetch        sees an empty access → POST copilot_internal/v2/token
                     → receives a token → client.auth.set writes it back
saved value          access=tid=…, expires=expires_at*1000-5min, refresh kept
foreign write        a package writing openai's sign-in is refused, store empty
```

Usage and model listing cross the bridge too:

```
Copilot auth.usage   asks copilot_internal/user (with the OAuth token, not the
                     inference token)
                     → { name: "Premium requests", used: 50, limit: 300,
                         amount: 150, resetsAt: "2026-01-01T00:00:00.000Z" }
a provider.models    model names, limit.context/output and modalities arrive
                     unchanged
```

Cancellation is wired too: aborting the signal passed to `fetch(url, {signal})`
really cancels the client's request (Go side: `context.WithCancel`) and rejects
the promise, instead of waiting for the network to give up.

```
AbortController + setTimeout(abort, 5ms)
fake host blocked on ctx.Done()
result: rejected:context canceled
```

The usage hook needs `AbortSignal.timeout`, which MoeJS does not provide, so the
prelude adds a small `AbortSignal`/`AbortController`: a timeout sets `aborted`
and runs its listeners. It does not pretend to cancel a request in flight.

Streaming is wired too. When the client also implements
`Streamer.DoStream(ctx, req, onChunk)`, fetch settles on the response head and
pushes each chunk into the package's `ReadableStream`:

```
package: const r = await fetch(url); for await (const c of r.body) …
chunks are bytes (Uint8Array), and a multi-byte character split across two
chunks still decodes correctly
a failure after the head ends the stream with an error (read() throws), not a
silent EOF
a client without Streamer falls back to whole responses, unchanged
```

Copilot's own `options.fetch` was checked as well: after it rewrites the auth
headers, the stream still arrives in pieces (`data: one`, `data: two`,
`data: [DONE]`), so the wrapper does not flatten it.

Protocol parsing and the retry policy are in place: whichever dialect a package
speaks, the client receives the same small set of events.

```
DialectChat       choices[0].delta.content → text; finish_reason → done
DialectResponses  response.output_text.delta / reasoning_*.delta / completed
DialectMessages   content_block_delta (text_delta | thinking_delta) / message_delta
error             a vendor error inside a 200 becomes EventError, not silence
usage             raw JSON kept with json.Number, so 9007199254740993 survives
```

`Attempt` records whether the reader has been shown anything; `MayRetry()`
returns `ErrNoRetryAfterOutput` once it has. An empty delta, a done event or a
vendor error does not count as output, so none of them locks retrying by mistake.

Routing is in place too: `Sender.Send` walks a list of endpoints (encoding the body in
each one's dialect) and treats failures differently depending on what the reader has seen.

```
nothing shown yet   try the next endpoint — an expired key or an empty quota is exactly
                    what a second account is for
text already shown  ErrNoRetryAfterOutput, and no further endpoint — a retry would repeat
                    or contradict what the reader read
empty delta / done  not output, so "the stream opened but said nothing" is still retryable
caller stopped      errStopStream comes back as is; no other endpoint runs
all failed          RouteError names the endpoints that were tried
```

The streaming half hid a real trap: when the client implements `Streamer`, an `io.Pipe`
feeds the decoder. The reader finishes on `[DONE]`, but the vendor may still be writing, so
the writer gets `io.ErrClosedPipe`. The first version reported that as a transport failure —
"it worked, but it said it failed". Now a closed pipe after the reader is done counts as
normal, while a reader that gave up still wins with its own error.

One trap is now pinned by a test: the final SSE block needs its own blank line or
the spec discards it — the first version of that test omitted it, and
`response.completed` vanished silently.

## The daily check-in

Some plugins can press their vendor's check-in themselves. The answer has a fixed set of
outcomes, so a client never parses a vendor's sentence:

```
claimed / done / ineligible / inactive / captcha / failed
```

`captcha` means the vendor wants a human check: **Magpie does not solve captchas and neither
does this**, so the user checks in in the vendor's own app. An answer outside the set becomes
`failed` with the plugin's own words in the reason (`the plugin answered "yes": …`), so its
wording is never mistaken for the host's conclusion.

## A plugin's own switches

Some packages declare switches ("free models only"); the user's choices are kept per
`provider|option` and read by the package through `input.magpie.option(provider, key)`. Two
details follow Magpie:

- **A key nobody declared answers `false`**, not `undefined`, so a package written as
  `if (input.magpie.option(…))` behaves the same on either host.
- **Another provider's values are unreachable**: `option("some other provider", …)` is always
  `false`, so a switch cannot leak across accounts.

## The model list comes from the vendor

Magpie keeps its own catalogue of which models each provider has and how large their
contexts are, because its gateway needs that up front. A client only needs to know **what
the user may pick**, so this does not copy a catalogue; it asks the party that knows:

```
the loader prepares the door → GET <baseURL>/models → {"data":[...]} or {"models":[...]}
```

Both shapes are accepted, and `id`, `name`, `context_window`, `max_output_tokens` and
`modalities` are normalised. A vendor that does not list models **says so** (with its HTTP
status) instead of answering an empty table or a made-up one — an empty table would read as
"this provider has no models".

## Normalised usage

A package reports its vendor's numbers; a client should see one shape. `NormalizeUsage`
follows Magpie's rules one by one, and a few of them are easy to get wrong:

```
used left out → null, not 0     "0% used" and "cannot tell" read very differently
used past 100 → kept            clamping would hide that the account is over its limit
seconds and millis both appear  below 1e11 is seconds; otherwise 1.7e9 becomes 1970
a string where a number goes → 0  matches Magpie: the vendor did say something
balance → only a real number    a prepaid wallet has a figure, not a share
```

## A conversation goes through the package's own door

A provider package does not hand over headers: its `loader(getAuth, provider)` refreshes the
account and returns a `fetch` that carries the sign-in. So the request has to go through the
package — building a parallel one in Core would mean a second copy of the refresh logic, and
two copies drift.

```go
inst.Chat(ctx, &ChatRequest{Model, Messages, System}, func(e Event) error { … })
```

Core builds the body → the plugin's `loader` refreshes and names a baseURL with a fetch → that
fetch sends it → the reply is parsed as it arrives into the same events (text, reasoning, done,
error, token usage).

Two things only a real run shows:

- **No streaming must look the same**: when the client implements no `Streamer`,
  `response.body` is null. The first version treated that as "no reply", so a conversation
  looked successful and said nothing. It now falls back to the whole text and feeds the same
  parser, so the events do not depend on that capability.
- **The handle has to be declared first**: in JavaScript, `const handle` placed later hits the
  temporal dead zone on the non-streaming branch — `Cannot access 'handle' before
  initialization`.

The test drives the original Copilot package against a fake service and asserts more than the
text: that the refresh happened before the request (`/copilot_internal/v2/token` appears) and
that the new token was written back (`access == tid=api`).

## The plugin ABI

A Magpie provider package exports a **function**; Magpie calls it with `{client}` and
gets back `{auth: {provider, methods, loader, usage, options}}`. A Swift client should not
re-implement that convention, so the bridge lives in Core: after
`Instance.InstallPluginBridge()` there is

```go
auth, _      := inst.PluginAuth()          // provider, sign-in methods, the plugin's own options
started, _   := inst.Authorize(0, inputs)  // the page to show the user
answer, _    := inst.Poll(started["id"])   // wait for the user (the callback polls itself)
inst.CancelSignIn(id)                      // give up on this sign-in
usage, _     := inst.Usage(provider)       // usage windows
```

Two details follow what Magpie actually does rather than what a document says:

- **Which function**: `default` first, otherwise the first function in the module's export
  order. The original Copilot package exports only `CopilotAuthPlugin` with no default — a
  default-only rule would call it "not a plugin".
- **The callback polls by itself**: Copilot's `callback()` loops until the vendor answers,
  so one `Poll` crosses both `authorization_pending` and the token. The fake service counts
  its calls, so the test proves it really asked twice instead of skipping ahead.

Tests against the original Copilot package:

```
TestPluginAuthDescribesThePackage  provider=github-copilot, methods/hasLoader/hasUsage correct
TestPluginDeviceFlow               page + code + one Poll yielding gho_…, with polls == 2
TestPluginCancelSignIn             a cancelled sign-in errors instead of waiting forever
TestPluginUsageReadsTheSignedInAccount
TestPluginBridgeNeedsAPlugin       a package with no plugin function says so plainly
TestPluginBundleIsPlainFiles       packages travel as path → bytes (base64)
```

## iOS bridge (mobile package)

Swift never sees a Go type: `mobile.Magpie` speaks JSON like the rest of the
surface.

```go
m := mobile.NewMagpie(host)           // the same Host the comic sources use
m.LoadPackage(filesJSON)              // {"path": base64 content}, all in memory
m.SetAuth("github-copilot", authJSON) // the sign-in the client read from Keychain
m.Call("Plugin", argJSON)             // run one export, get JSON back
m.SavedAuth()                         // what the package saved; store it in Keychain
m.Auth()                              // provider id, sign-in methods, plugin options
m.Authorize(0, inputsJSON)            // the page to open, plus this sign-in's id
m.Poll(id)                            // wait for the user; the callback polls itself
m.CancelSignIn(id)                    // the user gave up
m.Usage(provider)                     // usage windows, passed through as answered
m.Close()
```

`Auth`, `Authorize`, `Poll` and `Usage` answer the package's own JSON: what a usage
window looks like is the vendor's business, and Core does not invent numbers for it.

The whole surface is eighteen methods, and `mobile/surface_test.go` pins the names with an
AST walk — so a client can check the framework before compiling, instead of discovering the
gap as a Swift error about a missing method:

| Method | What it does | What the client must know |
|---|---|---|
| `LoadPackage(filesJSON)` | take a package in (`{"path": base64}`) | Core checks the paths and sizes again |
| `SetAuth(provider, authJSON)` | hand over an account's sign-in | the provider must match the package, or `client.auth.get` refuses the cross |
| `SavedAuth()` | what the plugin wrote back after a refresh | store it in the Keychain, not a plain file |
| `Auth()` | provider id, sign-in methods, plugin options | `methods[].kind` is `device` or `key`; `prompts` are the fields to render |
| `Authorize(idx, inputsJSON)` | begin a sign-in | `{id, immediate}`: when `immediate` is set the result is already in hand — do not poll |
| `Poll(id)` | wait for the user | for device codes; a key sign-in must not call it |
| `CancelSignIn(id)` | the user gave up | an unknown id is not an error |
| `Usage(provider)` | usage windows | normalised; "the vendor did not say" is `null`, never 0 |
| `UsageSummary(provider)` | a one-line summary | for a list, not for the detail view |
| `Models()` | the vendor's model table | a refusal is an error, not an empty table |
| `Checkin(provider)` | the daily claim | an unsupported vendor says so rather than pretending |
| `SetPluginOptions(optionsJSON)` | the plugin's own switches | keys are `provider\|option`; untouched switches keep the plugin's default |
| `Call(export, argJSON)` | run one export directly | for debugging and unusual paths |
| `ReadPackageArchive(bytes)` | unpack zip or tar.gz | the client does not need a second unpacker |
| `ChatStart/ChatNext/ChatCancel` | the conversation trio | pull, not push; `Close` cancels conversations in flight |
| `Close()` | release the instance | idempotent |

A conversation is delivered differently, because gomobile cannot push events from Go into
Swift:

```go
id := m.ChatStart("gpt-4o", messagesJSON, system, maxTokens) // begin one turn
ev := m.ChatNext(id)   // an event when ready, "" when not, {"kind":"end"} at the end
m.ChatCancel(id)       // the view went away: cancel the request
```

**Not blocking** is deliberate: `ChatNext` answers immediately and the client loops with its
own short wait. A blocking call would hold a Go invocation open for every "nothing yet", and a
cancel would only take effect once the next event arrived.

Requests still go through `Host.HTTP`, the same path a source uses, but the
partition is `magpie` and `cookies` is false: an AI sign-in reads no comic
source's cookie jar and writes nothing into it.

Driving the whole path against a fake host (install → device code → polling →
success):

```
TestMagpieRunsAPackageThroughTheHost  provider/url/result all correct, partition magpie
TestMagpieReturnsTheSavedSignIn       the refreshed access=tid=… and refresh come back
TestMagpieRefusesBadInput             bad JSON, bad base64, missing export, closed
```

Swift still needs its `Host` on `URLSession` (already exists) plus Keychain
storage, wrapped as a `MagpieRunner`; that part belongs in Nekularity.

## Current stream foundation

host.Transport.Do returns a complete byte slice and URLSessionTransport.send
waits for the complete response. Streaming needs a new interface and gomobile
event bridge, rather than presenting a buffered response as a live stream.
`host/sse` reads a client-owned stream without networking or interpreting provider
JSON or [DONE]. It handles chunks, LF/CRLF/CR, multiline data and persistent ids;
limits block bytes; propagates read errors; and discards unterminated final
blocks. The client owns cancellation, close and timeout. Higher protocol layers
must check completion markers: EOF alone is not generation success.
