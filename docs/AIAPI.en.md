# Standard AI API

English | [简体中文](AIAPI.md)

Core ships request construction for three standard protocols: OpenAI Chat Completions, OpenAI Responses and Anthropic Messages. None of them needs a plugin, and none of them goes through a Magpie JS package.

`aiapi` builds requests, decodes responses and executes optional extensions. Clients own connections and credential storage. Whole-response calls use `host.Transport`, and streamed calls use the host's chunked transport.

## Protocols and default endpoints

| Protocol | Vendor | Default API root | Endpoint |
|---|---|---|---|
| `chat` | OpenAI | `https://api.openai.com/v1` | `POST /chat/completions` |
| `responses` | OpenAI | `https://api.openai.com/v1` | `POST /responses` |
| `messages` | Anthropic | `https://api.anthropic.com/v1` | `POST /messages` |

The model list is `GET /models` under the same root, without a body, using the same credentials.

A custom API root **replaces** the default root entirely; no version directory is appended. Writing `https://gateway.example/openai` means `https://gateway.example/openai/chat/completions`. Core does not guess whether a gateway needs `/v1`. The root must be an absolute http or https URL without userinfo, query or fragment.

## Authentication headers

| Protocol | Headers |
|---|---|
| `chat`, `responses` | `Authorization: Bearer <key>` |
| `messages` | `x-api-key: <key>`, `anthropic-version: 2023-06-01` (overridable) |

An empty key, a blank key and a key with a control character are all refused. Error text never carries the key. Core does not persist credentials.

## Request body

The body is the protocol's **native** JSON object and Core keeps its bytes:

- Key order is unchanged.
- Exact numbers are unchanged (`9007199254740993` is not rounded).
- Tools, images, reasoning and vendor extension fields are not flattened into text.
- Core does not translate between protocols. A `chat` body cannot be sent to `messages`.

Fields checked per protocol:

| Protocol | Required | Notes |
|---|---|---|
| `chat` | `model`, `messages` | `messages` must be an array |
| `responses` | `model`, `input` | `input` may be a string or an array |
| `messages` | `model`, `messages`, `max_tokens` | `max_tokens` must be a positive whole number; `1.5` is refused |

`stream` may be absent, `true` or `false`. Streamed execution requires an explicit `"stream":true` in the body.

Ambiguous input is refused: two JSON values in one body, a repeated key in one object, a body that is not an object, an empty body.

## Cookies and partitions

`Cookies` is always `false`. An AI request neither reads nor writes a comic source's cookie jar. `Partition` is an opaque name for the client's own partitioning and does not relax that rule.

## Relation to plugins

Hosts register or supply `aiapi-ext/1` extensions per request. An empty extension set sends the client's selected account and native request directly.

- `RoutePolicy` receives a credential-free snapshot of candidates and state. Identical snapshots should yield identical order. Core checks explicit model, account and native protocol constraints. Unknown quota stays `null`. A Go caller opts in through `RequestMeta.Route`, and its `ResolveConfig` resolves the selected account alone.
- `UsageObserver` receives one terminal `UsageEvent` per attempted transport call. Counts come from the provider and missing values stay `null`. Go exposes a callback and mobile exposes `usageEvent` JSON with request metadata and outcome.
- `ContentMasker` receives native JSON before transport and returns a restoration session for the call. Core stops before sending if the hook fails, omits the session, returns invalid JSON or changes the model or stream flag. The session buffers split placeholders separately for text and reasoning and flushes at protocol completion. Whole-body restoration must produce a JSON object. Extensions implement detection, masking and restoration.


## Client decoding and verification

Standard AI and Magpie share framing in `host/sse`. It accepts an initial BOM and LF, CR or CRLF line endings. A blank line dispatches the event. An unfinished event is discarded at EOF. The event block limit counts comments and field names.

- Nekularity's standard AI streaming path reads chunks in Swift and calls `AIStreamOpen`, `AIStreamPush`, `AIStreamDone` and `AIStreamClose`. Each handle serializes pushes and completion checks and rejects use after closing. Callers submit chunks in network order.
- Incremental display, first-chunk delivery and cancellation need separate verification on devices with real accounts.

## The mobile entry points

The `mobile` package exports the following methods, JSON in and out:

| Method | What it does |
|---|---|
| `BuildAIRequest(configJSON, bodyJSON)` | builds only, returns `{method,url,headers,bodyBase64,cookies,partition}` |
| `BuildAIModelsRequest(configJSON)` | the model-list request for the same root, GET without a body |
| `SendAIRequest(callID, configJSON, bodyJSON)` | builds and performs it through the client's own `Host.HTTP`, returns `{status,headers,bodyBase64,finalUrl,streamed}` |
| `SendAIRequestStream(callID, configJSON, bodyJSON)` | reads chunks through `NewWithStream` and returns accumulated text, reasoning, finish reason and usage |
| `RunAIRequest(callID, configJSON, bodyJSON, streamed, masker)` | executes with an optional `AIContentMasker` and returns failures in the `error` object |
| `AIStreamPushEvents(handle, chunkBase64)`, `AIStreamState(handle)` | read an event array or current decoder state from the existing handle. `AIStreamPush` still returns an array of text deltas |
| `ClassifyAIHTTPError(protocol, status, headersJSON, bodyBase64)` | classifies HTTP failures for clients using their own host transport |
| `ResolveAIRoute(inputJSON)`, `ValidateAIRouteDecision(inputJSON, decisionJSON)` | returns the default candidate order or validates an external ranking. The client resolves the selected configuration |

The config JSON is `{"protocol":"chat|responses|messages","baseUrl":"","apiKey":"...","anthropicVersion":"","partition":""}`. With `"requireMasking":true`, supply a masking callback to `RunAIRequest`. Go callers use `RequestMeta.RequireMasking`.

`SendAIRequest` goes through Core's host bridge, so the request carries an `id` and `CancelHTTP` can stop it.

`SendAIRequestStream` needs a Core built with `NewWithStream(configJSON, host, stream)`; an instance from `New` reports `cannot stream` instead of reading SSE as a whole body and returning an empty reply. A body without `"stream":true` is refused too: a provider that was not asked for events answers with one JSON object, which decodes to no text at all and looks like a silent provider.

Success requires both HTTP 2xx and a protocol completion marker (`[DONE]`, `response.completed`, or `message_stop`). Transport EOF alone and an unterminated final event are incomplete responses, so partial text is not returned as a successful answer. HTTP errors such as 403/503 cannot become empty successes either.

Cancellation covers waits in both `StreamOpen` and `StreamNext`. The client's `StreamClose` must interrupt those waits and tolerate repeated calls. While `StreamOpen` has not returned its stream ID, closing uses the known `id` in the request JSON.

Structured events have `kind` values `text`, `reasoning`, `usage`, `done` or `error`. `finishReason` keeps the provider value and `outcome` gives the normalized finish. Counts are JSON numbers with `null` for missing values. Anthropic can report usage across multiple events. `response.incomplete` also ends a stream, with `outcome` distinguishing truncation from a content block.

The `error` object has `code`, an English `message`, `retryable` and optional `status`, `protocol` and `retryAfter` in seconds. Codes include `auth_failed`, `rate_limit`, `quota_exhausted`, `content_blocked`, `upstream_error`, `cancelled`, `invalid_request`, `invalid_format`, `masking_failed` and `unknown`. Core uses fixed diagnostic text. On HTTP failure the legacy whole-response method returns the status and structured error with an empty `bodyBase64`.

