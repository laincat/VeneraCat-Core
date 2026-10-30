# VeneraCat Core status

English | [简体中文](STATUS.md)

## A service reaches the network through a controlled bridge (2026-11-04)

A service's `fetch` used to land straight on whatever function the host supplied, so the restriction only existed in the caller. Core now ships `service.Bridge`: only `http`/`https`, an optional host and subdomain policy, **every redirect hop re-checked by the same rules**, a 4 MiB response bound by default, and no cookies by default. `Bridge.Host()` puts it behind the runtime, so a service has no other way out.

The tests use `localhost` against `127.0.0.1` - the same machine under a different host name - to show that the policy, not the network, is what stops a redirect; they also cover `file:`/`ftp:` refusal, the size bound, a 403, cancellation returning within 400 ms, and a refused call inside a service naming "not an allowed host".

## An async handler no longer becomes an empty object (2026-11-04)

`async function handle` returns a promise, and the runtime only ran `ToGo` on it, so an async handler produced `{}` - which reads as "the service had no result" while the answer was really being dropped. The state is now handled explicitly: a promise that settled during the call is read as its result, a rejection comes back as an error carrying the Error message, and one still waiting is refused with a clear "the returned promise has not settled; async handlers that wait are not supported yet".

`fetch` still blocks, so there is nothing useful to wait for yet. Regressions cover a settled async handler, an async throw, and a promise that never settles being refused instead of answering `{}`.

## A refusal must distinguish "never asked" from "not granted" (2026-11-04)

The permission check is "requested ∩ granted", but the refusal text always said the service never asked for the permission. When a service did ask and the host simply withheld it (the `secrets` case with no account value), that sentence sends whoever reads the log in the wrong direction. The two are now separate: `did not ask for the "x" permission` and `this host did not grant the "x" permission`. This came out of the desktop 0.2.16 to 0.2.17 profile compatibility test.

One regression covers each case.

## Service lifecycle: a deadline interrupts the runtime instead of giving up (2026-11-04)

Keeping several services resident needs a host that can stop them. `service.Supervisor` loads on first use and reuses afterwards; the resident set is bounded at 4 by default, the least recently used service goes past the bound; `Sweep` unloads one idle for 5 minutes by default and the next call loads it again; and the identity of the manifest the loader returns must match the request.

One call has a 15 second deadline by default, enforced by **interrupting the runtime**. A script is single-threaded and never looks at a context, so merely announcing a timeout would leave `while(true){}` holding the goroutine forever; in the test, a looping service with a 400 ms deadline returned `ErrTimeout` within 0.40 s and was unloaded, with the next call loading it again. A script error is the opposite case: the error goes back to the caller and the service stays resident.

For that, `Runtime` publishes its interrupt handle separately (`atomic.Pointer`); otherwise an interrupt would contend for the lock the running call holds and never arrive. Regressions cover loading once and reusing, the looping script timing out and being unloaded, a call after a script error, LRU eviction, idle sweeping, unknown services and calls after close, a mismatched manifest identity and payload pass-through; the race tests pass.

## Service packages travel as zip (2026-11-04)

A service entry in a collection needs a shape to be distributed in, and one file cannot hold "manifest plus entry plus helpers". `ReadPackageZip` reads the manifest and the entry from a zip and hands the whole package to the installer; it checks the declared sizes before expanding, then applies the limit again to the bytes actually read, so an archive that lies about its sizes cannot exhaust memory. Paths that are `../`, absolute, `./` or use backslashes are refused, and the entry must really be in the package.

This is reading and checking only: writing to disk, the install record and upgrades stay with the client. Regressions cover a normal package, a missing manifest, a missing entry, an invalid manifest, an entry that is not in the package, path traversal, a non-zip and an empty archive, plus extra files being kept.

## Service plugin runtime (2026-11-04)

After the manifests and permissions, the `service` package can now run a service: the module exports `handle(method, payload)`, the host calls it per method, and the return value is serialised as JSON. The module reaches the host through five globals, `fetch`, `storageGet`, `storageSet`, `secret` and `log`, each gated by the permissions in force. A refused call returns an error naming the missing permission rather than `undefined is not a function`, because the latter reads like a bug in the service instead of a decision by the host.

A few boundaries are worth stating: calls run one at a time (a runtime has a single execution thread); `fetch` blocks, so the first version supports synchronous handlers only and no `await`; when the host does not implement a capability at all (no network whatsoever) the call says the host has no such capability, which is a different thing from a missing permission; and the host may allow less than the service asked for, with the refused part exposed through `Grants.Denied` instead of silently running with less.

The contract is written down in `docs/SERVICE-PLUGINS.md`. Regressions cover a normal call, a throwing script, a missing handler, the gating and refusal of network and storage, the host allowing less than the service asked for, log delivery, and a cancelled call not running. Neither client is wired to install or the interface yet.

## Service manifests and permissions (2026-11-04)

Services such as Trakt, TMDB and Bangumi need a host, and the part a host can check starts with the manifest. The new `service` package parses and validates a service manifest (`id`, `name`, `version`, `kind`, `entry`, `permissions`) and intersects "what the service asks for" with "what the host allows", so an ungranted permission is never reported as available.

A few deliberate choices: unknown fields are rejected, because a service that believes it asked for a capability the host ignored would run in a smaller sandbox than it assumes; `entry` must be a relative path inside the package, with absolute paths, `../`, backslashes and `./` refused; and an unknown permission can neither appear in a manifest nor be granted by a host, because when the two sides disagree about what is being allowed, guessing is how a sandbox gets a hole. `Grants` exposes both `Effective` and `Denied` so an interface can show the real scope instead of silently running with less.

This is manifests and permissions only: no JavaScript is loaded or executed, and no network or storage implementation exists yet; the section above records the runtime that came after. Regressions cover a well-formed manifest, unknown fields, invalid identity/entry/permissions, the size bound, the intersection and refusals, and a manifest JSON round trip. Cross-platform CI and publication keep their own receipts.

## A stale webview login handle now reports itself (2026-11-04)

A login handle belongs to the runtime that issued it. When the source is updated or removed while the login window is open, the old handle has no callbacks left, but `WebviewLoginDone` used to return success: the client treated "the callback never ran" as a completed login. `WebviewStatus` threw "source does not define this function", which also contradicted its documented promise to answer false when the callback is absent.

Handles are now recorded when issued, and a handle this runtime never issued returns `ErrWebviewHandleGone`; `WebviewStatus` returns false as documented when the callback is missing; `WebviewLoginDone` still lets a URL-only source finish normally. Regressions cover both the replaced-source handle and the callback-free source.

This covers Windows targeted and race runs. Cross-platform CI and client consumption keep their own receipts and do not imply that iOS or the desktop client is finished.

## Mobile index-identity entry point (2026-11-04)

Added gomobile `InstallComicEntry`, delegating to existing InstallFromComicIndex. A mismatch between the index key and the script key observed after execution is rejected before publication; existing registry records remain unchanged. Targeted Windows race passed. This does not alter generic Install/Replace behavior and does not claim client wiring or Apple binding.



Active JS key collisions previously returned a plain runtime LoadError, bypassing registry ErrExists and preventing file/URL reinstall from entering the client's update path. The runtime now supplies Key/Kind through DuplicateKeyError while retaining LoadError classification and legacy text. The registry maps only keys it owns to KeyExistsError, still matching errors.Is(ErrExists). Builtin, index and restore collisions also carry structured keys. Wrong script kinds remain rejected; runtime-only keys are not claimed as installed registry entries, and script exception text never selects an update target.

The former behavior has a minimal failing reproduction. Targeted/full Windows race and vet passed. A single full run with community and vendor fixtures recorded577 top-level PASS,0 FAIL and1 SKIP (no live Wnacg input). Cases cover enabled/disabled comic/video collisions, legacy LoadError, externally loaded runtime keys, builtin/index/restore conflicts, forged exceptions and type rejection. The isolated Nekophage consumer uses errors.As(KeyExistsError) in keyOf; real file update, URL update and execution after restoring into a fresh runtime passed, with forged text rejected.

Evidence: `E:/Temp/core-duplicate-key-evidence/`. This covers Windows and an isolated consumer. Cross-platform CI, publication and consumer pins retain their own commit receipts; no Apple/UI/device acceptance is inferred. Changing error types does not expand guarantees about script init side effects.

## Shared source collection consumer foundation (2026-11-04)

Core parses `veneracat-source/1-draft`, preserves source and entry identities, resolves relative artifact URLs, reports platform/capability compatibility and verifies artifact size and SHA-256. Parsing performs no network access, installation or authorization; planned entries cannot contain installable artifacts. Windows targeted race, full race, vet and bilingual checks passed; the actual generated seven-entry collection and three artifacts parsed and verified. The gomobile JSON methods `ParseSourceCollection` and `VerifySourceArtifact` now expose compatibility and artifact verification to clients; targeted and full Windows race tests passed. Apple binding, client integration, subscription persistence, module execution and a new Core pin remain outstanding. Logs: `E:/Temp/nk-unified-consumer/`.

## Compile only the selected Magpie entry and reachable imports(local repair,2026-11-04)

Load previously linked only the first entry but compiled every candidate first. A package declaring dist/index.mjs was rejected merely because it also contained unused index.ts or invalid fallback index.js. Actual MoeJS load/call cases reproduced two failures. Only the selected entry and reachable imports are now compiled. Selected-entry syntax errors remain visible;selected/imported TypeScript is still rejected by current loader policy,whose error now distinguishes disabled support from the engine's available type erasure.

Targeted Windows race tests and go vet ./... passed. With existing community audit fixtures enabled, full go test -race -p 2 -v ./... recorded 567 top-level PASS, 0 FAIL and 2 SKIP. A separate TestVendorPackagesAreRefused run then passed using existing npm-package fixtures; the live Wnacg case remains unrun. The two commands retain separate scopes, not a claim of one skip-free full run. Logs: `E:/Temp/core-provider-entry-evidence/before.log`, `after.log`, `full-with-fixtures.log`, `vendor-refusal.log` and `vet.log`. No new Darwin/Apple or real-account evidence,no public release,and consumers remain pinned to3be57b5.

Updated 2026-11-03. Results come from the Windows development machine and from GitHub Actions (Linux, Windows, macOS 26 with Xcode 26.6).

## Cross-platform acceptance of first-chunk and pre-abort fixes (2026-11-03)

CI `41631619233` completed successfully on functional source `6129f59241160e0c3b14f0428d6bff414ed9d245`: vet/race/shuffled tests pass in the Linux, Windows and macOS Go jobs, and the Apple xcframework build plus macOS/iOS Simulator Swift smoke steps succeed. Evidence is GitHub job/step metadata. This step did not download or inspect the SDK artifact and does not infer individual test counts from step status. Receipt `E:/Temp/core-stream-release/apple-acceptance.json`; complete terminal status `E:/Temp/nk-ci-final-review/summary.json`.

The tested source is integrated into Core main, followed by repository-convention history folding with an identical tree and Git publication. Exact final commit/tree comparisons are recorded in `E:/Temp/core-stream-release/publish-receipt.json`. Client pins proceed separately; Core cross-platform success does not establish Swift-client, UI or physical-device acceptance.

## Pre-aborted fetch no longer starts host requests (2026-11-03)

An additional cancellation gap was reproduced on the first-chunk candidate: when AbortController was already aborted before fetch, the whole-response path still sent a request and resolved successfully; legacy streaming and early-header paths also entered the host. The fix preserves the cancelled reservation and checks context before invoking either host path. Regression tests cover all three host types: pre-aborted fetch rejects with zero host calls, while the next normal request on the same instance runs exactly once and succeeds. Before-fix failures: `E:/Temp/core-magpie-preabort/before.log`. Targeted race, go vet and the full race suite pass: 22 packages, 565 PASS/0 FAIL/1 SKIP; TestRealSourceWnacg remains skipped because VC_WNACG_JS is unset. Logs and summary.json are in that directory. This is local Windows fake-host evidence, not Swift/UI or real-provider acceptance; the fix is included in this Core main integration, while client pins remain pending.

## Candidate Magpie first-chunk delivery fix (2026-11-03)

Isolated branch `fix/magpie-first-chunk`, based on published `b55c1a4`, fixes plugin fetch waiting for body completion. Optional `HeaderStreamer.DoStreamWithHead` resolves fetch when response headers arrive. Mobile HostStream may provide status/headers/finalUrl in StreamOpen, the first body chunk, or a separate head-only chunk, without new gomobile methods. Legacy hosts supplying metadata only at completion remain compatible and retain delayed fetch timing. Abort listeners survive until the body ends and reader.cancel closes the actual request. Chunks are passed as Uint8Array bytes so split Chinese/emoji UTF-8 is not corrupted by per-chunk string conversion. End/error releases stream registration.

Four regression groups failed before the fix, including delayed heads/first chunks, cancellation that could not run promptly and corrupted split UTF-8. Targeted race tests and the full 22-package suite now pass: 564 PASS/0 FAIL/1 SKIP, with TestRealSourceWnacg skipped because VC_WNACG_JS is unset. Actual loopback uses public mobile.Magpie, the repository provider template and a Go client: ChatNext receives first while the server holds the remaining body; cancelling all three blocking phases still closes the corresponding connection and a subsequent answer completes. The same script times out waiting for first text on b55c1a4. Logs under `E:/Temp/core-magpie-first-chunk-evidence/`: `before.log`, `targeted.log`, `race-summary.json`, `loopback-old.log` and `loopback-fixed.log`.

The local evidence now has the cross-platform CI acceptance above; neither client pin has changed. Swift CoreStreamHost still supplies response headers only in the final chunk. Early-head delivery must be integrated there and pass Mac compilation plus actual HTTP/UI acceptance before claiming iOS streaming.

## Magpie chat cancellation only stopped the handle (2026-11-03)

A real gap at `2c9c83b`: `Magpie.ChatCancel` removed the conversation handle and cancelled the chat's own context, while `Instance.Chat` issued requests under the instance context. After cancelling, a blocked whole-response `Host.HTTP`, `StreamOpen` or `StreamNext` kept running. The existing `TestMagpieChatCancelEndsIt` only checked that the handle disappeared, so it could not prove the request was closed.

Isolated branch `fix/magpie-chat-cancel` starts from public-entry negative regressions. `mobile/magpie_chat_cancel_test.go` uses Core's own `openai-compatible` template and the real `mobile` API, with hosts blocking in HTTP or `StreamOpen`; before the fix neither received `CancelHTTP`/`StreamClose` (`E:/Temp/nk-magpie-cancel-evidence/before.log`). The fix:

- Each chat owns its runtime. `ChatCancel` cancels its context and closes that instance instead of relying on the shared instance context. The shared instance still serves `Models`, sign-in and usage.
- A refreshed credential is written back to `Magpie.auth`, so the independent chat's next `auth.get`, later chats and existing model/sign-in hooks all see it; the provider bound to the runtime must still match.
- Instance creation builds without holding the lock and rechecks `closed` plus any existing instance on registration, so only one shared instance is retained after concurrent creation and nothing is registered after close. `ChatStart` registers its handle before launching the goroutine so `Close` can see and stop it, and event writes respect cancellation instead of blocking on an unread channel.

Five targeted race cases pass afterwards. `mobile/magpie_chat_lifecycle_test.go` covers cancelling one chat without stopping another, later chats continuing, an existing model hook reusing the refreshed credential (refresh happens once), and a blocked `StreamNext` being closed (logs `focused-after.log`, `chat-lifecycle-fixed-host.log`). That covers all three blocking stages plus refresh and isolation against the real package template. The supplementary case first failed because its host compared `Authorization` case-sensitively; the host now follows HTTP header semantics and the original failure log `chat-lifecycle.log` is kept.

The full `go vet ./...` and `go test -race -count=1 -json ./...` with real package fixtures pass: 22 packages, 559 PASS / 0 FAIL / 1 SKIP at top level (`race.log`, `race-summary.json`; the SKIP is `TestRealSourceWnacg` with `VC_WNACG_JS` unset, not a pass). The execution channel is Windows with fake hosts and local third-party package fixtures, not iOS UI, devices or a real vendor, and no client pin changed.

## Cancellation CI, Apple SDK and actual loopback evidence (2026-11-03)

CI `41606605713` at `696a5049a372e22ca0e639c103258bf38cfffb55` passed all four jobs: Linux, Windows and macOS Go checks plus Apple xcframework/Swift smoke. The downloaded `apple-build` artifact is 72,763,815 bytes, SHA-256 `2253fcac9be9c68859a022abee489b6ddef304f7a26a760cef8252cfeb53f593`; CRC, device/simulator/macOS slice kinds and chatCancel headers verify. Bundled logs confirm eight Swift smoke cases each on macOS and iOS Simulator. Receipt `E:/Temp/core-696-apple/receipt.json`, check log `verify-artifact.log`. The SDK does not embed a Git revision; attribution comes from CI/artifact HEAD and the download digest. The initial binary-Git-marker assertion was inapplicable and its failure log is retained.

The Lead additionally exercised public `mobile.Magpie` APIs with the repository OpenAI-compatible package, a Go HTTP client and an actual loopback server. With the fix, the server observed the matching connection close during response-header waiting, waiting for another chunk and whole-body reading; the same runner then returned the complete `first second` reply. The old `2c9c83b` control timed out waiting for connection closure after cancellation. Logs `E:/Temp/nk-magpie-loopback/cancel-fixed.log`, `cancel-old-negative.log`; temporary executable source `main.go` is in the same directory. This verifies TCP behavior with a Go client, not Swift URLSession, Keychain, iOS UI or a real vendor.

**Historical incremental-delivery limitation at b55c1a4**: the original acceptance attempt timed out waiting for ChatNext text after the server sent its first piece while holding the remaining body (`E:/Temp/nk-magpie-loopback/fixed.log`). The code path agrees: `hostFetchStream` settles the fetch promise only when DoStream completes. Transport chunks arrive, but the plugin's await fetch has not returned, so public chat events wait until the body ends. Current HostStream metadata is carried on the final chunk, without an early response-head notification. The cancellation fix does not solve this. Chunked transport or a final burst of events must not be reported as UI streaming. Incremental delivery requires changes to both header timing and the plugin bridge; this remains an explicit limitation.

## Implemented

| Package | Contents |
|---|---|
| `host` | Client interfaces: Transport, Verifier, Sniffer; URL and header validation |
| `attribution` | Optional authors and homepage for sources and plugins |
| `challenge` | Cloudflare detection (403/429/503 with markers, or `cf-mitigated`) and **MangaDex-style edge challenges** (`DetectEdge`); `DoneRule` can now ask for URL query parameters, so a challenge that solves itself is recognised as finished instead of only timing out; retry with the WebView User-Agent after verification, per-partition isolation |
| `comic/js` | MoeJS runtime, **one isolated engine per source**; every official sendMessage method; LoadSource/Call; search options (`search.optionList`), a source's settings declaration (`settings`: select, switch, input, callback, including getters) and browses (`explore`); VideoSource plugins |
| `comic/js/init.js` | Clean-room rewrite of the official prelude from the interface specification |
| `comic/hostapi` | http/cookie host API, compatible `CloudflareException:` format |
| `comic/hostapi/convert` | utf8/gbk/base64/hashes/HMAC/AES (ECB/CBC/CFB-N/OFB-N)/RSA decryption |
| `comic/html` | DOM handle protocol (21 functions) |
| `comic/image` | modifyImage pixel buffers |
| `comic/version` | The official version comparison rules (including beta/hotfix) |
| `backup` | Reads official `.venera` backups, resolves Dart type hashes, selectable restore plans |
| `subscription` | Comic `index.json` (official URL rules) and video `vcvideo.json` |
| `video` | Video data model; play result validation |
| `video/kazumi` | Kazumi rule engine (XPath/API); 85 of 86 published rules import |
| `video/builtin/hongguo` | Hongguo web pages only (no App signing, device registration or DRM keys) |
| `video/hls` | m3u8 parsing, rewriting, ad removal, AES-128 decryption |
| `plugin` | One registry for comic sources, JS video plugins, Kazumi rules and built-ins |
| `aiapi` | request construction for the built-in standard protocols: OpenAI Chat/Responses and Anthropic Messages; native bodies, authentication headers, cookies never enabled; builds only, does not execute. **SSE decoding for streamed answers** lives here too (below) |
| `mobile` | gomobile API (JSON in and out), host bridge (HTTP, verification, storage, cookies, UI requests, clipboard), panic recovery, cancellation |
| `magpieplugin` retry decision | Whether a failed attempt may move to the next endpoint is answered by a **decision with a reason** (`Decide(err, attempt) → {Retry, Reason}`): caller gone, output already shown, request rejected, credential rejected, rate limited, upstream unavailable. `RouteError` carries `Stopped`, so "why did it stop while endpoints were left" is answerable from the error. Before this, anything that had not produced text moved on: a 400 walked every endpoint and waited for a timeout at each. `Send` also checks `ctx` before opening the first connection. | 13 cases (status table, 400 tried once, 401 falls back, cancelled starts nothing) |
| `magpieplugin` finish normalisation | The three dialects each spell why a reply ended (Chat `stop`, Responses `completed`, Anthropic `end_turn`; truncation is `length`/`incomplete`/`max_tokens`). They now normalise into one vocabulary (`NormalizeFinish`) while the raw value is kept. An ending nobody has seen passes through as itself rather than as a clean stop, because calling an unknown outcome "finished" is the one answer that is certainly wrong. This also fixed a real bug: when Chat sends the last text and the ending in one chunk, the ending was dropped, so a reply cut short by the token limit looked like a finished one. | 3 groups, 20 cases (vocabulary, unknown value, truncation seen in each dialect's stream) |
| `mobile` events carry `outcome` | A chat event used to carry only `finish`, the vendor's own word. It now also carries `outcome`, the normalised ending, so a client asking "was this reply cut short" does not need to know which vendor answered. The field is absent when there is nothing to normalise: an empty string would read as a clean finish. | 2 cases (both words together, empty omitted) |
| `magpieplugin` pin | "Who answers" and "may a retry pick someone else" are separated: `Pin` says which part of the route a retry may change — `PinModel` rotates the account but not the model (`PinAccount` rotates the address but not the key; the default is unrestricted). Without a pin, a conversation planned around the strong model could be quietly answered by a small one after the first account returned 401. A pin does not remove retries and does not override "the request itself was rejected"; endpoints kept out by the pin are named in the error (`stopped by the pin`), because "nothing else works" and "nothing else was asked" otherwise look the same. An endpoint with no account or model label is kept: unknown is not the same as unusable. | 5 cases (same model, same key, default unchanged, unlabelled kept, pin does not override a 400) |
| `version` | answers "which Core is this": its own revision when Core is the program (marked `-dirty` for a changed tree), and a stamp written by `SetRelease` when another program links Core |

## Verified

- `gofmt`, `go vet` and `go test -race` pass for all packages.
- Real data: the wnacg comic source end to end offline; the vensrc main and extended indexes and the official venera-configs index all parse.
- The backup wire format is pinned by `backup/contract_test.go`: each client decodes Core's `Plan` with its own models, so a renamed field fails silently there instead of here. The test fixes the key sets, including the fact that a kept picture has no top-level `url` or `page` — its pages sit in `episodes[].imageFavorites[]`.
- CI: Go tests for every package pass on Linux, Windows and macOS. All four jobs of the current published commit are green: Go on three platforms, plus Apple's xcframework and two Swift smoke tests (one on the iOS simulator). The run number is deliberately not written here — it changes with every push, so a constant would only go stale; the Actions page for that commit has it.
- Apple (CI, macOS 26 arm64, Xcode 26.6): the xcframework builds (iOS device, iOS simulator and macOS slices); the Swift smoke test passes all 8 tests on macOS and on the iOS simulator, covering the comic-source path, cookies, UI requests, the clipboard, hasComic, search options and a source's settings declaration.
- Size: the iOS device binary is 20 MB stripped and 7.2 MB gzipped; the whole xcframework is 95 MB (including the two-architecture simulator and macOS slices, which do not ship in the app).
- `Map` values that sources return (tags, chapters) become JSON objects in insertion order, as in the official app. They used to become an empty `{}`, which lost every chapter list. Integer keys such as chapter numbers `10` and `2` keep their order and are not sorted.
- Speed: on the iOS simulator a search takes 1–7 ms and installing a source 0.6–0.9 ms; on Windows each source engine uses about 112 KiB of memory.

### What a setting callback returns (2026-11-01)

A source's callback button (`type: "callback"`) is the one settings action a client runs on the source's behalf, and nine official sources declare one. Two things were pinned this round:

- **Which client actually uses this path is now measured (2026-11-01).** `mobile.CallSettingCallback` is the gomobile surface for iOS; the desktop does **not** go through it and carries its own `internal/core` with the same logic. A distinguishing experiment confirmed it: with the desktop's own normalisation removed and the new pin's Core in place, the callback still answered `null`, so this fix only reaches iOS and the desktop needed its own (it has one). Reading one side's result as "both clients are fine" is the mistake this note exists to prevent.
- **A callback with no return value no longer hands a JSON literal to the client.** `CallSettingCallback` used to pass `null` straight through, so a client printed `null` as if the source had said it; `null` now becomes an empty string and the clients' existing "done" wording takes over. A new case in `mobile/webview_login_test.go` uses a callback that only has a side effect; removing the normalisation makes it report `answered "null"`.
- **A failing callback is no longer hidden behind a deadline (client side).** The desktop client kept polling to its ninety-second limit when a source's `onLoginSuccess` threw, so the user read `context deadline exceeded` instead of the source's own reason; only "the page agreed but its cookies have not settled" waits now. That is client behaviour, recorded here because it decides how Core's errors reach a person.

Real-source evidence (desktop `scripts/callback-probe.sh`): ten callbacks across `copy_manga`, `copy_manga_multi_accounts`, `happy`, `jm`, `mxs` and `wnacg` all ran and returned; `jm.dailyCheckIn` answers with the source's own `Please login to check-in` when nobody is logged in, which is a correct result rather than a defect.

### Magpie provider plugins (2026-10-31)

- `gofmt` clean, `go vet ./magpieplugin ./mobile` passes, and `go test -race ./magpieplugin ./mobile` passes with no race reported.
- The **original Copilot package** (`magpie-community/plugins@c590a74`) works against a fake service end to end: module resolution (it exports only `CopilotAuthPlugin`, with no default), device-code sign-in, token refresh written back, usage, model list, streamed replies, one event model over three dialects, and "never retry once output started".
- `gobind -lang=objc` generates cleanly and `MobileMagpie` now exposes eighteen methods: `auth`, `authorize`, `poll`, `cancelSignIn`, `usage`, `usageSummary`, `models`, `chatStart`, `chatNext`, `chatCancel`, `loadPackage`, `readPackageArchive`, `setAuth`, `savedAuth`, `call`, `close`, `checkin`, `setPluginOptions`. The Swift adapter is written from that header, not from memory. An AST test (`mobile/surface_test.go`) pins those names: they are a cross-language contract, so a rename is not a compile error in Swift but a runtime surprise.
- **Installing from a downloaded archive** works for both zip and npm tar.gz: one enclosing directory stripped, symlinks and traversal refused, the same limits as a folder install (`magpieplugin/archive.go`).
- All 36 community packages were audited: the provider plugins that actually run are **the original Copilot 0.1.0 and cloudflare-workers-ai 0.0.4**; four that need Node built-ins or a CLI are refused by name, and the one whose entry is TypeScript (`search1api`) now says so on 2026-10-31 instead of reporting a syntax error.
- CI runs `go test -race ./...`, but the cases that use a real vendor package **skip there**: the original Copilot package is third-party code that is not distributed with this repository, so those tests `t.Skip` when the fixture is absent instead of failing. "Run against the original package" therefore means on a development machine; CI covers our own packages and the fake service.
- A package that loads is not automatically a package that is compatible: Node built-ins and npm dependencies still fail at `Load` with `ErrUnsupportedModule` instead of degrading into empty stubs.

### Chunked transport (2026-11-01)

- A client can keep implementing whole-reply transport only. To read a reply as it arrives, it also implements the optional `HostStream` (`streamOpen` / `streamNext` / `streamClose`) and builds the runner with `NewMagpieWithStream`. `NewMagpie` keeps its signature, so existing clients still compile.
- **No new method on old clients**: `mobileHost` and `mobileStreamHost` are two types and only the second has `DoStream`. The plugin host picks its path by type assertion, so if every host carried the method, a client that cannot stream would be asked to and the package's fetch would fail instead of falling back. `TestPlainHostIsNotAStreamer` pins that next to the existing whole-reply tests.
- Request ids come from one place (`nextHostRequestID`), shared by both paths, so the client's task table cannot have a cancel land on another request.
- The bridge is tested directly: chunks arrive in order, a consumer that stops closes the request, a client read failure is a failure rather than a finished stream, and cancelling the context closes the request (`mobile/stream_test.go`).
- `gobind -lang=objc` already emits the `MobileHostStream` protocol and `MobileNewMagpieWithStream`, so a Swift client implements what the header shows.- **Scheme-relative URLs are now filled in**: a source sometimes hands back an image address as `//images.example.com/1.jpg`, with no scheme. RFC 3986 reads that as "the scheme of the page", which in a reader is always https. Core gained `host.NormalizeURL` and the gomobile `Core.NormalizeURL`, so both clients apply the same rule. **This does not weaken the rules**: `file://`, `javascript:` and `ftp://` are never rewritten, the filled URL still has to pass `ValidateURL`, and a URL that is not scheme-relative is not touched at all. `TestNormalizeURLDoesNotWeakenValidateURL` and `TestNormalizeURLDoesNotRewriteUnsafeSchemes` pin both points.


- **The package bridge deadlock is fixed**: with the original Copilot package on a streaming host, the package's `__magpieChatPush` used to write into an `io.Pipe` synchronously while the reader stopped as soon as it saw `[DONE]`. When the package then pushed the rest of its body, the writer waited for a reader that had gone, its hook never returned, and the whole instance hung. The reply now travels through a `replyBuffer` (a buffer with a condition variable): a write never waits for the reader, and the read ends when the writer closes. `TestMagpieChatUsesTheChunkedHost` now **really passes** instead of being skipped. **Checked in reverse**: putting the blocking pipe back makes the same test report `panic: test timed out`.
- The buffer has six tests of its own: a write does not wait for a reader, the text arrives in order, a closed buffer reads as EOF, the reason reaches the reader, a write after close is refused, and concurrent writers lose nothing (`magpieplugin/reply_buffer_test.go`).


### Image config callbacks (2026-11-01)

- Besides an address and headers, `comic.onImageLoad` / `onThumbnailLoad` may carry three **functions**: `onResponse` (decrypt or rewrite the body), `modifyImage` (a pixel-buffer script) and `onLoadFailed` (another mirror when this address does not work). Functions cannot cross into JSON, so a client used to get **only the first four fields and lose all three callbacks** — which is why sources with encrypted images (`cxc`, `mojoin`, `manwaba`) handed ciphertext to the reader, and a source with mirror fallback (`4khd_cosplay`) treated an HTML error page as a picture.
- Core adds `comic/js.Runtime.ImageConfig/ApplyImageResponse/ImageFallback`: the config's functions stay in the source's runtime under a monotonically increasing handle, and a host that receives `handle` can hand the body to `onResponse` or ask for the next mirror. **It is optional**: a host that ignores handles keeps using `url`/`method`/`headers`/`data` and behaves exactly as before.
- Handles are bounded (512); past that the whole set is dropped. Ids only grow, so **a dropped handle cannot come back as another config's handle**. A malformed handle is refused before it reaches the runtime (`^i[0-9]{1,9}$`), so it is never spliced into JS.
- Bytes cross the boundary once: the body goes in as base64 and comes back as base64, decoded and encoded inside the runtime.
- gomobile exposes `Core.ImageConfig/ApplyImageResponse/ImageFallback`, with the body in base64 (binary survives gomobile) and `ErrNoImageCallback` reported separately so a caller can read it as "keep the body".
- Tests: five in `comic/js/image_test.go` (byte transform, mirror fallback, a source with no hook, the handle bound, a non-byte result refused) and one end-to-end in `mobile/image_callbacks_test.go`. **Clients are not wired yet**: desktop and iOS still take the address only, so the chain is not closed until each one adopts it.

### Running a modifyImage script (2026-11-01)

- The third callback of `comic.onImageLoad` is `modifyImage`: the source supplies a script that takes one `Image` and returns another, used to reorder the blocks of a long strip (`jm` does exactly this) or to rotate a page. A host used to receive only `url`/`headers`, so the script was dropped — those sources show the official client's N-part layout and the desktop client's single raw strip.
- Core adds `comic/js.Runtime.ModifyImage(ctx, script, bitmap)`: the decoded RGBA goes into a throwaway bitmap store, the script runs in **its own runtime** (it declares `modifyImage` itself; sharing the source's runtime would let two pages overwrite each other's globals), and the resulting bitmap comes back. **Decoding and encoding stay the client's job** — Core moves RGBA only, matching the split `comic/image` already documents.
- All three refusals have cases: an empty script, a script that declares no `modifyImage`, and a script that returns something that is not an image; a bitmap whose length disagrees with its size is refused before the script sees it.
- Four tests: a clockwise rotation (checked pixel by pixel), a block reorder written the way `jm` writes it, the three refusals, and two calls in a row not affecting each other (proof that each call gets a fresh runtime).

- gomobile exposes `Core.ModifyImage(key, script, width, height, rgbaB64)` answering `{"width","height","rgba"}`: pixels cross as base64 (binary survives gomobile), the caller decodes and encodes, and "the script produced no image" is reported as `ErrModifyImage`, apart from the script itself throwing.

## macOS package test run (2026-11-03, commit `44f1952`)

The same commit ran on macOS `darwin/arm64`, `go1.27.3`, `CGO_ENABLED=1`: `go vet ./...`, `gofmt -l .` and `./scripts/check-docs.sh` pass; `go test -race -v ./comic/js ./magpieplugin ./mobile` exits 0 with **249 PASS / 0 FAIL / 31 SKIP** at the top level. Logs are in `/tmp/venera-core-44f1952-evidence/` (`vet.log`, `race.log`, `test-summary.log`, `gofmt.log`, `docs.log`).

Key coverage (all with fake transports/hosts): promises and repeated `await`, recovery after a timeout interrupt, cancellation before a call starts, closing a session cancelling active and future calls, host panic/cancellation and cancellation reaching the transport; cross-source isolation, backup cookie partition naming, and distinct host IDs for comic and Magpie requests; Magpie fetch parsing, AbortSignal cancellation, chunked streaming arrival, stream failure reporting, ordering, consumer stop, client failure and cancellation; mobile plugin lifecycle, settings/search-option/explore/webview-login JSON, exact bytes for `9007199254740993` without float rounding, and cancellation reaching the same host request. The two new compatibility-boundary cases (`TestCompatibilityReadOnlyAtob`, `TestCompatibilityCallRegisterWindow`) are included.

The 31 SKIPs are not passes: the audit fixture directory is absent, and `VC_WNACG_JS` / `MAGPIE_VENDOR_DIR` are unset; these cover real Copilot, Cloudflare and community plugin fixtures. The audit directory used to be hard-coded to a Windows temporary path, so those cases skipped on macOS with no hint; `MAGPIE_AUDIT_DIR` now names it and an unset variable skips explicitly. Rerun on Windows on 2026-11-03 with both variables pointing at real fixtures (`magpieplugin` + `mobile`): **224 PASS / 0 FAIL / 0 SKIP**, log `E:/Temp/core-full-with-fixtures.log`.

A local pass of `go test -race ./...` across all 22 packages with `MAGPIE_AUDIT_DIR` and `MAGPIE_VENDOR_DIR` set also succeeded after `b2610a5` (`E:/Temp/core-race-all-jsname.log`).

CI at `4d4b8fb` (which contains the async error-name fix from `b2610a5`) is green in all four jobs: the three Go platforms and `Apple (xcframework + Swift smoke test)`. The Apple job was cancelled after 15 minutes in the queue without running a single step, and `gh run rerun --job` made it pass; that cancellation is a queue problem rather than a compile failure, so rerun before reading it as red.

CI at `63787bc` is green in all four jobs: `Go (windows-latest)`, `Go (macos-26)`, `Go (ubuntu-latest)` and `Apple (xcframework + Swift smoke test)` (run `41507032435`). The macOS job on the previous commit `8e301cf` shows as cancelled because the next push cancelled it under the concurrency policy, not because it failed — Windows and Linux passed there and the Apple job was skipped, which is recorded here so a cancellation is not read as red.

On 2026-11-03 at `6a66759` the whole repository was run again: `go test ./...` passes all 22 packages without the fixture variables (those cases skip by design), and with `MAGPIE_AUDIT_DIR` and `MAGPIE_VENDOR_DIR` set the `magpieplugin`, `mobile` and `comic/js` packages pass as well (`E:/Temp/core-go-test-all-6a66759.log`).

The same run closed the last `MAGPIE_VENDOR_DIR` skip: four published third-party packages were fetched with `npm pack` (factory `0.1.41`, grok `0.2.3`, qoder `0.2.54`, workbuddy `0.2.14`) and unpacked, and `TestVendorPackagesAreRefused` refuses each with an error naming `node:http` / `node:crypto` / `node:child_process` and saying it cannot resolve. The test used to match `-factory-auth` in the directory name, while the documented recipe unpacks into a directory called `factory`, so the recipe could never run; it now matches the package's own `name` field from `package.json`. Log `E:/Temp/core-vendor-refusal.log`. These results cover fake transports and loopback only, not live sites, real accounts or devices; the client pins are unchanged.

## Reproduced MoeJS compatibility boundaries (2026-11-02)

The public Core `Runtime.Eval` entry point reproduces two limits of pinned engine `0a0ed36ae7db`, covered by two tests in `comic/js/compatibility_limits_test.go`. Both pass under Windows `go test -race` (`E:/Temp/core-compatibility-limits-tests.log`); standalone output is in `E:/Temp/core-compatibility-limits-repro.log`.

- Global `atob` is non-writable and non-configurable. Strict assignment throws `TypeError`; sloppy assignment is ignored and `atob("QQ==")` still returns `A`, so absence of an exception does not establish a custom decoder was installed. A function-local decoder works without changing the global.
- A minimal call expression accepts 254 literal arguments; 255 triggers `function needs more than 256 registers`, and 256 triggers `too many arguments`. This is a 256-register compiler window per function, including callee/this and other local values, not a promise that every context accepts 254 arguments. Dynamic array spread passes 1000 arguments, and the earlier compile errors leave the next call usable.
- The source realm had **no `URL` or `URLSearchParams`**, and the official `manhuaren.js` builds a Referer from `new URL(imageUrl).host` (falling back to a regex when it throws). Core's prelude now provides both, covering what sources use: absolute addresses (protocol, host, port, path, query, fragment, origin), relative references resolved against a base, `searchParams` with `get`/`getAll`/`append`/`set`/`delete`/`sort`/`toString`/`forEach`, and `URL.canParse`; invalid input throws `TypeError: Invalid URL`. `search` and `searchParams` are one value the way a browser has them (editing one is visible in the other), and `TestURLPolyfillCoversWhatSourcesUse` plus `TestURLSearchAndSearchParamsAgree` pin those shapes. What it deliberately does not do: normalise percent escapes, resolve every dot-segment edge, or implement IDNA — a source that needs those has to handle them.
- `TextDecoder("gbk")` does not exist here: the engine's `TextDecoder` is the UTF-8 family, and `gbk` throws `RangeError: The "gbk" encoding is not supported`. A source that needs GBK has to go through the source API's own `sendMessage` → `method: "convert"` route (Go's `simplifiedchinese.GBK`) rather than looking for a browser decoder in JS. In addition to UTF-8 decoding and GBK rejection, `TestCompatibilityTextDecoderIsUTF8Only` now actually verifies that `Convert.decodeGbk(new Uint8Array([214,208,206,196]))` returns “中文”; a plain array also decodes. `Convert.encodeGbk("中文")` returns an ArrayBuffer whose `new Uint8Array(...)` view contains those same four bytes. The unrepresentable “🐾” returns null, and decoding still works afterward. This evidence uses Core 3be57b5 and strengthens tests without changing conversion code; subsequent actual Dart runs against enough_convert 1.6.0/1.7.1 confirm differences in malformed-byte consumption, single-byte euro and private-use mappings; see [GBK compatibility](GBK-COMPATIBILITY.en.md). The official app and its configuration were not exercised. Additionally, `TestHTTPThroughJS` uses a local fake transport to return raw GBK bytes, then decodes “中文” through `Network.fetchBytes` and `Convert.decodeGbk`, covering the HTTP response-to-script bridge. Logs: `E:/Temp/core-gbk-evidence/bridge-corrected.log`, `E:/Temp/core-gbk-evidence/http-gbk.log`.

The same round fixed an error string: a promise rejection arrives as a value, and `jsError` read only its `message`, dropping the object's `name` — so a type error inside an async `init()` read as `Cannot read properties of null (reading 'length')` while the synchronous throw said `TypeError: ...`. The name is now put back in front (not when it is `Error` itself, and not for thrown strings); see `E:/Temp/core-desc-probe.log`. `TestAsyncRejectionsKeepTheErrorName` covers an async type error, a thrown string and a synchronous throw, and `go vet ./...` plus `go test ./...` with the fixtures pass all 22 packages.

This round explicitly records and retains the pinned engine's behavior. Source scripts should use local/own decoders and array spread or smaller functions for large literal calls; Core does not rewrite scripts automatically. Production code, the engine dependency and both client pins are unchanged. These cases establish those boundaries, not every live site's methods.

## Official script load sweep (2026-11-02)

Loaded the official v2 scripts through the public `comic/js` entry point to look for engine gaps at load time. The probe lives outside the repository (`E:/Temp/gosrcprobe`, with a `replace` to this checkout) and is not a test case; the scripts are the 34 files in `E:/Temp/nkprobe/official-srcs`.

Result: with storage, a cookie store and a transport, **32/34 load completely** (key, kind, version and minAppVersion recorded). The other two fail because the probe's single canned body does not match their `init()`: `comic_walker` parses a token JSON and `jm` parses page HTML. `shonen_jump_plus` passed precisely because of that canned HTML, so this is a fixture problem, not an engine problem.

The 2026-11-03 rerun replaced that single body with per-request shapes: the iTunes lookup and the `jm` setting endpoint answer JSON, everything else answers HTML. **33/34 now load completely**, including `comic_walker` (key `comic_walker`, version `1.0.1`, minAppVersion `1.6.0`). `jm` still fails, and not because of the engine: its `init()` calls its own setting endpoint and decrypts the body with an AES key derived from the request time plus a fixed secret, so an offline stub cannot produce a body for that second. The failure surfaces as a JavaScript exception (`js: jm init failed: ...`), which shows the load-time error path works. Log: `E:/Temp/core-srcs-sweep-34.log`.

So this round found **no new JavaScript compatibility gap**: class fields, getters, `randomInt`, `loadData` and `Network.get` inside `init()` all work. Limits: only load time is covered, not each method's real network behaviour; `jm`'s `init()` needs a live response and was not rechecked online; dedicated register-window and frozen-`atob` cases are now recorded above and remain separate from this load sweep.

## Security review (2026-10-29)

An independent review found 12 issues (4 high, 5 medium, 3 low), **all fixed with regression tests**. The most serious let sources read each other's cookies and storage; it was reproduced and fixed by running each source in its own engine, see [JS-ISOLATION.en.md](JS-ISOLATION.en.md).

## Not verified / pending

1. **Devices**: measure start-up time and memory on a real iPhone (CI can only run the simulator).
2. **Cross-check the Dart hash on a real Dart VM**: `print('wnacg'.hashCode)` should print `823512256`.
3. Real-world sources cannot go into the repository (wnacg and others have no licence), so CI drives the whole comic pipeline with sources we wrote ourselves (`comic` in `mobile/mobile_test.go`, `miniSource` in `comic/js/async_test.go`); real-source compatibility is checked outside the repository by Nekophage's `TestLiveSource`.
4. Video download and offline cache, danmaku, Bangumi: not done (second phase).
5. AniBaka `anx-rule/2` pipeline rules: not done (second phase).
6. **Wire chunked transport into the clients**: the Core-side bridge, the package bridge and standard-protocol stream decoding are all in place; iOS implements `CoreStreamHost` and builds the Magpie runner with `NewMagpieWithStream`, but the main `Core` is still built with `New` and the standard-interface screen has not been wired to `SendAIRequestStream`, so standard calls still return whole replies. The desktop has no `HostStream` implementation yet.

## Standard stream completion and cancellation during open (2026-11-03)

Branch `fix/ai-stream-completion` fixes two reproduced gaps. `SendAIRequestStream` did not check HTTP status or protocol completion: partial Chat/Responses/Messages text could be returned as a complete answer after transport closure, and HTTP 403/503 could become empty successes. Of eight new subcases, all six error scenarios failed before the fix while two complete protocol replies passed; log `E:/Temp/core-ai-stream-before.log`. Success now requires HTTP 2xx and a protocol completion marker; an unterminated final event cannot stand in for completion. Requests also declare `Accept: text/event-stream`.

The chunk bridge previously registered cancellation only after `StreamOpen` returned, leaving it blocked while awaiting response headers. A blocking-host test reproduced the failure (`E:/Temp/core-ai-stream-open-before.log`). Cancellation during this phase now closes the request ID, and the opening callback is stopped or joined before leaving the phase. Clients must tolerate repeated closes and unblock waits when the request JSON ID is closed; the test releases its wait only for a matching ID. Subsequent `StreamNext` uses the actual returned stream ID.

After the fix, `go vet ./...` and `go test -race -count=1 ./...` pass across all 22 packages, as do paired-document and English-error checks. Full logs: `E:/Temp/core-ai-stream-vet.log`, `E:/Temp/core-ai-stream-race.log`, `E:/Temp/core-ai-stream-docs.log`, `E:/Temp/core-ai-stream-errors.log`.

The fix is fast-forwarded into main at `b5312596045caa8aa4cd8837b8f9ece84257bc4c`. CI `41589616003` succeeded on the Ubuntu, Windows and macOS 26 Go jobs (`gofmt`, `vet`, `test`, shuffled `test`, `docs`, English error text); Ubuntu and macOS run every check while Windows skips `gofmt` and `docs` per its existing configuration, not because of this change.

These are fake-host protocol and lifetime regressions, not real-provider, first-network-chunk or streaming-UI evidence. The branch is not integrated into main or client pins; SDK and consumer validation remain pending.

## Streamed decoding for the standard protocols (2026-11-03)

`aiapi` gains an SSE decoder and `RequireStream`; mobile gains `NewWithStream` and `SendAIRequestStream`. A standard-protocol answer can now be read and parsed as it arrives instead of only being available whole.

- **Different event shapes, same boundary**: all three protocols are `data:` lines closed by a blank line, but `chat` puts text in `choices[0].delta.content`, `responses` in `output_text.delta` events, and `messages` in the `text_delta` of `content_block_delta`. Only text deltas are extracted; the API returns only accumulated text, without tool calls, reasoning blocks, usage, raw events or incremental UI delivery.
- **A chunk is not an event**: one read may hold half a line, another several events. The decoder closes an event on a blank line and buffers across chunks, and reports an error past 1 MiB of an unfinished event so a provider that never sends a blank line cannot grow the buffer without bound.
- **Ending and failure stay distinct**: `[DONE]`, `response.completed` and `message_stop` mean the answer ended; a provider error event (`{"error":{...}}`) is raised as an error rather than "finished with no text".
- **Caller mistakes are named**: a body without `"stream":true` is refused up front — a provider that was not asked for events answers with one JSON object, which decodes to empty text and looks like a silent provider. A Core built with `New` instead of `NewWithStream` reports `cannot stream` rather than pretending.
- **Partitions do not mix**: the standard streamed request uses its own `partition` (empty by default) and never borrows a comic source's cookie jar, while the plugin stream stays on `magpie`. Both share one bridge, and the partition is a parameter rather than a constant.

Coverage (fake hosts and transports throughout): nine cases in `aiapi/stream_test.go` (chat deltas, chunk splits, Responses events, Anthropic text versus tool deltas, provider error, malformed event, unknown protocol, `RequireStream`), and seven in `mobile/aiapi_stream_test.go` (end-to-end delta assembly, missing `stream:true` refused before any connection opens, a non-streaming Core reporting its limitation, provider and transport failures, no answer after cancellation, an empty answer that is still valid JSON). `go test ./...` passes and `go test -race ./aiapi/ ./mobile/` passes; logs `E:/Temp/core-stream-full.log`, `E:/Temp/core-stream-race.log`.

**This is still not a streaming screen**: the iOS standard-interface view is not wired to it, so first-delta UI behaviour and a real provider's chunk pacing have no evidence yet, and no real account has been used.

## URL host case (2026-11-03)

After wiring `5bd3b85` into Nekophage, a source script showed `new URL(...).host` returning `Images.Example.Test:8443`, while the URL standard lowercases the host of a special scheme. A source comparing it with `example.test` would fail in a way that looks like a source bug. Hosts for `http`, `https`, `ws`, `wss` and `ftp` are now lowercased during construction and `host`/`origin`/`href` follow; non-special schemes keep their host as written. `TestURLPolyfillCoversWhatSourcesUse` covers both an uppercase host and a non-special scheme.

`go vet ./...`, `go test -race -count=1 ./...` (22 packages) and `scripts/check-docs.sh` pass; logs `E:/Temp/core-url-host-vet.log`, `E:/Temp/core-url-host-test.log`, `E:/Temp/core-url-host-docs.log`. The official-source sweep also ran again on `4f213ff`: **34 scripts, 33 loaded, 1 failed**, still `jm` because its `init()` needs a live response (`TypeError: Cannot read properties of null (reading 'length')`), unrelated to host case; log `E:/Temp/core-sweep-4f213ff.log`. This changes Core's production behaviour, so both pins move to the commit carrying it on the next client build.

## The official-source sweep after the URL class (2026-11-03)

Re-ran the same sweep after `5cfdef2` (the prelude now provides `URL` and `URLSearchParams`): **34 scripts, 33 loaded, 1 failed**, still `jm`, and still for the same reason — its `init()` needs a live response (`TypeError: Cannot read properties of null (reading 'length')`). That message now carries the `TypeError:` prefix, which is what `b2610a5` was for and now shows on a real path; nothing new broke. Log `E:/Temp/core-sweep-url.log`.

## Where the pins ended up (2026-11-03)

Both clients moved from `214e7782` to `8e14aad`, which closes the "pending upgrade" note for `b2610a5`.

- **Nekularity**: shipped in `0.3.0` (build 12), tag `v0.3.0`; unpacking the IPA finds `8e14aade75f563a95f50d4ec80be27bae0d02c63` inside the app binary, and CI `41525488814` passed all 411 tests.
- **Nekophage**: rebuilt and re-verified locally — `go test -race ./...` passes in **all 16 packages**, `scripts/check.sh` passes, the window suite is **14 PASS / 0 FAIL / 0 SKIP**, and `-version` reports the same pin.

Both bumps came after confirming that only `comic/js/engine.go` changed in production between `214e7782` and `8e14aad` and that nothing under `apple/` moved, so the difference is error text. The next production change in Core follows the same rule: roll it into the next client build.

## What justifies the client pin (2026-11-03)

Both clients still pin `214e7782` while HEAD is `8e301cf`. That is not a forgotten bump: `git diff --stat 214e7782..HEAD` shows 16 files and 415 lines, and the only changes outside tests and docs are the two READMEs (10 lines); production code, `go.mod` and the engine dependency are untouched. The Go compiler does not read `_test.go` files or Markdown, so a library built at `214e7782` is the same library as one built at HEAD, and bumping early would only make both clients rerun their builds without changing behaviour.

The bump trigger used to be "any change outside `_test.go`, `docs/` and `README*` moves both pins". `b2610a5` makes that need saying more precisely: it changed `comic/js/engine.go` so a promise rejection keeps its `TypeError:` name. That is production code, but it does not change the gomobile API shape — only the error text. Moving a pin makes each client rebuild its Core artifact (Nekularity rebuilds the xcframework), which is out of proportion here, so both clients stay at `214e7782` and this change is **recorded as pending**: the next time either client produces a new version (which rebuilds Core), the pin moves with it and the full suite runs. The rule becomes: a production change moves the pin at the client's next build; it moves immediately, with a rerun in the same round, only when it changes an interface or behaviour a client relies on. `scripts/check-core-pin.sh` on the client side still only keeps a pin consistent with the artifact.

