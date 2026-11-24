# 标准 AI 接口

[English](AIAPI.en.md) | 简体中文

Core 自带三种标准协议的请求构造:OpenAI Chat Completions、OpenAI Responses、Anthropic Messages。它们不需要安装任何插件,也不经过 Magpie 的 JS 包。

`aiapi` 构造请求、解码响应并执行可选扩展。网络连接和凭据存储归客户端所有。整包调用走 `host.Transport`，流式调用走同一个宿主的分段传输接口。

## 协议与默认端点

| 协议名 | 供应商 | 默认 API 根 | 端点 |
|---|---|---|---|
| `chat` | OpenAI | `https://api.openai.com/v1` | `POST /chat/completions` |
| `responses` | OpenAI | `https://api.openai.com/v1` | `POST /responses` |
| `messages` | Anthropic | `https://api.anthropic.com/v1` | `POST /messages` |

模型列表是同一根下的 `GET /models`,不带请求体,用同一套凭据。

自定义 API 根**整体替换**默认根,不追加版本目录。写 `https://gateway.example/openai` 就是 `https://gateway.example/openai/chat/completions`;Core 不猜第三方需要 `/v1` 还是别的。API 根必须是绝对的 http 或 https URL,不得带 userinfo、查询串或 fragment。

## 鉴权头

| 协议 | 头 |
|---|---|
| `chat`、`responses` | `Authorization: Bearer <key>` |
| `messages` | `x-api-key: <key>`、`anthropic-version: 2023-06-01`(可覆盖) |

空 key、只含空白的 key、含控制字符的 key 一律拒绝。错误文本不包含 key。Core 不持久化凭据。

## 请求体

请求体是该协议**原生**的 JSON 对象,Core 原样保留字节:

- 键顺序不变。
- 精确数字不变(`9007199254740993` 不会被舍入)。
- 工具、图片、推理与厂商扩展字段不压平成文本。
- Core 不做跨协议转换。`chat` 的正文不能发给 `messages`。

按协议检查的字段:

| 协议 | 必需 | 说明 |
|---|---|---|
| `chat` | `model`、`messages` | `messages` 必须是数组 |
| `responses` | `model`、`input` | `input` 可以是字符串或数组 |
| `messages` | `model`、`messages`、`max_tokens` | `max_tokens` 必须是正整数,`1.5` 不接受 |

`stream` 可以缺省，也可以是 `true` 或 `false`。流式执行要求正文明确指定 `"stream":true`。

歧义输入一律拒绝:一个正文里两个 JSON 值、同一个对象里重复的键、正文不是对象、正文为空。

## Cookie 与分区

`Cookies` 恒为 `false`。AI 请求不读取也不写入漫画源的 Cookie jar。`Partition` 只是给客户端分区用的不透明名字,不影响这个约束。

## 与插件的关系

`aiapi-ext/1` 由宿主注册或按请求提供。空扩展直接发送客户端选定的账号和原生请求，不启用策略或脱敏。

- `RoutePolicy` 接收不含凭据的候选与状态快照。同一快照应产生相同排序，显式模型、账号和原生协议约束由 Core 校验。未知额度保留为 `null`。Go 请求通过 `RequestMeta.Route` 选择启用，客户端的 `ResolveConfig` 只解析最终选中账号的配置。
- `UsageObserver` 在实际调用传输后收到一次终态 `UsageEvent`。用量来自供应商的实际计数，未知值为 `null`。事件包含请求元数据与结果，通过 Go 回调和移动端 `usageEvent` JSON 提供。
- `ContentMasker` 在发请求前接收原生 JSON，并为本次请求返回还原会话。钩子报错、缺少会话、返回无效 JSON 或改动模型/流式标记时，Core 拒绝发请求。会话分别缓冲文本与推理的跨块占位符，并在协议结束时刷新。整包还原结果必须是 JSON 对象。识别、替换与还原算法由扩展实现。


## 客户端解码与验收

标准 AI 与 Magpie 共用 `host/sse` 帧规则，接受开头的 BOM 和 LF、CR、CRLF 行尾。空行提交事件，流结束时丢弃未闭合的事件。每个事件块的字节上限包括注释和字段名。

- Nekularity 的标准 AI 流式路径由 Swift 读取分块并调用 `AIStreamOpen`、`AIStreamPush`、`AIStreamDone` 与 `AIStreamClose`。句柄串行处理分块与完成检查，关闭后拒绝使用。调用方按网络到达顺序提交分块。
- 真实账号和设备上的增量显示、首块与取消需要分别验收。

## mobile 入口

`mobile` 包导出以下方法,JSON 进出:

| 方法 | 作用 |
|---|---|
| `BuildAIRequest(configJSON, bodyJSON)` | 只构造请求,返回 `{method,url,headers,bodyBase64,cookies,partition}` |
| `BuildAIModelsRequest(configJSON)` | 同上的模型列表请求,GET 无正文 |
| `SendAIRequest(callID, configJSON, bodyJSON)` | 构造并通过客户端自己的 `Host.HTTP` 发出,返回 `{status,headers,bodyBase64,finalUrl,streamed}` |
| `SendAIRequestStream(callID, configJSON, bodyJSON)` | 用 `NewWithStream` 的分段传输边收边解析，返回汇总文本、推理、结束原因与用量 |
| `RunAIRequest(callID, configJSON, bodyJSON, streamed, masker)` | 使用可空的 `AIContentMasker` 执行请求，失败以 `error` 对象返回 |
| `AIStreamPushEvents(handle, chunkBase64)`、`AIStreamState(handle)` | 使用已有解码句柄，获取事件数组或累计状态。原 `AIStreamPush` 继续返回文本数组 |
| `ClassifyAIHTTPError(protocol, status, headersJSON, bodyBase64)` | 为使用自有宿主传输的客户端归类 HTTP 错误 |
| `ResolveAIRoute(inputJSON)`、`ValidateAIRouteDecision(inputJSON, decisionJSON)` | 获取默认候选顺序或校验外部策略排序，客户端按选中 ID 取得配置 |

config JSON 是 `{"protocol":"chat|responses|messages","baseUrl":"","apiKey":"...","anthropicVersion":"","partition":""}`。可加 `"requireMasking":true`，此时必须在 `RunAIRequest` 提供脱敏回调。Go 调用使用 `RequestMeta.RequireMasking`。

`SendAIRequest` 走 Core 的宿主桥,所以请求会带上 `id`,也能被 `CancelHTTP` 取消。

`SendAIRequestStream` 必须建在一个用 `NewWithStream(configJSON, host, stream)` 建立的 Core 上;用 `New` 建出来的实例会明确报 `cannot stream`,而不是把 SSE 当整包读成空回答。正文缺 `"stream":true` 时同样直接报错——供应商没被要求流式时只回一个 JSON 对象,解析出来是空文本,看起来像供应商沉默。

返回成功需要同时满足 HTTP 2xx 和协议结束标记(`[DONE]`、`response.completed`、`message_stop`)。只有连接 EOF 或没收完的最后一个事件都算未完成,不返回“成功”的部分文本;HTTP 403/503 等状态也不能变成空回答成功。

取消覆盖等待 `StreamOpen` 与 `StreamNext` 的阶段。客户端的 `StreamClose` 必须能打断等待,并允许重复关闭;当 `StreamOpen` 尚未返回自己的流 ID 时,关闭使用请求 JSON 中已知的 `id`。

结构化事件的 `kind` 为 `text`、`reasoning`、`usage`、`done` 或 `error`。`finishReason` 保存供应商原值，`outcome` 是归一结果。计数使用 JSON 数字，缺失的计数为 `null`，Anthropic 可在多个事件分次报告用量。`response.incomplete` 也是终态，客户端应按 `outcome` 区分截断或内容拦截。

`error` 对象含 `code`、英文 `message`、`retryable`，以及可选 `status`、`protocol`、`retryAfter`（秒）。错误码包括 `auth_failed`、`rate_limit`、`quota_exhausted`、`content_blocked`、`upstream_error`、`cancelled`、`invalid_request`、`invalid_format`、`masking_failed` 与 `unknown`。Core 使用固定诊断文本，HTTP 失败时原整包入口仍返回状态和结构化错误，`bodyBase64` 为空。

