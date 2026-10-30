# VeneraCat Core status

English | [简体中文](STATUS.md)

Updated 2026-10-30. Results come from the Windows development machine and from GitHub Actions (Linux, Windows, macOS 26 with Xcode 26.6).

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
| `aiapi` | request construction for the built-in standard protocols: OpenAI Chat/Responses and Anthropic Messages; native bodies, authentication headers, cookies never enabled; builds only, does not execute |
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

## Security review (2026-10-29)

An independent review found 12 issues (4 high, 5 medium, 3 low), **all fixed with regression tests**. The most serious let sources read each other's cookies and storage; it was reproduced and fixed by running each source in its own engine, see [JS-ISOLATION.en.md](JS-ISOLATION.en.md).

## Not verified / pending

1. **Devices**: measure start-up time and memory on a real iPhone (CI can only run the simulator).
2. **Cross-check the Dart hash on a real Dart VM**: `print('wnacg'.hashCode)` should print `823512256`.
3. Real-world sources cannot go into the repository (wnacg and others have no licence), so CI drives the whole comic pipeline with sources we wrote ourselves (`comic` in `mobile/mobile_test.go`, `miniSource` in `comic/js/async_test.go`); real-source compatibility is checked outside the repository by Nekophage's `TestLiveSource`.
4. Video download and offline cache, danmaku, Bangumi: not done (second phase).
5. AniBaka `anx-rule/2` pipeline rules: not done (second phase).
6. **Wire chunked transport into the clients**: both the Core-side bridge and the package bridge now work (the deadlock is fixed, see above), but no client implements `HostStream` yet, so iOS and the desktop still get whole replies. The next step is for iOS and the desktop each to implement `streamOpen` / `streamNext` / `streamClose`, and to move the Core pin forward to a commit that contains this fix.
