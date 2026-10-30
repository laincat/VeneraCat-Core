# GBK decoding compatibility boundary

[简体中文](GBK-COMPATIBILITY.md) | English

On 2026-11-04, portable Dart 3.13.7 actually executed `enough_convert` 1.6.0 and 1.7.1 on Windows. The same ten inputs ran through Core `3be57b5`'s public `Runtime.Eval` → `Convert.decodeGbk(new Uint8Array(...))` entry. Both Dart versions produced identical results for these samples; Core's Go decoder differs from them.

The Dart SDK archive matched Google's official SHA-256 and passed ZIP CRC checks; `dart pub get` pinned the dependencies. This executes the conversion library, not the official Venera app. That app's exact dependency version and `allowInvalid` setting have not been established.

## Observed results

Inputs are hexadecimal bytes. Code-point sequences avoid presenting private-use characters as ordinary Chinese text. `FormatException` means the library actually threw; a host implementing the documented exception-to-null contract would return null for these default-mode calls.

| Input | Core output code points | Dart default | Dart `allowInvalid: true` output code points |
|---|---|---|---|
| `41 00` | U+0041 U+0000 | Same as Core | Same as Core |
| `D6 D0 CE C4` | U+4E2D U+6587 (中文) | Same as Core | Same as Core |
| `80` | U+20AC (€) | FormatException | U+FFFD |
| `61 FF 62` | U+0061 U+FFFD U+0062 | FormatException | U+0061 U+FFFD |
| `D6` | U+FFFD | FormatException | U+FFFD |
| `81 20 61` | U+FFFD U+0020 U+0061 | FormatException | U+FFFD U+0061 |
| `81 7F` | U+FFFD U+007F | FormatException | U+FFFD |
| `81 30 81 30` | U+FFFD U+0030 U+FFFD U+0030 | FormatException | U+FFFD U+FFFD |
| `A1 40` | U+FFFD | U+E4C6 | U+E4C6 |
| `FE FE` | U+FFFD | U+E4C5 | U+E4C5 |

This includes consumption of following bytes: permissive decoding of `61 FF 62` returns `a�b` versus `a�`. Private-use mappings also differ; successful ordinary Chinese decoding does not establish parity. Core differs on eight samples from default mode and seven from permissive mode.

## Current handling

Keep existing Core conversion behavior and both client pins. This change adds source-bridge regression evidence and states the limits. Common valid Chinese bytes work through `Convert.decodeGbk`, but arbitrary GBK input is not promised to match the reference library byte-for-byte. Do not change every decoding error to null or consume following ASCII bytes without confirming the official configuration and actual source requirements.

Minimal reproduction: compare Dart's `GbkCodec().decode([0x61, 0xff, 0x62])` with `GbkCodec(allowInvalid: true).decode([0x61, 0xff, 0x62])`; in a Core source, run `Convert.decodeGbk(new Uint8Array([0x61, 0xff, 0x62]))`. The first throws, while the latter two return `a�` and `a�b` respectively.

Original input, Dart/Go drivers and per-case JSON results are under `E:/Temp/core-gbk-differential/`. Execution logs are `dart-1.6.0.log`, `dart-1.7.1.log` and `core.log`; the Go driver uses `go run -race`. These tests did not contact real comic sites or execute the official app.
