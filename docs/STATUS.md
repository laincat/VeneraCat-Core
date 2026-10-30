# VeneraCat Core 状态

[English](STATUS.en.md) | 简体中文

更新:2026-11-03。以下结论来自 Windows 本机,以及 GitHub Actions(Linux、Windows、macOS 26 + Xcode 26.6)。

## 首块与预取消修复的跨平台验收(2026-11-03)

功能源码 `6129f59241160e0c3b14f0428d6bff414ed9d245` 的 CI `41631619233` 已完成且成功:Linux、Windows、macOS 三个 Go 作业的 vet/race/随机顺序测试通过,Apple xcframework 构建与 macOS/iOS 模拟器 Swift smoke 步骤均成功。证据来自 GitHub 作业和步骤状态,本轮没有下载或核验 SDK artifact,也不从步骤状态推算单例数量。回执 `E:/Temp/core-stream-release/apple-acceptance.json`,完整终态 `E:/Temp/nk-ci-final-review/summary.json`。

受测源码已整合进 Core main,随后按仓库约定保树收拢并通过 Git 发布;精确最终提交与 tree 对照记录于 `E:/Temp/core-stream-release/publish-receipt.json`。客户端 pin 独立推进,不把 Core 的跨平台通过当作 Swift 客户端、UI 或真机验收。

## 预先取消的 fetch 不再启动宿主请求(2026-11-03)

在首块交付候选上补出一个独立取消缺口:调用 fetch 前 AbortController 已取消,整包路径仍发起请求并成功返回;旧流式与提前响应头路径也进入了宿主。修复保留已取消的请求登记状态,并在整包/流式调用宿主前检查上下文。回归覆盖三种宿主:预取消请求均拒绝且宿主调用次数为 0,同一实例的下一条正常请求恰好执行一次并成功。修复前失败见 `E:/Temp/core-magpie-preabort/before.log`;定向 race、go vet 和全套 race 通过,22 包、565 PASS/0 FAIL/1 SKIP,跳过仍为未提供 VC_WNACG_JS 的 TestRealSourceWnacg。日志与 summary.json 在同目录。这是 Windows 本机伪宿主验证,不等于 Swift/UI 或真实供应商通过;该功能已纳入本次 Core main 整合,客户端 pin 尚待升级。

## Magpie 首块交付修复候选(2026-11-03)

独立分支 `fix/magpie-first-chunk` 基于已发布 `b55c1a4`,修复插件 fetch 等正文结束才返回的问题。可选 `HeaderStreamer.DoStreamWithHead` 在响应头到达时兑现 fetch;mobile HostStream 可在 StreamOpen、首个带头块或独立头块中提供 status/headers/finalUrl,无需新增 gomobile 方法。旧宿主只在结束时给头仍兼容,其 fetch 仍延后。取消监听保留至正文结束,reader.cancel 会关闭实际请求;分块直接传递 Uint8Array,避免跨块中文/emoji 被逐块转字符串而损坏。结束与失败会释放流登记。

修复前四组回归失败,包括首头/首块迟到、取消无法及时执行和跨块 UTF-8 乱码;修复后定向 race 与全套 22 包、564 PASS/0 FAIL/1 SKIP 通过,跳过为未设置 VC_WNACG_JS 的 TestRealSourceWnacg。真实 loopback 使用公开 mobile.Magpie、仓库供应商模板和 Go 客户端:在服务器扣住后续正文时 ChatNext 收到 first,取消三个阻塞阶段仍关闭对应连接,后续回答正常;同一脚本在 b55c1a4 上等待首段文字超时。日志 `E:/Temp/core-magpie-first-chunk-evidence/` 中的 `before.log`、`targeted.log`、`race-summary.json`、`loopback-old.log` 与 `loopback-fixed.log`。

这些本机证据现已有上述跨平台 CI 补验,两端 pin 尚未改变。当前 Swift CoreStreamHost 仍只在最终块交付响应头,需接入提前头部通知并经过 Mac 编译和真实 HTTP/UI 验收,不能据此声称 iOS 已流式。

## Magpie 聊天取消只停了句柄(2026-11-03)

`2c9c83b` 上的真实缺口:`Magpie.ChatCancel` 只删掉会话句柄并取消聊天自己的上下文,而 `Instance.Chat` 发请求时用的是实例上下文。因此取消之后,阻塞中的整包 `Host.HTTP`、`StreamOpen` 与 `StreamNext` 都不会停止。原有 `TestMagpieChatCancelEndsIt` 只检查句柄消失,证明不了请求被关。

隔离分支 `fix/magpie-chat-cancel` 先写公开入口的负向回归:`mobile/magpie_chat_cancel_test.go` 用 Core 自己的 `openai-compatible` 模板和真实 `mobile` API,让宿主阻塞在 HTTP 或 `StreamOpen`,确认修复前两条都收不到 `CancelHTTP`/`StreamClose`(`E:/Temp/nk-magpie-cancel-evidence/before.log`)。修复内容:

- 每个聊天使用独立运行时,`ChatCancel` 取消它的上下文并 `Close` 该实例,不再借用共享实例上下文。共享实例仍服务 `Models`、登录与用量。
- 刷新后的凭据写回 `Magpie.auth`,独立聊天的下一次 `auth.get`、后续聊天以及已存在的模型/登录钩子都能看到新值;运行时绑定的 provider 必须仍匹配。
- 实例创建改为不持锁构建,并在注册时复查 `closed` 与已有实例,并发创建时只保留一个共享实例,关闭后不再写入。`ChatStart` 先登记句柄再启动 goroutine,`Close` 能看见并停止它;写事件也响应取消,不再向无人读取的通道阻塞。

修复后五条定向 race 用例通过:`mobile/magpie_chat_lifecycle_test.go` 覆盖取消一个聊天不影响另一个、后续聊天继续、已存在的模型钩子复用刷新后的凭据(仅刷新一次),以及阻塞的 `StreamNext` 被关闭(日志 `focused-after.log`、`chat-lifecycle-fixed-host.log`)。这条覆盖整包、打开、读取三个阻塞阶段,并把刷新与隔离放在真实包模板上。补充用例初次失败是测试宿主按大小写精确匹配 `Authorization`,已按 HTTP 头语义修正;原失败日志 `chat-lifecycle.log` 保留。

全套 `go vet ./...` 与带真实包夹具的 `go test -race -count=1 -json ./...` 通过:22 个包,顶层 559 PASS / 0 FAIL / 1 SKIP(`race.log`、`race-summary.json`;SKIP 是未设置 `VC_WNACG_JS` 的 `TestRealSourceWnacg`,不是通过)。执行渠道是 Windows 假宿主与本地第三方包夹具,不是 iOS UI、真机或真实供应商;客户端 pin 未变。

## 取消修复的 CI、Apple SDK 与真实 loopback(2026-11-03)

`696a5049a372e22ca0e639c103258bf38cfffb55` 的 CI `41606605713` 四个 job 全部成功:Linux、Windows、macOS Go 检查及 Apple xcframework/Swift smoke。实际下载 `apple-build` artifact:72,763,815 字节,SHA-256 `2253fcac9be9c68859a022abee489b6ddef304f7a26a760cef8252cfeb53f593`,CRC、设备/模拟器/macOS 三类切片与 `chatCancel` 头文件均核对。产物内日志确认 macOS 与 iOS 模拟器 Swift smoke 各 8 例通过。回执 `E:/Temp/core-696-apple/receipt.json`,检查日志 `verify-artifact.log`。SDK 本身不嵌入 Git 提交号,归属依据 CI/artifact 的 HEAD 与下载摘要;初次要求二进制含 Git 哈希的断言不适用,失败日志保留。

Lead 使用公开 `mobile.Magpie` API、仓库自带 OpenAI-compatible 包、Go HTTP 客户端与真实 loopback 服务器补验。修复后等待响应头、等待后续分块、整包读取三种场景均由服务器观察到对应请求连接关闭;同一个 runner 随后完整返回 `first second`。旧 `2c9c83b` 对照在取消后的连接关闭等待中超时。日志 `E:/Temp/nk-magpie-loopback/cancel-fixed.log`、`cancel-old-negative.log`,临时运行代码 `main.go` 位于同目录。它验证 Go 客户端的 TCP 行为,不等于 Swift URLSession、Keychain、iOS UI 或真实供应商通过。

**b55c1a4 的历史增量交付限制**:原补验在服务器已发送首段但仍扣住后续正文时,等待 `ChatNext` 文本超时(`E:/Temp/nk-magpie-loopback/fixed.log`)。代码路径吻合这一现象:`hostFetchStream` 直到 `DoStream` 完成才 settle `fetch` 的 promise;传输层虽收到块,插件的 `await fetch` 仍未返回,因而公共聊天事件等正文结束后才交付。现有 HostStream 把响应头放在最终块,尚未提供可用的提前响应头通知。取消修复不解决此项;不得把传输分块或最终连续吐出的事件记为界面流式。后续增量交付需要同时调整传输头部时序和插件桥,这里保留明确限制。

## 已实现

| 包 | 内容 |
|---|---|
| `host` | 客户端接口:Transport、Verifier、Sniffer;URL 与请求头校验 |
| `attribution` | 源与插件的作者和主页(可选) |
| `challenge` | Cloudflare 检测(403/429/503 加特征,或 `cf-mitigated`)与 **MangaDex 式 edge 挑战**检测(`DetectEdge`);`DoneRule` 现在能表达「URL 带回指定查询参数」,因此自解式挑战也能判定完成,不再只能等超时;验证后用 WebView 的 User-Agent 重试,按分区隔离 |
| `comic/js` | MoeJS 运行时,**每个源一个独立引擎**;官方 sendMessage 全部方法;LoadSource/Call;搜索选项(`search.optionList`)、源设置声明(`settings`,select/switch/input/callback,支持 getter)与分类浏览(`explore`);VideoSource 插件 |
| `comic/js/init.js` | 按接口规格净室重写的官方前导库 |
| `comic/hostapi` | http/cookie 宿主接口,兼容 `CloudflareException:` 格式 |
| `comic/hostapi/convert` | utf8/gbk/base64/哈希/HMAC/AES(ECB/CBC/CFB-N/OFB-N)/RSA 解密 |
| `comic/html` | DOM 句柄协议(21 个函数) |
| `comic/image` | modifyImage 像素缓冲 |
| `comic/version` | 官方版本比较规则(含 beta/hotfix) |
| `backup` | 读取官方 `.venera` 备份、反查 Dart 类型哈希、可选择的恢复计划 |
| `subscription` | 漫画 `index.json`(官方 URL 规则)与视频 `vcvideo.json` |
| `video` | 视频数据模型;播放结果校验 |
| `video/kazumi` | Kazumi 规则引擎(XPath/API),86 条公开规则中 85 条可导入 |
| `video/builtin/hongguo` | 红果短剧网页路径(不含 App 签名、设备注册和 DRM 密钥) |
| `video/hls` | m3u8 解析、改写、去广告、AES-128 解密 |
| `plugin` | 统一插件注册表:漫画源、JS 视频源、Kazumi 规则、内置源 |
| `aiapi` | 内置标准接口的请求构造:OpenAI Chat/Responses、Anthropic Messages;原生正文、鉴权头、恒不启用 Cookie;只构造不执行。**流式回答的 SSE 解码**也在这里(见下) |
| `mobile` | gomobile 接口(JSON 进出)、宿主桥(HTTP、验证、存储、Cookie、界面请求、剪贴板)、panic 保护、取消传递 |
| `magpieplugin` 重试判决 | 一次失败的尝试要不要换个端点,由一个**带理由的判决**回答(`Decide(err, attempt) → {Retry, Reason}`),理由包括:调用方已经走了、读者已看到部分回答、请求本身被拒、密钥被拒、限流、上游不可用。`RouteError` 带上 `Stopped` 说明「为什么在还有端点时停下」。从前是「只要没出字就一律换下一个」:一个 400 会把每个端点都试一遍,每个都等一次超时。`Send` 在开第一条连接前也会检查 `ctx`,没人等的请求不开连接。| 13 个用例(状态分类表 + 400 只试一次 + 401 换下一个 + 取消不发起) |
| `magpieplugin` 结束原因归一 | 三种方言把「为什么结束」各说一套(Chat `stop`、Responses `completed`、Anthropic `end_turn`;截断是 `length`/`incomplete`/`max_tokens`),现在都归一到一套词表(`NormalizeFinish`),原始值仍保留。没见过的结尾原样透出而不是当成正常结束——把未知结局报成「说完了」是唯一确定错误的答案。顺带修掉一个真问题:Chat 的最后一段文字与结束原因同在一块时,结束原因会被丢掉,于是被 token 上限截断的回复看起来像正常说完。 | 3 组 20 例(词表 / 未知值 / 三方言流内判定截断) |
| `mobile` 事件带 `outcome` | 聊天事件 JSON 里原来只有 `finish`(厂商自己的说法)。现在多一个 `outcome`,是 Core 归一后的结局,客户端问「这条回复是不是被截断了」不必知道是哪个厂商答的;没有归一结果时不写这个字段——空字符串会被读成「正常说完」。 | 2 例(同时带两种说法 / 空值不写出) |
| `magpieplugin` 钉住 | 「选谁回答」和「重试能不能换人」分开:`Pin` 决定重试允许改变哪一部分——`PinModel` 只换账号不换模型(`PinAccount` 只换地址不换密钥,默认不限)。没有钉住时,一次为强模型规划的对话可能因为首个账号 401 而由小模型悄悄答完。钉住不会让重试消失,也不覆盖「请求本身被拒」的判定;被钉住挡下的端点会在错误里说明(`stopped by the pin`),否则「还有别的可用」和「别的根本没问」看起来一样。没有标注账号/模型的端点不会被丢掉——未知不等于不可用。 | 5 例(换账号不换模型 / 同密钥换地址 / 默认不变 / 无标签保留 / 钉住不覆盖 400) |
| `version` | 回答「这是哪一版 Core」:自身是主程序时报自己的提交(有改动标 `-dirty`),被别处链接时由链接方 `SetRelease` 写入戳记 |

## 已验证

- 所有包的 `gofmt`、`go vet`、`go test -race` 都通过。
- 真实数据:wnacg 漫画源离线全链路跑通;vensrc 主订阅、扩展订阅和官方 venera-configs 索引全部解析成功。
- 真实网络(2026-10-30,由 Nekophage 驱动):拷贝漫画与 MangaDex 两个真实源完成安装、搜索、详情、章节与取图;搜索选项按源的默认值传入,`search.load` 不再收到 `undefined`;分类浏览读取 `explore` 并在两个源上显示分区与列表。
- 备份还原的线格式由 `backup/contract_test.go` 钉住:两端客户端各用自己的模型解码 Core 的 `Plan`,字段改名不会在这里报错,却会让还原静默失败,所以键集(含「图片收藏没有顶层 `url`/`page`,页嵌在 `episodes[].imageFavorites[]`」)用测试固定。
- CI:所有包的 Go 测试在 Linux、Windows、macOS 上都通过。当前发布提交的四个 job 全绿:三个平台的 Go,加 Apple 的 xcframework + 两份 Swift 冒烟测试(含 iOS 模拟器)。具体 run 号故意不写在这里 —— 它每推一次就变,写成常量只会让它过期;看该提交的 Actions 页面即可。
- Apple(CI,macOS 26 arm64,Xcode 26.6):xcframework 编译成功(iOS 真机、iOS 模拟器、macOS 三个版本);Swift 冒烟测试在 macOS 和 iOS 模拟器上各 8 项全部通过,覆盖漫画源全链路、Cookie、界面请求、剪贴板、漫画源查询、搜索选项与源设置声明。
- 体积:iOS 真机二进制去符号后 20 MB,gzip 压缩后 7.2 MB;整个 xcframework 95 MB(含模拟器和 macOS 的双架构版本,不进 App)。
- 源返回的 `Map`(标签、章节)按插入顺序转成 JSON 对象,与官方应用一致;此前会被转成空对象 `{}`,章节列表因此丢失。整数键(如章节号 `10`、`2`)保持原顺序,不按数字重排。
- 速度:iOS 模拟器中搜索 1–7 毫秒,每个源安装 0.6–0.9 毫秒;Windows 上每个源引擎约占 112 KiB 内存。

### 设置回调的返回值(2026-11-01)

源的「设置回调」按钮(`type: "callback"`)是客户端唯一会代源执行的设置动作,九个官方源声明了它。两件事在这一轮被固定下来:

- **哪个客户端真的走这条路,已经量清(2026-11-01)**:`mobile.CallSettingCallback` 是给 iOS 的 gomobile 接口;桌面端**不走它**,它有自己的 `internal/core` 与同一段逻辑。做区分性实验确认过:把桌面自己的归一化去掉、换成新 pin 的 Core 后,回调仍然返回 `null` —— 也就是说这个修复只对 iOS 生效,桌面必须自己修(已修)。若只看单侧结果,很容易把「Core 已修」误当成「两端都好了」。
- **没有返回值的回调不再把 JSON 字面量交给客户端**。`CallSettingCallback` 原先把 `null` 原样透传,客户端于是把 `null` 当成源说的话显示给用户。现在 `null` 归一化为空串,客户端已有的「已完成 / 已执行」文案随之生效。`mobile/webview_login_test.go` 里加了一条只有副作用的回调用例;把归一化去掉,用例会报 `answered "null"`。
- **回调失败不再被超时掩盖(客户端侧)**:桌面端原先在源的 `onLoginSuccess` 抛错时继续轮询到 90 秒上限,用户看到的是 `context deadline exceeded` 而不是源自己写的原因;现在只有「页面已同意但 Cookie 尚未落地」才继续等。这条属于客户端行为,记在这里是因为它决定了 Core 的错误怎么被呈现。

真实源证据(桌面端 `scripts/callback-probe.sh`):`copy_manga`、`copy_manga_multi_accounts`、`happy`、`jm`、`mxs`、`wnacg` 六个源共 10 个回调全部真正执行并返回;`jm.dailyCheckIn` 在未登录时返回源自己的 `Please login to check-in`,属于正确结果而非缺陷。

### Magpie 供应商插件(2026-10-31)

- `gofmt` 干净,`go vet ./magpieplugin ./mobile` 通过,`go test -race ./magpieplugin ./mobile` 通过(无竞态)。
- 用**原始 Copilot 包**(`magpie-community/plugins@c590a74`)在假服务上跑通:模块定位(它只导出 `CopilotAuthPlugin`,没有 default)、设备码登录、token 刷新写回、用量、模型列表、流式回复、三种方言的事件归一、以及「已输出就不再重试」。
- `gobind -lang=objc` 直接生成成功,`MobileMagpie` 现在导出 18 个方法:`auth/authorize/poll/cancelSignIn/usage/usageSummary/models/chatStart/chatNext/chatCancel/loadPackage/readPackageArchive/setAuth/savedAuth/call/close/checkin/setPluginOptions` —— Swift 侧的适配器照这份头文件写,不靠猜。这 18 个方法另有一份 AST 测试钉住(`mobile/surface_test.go`):名字是跨语言契约,改一个名字在 Swift 那边不是编译错误,而是运行时的意外。
- 从**下载的压缩包**安装:zip 与 npm tar.gz 都支持,剥掉一层顶层目录,拒绝符号链接与目录穿越,限额与文件夹安装一致(`magpieplugin/archive.go`)。
- 社区仓库 36 个包做过逐个审计:真正能跑的供应商插件是**原始 Copilot 0.1.0 与 cloudflare-workers-ai 0.0.4**;四个需要 Node 内建模块或 CLI 的被明确拒绝,入口是 TypeScript 的(`search1api`)在 2026-10-31 改成了明确报「这是 TypeScript」。
- CI 跑 `go test -race ./...`,但**真实供应商包的用例在那里会被跳过**:原始 Copilot 包是第三方代码,没有随仓库分发,夹具缺失时用例 `t.Skip` 而不是失败。所以「用原始包跑过」这一条只在开发机上成立,CI 覆盖的是我们自己的包与假服务。
- 一个包能被加载不等于被兼容:Node 内置模块与 npm 依赖仍在 `Load` 阶段明确失败(`ErrUnsupportedModule`),不会退化成空桩。

### 分段传输(2026-11-01)

- 客户端可以只实现整包传输;要边收边用,再实现可选的 `HostStream`(`streamOpen`/`streamNext`/`streamClose`),然后用 `NewMagpieWithStream` 建实例。`NewMagpie` 的签名没有变,老客户端照旧编译。
- **不给老客户端加方法**:`mobileHost` 与 `mobileStreamHost` 是两个类型,只有后者带 `DoStream`。插件宿主是按类型断言选路的,如果所有宿主都带这个方法,不能流式的客户端会被要求流式,包里的 fetch 直接失败,而不是退回整包。反向用例 `TestPlainHostIsNotAStreamer` 与原有整包用例一起钉住这条。
- 请求 id 只有一处分配(`nextHostRequestID`),整包与分段共用同一计数,客户端的任务表不会因为两条路径各数一套而把取消打到别的请求上。
- 直接测过分段桥:分片按序到达、消费方提前停止会关闭请求、客户端读取失败会当作失败而不是"完整结束"、上下文取消会关闭请求(`mobile/stream_test.go`)。
- `gobind -lang=objc` 已生成 `MobileHostStream` 协议与 `MobileNewMagpieWithStream`,Swift 侧照头文件实现即可。- **协议相对 URL 的容错**:一个源有时会把图片地址写成 `//images.example.com/1.jpg`(少了 scheme)。RFC 3986 说这表示「继承页面的 scheme」,而读者里那永远是 https。Core 新增 `host.NormalizeURL` 与 gomobile 的 `Core.NormalizeURL`,两端用同一条规则补全。**这不放宽安全规则**:`file://`/`javascript:`/`ftp://` 一律不改写,补全后仍要过 `ValidateURL`;非协议相对的 URL 一个字都不动。反向用例 `TestNormalizeURLDoesNotWeakenValidateURL` 与 `TestNormalizeURLDoesNotRewriteUnsafeSchemes` 钉住这两点。


- **包桥死锁已修**:原始 Copilot 包走分段宿主时,包的 `__magpieChatPush` 原来是往 `io.Pipe` 同步写,而读取侧读到 `[DONE]` 就停止读 —— 包再推一段(尾部的 rest)时,写方永远等不到读者,它的 hook 不返回,整个实例挂死。改成 `replyBuffer`(带条件变量的缓冲):写永不阻塞读,读侧结束由写侧关闭触发。`TestMagpieChatUsesTheChunkedHost` 现在**真的通过**,不再是 skip。**反向验证过**:把缓冲换回阻塞的 pipe,同一条用例立刻 `panic: test timed out`。
- 缓冲本身另有 6 条用例:写不等待读者、文本按序到达、关闭后读到 EOF、错误原因会传出去、关闭后再写被拒、多写者并发下不丢不乱(`magpieplugin/reply_buffer_test.go`)。


### 图片配置的回调(2026-11-01)

- `comic.onImageLoad` / `onThumbnailLoad` 除了地址和请求头,还可以带三个**函数**:`onResponse`(把响应体解密或改写后再用)、`modifyImage`(像素缓冲脚本)、`onLoadFailed`(这次地址不行就换下一个镜像)。函数进不了 JSON,所以客户端以前**只能拿到前四个字段,三个回调全被丢掉** —— 结果是加密图的源(`cxc`、`mojoin`、`manwaba`)把密文当图片,而写了镜像回退的源(`4khd_cosplay`)把 HTML 错误页当图片。
- Core 新增 `comic/js.Runtime.ImageConfig/ApplyImageResponse/ImageFallback`:配置里的函数留在源的运行时里、由一个单调递增的句柄命名,宿主拿到 `handle` 后可以「把响应体交给 onResponse」「问下一个镜像」。**这是可选能力**:不认句柄的宿主继续只用 `url`/`method`/`headers`/`data`,行为一个字都不变。
- 句柄有上限(512 个),超了整批丢弃;句柄 id 只增不减,所以**丢弃的句柄不会复活成另一个配置的句柄**。形状不对的句柄在进运行时之前就被拒(`^i[0-9]{1,9}$`),不会被拼进 JS。
- 字节只在边界上过一趟:body 以 base64 进、以 base64 出,中间用运行时里的解码/编码,不来回搬运。
- gomobile 侧是 `Core.ImageConfig/ApplyImageResponse/ImageFallback`,body 走 base64(二进制可跨 gomobile),「源没这个回调」单独报 `ErrNoImageCallback`,调用方可以读成「保持原样」而不是失败。
- 测试:`comic/js/image_test.go` 5 条(字节变换、镜像回退、没有钩子的源、句柄上限、非字节返回值被拒),`mobile/image_callbacks_test.go` 1 条端到端。**客户端还没接**:桌面与 iOS 仍是「只取地址」的旧路径,等各自接上后才算这条链打通。

### modifyImage 脚本执行(2026-11-01)

- `comic.onImageLoad` 的第三个回调是 `modifyImage`:源给一段脚本,收一个 `Image` 返回另一个 `Image`,用来做「把长条图按块重排」(`jm` 就是这么干的)或旋转。之前宿主只能取到 `url`/`headers`,这段脚本被丢掉 —— 结果是这些源在官方客户端是 N 段排版,在桌面端是一张原始长图。
- Core 新增 `comic/js.Runtime.ModifyImage(ctx, script, bitmap)`:把解码后的 RGBA 放进一次性的位图仓库,在**独立运行时**里跑脚本(脚本自己声明 `modifyImage`,共用源的运行时会让两页互相覆盖同名全局),再取回结果位图。**解码与编码仍然留给客户端**:Core 只搬 RGBA,和 `comic/image` 包的分工一致。
- 拒绝的三种情况都有用例:空脚本、脚本没声明 `modifyImage`、脚本返回的不是图像;尺寸与像素长度不一致的输入在进脚本之前就被挡。
- 测试 4 条:顺时针旋转(逐像素核对)、按块重排(照 `jm` 的写法)、三类拒绝、连续两次调用互不影响(证明每次是新的运行时)。

- gomobile 侧是 `Core.ModifyImage(key, script, width, height, rgbaB64)` → `{"width","height","rgba"}`:像素以 base64 过边界(二进制可跨 gomobile),解码与编码由调用方做,「脚本没给出图像」单独报 `ErrModifyImage`,与「脚本本身抛错」区分开。

## macOS 全套包测试(2026-11-03,提交 `44f1952`)

同一提交在 macOS `darwin/arm64`、`go1.27.3`、`CGO_ENABLED=1` 上执行:`go vet ./...`、`gofmt -l .`、`./scripts/check-docs.sh` 均通过;`go test -race -v ./comic/js ./magpieplugin ./mobile` 退出 0,顶层 **249 PASS / 0 FAIL / 31 SKIP**。日志在 `/tmp/venera-core-44f1952-evidence/`(`vet.log`、`race.log`、`test-summary.log`、`gofmt.log`、`docs.log`)。

关键覆盖(均为假传输/假宿主):Promise 与多次 `await`、超时中断后可恢复、调用开始前取消、关闭会话取消进行中与后续调用、宿主 panic/取消/取消传到传输层;跨源隔离、备份 Cookie 分区命名、漫画与 Magpie 请求使用不同宿主 ID;Magpie 的 fetch 解析、AbortSignal 取消、分块流式到达、流失败上报、顺序与消费端停止/客户端失败/取消;mobile 的插件生命周期、设置/搜索选项/浏览页/网页登录 JSON、`9007199254740993` 精确字节不经过浮点舍入、取消到达同一宿主请求。新增的两条兼容边界用例(`TestCompatibilityReadOnlyAtob`、`TestCompatibilityCallRegisterWindow`)也在其中。

31 项 SKIP 不是通过:缺少审计插件夹具目录,以及 `VC_WNACG_JS`、`MAGPIE_VENDOR_DIR` 未设置;对应真实 Copilot、Cloudflare 与社区插件夹具。审计目录此前写死成 Windows 临时路径,导致这些用例在 macOS 上毫无提示地跳过;现在改由 `MAGPIE_AUDIT_DIR` 指定,未设置时明确跳过。2026-11-03 在 Windows 上把两个变量都指向真实夹具后复跑(`magpieplugin` + `mobile`):**224 PASS / 0 FAIL / 0 SKIP**,日志 `E:/Temp/core-full-with-fixtures.log`。

本地另一轮:`go test -race ./...` 在 22 个包上全部通过(带 `MAGPIE_AUDIT_DIR` 与 `MAGPIE_VENDOR_DIR`),日志 `E:/Temp/core-race-all-jsname.log`;这是 `b2610a5` 改动之后的整仓竞态检查。

`4d4b8fb`(含 `b2610a5` 的异步错误名修复)CI 四作业全绿:三个 Go 平台与 `Apple (xcframework + Swift smoke test)`。Apple 作业第一次在队列里等了 15 分钟后被取消(没有任何步骤运行),`gh run rerun --job` 重跑后通过——这种取消是排队问题,不是编译失败,遇到时先重跑再判断。

CI 在 `63787bc` 上四个作业全绿:`Go (windows-latest)`、`Go (macos-26)`、`Go (ubuntu-latest)` 与 `Apple (xcframework + Swift smoke test)`(run `41507032435`)。上一提交 `8e301cf` 的 macOS 作业显示为 cancelled,是被紧随其后的推送按并发策略取消,不是失败——Windows 与 Linux 当时都通过,Apple 作业因此跳过;这一点写在这里,免得把「被取消」当成红。

2026-11-03 在 `6a66759` 上又跑了一遍:不带夹具变量 `go test ./...` 共 22 个包全过(相关用例按设计跳过);带上 `MAGPIE_AUDIT_DIR` 与 `MAGPIE_VENDOR_DIR` 时 `magpieplugin`、`mobile`、`comic/js` 三个包也全过,日志 `E:/Temp/core-go-test-all-6a66759.log`。

同一轮补齐了 `MAGPIE_VENDOR_DIR` 的最后一处 SKIP:用 `npm pack` 取回四个已发布的第三方包(factory `0.1.41`、grok `0.2.3`、qoder `0.2.54`、workbuddy `0.2.14`)并解包后,`TestVendorPackagesAreRefused` 按预期拒绝它们,错误文本各自点名 `node:http` / `node:crypto` / `node:child_process` 且说明无法解析。此前该测试按目录名匹配 `-factory-auth`,而文档给出的解包方式目录就叫 `factory`,配方本身跑不通;现在改为读包内 `package.json` 的 `name` 字段匹配。日志 `E:/Temp/core-vendor-refusal.log`。这些结果只覆盖假传输与 loopback,不代表真实站点、真实账号或设备闭环;客户端 pin 未变。

## 已复现的 MoeJS 兼容边界(2026-11-02)

通过 Core 公开 `Runtime.Eval` 入口复现当前固定引擎 `0a0ed36ae7db` 的两类限制,并加入 `comic/js/compatibility_limits_test.go` 的两条测试。两条测试均在 Windows 的 `go test -race` 中通过,日志 `E:/Temp/core-compatibility-limits-tests.log`;独立复现输出在 `E:/Temp/core-compatibility-limits-repro.log`。

- 全局 `atob` 的属性不可写、不可配置。严格模式赋值抛出 `TypeError`;非严格赋值被忽略,随后 `atob("QQ==")` 仍返回 `A`,不能以“不抛错”判断自定义解码器安装成功。函数局部的同名解码器可用,且不改变全局解码器。
- 最小调用表达式接受 254 个字面量实参;255 个触发 `function needs more than 256 registers`,256 个触发 `too many arguments`。这是单函数 256 个编译寄存器的窗口,包含 callee/this 和其他局部值,不能承诺所有上下文都支持 254 个实参。用动态数组展开传入 1000 个实参通过,且前面的编译错误没有破坏下一次调用。
- 源作用域原先**没有 `URL`/`URLSearchParams`**,而官方源 `manhuaren.js` 就用 `new URL(imageUrl).host` 拼 Referer(抛错时退回正则)。现在 Core 的 prelude 提供这两个类,覆盖源实际用到的部分:绝对地址(协议/主机/端口/路径/查询/片段/来源)、相对地址按 base 解析、`searchParams` 的 `get`/`getAll`/`append`/`set`/`delete`/`sort`/`toString`/`forEach`、`URL.canParse`,非法输入抛 `TypeError: Invalid URL`。`search` 与 `searchParams` 是联动的一体(`searchParams.set` 后 `u.search` 就是新查询,反之亦然),`TestURLPolyfillCoversWhatSourcesUse` 与 `TestURLSearchAndSearchParamsAgree` 钉住这些形状。明确不实现的部分:百分号转义的规范化、全部点段边界、IDNA——需要这些的源要自己处理。
- `TextDecoder("gbk")` 不可用:引擎的 `TextDecoder` 只认 UTF-8 一族,`gbk` 会抛 `RangeError: The "gbk" encoding is not supported`。需要 GBK 的源应走源 API 自己的 `sendMessage` → `method: "convert"` 路径(Go 侧 `simplifiedchinese.GBK`),不要在 JS 里找浏览器解码器。`TestCompatibilityTextDecoderIsUTF8Only` 除确认 UTF-8 可用、GBK 被拒外,已实际验证 `Convert.decodeGbk(new Uint8Array([214,208,206,196]))` 得到“中文”,普通数组也可解码。`Convert.encodeGbk("中文")` 返回 ArrayBuffer,用 `new Uint8Array(...)` 读取为相同四字节;不可编码的“🐾”返回 null,之后仍可继续解码。此证据基于 3be57b5,只加强测试,未修改转换实现;后续用Dart实际对照enough_convert 1.6.0/1.7.1,确认非法字节消耗、单字节欧元及私用区映射存在差异,详见 [GBK兼容边界](GBK-COMPATIBILITY.md);没有执行官方App或确认其配置。此外,`TestHTTPThroughJS` 使用本地伪 Transport 返回 GBK 原始字节,经 `Network.fetchBytes` 到 `Convert.decodeGbk` 得到“中文”,覆盖 HTTP 响应到脚本的完整桥接。日志 `E:/Temp/core-gbk-evidence/bridge-corrected.log`、`E:/Temp/core-gbk-evidence/http-gbk.log`。

同轮修了一处错误文本:promise reject 是以值的形式送回来的,`jsError` 原先只取 `message`,把对象上的 `name` 丢了——所以异步 `init()` 里的类型错误显示成 `Cannot read properties of null (reading 'length')`,而同步抛错却是 `TypeError: ...`。现在对象名会补回到消息前(名为 `Error` 时不加,抛字符串时也不加),日志 `E:/Temp/core-desc-probe.log`;新增用例 `TestAsyncRejectionsKeepTheErrorName` 覆盖异步类型错误、字符串抛出与同步抛出三种情况,`go vet ./...` 与带夹具的 `go test ./...` 22 个包全过。

本轮选择明确记录并保留固定引擎的行为。源脚本应使用局部/自有解码函数,大字面量调用改为数组展开或拆分函数;Core 不自动重写脚本。没有修改生产代码、引擎依赖或两端 pin。这些用例只说明上述边界,不代表所有真实站点方法已验证。

## 官方脚本加载巡检(2026-11-02)

用 `comic/js` 的公开入口跑一遍官方 v2 脚本,确认引擎在加载期没有兼容缺口。探针在仓库外(`E:/Temp/gosrcprobe`,以 `replace` 指向本仓库),不是测试用例;脚本来自 `E:/Temp/nkprobe/official-srcs` 的 34 份。

结果:提供存储 + Cookie 存储 + 传输后,**32/34 完整加载**(记录 key、kind、version、minAppVersion);其余两份失败的原因是探针给的固定响应体不匹配它们的 `init()`:`comic_walker` 的 `init()` 解析 token JSON,`jm` 的 `init()` 解析页面 HTML。同一轮里 `shonen_jump_plus` 正是靠探针返回的 HTML 才通过,说明这是夹具问题,不是引擎问题。

2026-11-03 复跑时把探针从「固定响应体」改成「按请求给形状」:iTunes 查询返回 JSON,`jm` 的设置端点返回 JSON,其余仍返回 HTML。这一轮 **33/34 完整加载**,`comic_walker` 通过(key `comic_walker`、version `1.0.1`、minAppVersion `1.6.0`);`jm` 仍失败,原因是它的 `init()` 调用自己的设置端点,并用「请求时间 + 固定密钥」派生的 AES 密钥解密响应体——离线桩无法预知那一秒的时间戳,因此它在无网络时本来就无法完成 `init()`,不是引擎缺口。失败以 JS 异常形式上抛(`js: jm init failed: ...`),说明加载期的错误传播链路正常。日志:`E:/Temp/core-srcs-sweep-34.log`。

因此本轮**没有发现新的 JS 兼容缺口**:类字段、getter、`randomInt`、`loadData`、`init()` 内的 `Network.get` 都正常。限制:只覆盖加载期,不覆盖各方法的真实网络行为;`jm` 的 `init()` 需要真实站点响应,未在联网环境复验;register 窗口与冻结 `atob` 的专门用例已在上一节补齐,仍不属于这次源加载巡检的覆盖范围。

## 安全审查(2026-10-29)

独立审查发现 12 个问题(4 个高危、5 个中危、3 个低危),**全部已修复并补了回归测试**。最严重的问题是源之间可以互相读取 Cookie 和存储,已复现,并通过"每个源一个引擎"修复,见 [JS-ISOLATION.md](JS-ISOLATION.md)。

## 未验证 / 待办

1. **真机**:在 iPhone 真机上测启动时间和内存(CI 只能跑模拟器)。
2. **在真实 Dart VM 上核对哈希**:`print('wnacg'.hashCode)` 应输出 `823512256`。
3. 真实世界源无法进仓库(wnacg 等没有许可证),所以 CI 的漫画全链路用自己写的内联源(`mobile/mobile_test.go` 的 `comic`、`comic/js/async_test.go` 的 `miniSource`)跑;真实源兼容性由 Nekophage 的 `TestLiveSource` 在仓库外验证。
4. 视频下载与离线缓存、弹幕、Bangumi:未做(第二阶段)。
5. AniBaka `anx-rule/2` 流水线规则:未做(第二阶段)。
6. **把分段传输接进客户端**:Core 侧的分段桥、包桥与标准接口的流式解码都已就位;iOS 已实现 `CoreStreamHost` 并用 `NewMagpieWithStream` 建 Magpie 实例,但主 `Core` 仍用 `New` 建,标准接口页面也还没接 `SendAIRequestStream`,所以标准接口目前仍整包返回。桌面端仍未实现 `HostStream`。

## 标准流式响应的完成条件与打开阶段取消(2026-11-03)

分支 `fix/ai-stream-completion` 修复了两个已复现的缺口。旧 `SendAIRequestStream` 未检查 HTTP 状态与协议结束:Chat/Responses/Messages 的部分文本在连接关闭后可被当作完整回答,HTTP 403/503 也可能成为空回答成功。新增八个子用例中,六个错误场景修复前如期失败,两个完整协议回答通过,日志 `E:/Temp/core-ai-stream-before.log`。现在只有 HTTP 2xx 且收到协议结束标记才返回成功;未收完的最后一个事件不能充当结束。请求同时声明 `Accept: text/event-stream`。

原分段桥只在 `StreamOpen` 返回后注册取消回调,等待响应头时仍可能卡住。新增阻塞宿主用例先复现失败(`E:/Temp/core-ai-stream-open-before.log`),随后把该阶段的取消接到请求 ID 对应的 `StreamClose`,并在离开打开阶段前收回其回调。重复关闭必须安全,客户端须能关闭请求 JSON 中的 ID 并唤醒等待;测试仅在 ID 匹配时释放阻塞。后续 `StreamNext` 仍用实际返回的流 ID。

修复后 `go vet ./...` 与 `go test -race -count=1 ./...` 全部通过(22 个包),文档配对与英文错误文本检查通过。完整日志 `E:/Temp/core-ai-stream-vet.log`、`E:/Temp/core-ai-stream-race.log`、`E:/Temp/core-ai-stream-docs.log`、`E:/Temp/core-ai-stream-errors.log`。

该修复已快进合入 main,提交 `b5312596045caa8aa4cd8837b8f9ece84257bc4c`。CI `41589616003` 的 Ubuntu、Windows 与 macOS 26 三个 Go job 均成功(`gofmt`/`vet`/`test`/打乱顺序的 `test`/`docs`/英文错误文本);Ubuntu 与 macOS 跑全部检查,Windows 上 `gofmt` 与 `docs` 按配置跳过,属于既有平台差异而非本轮改动。

这是伪宿主的协议与生命周期回归,不证明真实供应商、网络首块时序或流式界面。该分支尚未整合到 main 或客户端 pin,仍需后续 SDK 与消费端验证。

## 标准接口的流式解码(2026-11-03)

`aiapi` 新增 SSE 解码器与 `RequireStream`,mobile 侧新增 `NewWithStream` 与 `SendAIRequestStream`:标准协议的回答现在可以边收边解析成文本,不再只有「整包」一种口径。

- **事件形状不同,边界相同**:三种协议的 SSE 都是「`data:` 行 + 空行收尾」,但 `chat` 的文本在 `choices[0].delta.content`,`responses` 在 `output_text.delta` 事件里,`messages` 在 `content_block_delta` 的 `text_delta` 里。解码器只取文本增量;此接口仅返回汇总文本,不返回工具调用、推理块、用量或原始事件,也不能向界面逐块推送。
- **分块不是事件**:一次读取可能只拿到半行,也可能一次拿到多个事件。解码器按空行收尾事件、跨块缓冲,超过 1 MiB 仍未收尾就报错,避免一个不发空行的供应商把内存吃光。
- **结束与错误分开**:`[DONE]`、`response.completed`、`message_stop` 标记结束;供应商的错误事件(`{"error":{...}}`)直接作为错误抛出,不会被当成「答完了但没字」。
- **调用方错误要说清楚**:正文缺 `"stream":true` 时直接报错——供应商没被要求流式时只回一个 JSON 对象,解析结果是空文本,看起来像供应商沉默。用 `New`(而非 `NewWithStream`)建出来的 Core 会报 `cannot stream`,而不是假装流式。
- **分区不串**:标准接口的流式请求走自己的 `partition`(默认空),不会借漫画源的 Cookie 罐;插件流式仍固定 `magpie` 分区。两者共用同一个分段桥,但分区是参数而不是常量。

覆盖(全部为假宿主/假传输):`aiapi/stream_test.go` 9 例(chat 增量、跨块切分、responses 事件、anthropic 文本与工具增量区分、供应商错误、坏事件、未知协议、`RequireStream`),`mobile/aiapi_stream_test.go` 7 例(端到端增量拼接、正文缺 `stream:true` 且在开连接前拒绝、非流式 Core 明确报错、供应商错误与传输失败、取消后不再返回答案、空回答仍是合法 JSON)。`go test ./...` 全通过,`go test -race ./aiapi/ ./mobile/` 通过,日志 `E:/Temp/core-stream-full.log`、`E:/Temp/core-stream-race.log`。

**这仍不是流式界面**:iOS 标准接口页面尚未接入,首次增量到达的界面行为、真实供应商的分块节奏都还没有证据;真实账号调用仍未做。

## URL 主机名大小写(2026-11-03)

把 `5bd3b85` 这个 pin 接进 Nekophage 后,用一条源脚本实测 `new URL(...).host` 得到 `Images.Example.Test:8443`,而按 URL 标准特殊 scheme 的主机应是小写。源若拿它和 `example.test` 比较就会失败,错误看起来像源写错。现在 `http`/`https`/`ws`/`wss`/`ftp` 的主机名在构造时转小写,`host`/`origin`/`href` 随之一致;非特殊 scheme 保持原样。`TestURLPolyfillCoversWhatSourcesUse` 增加大小写主机与非特殊 scheme 用例。

`go vet ./...`、`go test -race -count=1 ./...`(22 个包)与 `scripts/check-docs.sh` 通过,日志 `E:/Temp/core-url-host-vet.log`、`E:/Temp/core-url-host-test.log`、`E:/Temp/core-url-host-docs.log`。官方脚本巡检也在 `4f213ff` 上重跑:**34 个脚本 / 33 加载成功 / 1 失败**,仍是 `jm` 需要真实 `init` 响应(`TypeError: Cannot read properties of null (reading 'length')`),与主机名改动无关,日志 `E:/Temp/core-sweep-4f213ff.log`。这是 Core 生产行为变化,两端 pin 随构建升到含此修复的提交。

## URL 补齐后重跑官方脚本巡检(2026-11-03)

`5cfdef2`(prelude 提供 `URL`/`URLSearchParams`)之后重跑同一份巡检:**34 个脚本 / 33 个加载成功 / 1 个失败**,仍是 `jm`,失败原因也仍是它的 `init()` 需要真实站点响应(`TypeError: Cannot read properties of null (reading 'length')`)。这条错误现在带 `TypeError:` 前缀——正是 `b2610a5` 想在真实路径上看到的效果,不是新问题。日志 `E:/Temp/core-sweep-url.log`。

## pin 维护结果(2026-11-03)

两端都已经从 `214e7782` 升到 `8e14aad`:`b2610a5` 的「待升级」到此关闭。

- **Nekularity**:随 `0.3.0`(build 12)发布,tag `v0.3.0`;解包 IPA 后能在应用二进制里读到 `8e14aade75f563a95f50d4ec80be27bae0d02c63`,CI `41525488814` 的 411 例全过。
- **Nekophage**:本地重打并复验——`go test -race ./...` **16 个包**全过、`scripts/check.sh` 通过、窗口套件 **14 PASS / 0 FAIL / 0 SKIP**,`-version` 自报同一 pin。

两边升 pin 时都先确认了 `214e7782..8e14aad` 之间只有 `comic/js/engine.go` 一个生产文件改动、`apple/` 下没有 Swift 表面变化,所以差异只体现在错误文案。下一次出现 Core 生产改动时,仍按「随下一次客户端构建升 pin」的规则走。

## 客户端 pin 的判定依据(2026-11-03)

两端客户端仍钉在 `214e7782`,HEAD 已经是 `8e301cf`。这不代表“忘记升级”:`git diff --stat 214e7782..HEAD` 显示改动只有 16 个文件、415 行,其中**非测试、非文档的只有两份 README**(共 10 行);生产代码、`go.mod` 与引擎依赖都没有动。Go 编译器不读 `_test.go` 和 Markdown,所以从 `214e7782` 重建的库产物与 HEAD 一致,提前升 pin 只会让两端各自重跑一遍构建而不改变行为。

升 pin 原本写成「出现任何非 `_test.go`、非 `docs/`、非 `README*` 的改动就两端一起升」。`b2610a5` 之后这条规则需要说准:那次改了 `comic/js/engine.go`,让 promise reject 保留 `TypeError:` 之类的名字,属于生产代码,但**不改变 gomobile 接口形状**,只改善错误文本。升 pin 要求两端各自重建 Core 产物(Nekularity 还要重建 xcframework),代价与收益不成比例,因此两端继续钉在 `214e7782`,这次改动**记为待升级**:下一次任一客户端产出新版本(需要重建 Core)时一并升 pin 并跑完整测试。规则据此改为:生产代码改动在**下一次客户端构建**时随版本一起升 pin;只有改动影响客户端依赖的接口或行为时,才立即升并当轮复验。客户端侧的 `scripts/check-core-pin.sh` 仍只保证 pin 与产物一致,不判断是否该升。

