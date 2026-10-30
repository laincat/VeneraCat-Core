# Magpie 插件系统(iOS)

[English](AI-SERVICE.en.md) | 简体中文

当前分层以 [标准 API 与插件的取舍](AI-CORE-BOUNDARY.md) 为准:Core 内置 OpenAI/Anthropic 标准 API 基础,Magpie 插件提供其他供应商与路由、用量、内容脱敏等扩展。iOS 在 App 内独立运行;桌面端首期连接本机安装的 Magpie。

下文保留早期设计与实现记录,其中“尚无 hook/认证”等阶段性描述已被后续实现覆盖,不是当前总状态。已有供应商 hook、授权、请求/流式与假服务测试;完整标准直连入口、通用扩展点和真实 iOS 账号验收仍待完成。包名 `magpieplugin`,独立于漫画源的 `plugin.Registry`。

## 用户目标

兼容 Magpie 使用的供应商插件,复用其认证、模型列表、请求适配和刷新逻辑;
再把需要的 Magpie 内置供应商按同一契约做成插件。iOS 在本机直接使用,
不以桌面网关、Magpie/Bun 配套进程作为运行前提。

这需要区分供应商插件、路由和协议转换:前者获取账号和发送供应商请求,
路由决定请求哪个账号/模型,协议转换把应用的消息变为目标 API。支持插件
不是自动获得 Magpie 的全部网关行为。

## 原始插件抽查

固定源码版本:
[magpie-community/plugins@c590a740](https://github.com/magpie-community/plugins/tree/c590a7405792c1149e554bc0e58f8cb44a1f3da3)。
检查 5 个包的 `package.json` 与 `index.mjs`,以及 Copilot 的两个相对模块。
五个清单均声明 MIT,无 dependencies;这不表示代码不依赖运行环境。

| 包 | 已看到的运行依赖 | iOS 含义 |
|---|---|---|
| Copilot | 包内 `endpoints.mjs` / `usage.mjs`,fetch、Headers、URL、定时器,设备码登录 | 优先兼容候选,未执行验证;认证服务是否接受本应用仍需实测 |
| Factory | node:http、node:fs、Buffer、Web Streams、相对模块 | 需检查调用路径并实现所需兼容接口,不能仅加载入口 |
| WorkBuddy | node:crypto、node:fs、node:os/path、node:zlib、TransformStream | 加密与压缩可做受控桥接;桌面文件读取不能假定在 iOS 可用 |
| Qoder | node:crypto、node:child_process、fs/promises、os/path/url、ReadableStream | 子进程相关路径需替代或禁用;存在非兼容路径 |
| Grok | spawn/execFile、fs、os/path、crypto,查找本机 CLI 的逻辑 | 依赖 CLI 的路径不能直接在 iOS 上照搬 |

静态发现 import 不等于整个插件必不可用:某些依赖只服务可选功能,需要逐条
追踪。也不能用空实现伪装成功,否则会把加载成功误报为供应商可用。分析样本
不涉及真实账号,未启动 CLI、未执行安装脚本、未读取其他应用的凭据。

## 独立系统与兼容边界

名称统一为「Magpie 插件系统」,英文 Magpie plugin system,代码包
`magpieplugin`。它直接读取 Magpie 供应商原包,单独管理安装记录、版本、
账号与运行实例,不使用漫画源 key、Cookie 或存储。协议转换和路由是
系统提供的 AI 服务能力,不冒充供应商插件本身已经实现的功能。

完全兼容是目标,不是当前结论:按固定版本原插件逐项验证加载、授权、刷新、
模型发现、流式请求与取消。对 iOS 无法提供的 CLI/子进程依赖明确报错,
不以空实现隐藏差异。初期范围为供应商插件,不承诺兼容 Magpie 的全部
页面、Agent、Pi 扩展或桌面功能。

## 兼容宿主的候选分层

| 层 | 职责 |
|---|---|
| 原包入口与模块 | 解析 package.json 的 main/exports,保留相对模块关系;TS 包需明确构建策略,不是改扩展名即可 |
| 供应商 hooks | 先验证 auth.provider、methods/authorize/callback、loader/getAuth、client.auth.set;再接 config、provider.models、chat.headers、usage |
| Web 兼容 API | fetch/Headers/Request/Response、URL、定时器、AbortSignal、编码与流;按实际样本确定实现集合 |
| 平台服务 | URLSession 流、取消、ASWebAuthenticationSession/设备码界面、Keychain、允许的加密操作 |
| AI 调度与协议 | 应用统一请求与事件模型、协议编解码、模型能力检查、后续路由与重试 |

Core 可承担协议/模块/hook 适配和可测调度;客户端仍负责网络、界面与凭据。
每插件/账号的凭据范围明确隔离,不进入漫画 Cookie、源设置或普通日志。
插件的 getAuth 只获得当前账号,client.auth.set 不能跨账号写入。供应商
脚本需要使用令牌发请求,因此不能声称脚本完全接触不到凭据;限制应是只见
自己的账号、不得从内容插件直接取得供应商凭据。

执行引擎尚未定案。优先验证现有 MoeJS 的模块、Promise 与宿主异步调用;
JavaScriptCore 可作为对照。两者都不会自动提供 Node/Bun 或完整 Web API。
补兼容接口必须通过客户端能力,不能偷偷在 Core 建连接或创建子进程。
安装包容器独立于兼容 API,目前没有决定 zip/7z 或 npm 在线安装机制。

## 先做离线兼容验证,再接真实服务

1. 用自编插件固定 hook 输入/输出、异步调用、账号作用域和错误传播。
2. 用固定版本 Copilot 原模块和假 HTTP 服务验证设备码授权、轮询取消、
   凭据写回、刷新和自定义 fetch,不在测试中放真实 token。
3. 加流式消息调用与模型发现,再用用户自己的账号做 iOS 端到端验证。
   只有实测后才能声称该供应商可用。
4. 扩展 Factory 等插件所需的兼容接口;对 CLI/桌面文件依赖逐项报告,
   再把选定的内置供应商按相同契约实现。路由随后基于结构化错误、用量
   和模型能力扩展,输出已经开始后不自动拼接另一家生成。

App 分发渠道对下载执行代码和自更新功能的限制需在发布前核对,不能用
脚本引擎兼容性替代上架可行性判断。

## 兼容性静态分析(已实现)

`magpieplugin.Analyze` 在内存快照上从入口出发,沿 `import` / `export … from` /
副作用导入 / 动态 `import()` / `require()` 走完整模块图,产出:

- `Modules`:包内可达文件(有序);
- `Externals`:包外依赖,分 `node`(Node 内建,需宿主适配)、`bare`(npm 包)、
  `url`;
- `Missing`:看起来是包内文件、实际不存在。

它先剥掉注释再扫描:真实包常用 JSDoc 类型引用
(如 `@type {import('@opencode-ai/plugin').Plugin}`),那不是运行时依赖,
扫描注释会凭空报出一个包从未加载的依赖。正则字面量未建模,已注明。

用固定版本原包实测(只读文件,未联网、未执行):

```
Copilot  entry=index.mjs
         modules=[endpoints.mjs index.mjs usage.mjs]
         node=[]  bare=[]  missing=[]
```

也就是说 Copilot 包只依赖 Web 基础能力(相对模块、`fetch`、`Headers`、`URL`、
定时器),是第一个值得做执行验证的候选。这只是静态结论:设备码授权、
凭据写回、刷新与流式请求都还没有跑过。Qoder / Grok / WorkBuddy / Factory
的 `node:` 依赖会在 `Externals` 里显式出现,不会被静默忽略。

静态分析看不到 `eval`、拼接出来的说明符或运行时才决定的路径,因此它是
"还缺什么"的清单,不是兼容性证明。

## 加载与执行(已实现)

`magpieplugin.Load` 把快照编译进 MoeJS,`Link` 只解析包内相对模块:Node 内建
或 npm 依赖在这里就失败(`ErrUnsupportedModule`),不会悄悄变成空实现。
`NewInstance` 为每个账号建独立运行时(独立全局、独立错误),`Call` 调用导出的
函数并把 `input` 传进去。

Magpie 的 hook 是 async 的,而 MoeJS 交给 Go 的是 Promise 对象本身,所以
`Call` 用一段内置脚本 `await` 那个 Promise,再用回调把结果交回 Go;调用跑在
`eventloop` 上,等异步工作完成后才读取答案。抛出的异常变成 Go 错误,不会
变成"成功但空结果"。

用固定版本 Copilot 原包实测(只读文件,未联网、未用账号):

```
Load     3 个模块编译、链接成功
Instance 顶层求值成功,exports CopilotAuthPlugin = true
```

这证明原包能在我们的引擎里编译、链接、求值并导出入口函数;授权流程、设备码
轮询、凭据写回、刷新与真正的请求都还没有执行过,所以仍不能称"该供应商可用"。

## 网络与 Web API 桥接(已实现)

`Instance.UseHost(h)` 由客户端提供 `Host.Do(ctx, *Request)`;Core 仍不联网。
它给插件装上 `fetch`、`Request`、`Headers`、`Response`、`URL` 五个基础接口:

- `fetch` 把请求交给 `Host`,在 loop 之外执行,完成后再兑现 Promise;
  慢请求不会卡住插件自己的定时器,context 被取消时 Promise 以错误拒绝。
- `Headers` 名字小写化(与 Web 一致),`Response` 提供 `status`/`ok`/`headers`/
  `json()`/`text()`;正文以字符串原样过桥,`9007199254740993` 这类整数不会
  被浮点化。
- `URL` 由 Go 的 `net/url` 解析后映射成 Web 的字段,含根路径 `/`。
- `Request` 是**被真实包装出来的**:cloudflare 的包会先 `new Request(...)` 读自己的
  url 与 headers,再决定往哪发。插件伸手拿不到它时抛的是 `ReferenceError`,
  看起来像包坏了,而不是缺一个 API。

用固定版本 Copilot 原包驱动真实登录流程(假服务,无网络、无账号):

```
authorize  → POST /login/device/code  → 返回 verification_uri + user_code
callback   → 轮询 /login/oauth/access_token,pending 两次后拿到 token
结果       → { type: "success", refresh: "gho_test_token" }
企业主机   → 请求打到 company.ghe.com,验证页也是该主机
拒绝       → { type: "failed" },只轮询一次
```

也就是说,这个供应商的**设备码登录**已经在我们的引擎里跑通,而不是"能加载"。

`Instance.UseClient(provider, store)` 再补上凭据一环:插件通过
`client.auth.get` / `client.auth.set` 读写**自己那个 provider** 的登录信息。
读别的 id 得到 `null`(不是"返回自己的",那种"看起来成功"会给包一份错凭据),
写别的 id 直接报错;存储由客户端实现(Apple 上是 Keychain),Core 不留凭据。
`AuthStore` 是本地存储,所以它在 loop 上同步应答;网络才走异步 Promise。

同一个 Copilot 原包继续验证刷新路径(假服务):

```
loader(getAuth)        给出 baseURL,并定义一个 fetch
调用 options.fetch     发现 access 为空 → 请求 copilot_internal/v2/token
                       → 拿到新 token → client.auth.set 写回
写回内容                access=tid=…、expires=expires_at*1000-5min、refresh 保留
越权写入               插件试图写 openai 的登录 → 被拒,store 无记录
```

用量与模型列表也走通了:

```
Copilot auth.usage   请求 copilot_internal/user(用 OAuth token,不是推理 token)
                     → { name: "Premium requests", used: 50, limit: 300,
                         amount: 150, resetsAt: "2026-01-01T00:00:00.000Z" }
自编 provider.models 返回的模型名、limit.context/output、modalities 原样过桥
```

取消也接上了:`fetch(url, {signal})` 的中止会真的取消客户端请求(Go 侧用
`context.WithCancel`),并被 Promise 以错误拒绝,而不是拖到网络自己超时。

```
AbortController + setTimeout(abort, 5ms)
fake host 阻塞在 ctx.Done()
结果 rejected:context canceled
```

用量 hook 要求 `AbortSignal.timeout`,而 MoeJS 不提供它,所以 prelude 补了
最小可用的 `AbortSignal`/`AbortController`:超时能触发 `aborted` 与监听器,
不假装支持请求中途取消。

流式输出也接上了。客户端若额外实现 `Streamer.DoStream(ctx, req, onChunk)`,
fetch 会先拿到响应头,再把每个分块推进插件的 `ReadableStream`:

```
package: const r = await fetch(url); for await (const c of r.body) …
chunk 是字节(Uint8Array),多字节字符跨块也能正确解码
响应头之后失败 → 流以错误结束(reader.read() 抛),不是静默 EOF
不实现 Streamer 的客户端 → 退回整段响应,行为不变
```

Copilot 原包的 `options.fetch` 也实测过:它重写鉴权头之后,流仍然逐块到达
(`data: one` / `data: two` / `data: [DONE]`),说明包装没有把流压平。

协议解析与重试策略也补上了:插件说什么方言,客户端拿到的都是同一组事件。

```
DialectChat       choices[0].delta.content → text;finish_reason → done
DialectResponses  response.output_text.delta / reasoning_*.delta / completed
DialectMessages   content_block_delta(text_delta|thinking_delta) / message_delta
error             vendor 在 200 里报的错成为 EventError,不是被吞掉
usage             保留原文与 json.Number,9007199254740993 不会变成浮点
```

`Attempt` 记录「是否已经把内容给用户看过」,`MayRetry()` 在已输出后返回
`ErrNoRetryAfterOutput`;空的 delta、done、vendor 错误都不算输出,不会误锁重试。

路由也补上了:`Sender.Send` 按顺序给一批 endpoint(body 按各自方言编码),失败的处理分两种。

```
没输出过      换下一个 endpoint —— 密钥过期、额度用完,正是第二个账号的用处
已经输出过    ErrNoRetryAfterOutput,不再往下试 —— 重试只会重复或自相矛盾
空 delta/done 不算输出,所以「流开了但一个字没来」仍然可以重试
调用方主动停  errStopStream 直接回传,不去试别的 endpoint
全都失败      RouteError 里带上试过哪几个
```

流式那半边有个真实的坑:客户端实现 `Streamer` 时,我们用 `io.Pipe` 把分块喂给
`StreamReader`。读者读到 `[DONE]` 就正常结束了,但 vendor 可能还在写 —— 于是写端
拿到 `io.ErrClosedPipe`。第一版把它当成传输错误,导致「明明成功了却报错」,现在把
这种情况和在读者已经结束后的关闭管道视为正常,而读者自己放弃时的错误优先返回。

一个容易踩的点也写进了测试:SSE 的最后一块必须有自己的空行,否则按规范
被丢弃——我第一版测试就少了它,导致 `response.completed` 静默消失。

## 每日签到

有的插件能自己按供应商的签到。结果只有固定几种,客户端不必解析各家的话:

```
claimed / done / ineligible / inactive / captcha / failed
```

`captcha` 意味着供应商要人机验证:**Magpie 不解决验证码,这里也不解决**,只能让用户去
它自己的 App 点。插件给了集合之外的结果时记为 `failed`,并把插件原话放在原因里
(`插件回答了 "yes":…`)—— 否则插件的措辞会被当成宿主的结论。

## 插件自己的开关

有些包声明开关(如「只用免费模型」),用户的选择按 `provider|option` 存,包通过
`input.magpie.option(provider, key)` 读。两个细节照 Magpie:

- **没声明过的 key 返回 `false`**,不是 `undefined` —— 写成 `if (input.magpie.option(…))`
  的包在两种宿主上行为一致。
- **拿不到别人家的值**:`option("别的 provider", …)` 一律 `false`,开关不会被跨账号读到。

## 模型列表来自供应商

Magpie 自己养了一份「每家有哪些模型、上下文多大」的目录,因为网关要先知道。客户端只需
要知道**用户能选什么**,所以这里不抄目录,而是问已经知道的人:

```
loader 准备好入口 → GET <baseURL>/models → {"data":[...]} 或 {"models":[...]}
```

两种形状都认;`id`/`name`/`context_window`/`max_output_tokens`/`modalities` 归一。
供应商不给列表时**明说**它不给(带 HTTP 状态),而不是回一个空表或编一份 —— 空表会让
用户以为「这家没有模型」。

## 用量归一

插件报的是各家的数字;客户端该看到同一个形状。`NormalizeUsage` 照 Magpie 的规则逐条
实现,几条容易走样的:

```
used 未给 → null,不是 0      「0% 已用」和「说不清」在配额卡上完全不同
used 过了 100 → 保留         clamp 会把「超额」藏起来
秒 / 毫秒都出现过             小于 1e11 当秒;否则 1.7e9 会被读成 1970 年
字符串当数字 → 0             与 Magpie 一致:供应商说了,只是不是数字
balance → 只有真的是数字才算   预付钱包有余额,没有「占多少比例」这回事
```

## 对话走插件自己的入口

供应商插件交出的不是请求头,而是一个 `loader(getAuth, provider)`:**它自己刷新登录,
返回一个带着凭据的 `fetch`**。所以请求必须穿过插件,不能由 Core 另外拼一份 —— 否则
刷新逻辑就得再写一遍,而且两份迟早不一致。

```go
inst.Chat(ctx, &ChatRequest{Model, Messages, System}, func(e Event) error { … })
```

链路:Core 生成 body → 插件 `loader` 刷新并给出 baseURL/fetch → 插件的 fetch 发出请求
→ 回复边到边解析成同一组事件(文本/思考/done/错误/token 用量)。

两件在实现里踩到的事:

- **不能流式也必须一样**:客户端没实现 `Streamer` 时,`response.body` 是 null。第一版
  直接当成「没有回复」,结果对话看起来成功却一个字都没有。现在退化成整段文本再喂给
  同一个解析器,事件不因传输能力而不同。
- **handle 要先声明**:JS 里把 `const handle` 放在后面,非流式分支就撞上暂时性死区,
  报 `Cannot access 'handle' before initialization`。这类错误只有真跑才看得见。

测试用原始 Copilot 包 + 假服务,断言的不只是文案:还确认**先刷新再请求**
(`/copilot_internal/v2/token` 出现过),以及新 token 被写回(`access == tid=api`)。

## 插件 ABI

Magpie 的供应商包导出的是一个**函数**,Magpie 用 `{client}` 调它,它返回
`{auth: {provider, methods, loader, usage, options}}`。客户端的 Swift 不该重写这套
约定,所以桥放在 Core:`Instance.InstallPluginBridge()` 之后有

```go
auth, _      := inst.PluginAuth()          // provider、登录方式、插件自己的选项
started, _   := inst.Authorize(0, inputs)  // 要展示给用户的页面
answer, _    := inst.Poll(started["id"])   // 等用户完成(回调自己会轮询)
inst.CancelSignIn(id)                      // 放弃这次登录
usage, _     := inst.Usage(provider)       // 用量窗口
```

两个细节是照着 Magpie 的实际行为来的,不是照文档猜的:

- **找哪个函数**:先看 `default`,没有就按模块导出顺序找第一个函数。原始 Copilot
  包只导出 `CopilotAuthPlugin`,没有 default —— 只认 default 会直接说「这不是插件」。
- **回调自己会轮询**:Copilot 的 `callback()` 内部是个直到成功的循环,所以一次
  `Poll` 就跨过了 `authorization_pending` 和成功两步。测试用假服务计数,确认它真的
  问了两次,而不是跳过待定直接拿 token。

用原始 Copilot 包跑的测试:

```
TestPluginAuthDescribesThePackage   provider=github-copilot,methods/hasLoader/hasUsage 都对
TestPluginDeviceFlow                页面 + 验证码 + 一次 Poll 拿到 gho_…,且 polls==2
TestPluginCancelSignIn              取消后再 Poll 报错,不挂着等
TestPluginUsageReadsTheSignedInAccount
TestPluginBridgeNeedsAPlugin        没有插件函数的包给出直白理由
TestPluginBundleIsPlainFiles        包按路径→字节(base64)交付
```

## iOS 桥接(mobile 包)

Swift 不需要认识 Go 类型:`mobile.Magpie` 与其余接口一样 JSON 进出。

```go
m := mobile.NewMagpie(host)          // host 就是漫画源用的那个 Host
m.LoadPackage(filesJSON)             // {"路径": base64 内容},全部在内存里
m.SetAuth("github-copilot", authJSON) // 账号登录信息,由客户端从 Keychain 取出
m.Call("Plugin", argJSON)             // 跑一个导出函数,结果回 JSON
m.SavedAuth()                         // 插件刷新后写回的值,客户端存回 Keychain
m.Auth()                              // 供应商 id、登录方式、插件选项
m.Authorize(0, inputsJSON)            // 要打开的页面 + 本次登录的 id
m.Poll(id)                            // 等用户完成;回调自己会轮询
m.CancelSignIn(id)                    // 用户放弃
m.Usage(provider)                     // 用量窗口(原样转交)
m.Close()
```

`Auth`/`Authorize`/`Poll`/`Usage` 都返回包自己的 JSON:用量窗口长什么样是供应商的事,
Core 不替它编造数字。

完整面是 18 个方法,`mobile/surface_test.go` 用 AST 把名字钉住 —— 客户端因此可以在
编译前先跑一次检查,而不是等到 Swift 报「没有这个方法」:

| 方法 | 做什么 | 客户端要注意 |
|---|---|---|
| `LoadPackage(filesJSON)` | 装入包(`{"路径": base64}`) | 路径与大小由 Core 再校验一次 |
| `SetAuth(provider, authJSON)` | 交出账号登录信息 | provider 与包声明的必须一致,否则 `client.auth.get` 拒绝跨 provider |
| `SavedAuth()` | 取回插件刷新后写回的值 | 存回 Keychain,不要落普通文件 |
| `Auth()` | 供应商 id、登录方式、插件选项 | `methods[].kind` 可能是 `device` 或 `key`,`prompts` 是要渲染的字段 |
| `Authorize(idx, inputsJSON)` | 开始一次登录 | `{id, immediate}`:`immediate` 非空就是已经拿到结果,别再轮询 |
| `Poll(id)` | 等用户完成 | 设备码用它;密钥登录不要调 |
| `CancelSignIn(id)` | 用户放弃 | id 不存在也算成功 |
| `Usage(provider)` | 用量窗口 | 归一后的形状;"供应商没说"是 `null` 不是 0 |
| `UsageSummary(provider)` | 一行摘要 | 用于列表,不用于详情 |
| `Models()` | 供应商的模型表 | 供应商拒绝时报错,不返回空表 |
| `Checkin(provider)` | 每日签到 | 不支持的供应商明确说,不假装成功 |
| `SetPluginOptions(optionsJSON)` | 插件自己的开关 | 键是 `provider\|option`,没动过的沿用插件默认 |
| `Call(export, argJSON)` | 直接跑一个导出 | 调试与特殊路径用 |
| `ReadPackageArchive(bytes)` | 解 zip / tar.gz | 客户端不必再写一个解包器 |
| `ChatStart/ChatNext/ChatCancel` | 对话三件套 | 拉取式;`Close` 会取消进行中的对话 |
| `Close()` | 释放实例 | 幂等 |

对话的交付方式不同,因为 gomobile 不能从 Go 反向推事件给 Swift:

```go
id := m.ChatStart("gpt-4o", messagesJSON, system, maxTokens) // 开始一轮
ev := m.ChatNext(id)   // 有事件就返回,没有是 "";结束时 {"kind":"end"}
m.ChatCancel(id)       // 用户离开,取消请求
```

**不阻塞**是刻意的:`ChatNext` 立刻返回,客户端在自己的循环里等一下再问。若它阻塞,
一次「没有新事件」的等待就会卡住一个 Go 调用,而取消只能等到下一次事件才生效。

请求仍走 `Host.HTTP`(与源请求同一条路径),但分区名是 `magpie` 且
`cookies=false`:AI 登录不读漫画源的 Cookie 罐,也不会把自己的会话写进去。

用假 Host 跑完整链路(安装 → 设备码 → 轮询 → 成功):

```
TestMagpieRunsAPackageThroughTheHost   provider/url/result 全对,分区为 magpie
TestMagpieReturnsTheSavedSignIn        刷新后的 access=tid=… 与 refresh 一并回传
TestMagpieRefusesBadInput              坏 JSON／坏 base64／缺导出／已关闭
```

Swift 侧还需实现 `URLSession` 版 `Host`(已存在)与 Keychain 存取,再把
`mobile.Magpie` 包成一个 `MagpieRunner`;那部分在 Nekularity 仓库里做。

## 当前流基础

`host.Transport.Do` 返回完整 `[]byte`,iOS `URLSessionTransport.send`
也等待完整响应;AI 流需要新增接口与 gomobile 事件桥接,不能复用为伪流。
`host/sse` 只读取客户端拥有的流,不联网、不解释供应商 JSON 或 `[DONE]`。
它处理分块、LF/CRLF/CR、多行 data 与持久 id,限制事件块字节数并传播
读取错误;未以空行结束的事件被丢弃。客户端负责取消/关闭/超时,上层协议
负责完成标记。EOF 不等于 AI 生成成功。
