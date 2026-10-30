# VeneraCat Core

English | [简体中文](README.md)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/brand/veneracat-core-logo-dark.png">
  <img src="docs/assets/brand/veneracat-core-logo.png" alt="VeneraCat Core" width="720">
</picture>

The shared core of Nekularity (iOS/iPadOS) and Nekophage (desktop), written in Go and licensed under GPL-3.0-or-later.

## Features

- **Comic sources**: runs official Venera `ComicSource` JavaScript scripts and their `index.json` subscription indexes on [MoeJS](https://github.com/MoeForge/moejs), one isolated engine per source.
- **Video sources**: JavaScript `VideoSource` plugins, [Kazumi](https://github.com/Predidit/Kazumi) rules and built-in providers, subscribed through `vcvideo.json`.
- **Backups**: reads official `.venera` backups and restores them item by item after a preview.
- **Human verification**: recognises Cloudflare challenges, which the client completes in a WebView before Core retries.
- **HLS**: playlist parsing, rewriting, ad removal and AES-128 decryption.

Core opens no network connections, WebViews or players; clients provide them through the interfaces in the `host` package.

## Layout

| Directory | Contents |
|---|---|
| `host/` | Interfaces the client implements |
| `comic/` | Comic source runtime and host APIs |
| `video/` | Video data model, Kazumi, built-ins, HLS |
| `plugin/` | Unified plugin registry |
| `subscription/` | Comic and video subscription indexes |
| `backup/` | Official `.venera` backups |
| `mobile/` | gomobile API for Swift |
| `version/` | answers "which Core is this"; see below |
| `apple/` | Apple platform smoke tests |
| `docs/` | Specifications and design documents |
| `scripts/` | Build and verification scripts |

## Development

```sh
gofmt -l .        # must print nothing
go vet ./...
go test -race ./...
```

For Apple platforms, see `scripts/mac-verify.sh` (macOS and Xcode required).

See [docs/README.en.md](docs/README.en.md) for the documentation index.

## Which Core is this

`version.String()` prints a version. When Core is the program, that is its own revision (with
`-dirty` when the tree had changes). When another program links Core, the revision in
`debug.ReadBuildInfo()` belongs to **that** program and cannot stand in for Core — so the linking
side writes a stamp:

```go
import coreversion "github.com/laincat/veneracat-core/version"

func init() { coreversion.SetRelease("35b3ed9e…") }
```

The desktop does this: `scripts/fetch-core.sh` writes a `corestamp.go` with the revision it synced,
so `Nekophage -version` can name both its own commit and Core's.
