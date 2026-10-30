# Magpie provider-plugin compatibility

English | [简体中文](MAGPIE-COMPATIBILITY.md)

Updated 2026-10-31. "Compatible" here means **actually run**, not "its JavaScript imports".

## Implemented and verified

| Capability | What it does | How it was verified |
|---|---|---|
| Package resolution | `exports["./server"]` → `main` → root export → `index.ts/tsx/js/mjs/cjs`; only package-relative modules are linked | the original Copilot package, entry `index.mjs` |
| Plugin factory | `default` first, otherwise the first exported function in module order, called with `{client, project, worktree, serverUrl, magpie}` | same package (it exports only a named function, no default) |
| `auth.methods` | lists sign-in methods; `authorize(inputs)` returns the page and code | device flow against a fake service: two `authorization_pending` answers, then a token |
| A method that settles at once | when an API-key method's `authorize` returns `{type:"success"}` directly, `Authorize` returns it under `immediate` and **no polling happens** — otherwise the client would poll for a callback that does not exist | a test package, plus the real `cloudflare-workers-ai`: its key sign-in settles at once and carries `metadata.accountId`, which its loader uses to build the base URL |
| Sign-in write-back | `client.auth.get/set`, limited to its own provider | the refreshed `access` is saved and `refresh` kept |
| `auth.usage` | normalised into one window shape; a share the vendor did not report is `null`, not 0 | six rule tests plus the original package |
| `auth.checkin` | fixed vocabulary `claimed/done/ineligible/inactive/captcha/failed` | a purpose-written package, including an outcome outside the set |
| `auth.options` | `input.magpie.option(provider, key)`; undeclared is `false`, another provider's is `false` | a purpose-written package |
| `loader` + `fetch` | requests go through the package's own fetch (it refreshes and authenticates) | original package: `/copilot_internal/v2/token` then `/chat/completions` |
| Streamed replies | `ReadableStream` chunks, normalised across three dialects; a client without `Streamer` hands the reply over whole to the same parser. **The package declares its dialect**: the loader returns `dialect` (`chat`/`responses`/`messages`) plus optional `path` and `headers`, so an Anthropic-shaped vendor needs no knowledge of it in the host | chunking, multi-byte characters across chunks; `TestAnthropicTemplateStreamsMessages` goes to `/messages` with `x-api-key` |
| Cancellation | each fetch cancels independently; a reader that gives up stops the transport | a blocking host plus `ctx.Done()` |
| Retry policy | nothing shown yet → another account is allowed; anything shown → `ErrNoRetryAfterOutput` | empty-delta retry and no-retry-after-text |
| Model list | `GET <baseURL>/models`, accepting both `data` and `models` | the original package and a fake service; a 404 is reported plainly |

## Explicitly unsupported (said plainly, never an empty stub)

| Case | Result |
|---|---|
| Node built-ins (`fs`, `child_process`, `http`, …) | `ErrUnsupportedModule` at `Load` |
| npm dependencies (bare specifiers) | the same |
| Packages that shell out to a CLI (Factory, Qoder, parts of WorkBuddy) | the same; they cannot run on iOS and this does not pretend otherwise |
| Outbound dialects other than Chat Completions | `Chat` sends Chat Completions only; Anthropic and Responses **inbound** parsing exists, outbound translation does not |
| Multiple accounts | one account per provider; Magpie's `provider#suffix` keys are not implemented |
| A scheduled check-in loop | manual trigger only; nothing runs "once a day" |
| Captchas | never solved; on `captcha` the user finishes in the vendor's own app |
| Gateway translation and routing strategy | this is not Magpie's full gateway; account choice and retries follow the two `Sender` rules |

## The community packages, one by one (2026-10-31)

Four packages from `magpie-community/plugins` at commit `5b4ef41` (the earlier pin
`c590a74` is no longer the tip — the repository moves) were audited statically first — `package.json`
and the imports in `index.mjs` — and then fetched from npm as the published packages and actually
loaded. Both agree: all four cannot run, and the reason is Node capability, not something left
unimplemented. Each refusal names the first module it could not resolve, for example
`factory`: `index.mjs:6:30: cannot resolve module "node:http"`. The recipe is in the comments of
`magpieplugin/community_vendor_test.go`: `npm pack` the four, then
`MAGPIE_VENDOR_DIR=<dir> go test -run TestVendorPackagesAreRefused -v ./magpieplugin`.
Without that directory the test skips, so CI depends on no third-party fixture.

| Package | Version | Licence | npm deps | Node capabilities used | Result on iOS |
|---|---|---|---|---|---|
| `@magpie-community/opencode-factory-auth` | 0.1.41 | MIT | none | `node:fs`, `node:http` | `ErrUnsupportedModule` (`fs`/`http`) |
| `@magpie-community/opencode-workbuddy-auth` | 0.2.3 | MIT | none | `node:crypto`, `node:fs`, `node:os`, `node:path`, `node:zlib` | `ErrUnsupportedModule`; `crypto`/`zlib` have no equivalent here |
| `@magpie-community/opencode-qoder-auth` | 0.2.50 | MIT | none | the above plus `node:child_process`, `process.env` | same, and it spawns a CLI — impossible by construction |
| `@magpie-community/opencode-grok-auth` | 0.2.3 | MIT | none | `node:child_process`, `node:crypto`, `node:fs`, `node:http`, `node:os`, `node:path` | same; it reads a local CLI's own sign-in state |

The audit was run again on 2026-10-31 with the same imports. It does not have to be done by hand:
`python3 scripts/audit-node-imports.py -a <package dir>` lists the Node built-ins and npm packages every
`.mjs` reaches for. `-a` matters — scanning only the entry file misses the half of it that sits in
`lib/server.mjs`, `adapter.mjs` or `oauth.mjs`. It also strips comments first, or one JSDoc `import(...)`
in the Copilot package reads as an npm dependency the package never loads.

I first shortlisted eight packages by "no Node built-in in `index.mjs`" and then loaded each
one for real — **the shortlist was wrong**: `index.mjs` only imports the package's own modules,
and `node:http`/`node:fs`/`node:crypto` sit inside `lib/server.mjs`, `adapter.mjs`, `oauth.mjs`
and `provider.mjs`. The actual result, with a test pinning each row:

| Package | Result |
|---|---|
| `cloudflare-workers-ai` 0.0.4 | **loads and reports a provider**, with `methods` (each with a `label`) and a `loader` |
| `calc` 0.1.0 | loads, but it is a calculator, not a provider (no `auth`) |
| `river` 0.1.0 | loads, and is a routing helper, not a provider |
| `chat` 0.1.3 | refused: `lib/server.mjs` needs `node:http` |
| `kimi-desktop` 0.1.5 | refused: `adapter.mjs` needs `node:fs` |
| `nous` 0.1.1 | refused: `oauth.mjs` needs `node:crypto` |
| `zen-free` 0.1.18 | refused: `provider.mjs` needs `node:crypto` |
| `search1api` 0.1.1 | refused: its entry is **TypeScript** (`src/index.ts`), and this runtime parses JavaScript only |

So there are **two** provider plugins that run: the original Copilot (0.1.0) and
`cloudflare-workers-ai` (0.0.4), plus two packages that load without being providers.
`search1api` also exposed a limitation that was not written down before: a package shipping a
`.ts`/`.tsx`/`.mts`/`.cts` file cannot be loaded. The error now says "this is TypeScript, and this
runtime parses JavaScript", instead of a syntax error that reads as if the file were corrupt.

Eleven more published packages were sampled on 2026-10-31 — cline, cursor, devin, kiro, mimo, trae,
zcode, zed, zen-free, commandcode and aider — and the same script reports **11 of 11 needing Node
built-ins**. All eleven reach for `node:crypto`, but each also needs `fs`, `http` or `child_process`, and
kiro needs `node:sqlite` plus `bun:sqlite`. What blocks them is a whole Node runtime, not one module:
adding `crypto` alone would not make any of them run.

**Besides those community packages, this repository now holds one that runs**:
`magpieplugin/testdata/openai-compatible/` uses only `fetch`, `Headers` and `URL`, and three tests
pin its key sign-in, its model list and its streamed reply (`TestTemplate*`). It is not a package
shipped with an app — that is a distribution decision — but the template for writing one: a package
needs `auth.provider`, `auth.methods`, and an `auth.loader` returning `{baseURL, fetch}`; usage and
check-in are optional, and missing means "the vendor did not report", not zero.

Two things follow, and both are worth saying:

- **Copilot running does not mean community packages generally run.** It happens to use only
  `fetch`/`Headers`/`URL` and timers, which makes it one of the few purely Web-API packages.
- Making these run would need a Node compatibility layer (`fs`/`crypto`/`zlib`/`child_process`)
  and real external CLIs, not "a few more APIs". That is outside what this iOS plugin system
  sets out to do; it is written here so nobody reads "Magpie compatible" as "every community
  package installs".

## The original Copilot package, walked through

Using the original files from `magpie-community/plugins@c590a74` against a fake service:
device-code sign-in → refresh written back → usage → model list → streamed reply → cancellation.
**Not** verified with a real account: that needs the client's transport and Keychain, which is
Nekularity's side.
