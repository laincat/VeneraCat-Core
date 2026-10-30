# 代码体检与清理

[English](CODE-AUDIT.en.md) | 简体中文

## 范围与基线

本轮检查以 Core `469edfcb48ec9564dca086a232a7b64e46fd0a9b` 为基线,覆盖 AI 请求构造、mobile 宿主桥与取消、备份限额与路径、JS 隔离、依赖和验证脚本。修复当前在本地;以下 Windows 验证针对工作树,不自动代表公开提交或 Apple 产物已验证。

完整三项目可视化报告位于同工作区的 `Nekophage/docs/analysis/architecture-audit.html`,可以离线打开。此页仅记录 Core 范围。

## 本轮修复

| 级别 | 问题 | 修复与证据 |
|---|---|---|
| P1 | Swift 先申请 call ID 再排队执行,期间取消会丢失 | `NewCall` 预先登记 context;开始调用时继承该状态;回归先取消后执行 |
| P1 | `Close` 没有取消所有 mobile 调用 | 共用 Core 生命周期 context,关闭取消有 ID 与匿名调用;关闭后新调用也得到取消状态 |
| P2 | `Model`/`Messages`/`Stream` 被 Go struct 当作合法字段,但正文原样发给供应商 | 按精确 key 读取并拒绝保留字段的大小写变体;不重写原生正文 |
| P2 | `anthropic-version` 可带控制字符 | 构造时检查头值,拒绝 CR/LF/空白;不等传输层报错 |
| P2 | 空 host、空 `?`/`#` 分隔符通过根 URL 检查 | 校验 `Hostname`、`ForceQuery` 与 fragment 分隔符 |
| P2 | 未知协议和重复字段错误会回显任意输入 | 用固定错误描述协议与重复键,配置 JSON 解码错误不引用正文 |
| P3 | `SendAIRequest` 第二次解码正文、再执行恒为 false 的赋值 | 删除重复解码与无效分支,保留 `streamed:false` 原契约 |
| P2 | 取 call ID 后早退的调用泄漏预留表 | 每个 `callID` 方法都 `defer endCall`;未知源、坏参数等早退路径也释放两张表;回归先复现泄漏再转绿 |

`aiapi/audit_test.go` 与 `mobile/cancellation_audit_test.go` 是新增的行为回归。修改前坏输入已复现,日志见 `E:/Temp/tri-audit/core-before.log` 与 `cancel-before.log`;修复后单包与 race 检查通过。没有用测试数量替代正确性判断。

## 架构与依赖判断

Go + MoeJS 的分层仍合适:Core 处理漫画源兼容、订阅、备份和协议,客户端拥有网络、Keychain/存储、WebView 与 UI。每个源独立 JS 运行时提高隔离性,同时使内存随已启动源数量增长。运行时应按生命周期复用与释放,不要为列表刷新重复编译包。

直接依赖的用途:

| 依赖 | 用途 | 判断 |
|---|---|---|
| MoeJS alpha.11 / `0a0ed36` | 官方漫画源及 Magpie JavaScript | 预览版本需固定 revision,升级必须带兼容回归 |
| cascadia 1.3.5 | CSS selector | 与 XPath 能力不同,不是同名重复依赖 |
| htmlquery 1.3.7 / xpath 1.3.9 | HTML / XPath 视频规则 | 保留;减少重复 parse 需测量具体热点 |
| x/net 0.62.0 / x/text 0.43.0 | HTML 与文本处理 | 保留当前版本,本轮不盲升 |
| modernc SQLite 1.61.0 | 读取 `.venera` 的数据库 | 是运行依赖,不是纯测试依赖;影响二进制体积 |
| x/mobile / gomobile | ObjC/Swift 桥与 xcframework 工具链 | 生成头文件是 API 事实源,不能凭方法名猜 Swift 导入形状 |

## 尚未解决

- 标准 AI 只有整包执行,`Host.HTTP` 与 iOS 网络桥尚未提供实时分块。`ChatNext` 轮询已有事件不证明真实网络流式。
- 结构化供应商错误、完整原生事件和通用策略/用量/脱敏 ABI 尚需继续实现;现有 `aiapi` 请求构造不代表完整 AI 服务已交付。
- gomobile JSON/base64 会复制正文与响应。先打通流式并测量峰值,再决定是否需要低拷贝入口。
- 框架体积与每源运行时的内存应在真实 App/真机测量。本轮没有吞吐量、峰值内存或优化百分比。
- 本轮没有执行依赖漏洞数据库扫描;`go vet`、race 和固定版本不能替代漏洞审计。

## 验证方式

完整输出写在 `E:/Temp/tri-audit`,每条 gate 使用原命令退出码,不以 `tail` 的退出码代替。格式检查须同时满足命令成功与输出为空。

Mac 侧对 iOS 修复树给出的终态是:真实 `generic/platform=iOS` 构建 `BUILD SUCCEEDED`、`167 tests / 32 suites`、0 warning、shape 与 Windows 一致;并复现了 iOS 网络与 Magpie 生命周期的 5 项 P2 缺陷(见客户端文档),这些仍需在客户端修完再验。

Core 另有一个已知待修:用量归一化输出的 `null` 数组会让客户端 `MagpieUsage` 解码失败,归属客户端模型解析,Core 侧不擅自把未知改成 0。

Windows 已运行的重点命令为 `go test -race -count=1 ./...`、`go vet ./...` 以及专项取消/AI 回归。最终整轮日志与 Mac 的 patched Core / Swift 构建证据在交付报告中列出。Mac 需要核对修复文件 md5,不能仅核对旧 pin 后声称补丁通过。
