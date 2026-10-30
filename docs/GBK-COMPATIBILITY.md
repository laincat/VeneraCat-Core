# GBK 解码兼容边界

[English](GBK-COMPATIBILITY.en.md) | 简体中文

2026-11-04，在 Windows 用便携 Dart 3.13.7 分别实际运行 `enough_convert` 1.6.0 和 1.7.1，并将同一组十份输入送入 Core `3be57b5` 的公开 `Runtime.Eval` → `Convert.decodeGbk(new Uint8Array(...))`。两个 Dart 库版本在这些样本上的结果完全相同；Core 使用的 Go 解码器与它们存在差异。

Dart SDK 压缩包已与 Google 官方 SHA-256 核对并检查 ZIP CRC，依赖经 `dart pub get` 锁定版本。这里执行的是转换库，不是官方 Venera App；没有确认该 App 的精确依赖版本和 `allowInvalid` 配置。

## 实测结果

所有输入都是十六进制字节。表中码点序列避免把私用区字符误显示成正常汉字。`FormatException` 表示库实际抛出异常；若宿主按行为规格吞异常并返回 null，则默认模式下的这些调用会得到 null。

| 输入 | Core 输出码点 | Dart 默认模式 | Dart `allowInvalid: true` 输出码点 |
|---|---|---|---|
| `41 00` | U+0041 U+0000 | 同 Core | 同 Core |
| `D6 D0 CE C4` | U+4E2D U+6587（中文） | 同 Core | 同 Core |
| `80` | U+20AC（€） | FormatException | U+FFFD |
| `61 FF 62` | U+0061 U+FFFD U+0062 | FormatException | U+0061 U+FFFD |
| `D6` | U+FFFD | FormatException | U+FFFD |
| `81 20 61` | U+FFFD U+0020 U+0061 | FormatException | U+FFFD U+0061 |
| `81 7F` | U+FFFD U+007F | FormatException | U+FFFD |
| `81 30 81 30` | U+FFFD U+0030 U+FFFD U+0030 | FormatException | U+FFFD U+FFFD |
| `A1 40` | U+FFFD | U+E4C6 | U+E4C6 |
| `FE FE` | U+FFFD | U+E4C5 | U+E4C5 |

这不是“遇错都替换成同样字符”的小差别：`61 FF 62` 的容错解码分别得到 `a�b` 和 `a�`，后续 ASCII 字节的保留也不同；私用区映射也不能由中文正常解码的用例推定一致。十份样本中，Core 与默认模式有八份结果不同，与容错模式有七份不同。

## 当前处理

保留现有 Core 转换行为和两端 pin，本次只增加来源脚本桥接的回归证据并明确限制。常见有效中文可通过 `Convert.decodeGbk` 使用，但不能承诺任意 GBK 字节与官方库逐字节兼容。不要在没有确认官方配置和实际源需求的情况下，把所有解码错误统一改成 null，或吞掉后续 ASCII 字节。

最小复现：Dart 中比较 `GbkCodec().decode([0x61, 0xff, 0x62])` 与 `GbkCodec(allowInvalid: true).decode([0x61, 0xff, 0x62])`；Core 源脚本执行 `Convert.decodeGbk(new Uint8Array([0x61, 0xff, 0x62]))`。前者抛错，后两者分别为 `a�` 和 `a�b`。

原始输入、Dart/Go 驱动及逐例 JSON 输出保存在 `E:/Temp/core-gbk-differential/`。执行日志为 `dart-1.6.0.log`、`dart-1.7.1.log` 和 `core.log`，Go 驱动使用 `go run -race`。测试没有连接真实漫画站点，也没有执行官方 App。
