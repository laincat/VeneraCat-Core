# VeneraCat Core

English | [简体中文](README.md)

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
