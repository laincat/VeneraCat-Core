# VeneraCat Core development guide

English | [简体中文](AGENTS.md)

This file is for people and AI assistants working on the code. Read it before making changes.

## Principles

- Core opens no network connections, WebViews or players. Clients implement `host.Transport`, `host.Verifier` and `host.Sniffer`; Core only builds requests and parses responses.
- The JavaScript engine is MoeJS (github.com/MoeForge/moejs, latest main branch or previews).
- Compatibility targets: official Venera `.venera` backups and legacy `ComicSource` scripts with their subscription index. Everything else follows the plugin plan in `docs/`.
- Video subscriptions use `vcvideo.json` with `"$schema": "vcvideo/1"`; repositories that publish one should add the GitHub topic `vcvideo`.
- Do not copy code from AGPL projects (Animeko, AnimeFlow) or from projects without a licence (AniCh). Kazumi is GPL-3.0 and KazumiRules is MIT; rewrite their behaviour, do not translate source line by line.
- Do not ship third-party credentials, accounts or site secrets. Signing keys embedded in other apps are not ours to redistribute.
- Keep JSON numbers as `json.Number` or `json.RawMessage`; never round IDs through float64.

## Repository conventions

- The root holds only files that tools or conventions require there; everything else goes into a directory.
- Every `.md` exists in Chinese and English: `X.md` is Chinese, `X.en.md` is English, and each links to the other at the top. Update both when you change one. `scripts/check-docs.sh` checks the pairs.

## Checks

From the repository root:

```sh
gofmt -l .        # must print nothing
go vet ./...
go test -race ./...
./scripts/check-docs.sh
```

Tests never use the network: use fake `host.Transport` implementations. Set `GOTMPDIR` and `GOCACHE` to move Go's temporary files and cache elsewhere.
