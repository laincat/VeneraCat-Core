# VeneraCat Core status

English | [简体中文](STATUS.md)

Updated 2026-10-30. Results come from the Windows development machine and from GitHub Actions (Linux, Windows, macOS 26 with Xcode 26.6).

## Implemented

| Package | Contents |
|---|---|
| `host` | Client interfaces: Transport, Verifier, Sniffer; URL and header validation |
| `attribution` | Optional authors and homepage for sources and plugins |
| `challenge` | Cloudflare detection (403/429/503 with markers, or `cf-mitigated`), retry with the WebView User-Agent after verification, per-partition isolation |
| `comic/js` | MoeJS runtime, **one isolated engine per source**; every official sendMessage method; LoadSource/Call; search options (`search.optionList`); VideoSource plugins |
| `comic/js/init.js` | Clean-room rewrite of the official prelude from the interface specification |
| `comic/hostapi` | http/cookie host API, compatible `CloudflareException:` format |
| `comic/hostapi/convert` | utf8/gbk/base64/hashes/HMAC/AES (ECB/CBC/CFB-N/OFB-N)/RSA decryption |
| `comic/html` | DOM handle protocol (21 functions) |
| `comic/image` | modifyImage pixel buffers |
| `comic/version` | The official version comparison rules (including beta/hotfix) |
| `backup` | Reads official `.venera` backups, resolves Dart type hashes, selectable restore plans |
| `subscription` | Comic `index.json` (official URL rules) and video `vcvideo.json` |
| `video` | Video data model; play result validation |
| `video/kazumi` | Kazumi rule engine (XPath/API); 85 of 86 published rules import |
| `video/builtin/hongguo` | Hongguo web pages only (no App signing, device registration or DRM keys) |
| `video/hls` | m3u8 parsing, rewriting, ad removal, AES-128 decryption |
| `plugin` | One registry for comic sources, JS video plugins, Kazumi rules and built-ins |
| `mobile` | gomobile API (JSON in and out), host bridge (HTTP, verification, storage, cookies, UI requests, clipboard), panic recovery, cancellation |

## Verified

- `gofmt`, `go vet` and `go test -race` pass for all packages.
- Real data: the wnacg comic source end to end offline; the vensrc main and extended indexes and the official venera-configs index all parse.
- CI: Go tests for every package pass on Linux, Windows and macOS.
- Apple (CI, macOS 26 arm64, Xcode 26.6): the xcframework builds (iOS device, iOS simulator and macOS slices); the Swift smoke test passes all 7 tests on macOS and on the iOS simulator, covering the comic-source path, cookies, UI requests, the clipboard, hasComic and search options.
- Size: the iOS device binary is 20 MB stripped and 7.2 MB gzipped; the whole xcframework is 95 MB (including the two-architecture simulator and macOS slices, which do not ship in the app).
- `Map` values that sources return (tags, chapters) become JSON objects in insertion order, as in the official app. They used to become an empty `{}`, which lost every chapter list. Integer keys such as chapter numbers `10` and `2` keep their order and are not sorted.
- Speed: on the iOS simulator a search takes 1–7 ms and installing a source 0.6–0.9 ms; on Windows each source engine uses about 112 KiB of memory.

## Security review (2026-10-29)

An independent review found 12 issues (4 high, 5 medium, 3 low), **all fixed with regression tests**. The most serious let sources read each other's cookies and storage; it was reproduced and fixed by running each source in its own engine, see [JS-ISOLATION.en.md](JS-ISOLATION.en.md).

## Not verified / pending

1. **Devices**: measure start-up time and memory on a real iPhone (CI can only run the simulator).
2. **Cross-check the Dart hash on a real Dart VM**: `print('wnacg'.hashCode)` should print `823512256`.
3. The wnacg source has no licence and cannot go into the repository; CI needs a test source of our own.
4. Video download and offline cache, danmaku, Bangumi: not done (second phase).
5. AniBaka `anx-rule/2` pipeline rules: not done (second phase).
