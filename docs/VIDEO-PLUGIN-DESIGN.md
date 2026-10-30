# 插件与视频源

[English](VIDEO-PLUGIN-DESIGN.en.md) | 简体中文

更新:2026-10-30。本文描述 Core 当前实现的插件类型、视频源写法和订阅格式。网络、Cookie、WebView 和播放器由客户端提供。

## 1. 范围

Core 必须兼容官方的两样东西:

1. **官方备份**(`.venera`):读入、预览、恢复;无法表达的数据原样保留并列出。见 [VENERA-BACKUP.md](VENERA-BACKUP.md)。
2. **漫画源与漫画订阅**:legacy `ComicSource` 脚本(接口见 [SENDMESSAGE-SPEC.md](SENDMESSAGE-SPEC.md))和订阅索引 `index.json`。索引是一个 JSON 数组,每项为 `{name, key, version, fileName, url, minAppVersion, description}`,与官方 venera-configs 和 laincat/vensrc 一致。

在此基础上增加:插件化,以及视频源。

## 2. 调研的视频 App(来自 laincat/AltStore)

| App | 许可证 | 源格式 | 视频地址怎么拿 | 对本项目 |
|---|---|---|---|---|
| Kazumi | GPL-3.0 | **声明式 JSON 规则**(XPath 或 API/JSONPath),不执行 JS | 多数靠 **WebView 嗅探** m3u8/mp4 | 规则仓库 `Predidit/KazumiRules` 是 **MIT**,可直接导入 |
| AniBaka | GPL-3.0 | `anx-rule/2`:按 search/detail/play 三阶段的**操作流水线**(fetch、select、regex、json、crypto、sniff 等 30 余种操作) | 流水线 `videoUrl`,或用 `sniff` 回退到 WebView;另有 HLS 改写和去广告 | 能力最完整,可作为声明式规则的参考 |
| AnimeFlow | AGPL-3.0 | Kazumi 规则的变体(字段基本一致) | WebView | 只参考,不复制代码 |
| Animeko | AGPL-3.0 | Selector 源(CSS 选择器加正则匹配视频地址),有订阅仓库 | WebView 嗅探加正则 | 只参考,不复制代码 |
| AniCh | **无许可证** | — | — | **不得复制任何代码** |
| 真果鉴 guoapp | GPL-3.0 | **Go 原生 provider**,编译进 App,不是运行期脚本 | Go 内置 HLS 代理 | 证明了 Go `c-archive` 静态链接进 iOS 可行,并在 Go 里做 HLS 改写 |

结论:

- 视频源**几乎都需要 WebView 嗅探**作为兜底:页面里的播放器经常把真实地址藏在混淆过的 JS 里。
- 第二个共性是 **HLS 处理**:注入 Referer 和 Cookie、过滤插播广告、去掉分片前缀、解码加密的播放列表。
- 规则生态中最大的是 Kazumi(MIT 规则库),值得直接兼容。

## 3. 插件类型

所有插件共用一个 key 命名空间(`^[a-zA-Z0-9_]+$`),由 `plugin` 包统一管理安装、更新、启用、停用和恢复。

| 类型 | 入口 | 执行方式 | 来源 |
|---|---|---|---|
| `comic` | `*.js`,`class X extends ComicSource` | MoeJS,每个源一个引擎 | 漫画 `index.json`、文件、备份 |
| `video-js` | `*.js`,`class X extends VideoSource` | MoeJS,每个源一个引擎 | `vcvideo.json`、文件 |
| `kazumi-rule` | Kazumi 规则 `*.json` | Core 内置规则引擎(XPath/JSONPath) | `vcvideo.json`、文件 |
| `builtin` | 编译进 Core | Go | 用户在应用内启用 |

主题、布局、功能模块、弹幕和 Bangumi 同步属于后续阶段,尚未定义类型。

## 4. 视频订阅 `vcvideo.json`

视频源**单独使用一份索引**,不与漫画 `index.json` 混排,因此只认识漫画的客户端永远不会把视频插件当成漫画源导入。

```json
{
  "$schema": "vcvideo/1",
  "name": "示例视频订阅",
  "author": {"name": "Maintainer", "url": "https://github.com/maintainer"},
  "homepage": "https://github.com/maintainer/vcvideo",
  "plugins": [
    {"type": "js", "key": "demo_anime", "name": "示例番剧站", "version": "0.1.0",
     "url": "demo_anime.js", "sha256": "…64 位小写十六进制…", "minCoreVersion": "1.0.0"},
    {"type": "kazumi-rule", "key": "kz_AGE", "name": "AGE", "version": "1.6",
     "url": "https://raw.githubusercontent.com/Predidit/KazumiRules/main/AGE.json"}
  ]
}
```

规则(见 `subscription/vcvideo.go`):

- `$schema` 必须是字符串 `vcvideo/1`,`name` 必填。`$schema` 是固定标识而不是网址,便于在 GitHub 上用代码搜索找到第三方订阅;发布订阅的仓库还应添加 topic `vcvideo`。
- `plugins[].type` 只能是 `js`(安装为 `video-js`)或 `kazumi-rule`;其他值会使整份索引无效。
- `key`、`name`、`version`、`url` 必填。`url` 可以是相对地址,按索引地址解析;只接受 http(s),不能带账号密码。
- `sha256` 可选;提供时客户端下载的文件必须匹配。
- 安装时脚本声明的 key 必须与条目的 `key` 一致。
- 最多 2000 个插件;有任何条目不合法,整份索引无效。

## 5. 视频源 JS 写法(对照漫画源)

沿用同一套全局:`sendMessage`、`Network`、`fetch`、`Convert`、`HtmlDocument`、`UI`、`APP`、`log`。新增 `VideoSource` 基类和三个数据形状 `Video`、`VideoDetails`、`PlayInfo`(见 `comic/js/video-prelude.js`)。

```js
class DemoAnime extends VideoSource {
  name = "示例番剧站"
  key = "demo_anime"          // ^[a-zA-Z0-9_]+$,与漫画源同规则
  version = "0.1.0"
  minCoreVersion = "1.0.0"
  url = "https://…/demo_anime.js"   // 更新地址

  search = {
    load: async (keyword, options, page) => ({
      videos: [new Video({id, title, cover, subtitle, tags})],
      maxPage: 3,
    }),
    // 或 loadNext(keyword, options, next) => {videos, next},与漫画源一致
  }

  video = {
    // 详情:多条线路,每条线路一组剧集
    loadInfo: async (id) => new VideoDetails({
      title, cover, description, tags,
      lines: [{id: "line1", name: "线路一", episodes: [{id: "ep1", title: "第 1 集"}]}],
    }),

    // 播放:返回可直接播放的地址,拿不到时交给宿主嗅探
    loadPlay: async (id, lineId, episodeId) => new PlayInfo({
      url: "https://…/index.m3u8",
      format: "hls",                 // hls | mp4 | dash | auto
      headers: {Referer: "https://…"},
      hls: {filterAds: true},        // 交给 Core 的 HLS 改写
      // 或:sniff: {page: "https://…/play/1", match: "\\.m3u8", timeoutMs: 15000}
    }),
  }
}
```

设计原则:

- **函数签名和返回形状对照漫画源**(`search.load`、`loadNext`、`loadInfo`),只把"章节→图片"换成"线路→剧集→播放地址"。会写漫画源的人能直接写视频源。
- `loadPlay` 拿不到地址时返回 `sniff`,由客户端打开 WebView 嗅探;脚本不直接操作 WebView。
- `minCoreVersion` 与 Core 的 API 版本比较,而漫画源的 `minAppVersion` 与客户端版本比较。
- 播放结果一律经过校验:地址只允许 http(s),请求头必须合法,否则视为错误。

## 6. 客户端提供的能力

| 能力 | 方式 | 说明 |
|---|---|---|
| HTTP | `host.Transport` | Core 构造请求,客户端发送;每个插件独立的 Cookie 分区 |
| 人机验证 | `host.Verifier` | 在 WebView 中完成 Cloudflare 挑战,返回 WebView 的 User-Agent |
| WebView 嗅探 | 处理播放结果中的 `sniff` | 打开页面,按正则拦截媒体请求,得到播放地址;可选跟随 iframe |
| 播放 | 客户端播放器 | iOS 用 AVPlayer;桌面用 mpv 或 libVLC 等 |
| 本地 HLS 代理 | 客户端 | 注入 Referer 和 Cookie 必须走本地代理,因为 AVPlayer 不能给每个分片加自定义请求头 |

Core 负责不联网的部分:

- `video/hls`:m3u8 解析与改写、过滤插播广告、去分片前缀、主播放列表选一路、把分片地址改写到客户端代理、AES-128 解密。
- `video/kazumi`:Kazumi 规则引擎,输入 HTML 或 JSON 文本,输出结构化结果。

## 7. 代码结构

```
VeneraCat Core (Go)
├─ host          客户端接口与 URL、请求头校验
├─ challenge     Cloudflare 检测与验证后重试
├─ attribution   作者与主页
├─ comic/js      MoeJS 运行时、init.js、VideoSource 前导库
├─ comic/...     convert、html、image、hostapi(http/cookie)、version
├─ video         视频数据模型与播放结果校验
├─ video/kazumi  Kazumi 规则引擎
├─ video/builtin 内置源(红果短剧)
├─ video/hls     HLS 工具
├─ subscription  漫画 index.json 与 vcvideo.json
├─ plugin        统一插件注册表
├─ backup        .venera 读入与恢复
└─ mobile        供 Swift 调用的 gomobile 接口
```

## 8. 已定事项与后续

已定:

1. 视频源单独使用 `vcvideo.json`,不与漫画源混排。
2. 直接兼容 KazumiRules;公开规则中 86 条有 85 条可以导入(唯一失败的一条已弃用,且缺少必填选择器)。

后续阶段:

1. AniBaka 的 `anx-rule/2` 流水线规则:能力最强,但操作多达 30 余种,工作量大。
2. 弹幕来源(弹弹 Play、Bangumi 等)和 Bangumi 同步。
3. 视频下载与离线缓存。

## 9. 作者与主页(所有插件)

所有插件都可以声明作者和主页,两项都**可选**:缺失时客户端在界面上提示"未提供作者/主页",不拒绝安装。

写在脚本里(漫画源与视频源相同):

```js
class Demo extends VideoSource {
  name = "Demo"; key = "demo"; version = "1.0.0"
  author = [{name: "Alice", url: "https://github.com/alice"}, "Bob"]   // 字符串、对象或二者组成的数组
  homepage = "https://github.com/alice/demo-source"
}
```

写在订阅条目里(漫画 `index.json` 与 `vcvideo.json` 的插件条目都支持,`vcvideo.json` 顶层也可以写订阅维护者):

```json
{"name": "Demo", "key": "demo", "version": "1.0.0", "fileName": "demo.js",
 "author": {"name": "Alice", "url": "https://github.com/alice"},
 "homepage": "https://github.com/alice/demo-source"}
```

上例是漫画 `index.json` 的条目;`vcvideo.json` 的条目还需要 `type` 和 `url`,见第 4 节。Kazumi 规则文件里也可以加 `author`、`homepage`;Kazumi 会忽略未知字段,规则仍然兼容 Kazumi。

规则:

- 脚本(或规则)里声明的优先,缺的部分用订阅条目补齐;作者和主页分别合并。
- `url` 和 `homepage` 只接受 http(s),不能带账号密码;不合法的部分丢弃并给出警告,不影响安装。
- 最多 16 位作者,名字最长 100 个字符。
- 内置源注明其改写来源,例如红果短剧注明 Xanerix 的真果鉴。
