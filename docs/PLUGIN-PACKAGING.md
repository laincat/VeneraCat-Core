# 插件以什么形式分发,以及为什么现在还不是压缩包

[English](PLUGIN-PACKAGING.en.md) | 简体中文

日期:2026-11-01。本文只记录现状与取舍,不构成已实现的能力。

## 现状:一个插件就是一个文本文件

`plugin.Record` 里装的是全文文本,不是路径:

```go
type Record struct {
    Type, Key, Name, Version string
    Body   string // 脚本或规则全文
    SHA256 string // Body 的 sha256
    Origin string // 从哪儿来:订阅条目、文件、备份
    IndexURL string
    Enabled bool
    Attribution attribution.Info
}
```

| 类型 | `Body` 是什么 | 从哪儿装 |
|---|---|---|
| `comic` | 一个 Venera 兼容的 `.js` 源脚本 | 文件、`index.json` 订阅、`.venera` 备份 |
| `video-js` | 一个视频源 `.js` 脚本 | `vcvideo.json` 订阅 |
| `kazumi-rule` | 一个 Kazumi 规则 `.json` | `vcvideo.json` 订阅 |
| `builtin` | 空(实现在 Core 里编译好了) | 用户启用即可 |

脚本安装会在隔离引擎中执行并读取声明的 `key`;通过订阅安装时,它必须
等于条目中的 `key`(`ErrKey`)。调用方提供预期 `sha256` 时会校验正文,
不符即拒绝(`ErrChecksum`);未提供预期哈希时仍计算并记录正文哈希,
但这不证明发布者身份。Kazumi 规则按 JSON 解析,内置实现没有脚本正文。

脚本必须**自包含**:`comic/js` 里没有任何 `require` / `import` 解析,
一个源就是一个文件。这也是官方 Venera 漫画源的既有形态。

## 为什么现在不能吃压缩包

源插件安装入口 `Install`、`InstallFromComicIndex`、
`InstallFromVideoIndex` 接受 `body string`,没有插件包解码层。它们不能
直接安装 `.zip`、`.7z` 或 `.tar.gz`;如果把压缩数据作为正文传入,
会按所选类型解析并失败。Core 的 `.venera` 备份解包是另一条独立流程,
不代表源插件支持压缩包。

## 真要支持"包",选哪个容器

| 容器 | 代价 | 结论 |
|---|---|---|
| `zip` | Go 标准库 `archive/zip`,零额外依赖,Apple/Windows/Linux 都能直接用 | 推荐 |
| `tar.gz` | 标准库 `archive/tar` + `compress/gzip`,同样零依赖,但 Windows 上容易被当成"文件"而非包 | 可用,但不如 zip 直观 |
| `7z` | 标准库不支持,需要第三方解码器;存在纯 Go 实现,不必依赖 cgo。具体库的格式覆盖、资源上限、许可证及 Apple 构建兼容性尚未评估 | 待评估 |

当前推荐 zip 的理由是标准库即可实现。尚未对 7z 的包体积、压缩收益或
移动端构建做对比测试,不能据此断言它不可用或依赖库维护不佳。

## 支持包之前必须先定下的六件事

1. 包内清单:文件名与字段(建议 `vcplugin.json` / `vcplugin/1`,
   含 `key`、`version`、`main`、`assets`、`locales`)。
2. 条目白名单:只解 `main.js`、`assets/*`、`locales/*`,其余一律忽略。
3. 上限:条目数、单文件大小、解压后总大小(防解压炸弹)。
4. 路径规则:拒绝绝对路径与 `..`,拒绝符号链接与设备文件。
5. 校验口径:定义整包与正文哈希各自的字段。`Record.SHA256` 已表示
   `Body` 的哈希,不能填入整包哈希;新增包索引时还需明确其校验对象,
   保留现有文本订阅的含义。
6. 是否签名:未签名与已签名如何区分显示。

其中包格式、可扩展能力与信任模型需要确定产品方向;路径校验、资源上限等
属于实现必须落实的边界。以下仅为候选设计,尚未实现或批准。

## 若决定做,最小安全版本长这样

- 容器 `zip`,扩展名 `.vcplugin`;
- 根部 `vcplugin.json`,`main` 指向一个自包含 Venera 脚本;
- 解包时按白名单挑条目,超限直接失败,不做部分安装;
- 装进 `Registry` 的仍是脚本正文,`Record.SHA256` 仍校验正文;
  整包哈希由新增的包元数据单独记录与校验,不混用两个值;
- 本候选仅携带资源,运行期仍按单文件脚本执行;包内模块导入需要另行扩展加载器;
- `assets` / `locales` 交给客户端保存与显示,Core 不解释它们。

这样多文件插件(带图标、多语言)可以分发,而 Core 的运行模型不变。

## Magpie 的参考边界

参考版本:[yetone/magpie@4a4a90c](https://github.com/yetone/magpie/tree/4a4a90c23793ccf4ef197c31aadea7b7c76283c7),MIT。
本节记录源码分析,不表示我们已实现其能力。

| 层 | Magpie 的实际做法 | 对本项目的意义 |
|---|---|---|
| 内置功能 | `internal/pluginrt` 注册 Go `Manifest`,按 `Requires` / `Provides` 启停,逆序撤销注册;代码随程序编译 | 可参考客户端制作台等可选模块的生命周期 |
| 社区分发 | npm / Git / 本地文件或目录;`package.json` 声明入口,Bun 安装使用 `--ignore-scripts`;备用渠道校验 `.tgz` 的 SHA-512 | 分发容器与运行能力应分别定义;禁用安装脚本不等于禁用运行时代码 |
| 后台 | Bun 子进程动态加载 JS/TS,通过逐行 JSON 与 Go 通信;多个插件共用 host,可用文件、网络和命令 | 不是每源权限沙箱,不能直接作为 iOS 共用执行方案 |
| 受限逻辑 | `internal/jsmod` 用 MoeJS 链接包内相对模块,不解析 `node_modules`;中间件按 hook 限时 | 多文件支持需要加载器,并非 MoeJS 引擎本身不支持 |
| 页面 | `magpie.page` / `panels` / `actions` 声明扩展点;页面置于受限 iframe,桥接与服务端校验允许的读取 | 界面扩展放客户端;页面权限不等于 Bun 后台权限 |

可参考的是清单、明确的扩展点、启停清理和局部失败状态。官方 Venera
单文件源继续兼容;包内模块、页面扩展及桌面专属后台能力都属于后续设计。
Core 仍不直接联网、不打开 WebView 或播放器,由客户端提供宿主能力。

## 从下载的压缩包安装

用户手上的包通常不是一个文件夹,而是下载来的 `.zip` 或 npm 的 `.tar.gz`。Core 现在负责解开它们,而不是让每个客户端各写一份解压:

```
ReadArchive(data) → map[路径]字节        // magpieplugin
mobile.Magpie.ReadPackageArchive(data) → base64 文件表 JSON   // 客户端调用
```

规则与文件夹安装**完全一致**,因为两条路最终都汇到同一个 `NewBundle`:

- 只接受普通文件;符号链接、设备文件、`../` 或绝对路径直接拒绝(链接在内存里没有意义,而跟着它读会读到包外)。
- 单文件 8 MiB、总量 32 MiB、最多 1024 个文件,与 `NewBundle` 相同的上限。
- 若所有文件都在同一个顶层目录下,这一层会被剥掉:`zip 这个文件夹` 与 npm tarball 都会带一层,而它只是打包习惯,不是包结构。
- 既不是 zip 也不是 gzip 的字节返回 `ErrNotAnArchive`,不会被当成包。

