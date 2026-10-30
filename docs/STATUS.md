# VeneraCat Core 状态

[English](STATUS.en.md) | 简体中文

更新:2026-10-30。以下结论来自 Windows 本机,以及 GitHub Actions(Linux、Windows、macOS 26 + Xcode 26.6)。

## 已实现

| 包 | 内容 |
|---|---|
| `host` | 客户端接口:Transport、Verifier、Sniffer;URL 与请求头校验 |
| `attribution` | 源与插件的作者和主页(可选) |
| `challenge` | Cloudflare 检测(403/429/503 加特征,或 `cf-mitigated`),验证后用 WebView 的 User-Agent 重试,按分区隔离 |
| `comic/js` | MoeJS 运行时,**每个源一个独立引擎**;官方 sendMessage 全部方法;LoadSource/Call;搜索选项(`search.optionList`);VideoSource 插件 |
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
| `mobile` | gomobile 接口(JSON 进出)、宿主桥(HTTP、验证、存储、Cookie、界面请求、剪贴板)、panic 保护、取消传递 |

## 已验证

- 所有包的 `gofmt`、`go vet`、`go test -race` 都通过。
- 真实数据:wnacg 漫画源离线全链路跑通;vensrc 主订阅、扩展订阅和官方 venera-configs 索引全部解析成功。
- 真实网络(2026-10-30,由 Nekophage 驱动):拷贝漫画与 MangaDex 两个真实源完成安装、搜索、详情、章节与取图;搜索选项按源的默认值传入,`search.load` 不再收到 `undefined`。
- CI:所有包的 Go 测试在 Linux、Windows、macOS 上都通过。
- Apple(CI,macOS 26 arm64,Xcode 26.6):xcframework 编译成功(iOS 真机、iOS 模拟器、macOS 三个版本);Swift 冒烟测试在 macOS 和 iOS 模拟器上各 7 项全部通过,覆盖漫画源全链路、Cookie、界面请求、剪贴板、漫画源查询与搜索选项。
- 体积:iOS 真机二进制去符号后 20 MB,gzip 压缩后 7.2 MB;整个 xcframework 95 MB(含模拟器和 macOS 的双架构版本,不进 App)。
- 源返回的 `Map`(标签、章节)按插入顺序转成 JSON 对象,与官方应用一致;此前会被转成空对象 `{}`,章节列表因此丢失。整数键(如章节号 `10`、`2`)保持原顺序,不按数字重排。
- 速度:iOS 模拟器中搜索 1–7 毫秒,每个源安装 0.6–0.9 毫秒;Windows 上每个源引擎约占 112 KiB 内存。

## 安全审查(2026-10-29)

独立审查发现 12 个问题(4 个高危、5 个中危、3 个低危),**全部已修复并补了回归测试**。最严重的问题是源之间可以互相读取 Cookie 和存储,已复现,并通过"每个源一个引擎"修复,见 [JS-ISOLATION.md](JS-ISOLATION.md)。

## 未验证 / 待办

1. **真机**:在 iPhone 真机上测启动时间和内存(CI 只能跑模拟器)。
2. **在真实 Dart VM 上核对哈希**:`print('wnacg'.hashCode)` 应输出 `823512256`。
3. wnacg 源没有许可证,不能放进仓库;CI 需要一个自有的测试源。
4. 视频下载与离线缓存、弹幕、Bangumi:未做(第二阶段)。
5. AniBaka `anx-rule/2` 流水线规则:未做(第二阶段)。
