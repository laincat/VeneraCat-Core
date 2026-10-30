# 标准 AI 接口

[English](AIAPI.en.md) | 简体中文

Core 自带三种标准协议的请求构造:OpenAI Chat Completions、OpenAI Responses、Anthropic Messages。它们不需要安装任何插件,也不经过 Magpie 的 JS 包。

这一层只负责**构造请求**:不联网、不保存凭据、不做路由决策、不做内容脱敏。实际发请求的是客户端(iOS 的 URLSession,或桌面端),通过 Core 的 `host.Transport` 桥。

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

`stream` 可以缺省,也可以是 `true` 或 `false`,原样透传。**本阶段只构造请求,不代表已实现流式执行。**

歧义输入一律拒绝:一个正文里两个 JSON 值、同一个对象里重复的键、正文不是对象、正文为空。

## Cookie 与分区

`Cookies` 恒为 `false`。AI 请求不读取也不写入漫画源的 Cookie jar。`Partition` 只是给客户端分区用的不透明名字,不影响这个约束。

## 与插件的关系

路由策略(按难度、到期时间、额度挑账号)、配额查询、正文脱敏属于**插件**,不在这一层。Core 只提供标准协议的调用底座与扩展机制,见 [AI-CORE-BOUNDARY.md](AI-CORE-BOUNDARY.md)。

旧的 `magpierouting` 与 `mobile.Magpie.PlanRoute` 是早期硬编码原型,保留供拆分参考,不再是标准调用的必经路径。

## 尚未实现

- **实时流式**:`mobileHost` 未实现 `Streamer`,iOS 网络层仍等待完整响应。`SendAIRequest` 即使正文写了 `"stream":true` 也只返回完整响应,并在 `streamed` 字段里说 `false`。
- **真实账号验证**:尚未用真实凭据调用过任何供应商接口。

## mobile 入口

`mobile` 包导出三个方法,JSON 进出:

| 方法 | 作用 |
|---|---|
| `BuildAIRequest(configJSON, bodyJSON)` | 只构造请求,返回 `{method,url,headers,bodyBase64,cookies,partition}` |
| `BuildAIModelsRequest(configJSON)` | 同上的模型列表请求,GET 无正文 |
| `SendAIRequest(callID, configJSON, bodyJSON)` | 构造并通过客户端自己的 `Host.HTTP` 发出,返回 `{status,headers,bodyBase64,finalUrl,streamed}` |

config JSON 是 `{"protocol":"chat|responses|messages","baseUrl":"","apiKey":"...","anthropicVersion":"","partition":""}`。

`SendAIRequest` 走 Core 的宿主桥,所以请求会带上 `id`,也能被 `CancelHTTP` 取消。

