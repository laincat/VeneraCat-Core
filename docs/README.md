# 文档

[English](README.en.md) | 简体中文

## 规格

- [sendMessage 接口](SENDMESSAGE-SPEC.md):legacy ComicSource 脚本的全部消息,以及 init.js 重写范围
- [.venera 备份格式](VENERA-BACKUP.md):官方备份结构、type 哈希,以及 Core 的读入和恢复行为
- [插件与视频源](VIDEO-PLUGIN-DESIGN.md):插件类型、视频源写法、`vcvideo.json`、作者与主页
- [插件分发形态](PLUGIN-PACKAGING.md):插件今天是一个文本文件、为什么还不是压缩包、选 zip 而非 7z 的理由
- [服务插件契约](SERVICE-PLUGINS.md):服务条目的清单、权限、模块 `handle` 契约与宿主能力
- [标准 AI 接口](AIAPI.md):内置的 OpenAI/Anthropic 请求构造、鉴权头、原生正文与 Cookie 约束

## 设计记录

- [Magpie 插件系统](AI-SERVICE.md):iOS 直接兼容供应商原包,独立的账号与运行环境,兼容验证计划
- [Magpie 兼容性](MAGPIE-COMPATIBILITY.md):哪些能力被真的跑过、怎么验证的,哪些明确不支持
- [JavaScript 隔离](JS-ISOLATION.md):为什么每个源一个引擎
- [状态](STATUS.md)汇总现行架构、兼容限制、验收入口与待办。

## 笔记

- [验证指南](VERIFICATION.md)说明如何验证、复核和区分不同环境的证据。
- [未决问题](OPEN-QUESTIONS.md)记录仍需产品决策的事项。
- [供应商包模板](PROVIDER-TEMPLATE.md)提供可运行的模板、打包与自测步骤。
- [GBK 兼容边界](GBK-COMPATIBILITY.md)记录有效字符、非法字节与私用区映射的差异。
