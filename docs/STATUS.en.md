# VeneraCat Core status

English | [简体中文](STATUS.md)

## Architecture and capabilities

| Area | Current contract |
|---|---|
| Host | `host` defines HTTP, verification and sniffing. Clients own networking, cookies, WebViews, playback and UI. `challenge` coalesces Cloudflare and edge verification by partition, supports canceling a queued request, and replays the verified User-Agent. |
| Comics | `comic/js` runs each source in an isolated MoeJS runtime. Legacy ComicSource scripts use [sendMessage](SENDMESSAGE-SPEC.en.md). MoeJS supplies URL construction and HTTP goes through the host. |
| Plugins and video | `plugin` registers comic, video and novel (`novel-js`) sources. Video supports VideoSource, Kazumi XPath/API rules, AniBaka `anx-rule/2` declarative three-stage pipelines (`anibaka-rule`), the Hongguo web entry point, HLS processing, the `video/offline` cache foundation (HLS/direct planning, `host.Transport` fetching, resume, SHA-256 verification, local m3u8 rewriting, path-traversal guards and pruning) and the `video/danmaku` local timed-comment layer (Bilibili XML, ASS and JSON parsing, window queries and anti-overlap track layout; online fetching is deferred to plugins). `subscription` parses comic indexes, `vcvideo.json` and source collections. See [plugin design](VIDEO-PLUGIN-DESIGN.en.md). |
| Novels and adaptation | `novel` and `plugin` provide the Wave 4A novel runtime (sharing the `comic:<key>` cookie partition and isolated MoeJS instances), and `gohost` and `mobile` expose `NovelPopular`, `NovelSearch`, `NovelDetail` and `NovelChapter`. `novel/adapt` implements the Wave 4B-1/4B-2 pipeline: chapter HTML extraction and chunking, strict storyboard JSON validation, cross-chapter character sheet merging, multi-chunk checkpoint resume (`AdaptCheckpoint`), budget caps (`ErrBudgetExceeded`), and static comic single-panel/storyboard rendering via Gemini and OpenAI Images (with local style/character reference images, `ForceRerender` resumable rendering, and zero retries on `ErrContentBlocked`). |
| Backups | `backup` reads official `.venera` backups and builds a selectable restore plan. Cookie targets come from the sources in the backup, falling back to installed partitions when the backup contains no scripts. Clients confirm and write the cookies. See the [backup specification](VENERA-BACKUP.en.md). |
| Services | `service` installs, upgrades and removes packages. Ordinary KV data lives outside the package, and clients hold account values. The mobile session runner binds to a host and manages residency, cancellation and version invalidation. See the [service contract](SERVICE-PLUGINS.en.md). |
| AI | `aiapi` builds and executes native OpenAI Chat/Responses and Anthropic Messages requests and shares structured errors and SSE decoding with `magpieplugin`. Replies can carry text, reasoning, raw finish reasons and nullable token usage. `aiapi-ext/1` defines optional routing, usage observation and content masking. Standard calls use the account and model supplied by the client by default. See the [standard API](AIAPI.en.md) and [Magpie compatibility](MAGPIE-COMPATIBILITY.en.md). |
| Host and mobile bridge | `gohost` (desktop Go host) and `mobile` (gomobile JSON bridge) expose comic, video (including offline cache plan/download/verify/list/prune and local danmaku parse/query/layout entry points), the four novel entry points, and Wave 4B adaptation/rendering entry points (`AdaptChapter`, `RenderPanel`, `RenderStoryboard`), routing all requests through `host.Transport` with `Cookies: false` and never reading environment variables. Mobile reuses `callID` cancellation propagation, and whole-response, chunked and service HTTP share the host request ID space. Each client updates its pin and build artifacts when Core is released. |

## Known limitations

- MoeJS has a read-only global `atob`, a 256-register window for a single call expression and partial `Intl` coverage. See `comic/js/compatibility_limits_test.go`. GBK uses `Convert.decodeGbk`. See [GBK compatibility](GBK-COMPATIBILITY.en.md).
- Magpie packages that use Node, Bun or process modules need additional host capabilities. Standard AI exposes text, reasoning, usage and completion events. Clients can read native whole responses for tool and multimodal fields.
- Service `fetch` blocks in the Go host callback. A promise settled during the call can be read, and a handler that is still waiting produces an explicit error.

## Verification

From the repository root run `gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./...`, `sh scripts/check-docs.sh` and `sh scripts/check-error-text.sh`. Formatting should produce no output.

- Use the URL, host and compatibility tests in `comic/js`. Supply the optional official source fixture through `VC_WNACG_JS`. Verify live sites separately.
- Set `MAGPIE_AUDIT_DIR` and `MAGPIE_VENDOR_DIR` for optional original Magpie package fixtures. The corresponding tests report skips when fixtures are absent.
- Use `scripts/mac-verify.sh` and `apple/CoreSmoke` for the Apple bindings, macOS/iOS simulator smoke tests and framework measurements. Verify the API against generated `Mobile.objc.h`. Record device and client end-to-end results separately using the [verification guide](VERIFICATION.en.md).

## Pending work

- Wire desktop and iOS clients to the Wave 4A novel reader and Wave 4B resumable storyboard adaptation, local reference images, static comic panel/chapter rendering UI, and local asset persistence.
- Adopt structured AI errors, streamed reasoning and `aiapi-ext/1` in the clients. Verify original Magpie strategy and middleware ABI compatibility per package, plus first-chunk delivery and cancellation on devices with real accounts.
- Measure framework size, per-source runtime memory, image callbacks and peak JSON/base64 copying in the real app before deciding whether a lower-copy entry point is needed. Verify dependency vulnerability scans separately.
- Adopt the session service runner and shared KV in clients. Coordinate comic comments and links and the host interface split. Check real accounts, site verification, first streamed chunks and cancellation on devices.
- Wire desktop and iOS clients to video offline caching, local timed-comment rendering and AniBaka `anx-rule/2` source management; online danmaku fetching belongs to future plugins. Cross-check legacy `.venera` Dart hashes against a Dart VM.
