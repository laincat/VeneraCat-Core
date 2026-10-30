# init.js 来源说明

[English](NOTICE-init.en.md) | 简体中文

本目录的 `init.js` 依据 [docs/SENDMESSAGE-SPEC.md](../../docs/SENDMESSAGE-SPEC.md) 中的行为规格编写,编写时没有打开官方的 `init_js.dart` 或 `init.js`。规格列出了脚本依赖的全部全局名、方法名、消息字段和已知怪癖。

接口名称(全局对象、方法、消息字段、构造函数参数名)是兼容现有 ComicSource 脚本所必需的,不属于创作性表达;包装这些接口的代码是原创的。

`video-prelude.js` 是 VeneraCat 的扩展,增加 `VideoSource` 基类,不属于官方接口。
