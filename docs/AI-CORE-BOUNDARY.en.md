# iOS AI boundaries: standard APIs and Magpie plugins

English | [简体中文](AI-CORE-BOUNDARY.md)

This compares the user's new design with the earlier Core routing prototype to set implementation boundaries. This revision inspects code and changes documentation only; it adds no API, deletes no prototype and changes no client implementation. Desktop Nekophage continues to use the locally installed Magpie; this concerns independent execution inside the iOS app.

## Recommendation

Adopt standard APIs built in and extensions provided by plugins, while keeping execution mechanisms in Core. Core supplies protocols, the plugin host and extension entry points. Plugins implement routing policies, provider quota queries, content masking and related features. Standard OpenAI/Anthropic API-key calls must work without installing extension packages.

Existing `magpierouting` and `mobile.Magpie.PlanRoute` are hard-coded policy prototypes, not a host compatible with Magpie routing plugins. Do not expand them into mandatory built-in scheduling. Retain their source/tests for later separation; removal of exported methods or files requires separate compatibility and deletion handling.

## Trade-offs between three designs

| Design | Strength | Main cost | Fit |
|---|---|---|---|
| Earlier: providers and full scheduling inside Core | Testable implementation, complex policies without plugin installs | Plan, priority, masking and analytics changes drive Core/framework/app updates, turning Core into most of Magpie | Do not expand further |
| Everything in plugins, including protocols and execution rules | Maximum replacement surface | Standard calls need package installation; independent stream, cancellation and credential handling can drift | Not recommended |
| Recommended: built-in standard protocols/execution, installed policies/extensions | Standard APIs work out of the box and optional features share reliable mechanisms | Requires explicit extension APIs, lifecycle and error semantics, not merely moving functions into JS | Best match |

Pluginisation reduces future business coupling, not necessarily initial implementation effort: routing, middleware and usage observer APIs are not wired yet. A plugin update may still require a host update when it needs a new API version.

## What Core must include

| Content | Why it stays in Core | Actual status |
|---|---|---|
| OpenAI Chat Completions/Responses and Anthropic Messages | Native protocol support should not depend on downloading provider plugins | Basic encoding/SSE parsing exists, but no independent product-facing standard-provider API |
| Standard authentication request construction | OpenAI normally uses `Authorization: Bearer`; Anthropic uses `x-api-key` and a version header, not the same authentication | Mainly supplied through template plugins or caller headers today; needs a built-in path |
| Shared structured messages, tools/images and stream events | Routing/middleware need reliable input and clients should not parse each vendor's stream | Messages are mainly text; Responses flattens conversation to a string, so tool/multimodal fidelity needs work |
| Cancellation, timeouts, state and structured errors | Plugins and standard entry points need reliable shared execution | Parts exist; unify and verify without claiming completeness |
| Plugin loading/versioning/enablement/isolation/capabilities | Packages cannot activate without a host | Provider runtime and archive validation exist; generic extension registration/disposal is missing |
| Request lifecycle and strategy entry points | Core calls plugins, validates their output and executes the final selection | No full pipeline yet; extension points need distinct designs |
| Raw token usage and request events | Analytics plugins need factual inputs | Some usage parsing exists, not an account-balance service or full analytics |
| URL/header validation, credential scope and secret scrubbing in logs | Missing an optional content-masking plugin must not leak API keys | Some isolation/validation exists; new standard paths also need coverage |
| gomobile JSON surface | iOS calls standard APIs and extensions through one boundary | Provider and prototype routing bridges exist; standard API/extension bridges remain |

Built-in APIs construct requests and parse replies. iOS URLSession still opens connections, while Keychain, login UI, package storage, persistent statistics and UI remain client responsibilities. Core neither opens connections nor persists secrets. API keys do not imply website subscription accounts or OAuth access.

## Which Magpie capabilities plugins provide

| Capability | Plugin responsibility | Required Core/iOS support |
|---|---|---|
| Non-standard providers | Own login, refresh, model/plan reads and request adaptation | Isolated accounts, Keychain bridge, network/cancellation, package-owned loader/fetch |
| Routing | Difficulty classification, cost/speed/expiry/quota ordering, weights and session policy | Candidate/state snapshots, decision time, capability constraints, result validation, execution and explanations |
| Usage | Provider allowance reads, extra normalisation, aggregation/billing/alerts | Actual response usage/events, sourced/timestamped state, storage/UI; preserve unknowns |
| Content masking | Sensitive-content detection, substitution and response restoration | Outbound/return hooks and streaming context; do not send plaintext when masking was required but unavailable |
| Middleware | Request/reply processing and additional logic | Hook ordering, cancellation, error isolation and permissions |
| Budgets, tools, observers and pages | Feature logic and plugin UI | Category-specific APIs/platform bridges; loading alone is not compatibility |

Provider quota, per-request token usage and analytics reports are distinct. Queries, analysis and presentation can be extensions, but standard responses' actual usage must be preserved. API-key log scrubbing also differs from optional masking of user content.

## Compatibility assumptions to avoid

Upstream Magpie has at least two extension forms:

- Installable JS provider/gateway extensions. Reference source includes `strategies` and `onUsage`, useful starting points for routing/analytics compatibility.
- Go plugins compiled into upstream. For example, `plugins/rotation` registers strategies and `plugins/redact` registers an internal masker, relying on upstream services. Copying those folders does not create packages iOS's JS loader can execute.

The proposed division is viable, but upstream routing, usage or masking modules may lack directly installable iOS-compatible packages. Track original-package execution, host adaptation and required plugin ports separately. Node/Bun/CLI dependencies remain real iOS gaps; pluginisation does not eliminate them.

Bundling existing `testdata/*-compatible` templates must not be called the built-in standard APIs. Templates remain behavioural references; the goal is a first-party standard path that needs no plugin installation.

## Handling existing work

| Existing work | Recommendation |
|---|---|
| `magpieplugin` package/module/auth/streaming foundation | Reuse, isolated from comic plugins |
| Chat/Responses/Messages encoding/decoding | Extract or reuse as protocol infrastructure; complete structured messages/events |
| OpenAI/Anthropic templates | Keep as fixtures/references, not mandatory product packages |
| `magpierouting` / `PlanRoute` | Retain as prototypes; separate infrastructure checks from policies later, without claiming plugin compatibility |
| Swift routing wrapper/policy storage task | Review any patch first; stop expanding mandatory built-in policies until extension APIs define the final entry point |
| Copilot simulator validation | Continue testing in-app non-standard provider plugins, Keychain, login and streaming, independent of desktop Magpie |
| Desktop | Connect to installed Magpie, independently of the iOS implementation |

Independent built-in standard APIs, generic routing strategy registration, masking middleware and the complete usage observation pipeline remain unfinished. Existing Core builds and iOS's 164-test results do not establish those new capabilities.

## Suggested implementation order

1. Independent standard APIs: OpenAI Chat/Responses and Anthropic Messages, custom base URL, protocol-specific authentication, streaming/cancellation/errors, with networking supplied by iOS.
2. Shared lifecycle for standard calls and provider plugins, preserving events/usage; define versioned extension inputs/outputs.
3. Routing strategy and quota-query/usage-observer plugins, covering duplicate model IDs across providers, multiple accounts, unknown quotas, strategy failures and explicit choices.
4. Masking request/reply pipeline, including cross-chunk restoration, no outbound data when required masking fails, and no secrets in logs.
5. Other Magpie extensions individually, verified against pinned original packages. Continue real-account Copilot testing alongside this work.

Without extensions, the baseline is direct calls to the user's selected standard account/model, without silently enabling expiry preference, classification or content-masking policies.

## First implementation contract: standard request construction

Propose an `aiapi` package beginning with offline request construction; execution and gomobile follow. Draft entry points are `BuildRequest(config, json.RawMessage)` and `BuildModelsRequest(config)`, returning `host.Request` or an error. This stage loads no JS package, installs no template and calls no routing prototype.

- Explicit `chat`, `responses` and `messages` protocols default to OpenAI `/v1/chat/completions`, `/v1/responses` and Anthropic `/v1/messages`. Custom base URLs are API root paths; do not guess vendor version directories.
- OpenAI uses a Bearer key; Anthropic uses `x-api-key` and a version header. Reject missing credentials or invalid URLs/headers without exposing keys. Core does not persist secrets.
- Accept native-protocol JSON objects and preserve original bytes, field order and exact numbers. Do not flatten `messages`/`input`, tools, images, reasoning or unknown fields, or claim cross-protocol conversion.
- Allow ordinary and streamed requests according to the body's `stream`; this stage only constructs requests, not streamed execution. Build model discovery separately as `GET /models`, without inventing a successful catalog.
- Disable automatic cookies. Extra headers cannot override protocol authentication or forge Host/message length. URL userinfo, queries and fragments are not shortcuts for passing secrets.

The iOS network bridge still needs a real streaming contract. Earlier `mobileHost.Do` omitted request `id` and cancellation forwarding, while Swift `CoreHost.WireRequest` requires `id`. A local fix now reuses `hostBridge.Do`, shares HTTP ID allocation across comic and Magpie bridges, and propagates `ctx` to `CancelHTTP`. Four contract tests verify required IDs, cross-bridge uniqueness, matching cancellation and no host call after pre-cancellation; `go test -race ./mobile` passes. This fix is not yet published or verified on iOS and does not establish real-account login.

`mobileHost` still does not implement `Streamer`, and Swift `URLSessionTransport.send` waits for the complete body. Acceptance still needs iOS evidence of first chunks before completion and cancellation stopping the underlying request. SSE parsing or polling events already buffered is not real-time streaming verification.
