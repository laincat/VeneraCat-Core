# 文档

[English](README.en.md) | 简体中文

## 规格

- [sendMessage 接口](SENDMESSAGE-SPEC.md):legacy ComicSource 脚本的全部消息,以及 init.js 重写范围
- [.venera 备份格式](VENERA-BACKUP.md):官方备份结构、type 哈希,以及 Core 的读入和恢复行为
- [插件与视频源](VIDEO-PLUGIN-DESIGN.md):插件类型、视频源写法、`vcvideo.json`、作者与主页
- [插件分发形态](PLUGIN-PACKAGING.md):插件今天是一个文本文件、为什么还不是压缩包、选 zip 而非 7z 的理由
- [标准 AI 接口](AIAPI.md):内置的 OpenAI/Anthropic 请求构造、鉴权头、原生正文与 Cookie 约束

## 设计记录

- [Magpie 插件系统](AI-SERVICE.md):iOS 直接兼容供应商原包,独立的账号与运行环境,兼容验证计划
- [Magpie 兼容性](MAGPIE-COMPATIBILITY.md):哪些能力被真的跑过、怎么验证的,哪些明确不支持
- [JavaScript 隔离](JS-ISOLATION.md):为什么每个源一个引擎
- [状态](STATUS.md):已实现、已验证和待办
- [代码体检](CODE-AUDIT.md):取消、AI 校验与三项目架构清理结论

## 笔记

`notes/` 存放实验留下的参考材料,例如 MoeJS 可行性测试,不属于规格。
| [VERIFICATION.md](VERIFICATION.md) | 验证与证据:什么算验证过,怎么复核,以及三次「看起来验过」的教训 |
| [OPEN-QUESTIONS.md](OPEN-QUESTIONS.md) | 悬而未决的决定:需要产品/安全取向的五件事,以及不定时的后果 |
| [PROVIDER-TEMPLATE.md](PROVIDER-TEMPLATE.md) | 写一个供应商包:契约、两个可跑的模板、怎么压包与自测 |
