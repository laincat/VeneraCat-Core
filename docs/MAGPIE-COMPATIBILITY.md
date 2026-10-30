# Magpie 供应商插件兼容性

[English](MAGPIE-COMPATIBILITY.en.md) | 简体中文

更新:2026-10-31。「兼容」在这里指**被真的跑过**,不是「能导入 JS」。

## 已实现并验证

| 能力 | 说明 | 验证方式 |
|---|---|---|
| 包定位 | `exports["./server"]` → `main` → 根导出 → `index.ts/tsx/js/mjs/cjs`;只链接包内相对模块 | 原始 Copilot 包(入口 `index.mjs`) |
| 插件工厂 | `default` 优先,否则按模块导出顺序取第一个函数;以 `{client, project, worktree, serverUrl, magpie}` 调用 | 同上(该包只导出命名函数,无 default) |
| `auth.methods` | 列出登录方式;`authorize(inputs)` 返回页面与验证码 | 设备码流程,假服务两轮 `authorization_pending` 后给 token |
| 直接交结果的登录 | 密钥类登录里 `authorize` 直接返回 `{type:"success"}` 时,`Authorize` 把它放在 `immediate` 里返回,**不需要轮询** —— 否则客户端会去 poll 一个不存在的 callback | 自写包,以及真实的 `cloudflare-workers-ai`:它的密钥登录当场返回,并带上 `metadata.accountId`(它的 loader 靠这个拼 baseURL) |
| 登录写回 | `client.auth.get/set`,只能读写自己的 provider | 刷新后 `access` 写回,`refresh` 保留 |
| `auth.usage` | 归一成统一窗口结构;未报的份额是 `null` 而不是 0 | 6 条规则测试 + 原始包 |
| `auth.checkin` | 固定词表 `claimed/done/ineligible/inactive/captcha/failed` | 自写插件 + 词表外结果 |
| `auth.options` | `input.magpie.option(provider, key)`,未声明为 `false`,别家为 `false` | 自写插件 |
| `loader` + `fetch` | 请求穿过插件自己的 fetch(它负责刷新与鉴权) | 原始包:先 `/copilot_internal/v2/token` 再 `/chat/completions` |
| 流式回复 | `ReadableStream` 分块 + 三种方言归一;没实现 `Streamer` 时整段文本走同一解析器。**包自己声明方言**:loader 返回 `dialect`(`chat`/`responses`/`messages`)、可选 `path` 与 `headers` —— 所以 Anthropic 形态的供应商不需要宿主认识它,只要说清自己说什么方言 | 分块、跨分块多字节字符;`TestAnthropicTemplateStreamsMessages` 走 `/messages` + `x-api-key` |
| 取消 | 每次 fetch 独立取消;读者放弃时传输停止 | 阻塞 host + `ctx.Done()` |
| 重试策略 | 未输出可换账号,已输出即 `ErrNoRetryAfterOutput` | 空 delta 重试 / 有文本后不重试 |
| 模型列表 | `GET <baseURL>/models`,认 `data` 与 `models` 两种形状 | 原始包 + 假服务;404 时明确报错 |

## 明确不支持(会直说,不会退化成空桩)

| 情况 | 结果 |
|---|---|
| Node 内置模块(`fs`、`child_process`、`http` 等) | `Load` 阶段 `ErrUnsupportedModule` |
| npm 依赖(bare specifier) | 同上 |
| 需要 CLI 子进程的包(Factory、Qoder、部分 WorkBuddy) | 同上;这些包在 iOS 上跑不了,不假装能跑 |
| 非 Chat Completions 的**出站**方言 | `Chat` 只发 Chat Completions;Anthropic/Responses 的**入站**解析已支持,出站转换未实现 |
| 多账号 | 一个 provider 一个账号;Magpie 的 `provider#suffix` 多键未实现 |
| 签到定时循环 | 只有手动触发,没有「每天一次」的调度 |
| 验证码 | 不解决;`captcha` 时让用户去供应商自己的 App |
| 网关翻译/路由策略 | 不做 Magpie 完整网关;账号选择与重试由客户端按 `Sender` 的两条规则决定 |

## 社区包的逐个走查(2026-10-31)

对 `magpie-community/plugins` 提交 `5b4ef41`(此前钉的 `c590a74` 已不是最新,仓库在动)的四个包,先做静态审计(读 `package.json` 与 `index.mjs` 的 import),再从 npm 取回**已发布的整包**真的加载一次:结论一致,**四个都跑不了**,原因都是 Node 能力,不是我们没实现。加载时的原文报错各自指出第一个缺的模块,例如 `factory` 的 `index.mjs:6:30: cannot resolve module "node:http"`。复跑方式写在 `magpieplugin/community_vendor_test.go` 的注释里:把四个包 `npm pack` 出来,`MAGPIE_VENDOR_DIR=<dir> go test -run TestVendorPackagesAreRefused -v ./magpieplugin`;没有该目录时这个测试自动跳过,CI 不依赖第三方 fixtures。

| 包 | 版本 | 许可 | npm 依赖 | 用到的 Node 能力 | 在 iOS 上的结论 |
|---|---|---|---|---|---|
| `@magpie-community/opencode-factory-auth` | 0.1.41 | MIT | 无 | `node:fs`、`node:http` | `ErrUnsupportedModule`(`fs`/`http`) |
| `@magpie-community/opencode-workbuddy-auth` | 0.2.3 | MIT | 无 | `node:crypto`、`node:fs`、`node:os`、`node:path`、`node:zlib` | `ErrUnsupportedModule`;`crypto`/`zlib` 在 iOS 上没有等价物 |
| `@magpie-community/opencode-qoder-auth` | 0.2.50 | MIT | 无 | 上面这些 + `node:child_process`、`process.env` | 同上,且它要起子进程调 CLI,直接不可能 |
| `@magpie-community/opencode-grok-auth` | 0.2.3 | MIT | 无 | `node:child_process`、`node:crypto`、`node:fs`、`node:http`、`node:os`、`node:path` | 同上,依赖本机 CLI 登录状态 |

2026-10-31 复跑过一次,四个包的 import 与上表一致。这件事不必靠手工:`python3 scripts/audit-node-imports.py -a <包目录>` 会列出每个 `.mjs` 用到的 Node 内置与 npm 依赖。`-a` 很重要 —— 只扫入口会漏掉藏在 `lib/server.mjs`、`adapter.mjs`、`oauth.mjs` 里的那一半;顺带它会先去掉注释,否则 Copilot 包里一行 JSDoc 的 `import(...)` 会被误报成 npm 依赖,而那个包实际上什么都不用。

我一开始按「`index.mjs` 没有 Node 内置模块」筛出 8 个候选包,逐个真装之后**发现这个筛法是错的**:`index.mjs` 只 import 自己的模块,`node:http`/`node:fs`/`node:crypto` 藏在 `lib/server.mjs`、`adapter.mjs`、`oauth.mjs`、`provider.mjs` 里。真实结果(每个都有测试钉住):

| 包 | 结果 |
|---|---|
| `cloudflare-workers-ai` 0.0.4 | **加载并读出 provider**,有 `methods`(带 `label`)、有 `loader` |
| `calc` 0.1.0 | 加载成功,但它是计算器不是供应商(没有 `auth`) |
| `river` 0.1.0 | 加载成功,是路由辅助包,也不报 `auth` |
| `chat` 0.1.3 | 拒绝:`lib/server.mjs` 需要 `node:http` |
| `kimi-desktop` 0.1.5 | 拒绝:`adapter.mjs` 需要 `node:fs` |
| `nous` 0.1.1 | 拒绝:`oauth.mjs` 需要 `node:crypto` |
| `zen-free` 0.1.18 | 拒绝:`provider.mjs` 需要 `node:crypto` |
| `search1api` 0.1.1 | 拒绝:入口是 **TypeScript**(`src/index.ts`),本运行时只解析 JavaScript |

所以能跑的供应商插件目前是**两个**:原始 Copilot(0.1.0)与 `cloudflare-workers-ai`(0.0.4),外加两个能加载但不是供应商的包。`search1api` 还顺带暴露一个之前没写明的限制:包里带 `.ts`/`.tsx`/`.mts`/`.cts` 的文件不能加载。错误现在会直说「这是 TypeScript,本运行时只解析 JavaScript」,而不是抛一个把类型注解当成语法错误的信息 —— 后者会让人以为包坏了。

2026-10-31 又抽查了 npm 上另外 11 个已发布包(cline、cursor、devin、kiro、mimo、trae、zcode、zed、zen-free、commandcode、aider),用上面的脚本逐个看:**11/11 都需要 Node 内置**,且 11/11 都用到 `node:crypto`,但同时还要 `fs`/`http`/`child_process`,kiro 甚至要 `node:sqlite` 与 `bun:sqlite`。所以卡住的不是某一个模块,而是一整个 Node 运行时 —— 补一个 `crypto` 也不会让其中任何一个变得能跑。

**除了那些社区包,本仓库里现在有一个能跑的参考包**:`magpieplugin/testdata/openai-compatible/` —— 只用 `fetch`/`Headers`/`URL`,密钥登录、模型列表、流式对话三条都有测试钉着(`TestTemplate*`)。它不是随 App 发布的包(那属于分发决定),而是**写包的模板**:一个包需要 `auth.provider`、`auth.methods`、`auth.loader` 返回 `{baseURL, fetch}`,用量与签到是可选的 —— 缺了就是"供应商没报",不是 0。

由此可以明确讲两句:

- **Copilot 能跑,不代表社区包普遍能跑。** 它恰好只用 `fetch`/`Headers`/`URL` 与计时器,是少数纯 Web 能力的包。
- 让这些包跑起来需要的是 Node 兼容层(`fs`/`crypto`/`zlib`/`child_process`)与真正的外部 CLI,不是"再补几个 API"。这条路不在这套 iOS 插件的目标里,写在这里是为了不让读者以为"兼容 Magpie"等于"所有社区包都能装"。

## 原始 Copilot 包的走查记录

用 `magpie-community/plugins@c590a74` 的原始文件,在假服务上跑通:
设备码登录 → 刷新写回 → 用量 → 模型列表 → 流式回复 → 取消。
**没有**用真账号验证:那需要客户端提供传输与 Keychain,属于 Nekularity 侧。
