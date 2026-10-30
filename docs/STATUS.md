# VeneraCat Core 状态

[English](STATUS.en.md) | 简体中文

更新:2026-10-30。以下结论来自 Windows 本机,以及 GitHub Actions(Linux、Windows、macOS 26 + Xcode 26.6)。

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
| `aiapi` | 内置标准接口的请求构造:OpenAI Chat/Responses、Anthropic Messages;原生正文、鉴权头、恒不启用 Cookie;只构造不执行 |
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

## 安全审查(2026-10-29)

独立审查发现 12 个问题(4 个高危、5 个中危、3 个低危),**全部已修复并补了回归测试**。最严重的问题是源之间可以互相读取 Cookie 和存储,已复现,并通过"每个源一个引擎"修复,见 [JS-ISOLATION.md](JS-ISOLATION.md)。

## 未验证 / 待办

1. **真机**:在 iPhone 真机上测启动时间和内存(CI 只能跑模拟器)。
2. **在真实 Dart VM 上核对哈希**:`print('wnacg'.hashCode)` 应输出 `823512256`。
3. 真实世界源无法进仓库(wnacg 等没有许可证),所以 CI 的漫画全链路用自己写的内联源(`mobile/mobile_test.go` 的 `comic`、`comic/js/async_test.go` 的 `miniSource`)跑;真实源兼容性由 Nekophage 的 `TestLiveSource` 在仓库外验证。
4. 视频下载与离线缓存、弹幕、Bangumi:未做(第二阶段)。
5. AniBaka `anx-rule/2` 流水线规则:未做(第二阶段)。
6. **把分段传输接进客户端**:Core 侧的分段桥与包桥都通了(死锁已修,见上),客户端还没有实现 `HostStream`,所以 iOS 与桌面端目前仍整包返回。下一步是让 iOS 与桌面各实现一次 `streamOpen`/`streamNext`/`streamClose`,并把 Core 的 pin 提到含这次修复的提交。
