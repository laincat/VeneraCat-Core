# How a plugin is distributed, and why it is not an archive yet

English | [简体中文](PLUGIN-PACKAGING.md)

Date: 2026-11-01. This records the current shape and the trade-offs; it does not
describe a capability that exists.

## Today: one plugin is one text file

`plugin.Record` holds the whole text, not a path:

```go
type Record struct {
    Type, Key, Name, Version string
    Body   string // the script or rule, in full
    SHA256 string // sha256 of Body
    Origin string // where it came from: index entry, file, backup
    IndexURL string
    Enabled bool
    Attribution attribution.Info
}
```

| Type | What `Body` is | Installed from |
|---|---|---|
| `comic` | one Venera-compatible `.js` source script | a file, an `index.json` entry, a `.venera` backup |
| `video-js` | one video-source `.js` script | a `vcvideo.json` entry |
| `kazumi-rule` | one Kazumi rule `.json` | a `vcvideo.json` entry |
| `builtin` | empty (the implementation is compiled into Core) | the user enables it |

Script installation executes the script in an isolated engine and reads its
`key`; installation through an index requires it to match the entry (`ErrKey`).
An expected `sha256`, when supplied, is checked against the body and a mismatch
is refused (`ErrChecksum`). Without an expected hash the body digest is still
recorded, but it does not establish publisher identity. Kazumi rules are parsed
as JSON; built-ins have no script body.

Scripts must be **self-contained**: `comic/js` has no `require` or `import`
resolution, so a source is a single file. That is also the shape official
Venera comic sources come in.

## Why an archive cannot be installed now

The source-plugin entry points `Install`, `InstallFromComicIndex` and
`InstallFromVideoIndex` take a `body string` without a package decoding layer.
They cannot directly install `.zip`, `.7z` or `.tar.gz` archives; compressed
bytes supplied as a body fail parsing for the selected type. Core's `.venera`
backup unpacking is a separate path and does not provide source-package support.

## If packages are wanted, which container

| Container | Cost | Verdict |
|---|---|---|
| `zip` | standard library `archive/zip`, no extra dependency, works on Apple, Windows and Linux as-is | recommended |
| `tar.gz` | standard library `archive/tar` plus `compress/gzip`, also no dependency, but Windows treats it as an ordinary file rather than a package | workable, less obvious |
| `7z` | not in the standard library; requires a third-party decoder. Pure-Go implementations exist, so cgo is not mandatory. Format coverage, resource limits, licensing and Apple build compatibility of a specific library have not been evaluated | needs evaluation |

Zip is currently recommended because the standard library is sufficient. No
comparison of 7z package size, compression benefit or mobile builds has been
performed; this does not establish that 7z is unusable or poorly maintained.

## Six things to settle before supporting packages

1. The manifest: file name and fields (suggested `vcplugin.json` / `vcplugin/1`
   with `key`, `version`, `main`, `assets`, `locales`).
2. An entry allowlist: unpack only `main.js`, `assets/*`, `locales/*`; ignore
   everything else.
3. Limits: entry count, per-file size, total unpacked size (against zip bombs).
4. Path rules: refuse absolute paths and `..`, refuse symlinks and device files.
5. Checksum scope: give package and body digests separate fields.
   `Record.SHA256` already hashes `Body` and must not contain a package digest.
   A new package index must define its checksum scope while preserving the
   meaning of existing text-source indexes.
6. Signing: how an unsigned package is distinguished from a signed one.

Package format, extension capabilities and the trust model need a product
direction. Path validation and resource limits are implementation boundaries
that must be enforced. The design below is a candidate, neither implemented
nor approved.

## The smallest safe version, if it is wanted

- container `zip` with the extension `.vcplugin`;
- `vcplugin.json` at the root, `main` naming a self-contained Venera script;
- unpacking picks entries by the allowlist, fails outright when a limit is
  exceeded, and never installs half a package;
- the `Registry` still receives the script body and `Record.SHA256` still
  checks that body; new package metadata records and checks the archive digest
  separately, without mixing the two values;
- this candidate only carries resources; runtime execution remains a single
  script. Package-local module imports would need a loader extension;
- `assets` and `locales` are the client's to store and display; Core does not
  interpret them.

That would let a multi-file plugin (icon, translations) ship while Core's
runtime model stays as it is.

## What Magpie provides as a reference

Reference: [yetone/magpie@4a4a90c](https://github.com/yetone/magpie/tree/4a4a90c23793ccf4ef197c31aadea7b7c76283c7), MIT.
This is a source analysis, not a list of features we have implemented.

| Layer | What Magpie does | Implication here |
|---|---|---|
| Built-in features | `internal/pluginrt` registers Go manifests, activates by `Requires` / `Provides`, and undoes registrations in reverse; code ships compiled | Useful lifecycle reference for optional client modules such as the workbench |
| Community distribution | npm / Git / local file or directory; `package.json` declares entries; Bun installs with `--ignore-scripts`; the fallback channel checks `.tgz` SHA-512 | Define distribution and runtime capabilities separately; skipping install scripts does not prevent runtime execution |
| Backend | A Bun subprocess dynamically imports JS/TS and exchanges JSON lines with Go; plugins share a host and can use files, networking and commands | Not a per-source permission sandbox or a shared iOS execution solution |
| Restricted logic | `internal/jsmod` links package-relative modules in MoeJS without `node_modules`; middleware hooks have execution limits | Multiple files require a loader extension; the engine itself can link modules |
| Pages | `magpie.page`, `panels` and `actions` declare extension points; restricted iframes use a bridge with server-side checks on reads | UI extensions belong in clients; page permissions do not constrain Bun backends |

The useful references are manifests, explicit extension points, lifecycle
cleanup and local failure states. Official Venera single-file sources remain
compatible. Package-local modules, UI extensions and desktop-only backends
remain future design work. Core still delegates networking, WebViews and
players to client host implementations.

## Installing from a downloaded archive

What a user has is usually a downloaded `.zip` or an npm `.tar.gz`, not a folder. Core unpacks
them, so each client does not grow its own extractor:

```
ReadArchive(data) → map[path]bytes                        // magpieplugin
mobile.Magpie.ReadPackageArchive(data) → base64 file table // what a client calls
```

The rules are exactly the folder rules, because both routes end at the same `NewBundle`:

- Regular files only. Symlinks, device entries, `../` and absolute paths are refused: a link has
  no meaning once the files live in memory, and following one would read outside the package.
- 8 MiB per file, 32 MiB total, at most 1024 files — the same limits as `NewBundle`.
- One enclosing directory is stripped when every file shares it: both "zip this folder" and an
  npm tarball add one, and it is a packing habit rather than package structure.
- Bytes that are neither zip nor gzip return `ErrNotAnArchive` instead of being treated as a
  package.
