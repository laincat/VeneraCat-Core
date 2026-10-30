# Plugins and video sources

English | [简体中文](VIDEO-PLUGIN-DESIGN.md)

Updated 2026-10-30. This document describes the plugin types, how to write video sources and the subscription formats as Core currently implements them. Networking, cookies, WebViews and players are provided by the client.

## 1. Scope

Core must be compatible with two things from the official app:

1. **Official backups** (`.venera`): read, preview and restore; data that cannot be represented is kept unchanged and listed. See [VENERA-BACKUP.en.md](VENERA-BACKUP.en.md).
2. **Comic sources and comic subscriptions**: legacy `ComicSource` scripts (interface in [SENDMESSAGE-SPEC.en.md](SENDMESSAGE-SPEC.en.md)) and the `index.json` subscription index. The index is a JSON array whose entries are `{name, key, version, fileName, url, minAppVersion, description}`, as in the official venera-configs and laincat/vensrc.

On top of that it adds plugins and video sources.

## 2. Video apps surveyed (from laincat/AltStore)

| App | Licence | Source format | How the video URL is found | For this project |
|---|---|---|---|---|
| Kazumi | GPL-3.0 | **Declarative JSON rules** (XPath or API/JSONPath), no JS execution | Mostly by **WebView sniffing** for m3u8/mp4 | The rule repository `Predidit/KazumiRules` is **MIT** and can be imported directly |
| AniBaka | GPL-3.0 | `anx-rule/2`: an **operation pipeline** in three stages, search/detail/play (30-odd operations such as fetch, select, regex, json, crypto, sniff) | Pipeline `videoUrl`, or `sniff` falling back to a WebView; also HLS rewriting and ad removal | The most capable; a reference for declarative rules |
| AnimeFlow | AGPL-3.0 | A variant of Kazumi rules (mostly the same fields) | WebView | Reference only, no code copied |
| Animeko | AGPL-3.0 | Selector sources (CSS selectors plus regular expressions matching video URLs), with subscription repositories | WebView sniffing plus regular expressions | Reference only, no code copied |
| AniCh | **No licence** | — | — | **No code may be copied** |
| 真果鉴 guoapp | GPL-3.0 | **Native Go providers** compiled into the app, not scripts loaded at run time | A built-in Go HLS proxy | Shows that statically linking Go (`c-archive`) into iOS works, and that HLS rewriting can be done in Go |

Conclusions:

- Video sources **almost always need WebView sniffing** as a fallback: page players often hide the real URL in obfuscated JS.
- The second common need is **HLS handling**: injecting Referer and cookies, filtering inserted ads, removing segment prefixes, decoding encrypted playlists.
- The largest rule ecosystem is Kazumi's (an MIT rule library), which is worth supporting directly.

## 3. Plugin types

All plugins share one key namespace (`^[a-zA-Z0-9_]+$`). The `plugin` package manages installing, updating, enabling, disabling and restoring all of them.

| Type | Entry | Runs on | Comes from |
|---|---|---|---|
| `comic` | `*.js`, `class X extends ComicSource` | MoeJS, one engine per source | Comic `index.json`, files, backups |
| `video-js` | `*.js`, `class X extends VideoSource` | MoeJS, one engine per source | `vcvideo.json`, files |
| `kazumi-rule` | Kazumi rule `*.json` | Core's built-in rule engine (XPath/JSONPath) | `vcvideo.json`, files |
| `builtin` | Compiled into Core | Go | Enabled by the user in the app |

Themes, layouts, feature modules, danmaku and Bangumi sync belong to later phases and have no type yet.

## 4. Video subscriptions: `vcvideo.json`

Video sources use **their own index** and are never mixed into a comic `index.json`, so a client that only knows comics never imports a video plugin as a comic source.

```json
{
  "$schema": "vcvideo/1",
  "name": "示例视频订阅",
  "author": {"name": "Maintainer", "url": "https://github.com/maintainer"},
  "homepage": "https://github.com/maintainer/vcvideo",
  "plugins": [
    {"type": "js", "key": "demo_anime", "name": "示例番剧站", "version": "0.1.0",
     "url": "demo_anime.js", "sha256": "…64 位小写十六进制…", "minCoreVersion": "1.0.0"},
    {"type": "kazumi-rule", "key": "kz_AGE", "name": "AGE", "version": "1.6",
     "url": "https://raw.githubusercontent.com/Predidit/KazumiRules/main/AGE.json"}
  ]
}
```

Rules (see `subscription/vcvideo.go`):

- `$schema` must be the string `vcvideo/1`, and `name` is required. `$schema` is a fixed identifier, not a URL, so a GitHub code search can find third-party indexes; repositories that publish one should also add the topic `vcvideo`.
- `plugins[].type` must be `js` (installed as `video-js`) or `kazumi-rule`; any other value makes the whole index invalid.
- `key`, `name`, `version` and `url` are required. `url` may be relative and is resolved against the index URL; only http(s) is accepted, without credentials.
- `sha256` is optional; when present the file the client downloads must match it.
- On install, the key the script declares must equal the entry's `key`.
- At most 2000 plugins; any invalid entry makes the whole index invalid.

## 5. Writing a video source in JS (mirrors comic sources)

The same globals are available: `sendMessage`, `Network`, `fetch`, `Convert`, `HtmlDocument`, `UI`, `APP`, `log`. The `VideoSource` base class and three data shapes, `Video`, `VideoDetails` and `PlayInfo`, are added (see `comic/js/video-prelude.js`).

```js
class DemoAnime extends VideoSource {
  name = "示例番剧站"
  key = "demo_anime"          // ^[a-zA-Z0-9_]+$,与漫画源同规则
  version = "0.1.0"
  minCoreVersion = "1.0.0"
  url = "https://…/demo_anime.js"   // 更新地址

  search = {
    load: async (keyword, options, page) => ({
      videos: [new Video({id, title, cover, subtitle, tags})],
      maxPage: 3,
    }),
    // 或 loadNext(keyword, options, next) => {videos, next},与漫画源一致
  }

  video = {
    // 详情:多条线路,每条线路一组剧集
    loadInfo: async (id) => new VideoDetails({
      title, cover, description, tags,
      lines: [{id: "line1", name: "线路一", episodes: [{id: "ep1", title: "第 1 集"}]}],
    }),

    // 播放:返回可直接播放的地址,拿不到时交给宿主嗅探
    loadPlay: async (id, lineId, episodeId) => new PlayInfo({
      url: "https://…/index.m3u8",
      format: "hls",                 // hls | mp4 | dash | auto
      headers: {Referer: "https://…"},
      hls: {filterAds: true},        // 交给 Core 的 HLS 改写
      // 或:sniff: {page: "https://…/play/1", match: "\\.m3u8", timeoutMs: 15000}
    }),
  }
}
```

Design principles:

- **Function signatures and result shapes mirror comic sources** (`search.load`, `loadNext`, `loadInfo`); only "chapter → images" becomes "line → episode → play URL". Anyone who can write a comic source can write a video source.
- When `loadPlay` cannot get the URL it returns `sniff`, and the client opens a WebView to sniff it; scripts never drive the WebView themselves.
- `minCoreVersion` is compared with Core's API version, whereas a comic source's `minAppVersion` is compared with the client version.
- Every play result is validated: only http(s) URLs and well-formed headers are accepted; anything else is an error.

## 6. What the client provides

| Capability | How | Notes |
|---|---|---|
| HTTP | `host.Transport` | Core builds requests and the client sends them; each plugin has its own cookie partition |
| Human verification | `host.Verifier` | Completes Cloudflare challenges in a WebView and returns the WebView's User-Agent |
| WebView sniffing | Handles `sniff` in play results | Loads the page, intercepts media requests matching a regular expression and obtains the play URL; can follow iframes |
| Playback | The client's player | AVPlayer on iOS; mpv, libVLC or similar on the desktop |
| Local HLS proxy | The client | Injecting Referer and cookies needs a local proxy, because AVPlayer cannot add custom headers to each segment |

Core handles everything that needs no network:

- `video/hls`: m3u8 parsing and rewriting, filtering inserted ads, removing segment prefixes, picking one variant from a master playlist, rewriting segment URLs to the client's proxy, AES-128 decryption.
- `video/kazumi`: the Kazumi rule engine; it takes HTML or JSON text and produces structured results.

## 7. Code layout

```
VeneraCat Core (Go)
├─ host          client interfaces and URL/header validation
├─ challenge     Cloudflare detection and retry after verification
├─ attribution   authors and homepages
├─ comic/js      MoeJS runtime, init.js, VideoSource prelude
├─ comic/...     convert, html, image, hostapi (http/cookie), version
├─ video         video data model and play result validation
├─ video/kazumi  Kazumi rule engine
├─ video/builtin built-in providers (Hongguo)
├─ video/hls     HLS tools
├─ subscription  comic index.json and vcvideo.json
├─ plugin        unified plugin registry
├─ backup        .venera reading and restore
└─ mobile        gomobile API for Swift
```

## 8. Decided and later work

Decided:

1. Video sources use a separate `vcvideo.json` and are not mixed with comic sources.
2. KazumiRules is supported directly; 85 of the 86 published rules import (the one that fails is deprecated and lacks a required selector).

Later phases:

1. AniBaka's `anx-rule/2` pipeline rules: the most capable, but with 30-odd operations they are a lot of work.
2. Danmaku sources (DanDanPlay, Bangumi and others) and Bangumi sync.
3. Video download and offline cache.

## 9. Authors and homepages (all plugins)

Every plugin can declare authors and a homepage. Both are **optional**: when one is missing the client shows "no author/homepage given" and the install is not rejected.

In the script (the same for comic and video sources):

```js
class Demo extends VideoSource {
  name = "Demo"; key = "demo"; version = "1.0.0"
  author = [{name: "Alice", url: "https://github.com/alice"}, "Bob"]   // 字符串、对象或二者组成的数组
  homepage = "https://github.com/alice/demo-source"
}
```

In subscription entries (both comic `index.json` entries and `vcvideo.json` plugin entries support it; the top level of `vcvideo.json` can also name the index maintainer):

```json
{"name": "Demo", "key": "demo", "version": "1.0.0", "fileName": "demo.js",
 "author": {"name": "Alice", "url": "https://github.com/alice"},
 "homepage": "https://github.com/alice/demo-source"}
```

The example above is a comic `index.json` entry; a `vcvideo.json` entry also needs `type` and `url`, see section 4. Kazumi rule files may add `author` and `homepage` too; Kazumi ignores unknown fields, so such rules stay compatible with Kazumi.

Rules:

- What the script (or rule) declares wins, and the subscription entry fills in what is missing; authors and homepage are merged separately.
- `url` and `homepage` accept only http(s) without credentials; invalid parts are dropped with a warning and do not block the install.
- At most 16 authors, each name at most 100 characters.
- Built-in providers credit the project they were rewritten from; for example Hongguo credits 真果鉴 by Xanerix.
