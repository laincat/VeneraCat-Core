# JavaScript 隔离

[English](JS-ISOLATION.en.md) | 简体中文

设计记录,2026-10-29。

## 问题

审查发现并经复现确认:所有源共用一个 MoeJS 运行时,Cookie 和验证分区取自全局变量 `activeKey`,存储 key 取自脚本自己写在消息里的 `key` 字段。因此一个源可以读取另一个源的 Cookie 和存储数据,在构造函数里就能做到(回归测试:`TestCrossSourceIsolation`)。此外,`await` 之后 `activeKey` 可能已经属于别的源;并发调用 `LoadSource` 时还会共用 `globalThis.__vcPending`。

## 方案:每个源一个引擎

- `Runtime` 改为管理者,持有 `map[key]*engine`。每个 `engine` 有自己的 `moejs.Runtime`、事件循环、`html.Store` 和 `image.Store`,并绑定一个 key。
- `sendMessage` 是引擎的方法。分区、存储和设置一律使用引擎绑定的 key,忽略消息里的 `key` 字段。
- `LoadSource` 先在一个新的、未绑定的引擎中执行脚本并读取元数据,校验通过后才按 key 注册;失败则关闭该引擎。未绑定期间,网络请求使用独立分区,存储是内存中的临时区,碰不到任何已安装源的数据。注册成功后,加载期间写入的数据会复制到正式存储。
- 公开接口(`New`、`LoadSource`、`Call`、`Has`、`Unload`、`Sources`、`Eval`、`VideoProvider`、`Close`)签名不变。`Eval` 在一个不属于任何源的"global"引擎上运行,用于测试和调试。
- 删除了 `runAs` 和 `activeKey`。

开销:每个源约 0.9 毫秒加载、112 KiB 内存(Windows 实测)。

## 一并修复的审查问题

- UI 回调在事件循环上同步触发时会自锁;现在客户端的 UI 处理函数不在事件循环上运行。
- `Close` 先取消宿主工作再加锁,不必等到超时。
- 脚本发起的 `http` 使用调用的 context,而不是运行时的 context,`Cancel(callID)` 能传到客户端。
- 视频播放结果经过校验:只允许 http(s) 地址,请求头必须合法。
