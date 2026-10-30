# VeneraCat Core 开发约定

[English](AGENTS.en.md) | 简体中文

本文件写给参与开发的人和 AI 助手。修改代码前请先阅读。

## 原则

- Core 不发起网络连接,也不打开 WebView 或播放器。客户端实现 `host.Transport`、`host.Verifier` 和 `host.Sniffer`;Core 只构造请求、解析响应。
- JavaScript 引擎使用 MoeJS(github.com/MoeForge/moejs,最新 main 分支或预览版)。
- 兼容目标:官方 Venera 的 `.venera` 备份,以及 legacy `ComicSource` 脚本与其订阅索引。其余功能按 `docs/` 中的插件规划实现。
- 视频订阅使用 `vcvideo.json`,`"$schema": "vcvideo/1"`;发布订阅的仓库应添加 GitHub topic `vcvideo`。
- 不要从 AGPL 项目(Animeko、AnimeFlow)或没有许可证的项目(AniCh)复制代码。Kazumi 是 GPL-3.0,KazumiRules 是 MIT;按行为重写,不要逐行翻译源码。
- 不要在仓库中放入第三方凭据、账号或站点密钥。其他 App 内嵌的签名密钥不属于我们,不得再分发。
- JSON 数字保持为 `json.Number` 或 `json.RawMessage`,ID 绝不经过 float64 舍入。

## 仓库约定

- 根目录只放工具链或惯例要求必须在根目录的文件,其余放进目录。
- 所有 `.md` 都提供中英双语:`X.md` 为中文,`X.en.md` 为英文,顶部互相链接。修改其中一份时同步修改另一份。`scripts/check-docs.sh` 会检查配对,并比较两份的表格行数与标题数 —— 链接对得上、内容却少一行的情况,今天真的发生过。
- 错误文本一律**英文**:Core 是共享层,不假设界面语言。面向用户的本地化在客户端做 —— Nekularity 把漫画源错误与 Magpie 的两类拒绝(见 `MagpieErrorText`)翻成中文并保留原文,桌面端直接显示中文框架加 Core 原文。不要在 Core 里塞中文,那会把语言选择绑死在库里。
- 公开历史是一个提交。收拢历史用 `scripts/squash.sh`:它比较收拢前后的 tree id,不一致就回滚,失败也会恢复原状 —— 手工 rebase 掉文件是看不出来的。推送后客户端 pin 必须跟着改。

## 检查

在仓库根目录运行:

```sh
gofmt -l .        # 应无输出
go vet ./...
go test -race ./...
./scripts/check-docs.sh
```

测试不访问网络,使用伪造的 `host.Transport`。需要把 Go 的临时目录和缓存放到别处时,设置 `GOTMPDIR` 和 `GOCACHE`。
