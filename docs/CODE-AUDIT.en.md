# Code audit and cleanup

English | [简体中文](CODE-AUDIT.md)

## Scope and baseline

This audit starts from Core `469edfcb48ec9564dca086a232a7b64e46fd0a9b`. It covers AI request construction, mobile transport and cancellation, backup limits and paths, JS isolation, dependencies and gates. Fixes are local; Windows results apply to the working tree and do not automatically verify a public commit or an Apple artifact.

The offline visualization for all three projects lives at `Nekophage/docs/analysis/architecture-audit.html` in the shared workspace. This page records only the Core portion.

## Fixes in this pass

| Priority | Problem | Change and evidence |
|---|---|---|
| P1 | Swift reserves an ID before queueing a call; cancellation in between was lost | `NewCall` reserves a context, inherited when the call starts; regression cancels before execution |
| P1 | `Close` did not cancel all mobile calls | A Core lifetime context cancels named and anonymous calls; future calls are cancelled after closure |
| P2 | Go struct decoding accepted `Model`/`Messages`/`Stream` while forwarding native bytes | Read exact keys and reject case variants of reserved fields; do not rewrite native bodies |
| P2 | `anthropic-version` accepted control characters | Validate the header at construction; reject CR/LF and blank values before transport |
| P2 | Empty host and empty `?`/`#` delimiters passed API root checks | Validate `Hostname`, `ForceQuery` and fragment delimiters |
| P2 | Unknown-protocol and duplicate-field errors reflected arbitrary input | Use fixed protocol/duplicate-key descriptions and avoid echoing config JSON in decode errors |
| P3 | `SendAIRequest` decoded the body twice and assigned false to an already false flag | Remove redundant decoding and the dead branch; retain `streamed:false` |
| P2 | A call that returned early after taking a call ID leaked its reservation | Every `callID` method defers `endCall`; unknown source and bad-argument paths release both maps; the regression reproduced the leak before turning green |

`aiapi/audit_test.go` and `mobile/cancellation_audit_test.go` add behavioral regressions. Failures were reproduced before fixes in `E:/Temp/tri-audit/core-before.log` and `cancel-before.log`; focused and race checks pass after fixes. Test counts are not used as proof of correctness.

## Architecture and dependency assessment

Go + MoeJS remains a suitable boundary: Core handles source compatibility, subscriptions, backups and protocols; clients own networking, Keychain/storage, WebViews and UI. Per-source JS runtimes improve isolation and increase memory as more sources start. Reuse and release runtimes according to lifecycle rather than recompiling packages during list refreshes.

Direct dependencies:

| Dependency | Purpose | Assessment |
|---|---|---|
| MoeJS alpha.11 / `0a0ed36` | Official comic scripts and Magpie JavaScript | Pin this preview revision; upgrades need compatibility regressions |
| cascadia 1.3.5 | CSS selectors | Distinct from XPath support rather than a duplicate dependency |
| htmlquery 1.3.7 / xpath 1.3.9 | HTML / XPath video rules | Retain; measure specific hotspots before reducing repeated parsing |
| x/net 0.62.0 / x/text 0.43.0 | HTML and text processing | Retain current versions; no speculative upgrades in this pass |
| modernc SQLite 1.61.0 | Reading `.venera` databases | A runtime dependency, not merely a test dependency; contributes binary size |
| x/mobile / gomobile | ObjC/Swift bridge and xcframework tooling | Generated headers define the API; Swift import shapes must not be guessed |

## Remaining work

- Standard AI executes complete bodies only. `Host.HTTP` and the iOS bridge do not yet expose live chunks. Polling buffered events with `ChatNext` does not prove network streaming.
- Structured provider errors, complete native events and generic routing/usage/redaction ABIs remain unfinished. The `aiapi` builder is not a complete AI service.
- gomobile JSON/base64 copies requests and responses. Implement streaming and measure peak memory before choosing a lower-copy interface.
- Framework size and per-source runtime memory need real App/device measurements. No throughput, peak-memory or percentage speedup is claimed here.
- No dependency vulnerability database scan was run. `go vet`, race checks and pinned versions do not replace one.

## Verification approach

Full logs live in `E:/Temp/tri-audit`. Gates use the original command's exit status rather than `tail`'s status. Formatting passes only when the process succeeds and its output is empty.

Mac reported the iOS patched tree as a real `generic/platform=iOS` `BUILD SUCCEEDED`, `167 tests / 32 suites`, 0 warnings and a shape matching Windows. It also reproduced five P2 network and Magpie lifecycle defects recorded in the client document; those still need client fixes and re-verification.

One Core-adjacent issue remains open: the normalized usage output can carry a `null` array that breaks the client's `MagpieUsage` decoding. That belongs to client model parsing; Core will not turn unknown into 0.

Windows checks include `go test -race -count=1 ./...`, `go vet ./...` and focused cancellation/AI regressions. Delivery records final logs and Mac evidence for patched Core / Swift builds. Mac validation must compare patched-file hashes; an old pin alone cannot verify new patches.
