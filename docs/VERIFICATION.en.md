# Verification and evidence

[简体中文](VERIFICATION.md) | English

This page says **what "verified" means in this project**, and how to check it yourself. It exists
because "looks verified" has been wrong here more than once: totals matched while files were
missing, a framework lagged its source, and a transfer rewrote a segment to the same length.

## What counts as evidence

| Claim | Evidence needed |
|---|---|
| The Go code builds and tests | `go vet ./...`, `go test -count=1 ./...`, plus `-race` when the change touches concurrency |
| The Apple side builds | the CI Apple job: an xcframework plus Swift smoke tests on macOS and the iOS simulator |
| A client commit was verified | a per-file manifest of that source **and** the run's output — never "the totals look about right" |
| A provider package runs | the package itself going through load, sign-in, refresh, models, usage, streaming and cancellation; loading alone is not enough |

## Four traps this project hit

1. **Matching totals do not mean the same tree.** `112 tests` and `114 tests` were both plausible,
   and both trees were missing files. The check has to be per file: the client uses
   `scripts/check-tree.sh` (which prints a `shape` digest) and `scripts/manifest.sh` (md5 per file).
   Equal `shape` is what shows the same tests live in the same files.
2. **A framework lags its source.** When the source called `chatStart` and the framework in
   `Vendor/` did not have it, the error appeared inside a Swift compile and read like a typo. The
   client therefore runs `scripts/check-core-surface.sh` before compiling, with the eighteen
   selectors it expects listed by name.
3. **A transfer can rewrite a segment to the same length.** One base64 segment arrived with the
   right length and the wrong content; only per-segment md5 found it, because an archive md5 says
   *that* it is broken, not *where*. Core is public on GitHub now, so clone it rather than carry it.
4. **A tool's entry point is not always an environment variable.** When the framework's
   macOS deployment target is newer than the client declares, every link warns.
   `export MACOSX_DEPLOYMENT_TARGET=26.0` looks like the fix and does nothing: clang
   honours it and plain cgo honours it, but `gomobile` only reads its own `-macosversion`
   flag. When a change like this cannot be verified on a real machine, say so in the
   commit instead of implying it was tested.

## Checking it yourself

Core:

```sh
go vet ./... && go test -race -count=1 ./...
sh scripts/check-docs.sh
gh run list --limit 1        # the Apple job is in there too
```

Client (on macOS, with Core cloned):

```sh
scripts/fetch-core.sh        # builds the xcframework at the commit pinned in scripts/core-version
sh scripts/check-core-surface.sh
sh scripts/check-tree.sh
swift test
```

## Handing a change to another machine

"Delete these two lines and keep one blank line" cannot be checked: the other side
follows it, the md5 still differs, and another round begins. That happened three times
here.

What can be checked is a patch, proven on this side before it is sent:

```sh
scripts/make-patch.sh BASE_DIR OUTDIR FILE...   # a tool in the client repository
```

It diffs against the copy the other machine holds, applies the result to a scratch
tree, and compares md5 per file; a patch that does not reproduce the bytes is never
sent. The receiver runs `patch -p0 < fix.patch` and compares the md5 once.

The same holds for "are the two trees the same": a shape digest shows how tests are
distributed, not which bytes exist. The loop closed only when the per-file md5s
matched — thirteen Magpie sources plus the test file, with the three that had drifted
all aligned in the end.

## Getting the code to a machine without credentials

A verification machine often cannot reach a private repository: no `gh`, no token, no
private key in `~/.ssh`. All three attempts (HTTPS, `gh`, SSH) fail, and they fail fast —
**do not try them in a loop, and do not have the machine generate keys or change its
authentication settings**. That is somebody else's machine and somebody else's account.

The order that works:

1. Clone a public repository directly (this one is); a private one is the case below.
2. Build a whole payload with `scripts/make-payload.sh`, put it in a **secret gist**, and
   have the machine `curl` it back. The payload carries the archive md5, the base64 md5 and
   a per-file md5 table, and the receiver verifies before unpacking.
3. Once both sides have a tree, send only patches.

Two details that matter:

- **Delete the gist as soon as it is used.** Secret means unlisted, not private: anyone with
  the link can read it, so it is not a substitute for the repository.
- **A hand-typed hash is the part that goes wrong.** Three times in one day a 32-character
  hash was mistyped into a message. `scripts/make-transfer.sh` now reads the values from the
  files and prints the whole message; the receiving side is told to stop and report a
  mismatch rather than "fix" it.

One thing that turns a normal situation into an apparent failure: `fetch-core.sh` on the
verification machine clones Core into `build/`, and Core's own `apple/CoreSmoke` contains
`.swift` files. A file-count script that does not exclude `build/` will disagree with the
repository by exactly two, every time. Exclude it — do not explain it in every report.

## Saying what was *not* verified

That is worth more than one extra sentence of confidence. The client's verification, for example,
ran `swift test` and the Swift 6 type checks but did **not** run an `xcodebuild` archive or signing,
and there is no iOS Simulator runtime; the verified tree has no `project.yml`. That is written in
the client's STATUS page, because a reader will otherwise assume "verified" covered it.
