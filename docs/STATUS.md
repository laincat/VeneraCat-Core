# VeneraCat Core 状态

[English](STATUS.en.md) | 简体中文

## 架构与能力

| 范围 | 当前契约 |
|---|---|
| 宿主 | `host` 定义 HTTP、验证与嗅探接口。客户端拥有网络、Cookie、WebView、播放器与 UI。`challenge` 按分区合并 Cloudflare 和 edge 验证，支持等待取消与验证后的 User-Agent 重放。 |
| 漫画 | `comic/js` 每个源使用独立 MoeJS 运行时。官方 legacy ComicSource 走 [sendMessage 规格](SENDMESSAGE-SPEC.md)，URL 构造由 MoeJS Web API 提供，HTTP 仍走宿主。 |
| 插件与视频 | `plugin` 统一注册漫画和视频源。视频支持 VideoSource、Kazumi XPath/API、红果网页入口和 HLS 处理。`subscription` 解析漫画索引、`vcvideo.json` 与统一源合集。参见 [插件设计](VIDEO-PLUGIN-DESIGN.md)。 |
| 备份 | `backup` 读取官方 `.venera` 并生成可选择的恢复计划。Cookie 优先恢复到备份内的源分区。无源脚本时回退到已安装分区，实际写入由客户端确认并完成。参见 [备份规格](VENERA-BACKUP.md)。 |
| 服务 | `service` 安装、升级和删除服务包，普通 KV 保存在包外，账号值由客户端持有。移动端会话运行器绑定宿主，管理常驻、取消和版本失效。参见 [服务契约](SERVICE-PLUGINS.md)。 |
| AI | `aiapi` 构造 OpenAI Chat/Responses 和 Anthropic Messages 请求，提供文本 SSE 增量解码。`magpieplugin` 通过供应商包自己的宿主路径处理登录、模型、用量与聊天，保留原始结束原因和归一结果。参见 [标准 API](AIAPI.md) 与 [Magpie 兼容性](MAGPIE-COMPATIBILITY.md)。 |
| 移动桥 | `mobile` 提供 gomobile JSON 接口。整包、分段 HTTP 和服务共用宿主请求 ID。发布新 Core 后，各客户端更新自己的 pin 和构建产物。 |

## 已知限制

- MoeJS 的全局 `atob` 不可重定义，单次调用表达式有 256 寄存器窗口，`Intl` 只实现子集。复现见 `comic/js/compatibility_limits_test.go`。GBK 使用 `Convert.decodeGbk`，差异见 [GBK 兼容边界](GBK-COMPATIBILITY.md)。
- Magpie 的 Node、Bun 和系统进程模块需要额外宿主能力。标准 AI decoder 输出文本，供应商插件可提供 reasoning、usage 和结束原因。未知用量保持未知，客户端解码需接受可空字段。
- 服务的 `fetch` 在 Go 宿主回调中阻塞。调用内已结算的 Promise 可读取，仍在等待的 handler 会明确报错。

## 验收入口

在仓库根目录运行 `gofmt -l .`、`go vet ./...`、`go test -race -count=1 ./...`、`sh scripts/check-docs.sh` 和 `sh scripts/check-error-text.sh`。格式检查应无输出。

- 漫画兼容用 `comic/js` 的 URL、宿主和兼容性用例。可选官方源夹具通过 `VC_WNACG_JS` 提供，真实站点需要单独验收。
- Magpie 可选原包夹具由 `MAGPIE_AUDIT_DIR` 和 `MAGPIE_VENDOR_DIR` 指定。未提供夹具时，相关用例报告跳过。
- Apple 绑定、macOS/iOS 模拟器 smoke 和框架测量入口为 `scripts/mac-verify.sh` 与 `apple/CoreSmoke`。接口以生成的 `Mobile.objc.h` 为准，真机和客户端闭环按 [验证指南](VERIFICATION.md) 另行记录。

## 待办地图

- 补足结构化供应商错误、完整原生事件和 [策略、用量观察与脱敏接口](AI-CORE-BOUNDARY.md)。
- 测量真实 App 的框架体积、每源运行时内存、图片回调及 JSON/base64 复制峰值，再决定是否需要低拷贝入口。依赖漏洞扫描单独验收。
- 客户端逐步采用会话服务运行器和共享 KV，联动处理漫画评论与链接契约、宿主接口拆分。真机另验真实账户、网站验证、流式首块和取消。
- 视频下载、离线缓存、弹幕和 AniBaka `anx-rule/2` 规则仍待实现。`.venera` 的历史 Dart 哈希需要独立 VM 对照。
