# VeneraCat Core 状态

[English](STATUS.en.md) | 简体中文

## 架构与能力

| 范围 | 当前契约 |
|---|---|
| 宿主 | `host` 定义 HTTP、验证与嗅探接口。客户端拥有网络、Cookie、WebView、播放器与 UI。`challenge` 按分区合并 Cloudflare 和 edge 验证，支持等待取消与验证后的 User-Agent 重放。 |
| 漫画 | `comic/js` 每个源使用独立 MoeJS 运行时。官方 legacy ComicSource 走 [sendMessage 规格](SENDMESSAGE-SPEC.md)，URL 构造由 MoeJS Web API 提供，HTTP 仍走宿主。 |
| 插件与视频 | `plugin` 统一注册漫画、视频和小说源（`novel-js`）。视频支持 VideoSource、Kazumi XPath/API、AniBaka `anx-rule/2` 声明式三阶段流水线（`anibaka-rule`）、红果网页入口、HLS 处理、`video/offline` 离线缓存底座（HLS/直链规划、经 `host.Transport` 拉取、断点续传、SHA-256 完整性校验、本地 m3u8 重写、路径穿越防护与清理）及 `video/danmaku` 本地弹幕层（Bilibili XML、ASS、JSON 解析、时间窗查询与基础轨道防重叠排版；在线弹幕抓取后置由插件实现）。`subscription` 解析漫画索引、`vcvideo.json` 与统一源合集。参见 [插件设计](VIDEO-PLUGIN-DESIGN.md)。 |
| 小说与改编 | `novel` 与 `plugin` 提供 Wave 4A 小说源运行时（共享 `comic:<key>` Cookie 分区与独立 MoeJS 实例），`gohost` 与 `mobile` 暴露 `NovelPopular`、`NovelSearch`、`NovelDetail` 与 `NovelChapter`。`novel/adapt` 提供 Wave 4B-1/4B-2 管线：章节 HTML 抽取分块、严格分镜 JSON 校验、跨章角色表合并、多 chunk 断点续跑（`AdaptCheckpoint`）、预算熔断（`ErrBudgetExceeded`），以及基于 Gemini / OpenAI Images 的静态漫画单格与整章渲染（支持本地画风/角色参考图、`ForceRerender` 断点续画与 `ErrContentBlocked` 不重试）。 |
| 备份 | `backup` 读取官方 `.venera` 并生成可选择的恢复计划。Cookie 优先恢复到备份内的源分区。无源脚本时回退到已安装分区，实际写入由客户端确认并完成。参见 [备份规格](VENERA-BACKUP.md)。 |
| 服务 | `service` 安装、升级和删除服务包，普通 KV 保存在包外，账号值由客户端持有；`fetch(url)` 保持向后兼容，`fetch(url, init)` 经 `Host.FetchRequest`、`service.Bridge`（支持 `host.Transport`）与移动端共享宿主桥完整透传 method/headers/body 与状态码（禁用 `Cookie` 头绕过）。移动端会话运行器绑定宿主，管理常驻、取消和版本失效。参见 [服务契约](SERVICE-PLUGINS.md)。 |
| AI | `aiapi` 内置 OpenAI Chat/Responses 和 Anthropic Messages 请求及执行入口，与 `magpieplugin` 共用结构化错误和 SSE 解码。会话可提供文本、推理、原始结束原因与可空 token 用量。`aiapi-ext/1` 定义可选路由、用量观察与内容脱敏，普通调用直接使用客户端提供的标准账号和模型。参见 [标准 API](AIAPI.md) 与 [Magpie 兼容性](MAGPIE-COMPATIBILITY.md)。 |
| 宿主与移动桥 | `gohost`（桌面 Go 宿主）与 `mobile`（gomobile JSON 桥）统一暴露漫画、视频（含离线缓存计划/下载/校验/列表/清理与本地弹幕解析/时间窗/排版入口）、小说四入口及 Wave 4B 改编渲染出口（`AdaptChapter`、`RenderPanel`、`RenderStoryboard`，严格经 `host.Transport` 且 `Cookies: false`，无环境变量读取）。移动端复用 `callID` 取消传播，整包、分段 HTTP 和服务共用宿主请求 ID。发布新 Core 后，各客户端更新自己的 pin 和构建产物。 |

## 已知限制

- MoeJS 的全局 `atob` 不可重定义，单次调用表达式有 256 寄存器窗口，`Intl` 只实现子集。复现见 `comic/js/compatibility_limits_test.go`。GBK 使用 `Convert.decodeGbk`，差异见 [GBK 兼容边界](GBK-COMPATIBILITY.md)。
- Magpie 的 Node、Bun 和系统进程模块需要额外宿主能力。标准 AI 的事件投影覆盖文本、推理、用量与结束状态。客户端仍可读取原生整包响应来处理工具调用和多模态字段。
- 服务的 `fetch` 在 Go 宿主回调中阻塞。调用内已结算的 Promise 可读取，仍在等待的 handler 会明确报错。

## 验收入口

在仓库根目录运行 `gofmt -l .`、`go vet ./...`、`go test -race -count=1 ./...`、`sh scripts/check-docs.sh` 和 `sh scripts/check-error-text.sh`。格式检查应无输出。

- 漫画兼容用 `comic/js` 的 URL、宿主和兼容性用例。可选官方源夹具通过 `VC_WNACG_JS` 提供，真实站点需要单独验收。
- Magpie 可选原包夹具由 `MAGPIE_AUDIT_DIR` 和 `MAGPIE_VENDOR_DIR` 指定。未提供夹具时，相关用例报告跳过。
- Apple 绑定、macOS/iOS 模拟器 smoke 和框架测量入口为 `scripts/mac-verify.sh` 与 `apple/CoreSmoke`。接口以生成的 `Mobile.objc.h` 为准，真机和客户端闭环按 [验证指南](VERIFICATION.md) 另行记录。

## 待办地图

- 桌面端与 iOS 端接入 Wave 4A 小说阅读器，以及 Wave 4B 分镜断点续跑、本地参考图与静态漫画单格/整章渲染 UI 和本地持久化存储。
- 在客户端接入结构化 AI 错误、流式推理和 `aiapi-ext/1` 扩展。按包验证原版 Magpie 策略及中间件 ABI 的兼容性，并持续验收真实账号、设备首块和取消。
- 测量真实 App 的框架体积、每源运行时内存、图片回调及 JSON/base64 复制峰值，再决定是否需要低拷贝入口。依赖漏洞扫描单独验收。
- 客户端逐步采用会话服务运行器和共享 KV，联动处理漫画评论与链接契约、宿主接口拆分。真机另验真实账户、网站验证、流式首块和取消。
- 桌面端与 iOS 端接入视频离线缓存、本地弹幕渲染与 AniBaka `anx-rule/2` 源管理；在线弹幕抓取由后续插件层实现。`.venera` 的历史 Dart 哈希需要独立 VM 对照。
