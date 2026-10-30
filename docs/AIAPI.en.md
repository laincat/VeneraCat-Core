# Standard AI API

English | [简体中文](AIAPI.md)

Core ships request construction for three standard protocols: OpenAI Chat Completions, OpenAI Responses and Anthropic Messages. None of them needs a plugin, and none of them goes through a Magpie JS package.

This layer only **builds requests**. It opens no connection, stores no credential, makes no routing decision and masks no content. The client performs the request — URLSession on iOS, or the desktop client — through Core's `host.Transport` bridge.

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

`stream` may be absent, `true` or `false`, and is passed through as written. **This stage only builds requests; it does not implement streamed execution.**

Ambiguous input is refused: two JSON values in one body, a repeated key in one object, a body that is not an object, an empty body.

## Cookies and partitions

`Cookies` is always `false`. An AI request neither reads nor writes a comic source's cookie jar. `Partition` is an opaque name for the client's own partitioning and does not relax that rule.

## Relation to plugins

Routing policies — picking an account by difficulty, expiry or remaining quota — quota queries and content masking belong to **plugins**, not to this layer. Core provides the standard-protocol base and the extension mechanism; see [AI-CORE-BOUNDARY.en.md](AI-CORE-BOUNDARY.en.md).

The older `magpierouting` package and `mobile.Magpie.PlanRoute` are an early hard-coded prototype. They are kept as reference for splitting, and are no longer the required path for a standard call.

## Not implemented yet

- **Real-time streaming**: `mobileHost` does not implement `Streamer`, and the iOS network layer still waits for the whole response. `SendAIRequest` returns the whole response even when the body says `"stream":true`, and says `false` in the `streamed` field.
- Real-account verification: no vendor endpoint has been called with a real credential yet.

## The mobile entry points

The `mobile` package exports three methods, JSON in and out:

| Method | What it does |
|---|---|
| `BuildAIRequest(configJSON, bodyJSON)` | builds only, returns `{method,url,headers,bodyBase64,cookies,partition}` |
| `BuildAIModelsRequest(configJSON)` | the model-list request for the same root, GET without a body |
| `SendAIRequest(callID, configJSON, bodyJSON)` | builds and performs it through the client's own `Host.HTTP`, returns `{status,headers,bodyBase64,finalUrl,streamed}` |

The config JSON is `{"protocol":"chat|responses|messages","baseUrl":"","apiKey":"...","anthropicVersion":"","partition":""}`.

`SendAIRequest` goes through Core's host bridge, so the request carries an `id` and `CancelHTTP` can stop it.

