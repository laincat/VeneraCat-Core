# sendMessage interface specification (basis for the Go Core and the init.js rewrite)

English | [简体中文](SENDMESSAGE-SPEC.md)

Date: 2026-10-29. Source: official `venera-app/venera_core@50711c98`
(`lib/src/comic_source/init_js.dart` SHA-256 `f037d902…ada94`, plus `_messageReceiver` / `_convert` / `handleHtmlCallback` /
`handleCookieCallback` in `js_engine.dart` in the same directory). All "line numbers" refer to the JS text of `init_js.dart` with the Dart wrapper removed.

This file is a **behavioural specification**, not a copy of the official code. In the "implementation status" column:
**Core** = implemented by the Go Core; **host** = implemented by each client (Swift/Go UI), with Core supplying only a request plan or a callback.

## 0. General rules

- `sendMessage(msg)` is a global function injected by the engine. `msg` is an object; `msg.method` selects the handler.
- The return value can be a **synchronous value** or a **Promise**. In the official implementation only `delay`, `http`, `compute`,
  the `UI.*` calls that need user interaction, and the clipboard return a Promise; everything else is synchronous.
- Byte data on the JS side is an `ArrayBuffer` (a `Uint8Array`, an integer array, or a string = UTF-8 bytes are also accepted).
- When a handler returns an `Exception` object, the JS side **receives that object as a value** and nothing is thrown
  (the behaviour of the official `_missingMessageHandler`). The Go version should explicitly change this to **throwing an Error**, and record this deviation in the compatibility tests.
- Unknown `method`: the official implementation returns an Exception (see above). Go version: throw `Error("No JavaScript message handler provided for method: X")`.
- There is also a global `appVersion` (string), set by the engine before init.

## 1. Pure Core methods (no external dependencies, implemented directly in Go)

### 1.1 `delay` → Promise<void>
| Parameter | Type | Description |
|---|---|---|
| `time` | number | Milliseconds, default 0 |

### 1.2 `uuid` → string
UUID v1 (the official implementation uses `Uuid().v1()`). For the Go version v4 is recommended; source scripts generally do not depend on the version, and the compatibility tests check only the format.

### 1.3 `random` → number
| Parameter | Type | Description |
|---|---|---|
| `type` | `"int"` \| `"double"` | Default `int` |
| `min`,`max` | number | Default 0 and 1 |
`min + (max-min)*rand()`; `int` truncates to an integer (**min inclusive, max exclusive**).

### 1.4 `log` → undefined
`level` (`info`/`warning`/`error`), `title`, `content`. The official Dart side **ignores level** and logs everything as error.
Go version: keep level and pass it to the host's log callback.

### 1.5 `convert` → depends on `type`
Common parameters: `type`, `value`, `isEncode` (true = encode/encrypt/hash). On error the official implementation **swallows the exception and returns null**.
| `type` | Parameters | Encode returns | Decode returns |
|---|---|---|---|
| `utf8` | value | ArrayBuffer | string |
| `gbk` | value | ArrayBuffer | string |
| `base64` | value | string | ArrayBuffer |
| `md5` `sha1` `sha256` `sha512` | value (bytes) | ArrayBuffer | (no decode) |
| `hmac` | value, key, `hash`∈md5/sha1/sha256/sha512, `isString` | ArrayBuffer; a hexadecimal string when `isString=true` | — |
| `aes-ecb` | value, key | ArrayBuffer | ArrayBuffer |
| `aes-cbc` | value, key, iv | Same as above | Same as above |
| `aes-cfb` | value, key, iv, `blockSize` | Same as above | Same as above |
| `aes-ofb` | value, key, **iv**, `blockSize` | Same as above | Same as above |
| `rsa` | value, key (Base64 PKCS#8 private key) | **null** (decrypt only) | ArrayBuffer (PKCS#1 v1.5, block by block) |
| Other | | Returns `value` unchanged | |

Known deviations, which must be pinned down in the specification:
1. **AES has no padding.** The official implementation processes block by block, and the `result` length equals the input length; when the input is not a multiple of the block size the official behaviour is undefined. Go version: require block alignment, otherwise return null and log.
2. **The OFB IV gap.** The JS helper functions `encryptAesOfb/decryptAesOfb(value, key, blockSize)` **do not pass `iv`**,
   while the Dart side calls `_bytes()` on `iv`, which throws when it is missing, and the error is swallowed into null. So official OFB is effectively **unusable**.
   The old Core document `HOST-AES.md` records that Swift currently uses an all-zero IV. Go version decision: keep "missing IV → null" and do **not** silently use a zero IV;
   also add an optional 4th parameter `iv` to the JS helper functions (an extension that does not break old calls).
3. **RSA supports only private-key decryption**; encryption is always null.
4. **GBK**: the official implementation uses `enough_convert`; Go uses `golang.org/x/text/encoding/simplifiedchinese.GBK`. The replacement behaviour for invalid bytes must be compared using fixtures.
5. For `hmac`, an unknown `hash` throws in the official implementation → null.

### 1.6 `html` → see section 4 (DOM handle protocol)
### 1.7 `image` → see section 5 (pixel buffer protocol)
### 1.8 `compute` → Promise<any>
| Parameter | Type | Description |
|---|---|---|
| `function` | string | **A JS source string**, evaluated to obtain a function |
| `args` | array | Spread as the function's arguments |
The official implementation evaluates `(${function})` and calls it in a microtask on the **same engine**; it is **not** a separate thread
(the "engine pool" comment does not match the implementation). Go version: first do the same thing in the same runtime; a separate worker will be designed separately.
If `function` is not a string or `args` is not an array → throw `Function must be a string` / `Args must be a list`.

## 2. Storage and settings (Core defines the semantics, the host persists the data)

| method | Parameters | Returns | Official semantics |
|---|---|---|---|
| `load_data` | `key` (source key), `data_key` | any \| null | Reads `source.data[data_key]`; source does not exist → null |
| `save_data` | `key`, `data_key`, `data` | undefined | **Throws when `data_key === "setting"`**; otherwise writes and persists to disk asynchronously |
| `delete_data` | `key`, `data_key` | undefined | Deletes and persists to disk asynchronously |
| `load_setting` | `key`, `setting_key` | any | Order: `data.settings[setting_key]` → `settings[setting_key].default` → throw `Setting not found: …` |
| `isLogged` | `key` | boolean | `data["account"] != null` |

Stored data is a single JSON object per source (`<dataPath>/comic_source/<key>.data`). **Number precision**:
the old Rust Core had bugs because JSON numbers were rounded (values beyond u64, long decimals). The Go version must use `json.RawMessage`/`json.Number`
to keep the number text, and must not pass it through `float64`.

## 3. Networking (**split out to the client**, Core defines only the envelope)

### 3.1 `http` → Promise<{status, headers, body, error}>
Request parameters:
| Parameter | Type | Description |
|---|---|---|
| `http_method` | string | Default GET, converted to upper case |
| `url` | string | |
| `headers` | object | If User-Agent is missing (case-insensitive), the host adds a default UA |
| `data` | any | Array → bytes; anything else passed as is |
| `bytes` | boolean | true → `body` is an ArrayBuffer, otherwise a UTF-8 string (lenient decoding) |
| `extra` | object | Passed through to the host interceptor |
Response: `status` (int), `headers` (multiple values for the same name joined with `,` into a single value), `body`, `error` (string or null).
**Does not throw on 4xx/5xx** (`validateStatus: true`). When the host fails it returns `{status:null, error:"…"}`, and the JS helper function then does `throw result.error`.

Go Core boundary: Core does not send requests; it calls the `Transport` interface injected by the host (Go interface / Swift callback).
Core is responsible for: parameter validation, the default UA injection point, `bytes` mode, response envelope normalisation, cancellation and timeouts.

### 3.2 `cookie` → depends on `function`
| `function` | Parameters | Returns |
|---|---|---|
| `set` | `url`, `cookies:[{name,value,domain?,path?}]` | undefined |
| `get` | `url` | `[{name,value,domain,path,expires,max-age,secure,httpOnly,session}]` |
| `delete` | `url` | undefined (deletes **all** cookies under that URI) |
Cookie storage and matching belong to the client; Core only validates the envelope.

## 4. HTML DOM handle protocol (`method: "html"`)

A document is identified by an integer `key`; elements/nodes are integer handles within the document (**they only increase, never decrease**; only `dispose` releases them all at once).
Official limit: at most about 9 documents at the same time (beyond that the oldest is evicted).
| `function` | Input | Returns | Notes |
|---|---|---|---|
| `parse` | `key`, `data` (html string) | null | Creates a document |
| `dispose` | `key` | null | |
| `querySelector` / `querySelectorAll` | `key`, `query` | handle / handle array / null | CSS selector; document level uses `key` |
| `getElementById` | `key`, `id` | handle \| null | |
| `dom_querySelector` / `dom_querySelectorAll` | `doc`, `key` (element), `query` | Same as above | Element level |
| `getText` | `doc`, `key` | string | `element.text` = recursive text |
| `getAttributes` | `doc`, `key` | {name: value} | |
| `getInnerHTML` | `doc`, `key` | string | |
| `getChildren` | `doc`, `key` | array of element handles | Element children only |
| `getNodes` | `doc`, `key` | array of **node** handles | Includes text/comment nodes; node handles and element handles are **two independent numbering spaces** |
| `getParent` / `getPreviousSibling` / `getNextSibling` | `doc`, `key` | handle \| null | Siblings consider element siblings only |
| `getClassNames` | `doc`, `key` | string[] | |
| `getId` / `getLocalName` | `doc`, `key` | string \| null | |
| `node_text` | `doc`, `key` (node) | string | |
| `node_type` | `doc`, `key` | `element`/`text`/`comment`/`document`/`unknown` | |
| `node_to_element` | `doc`, `key` | element handle \| null | |

**Official bug (compatibility must be kept):** the JS helper function `HtmlNode.toElement()` sends **`node_toElement`** (camel case),
while Dart handles only **`node_to_element`** (underscore). So this official API actually returns null (it falls through to `return null`).
The old Core already treats both spellings as aliases. Go version: **accept both**.
Another gap: for an unknown `function` in an `html` message, the official implementation silently returns null. Core keeps this behaviour by default; with `Strict` set on `html.Store` it returns an error instead.

Go implementation: `golang.org/x/net/html` + `github.com/andybalholm/cascadia` (verified to work with `go get`).
Note that the Dart `html` package and x/net/html may differ in the **error-tolerant tree built for malformed HTML** and in how `.text` handles `<script>/<style>`;
this needs fixture comparison and must not be assumed equivalent.

## 5. Pixel buffer protocol (`method: "image"`, used only by `modifyImage`)

The official JS-side `Image` class wraps an integer `key`; **Dart's `_messageReceiver` has no `image` branch**,
and the implementation is on a different path in the host (the `modifyImage` script returned by `onImageLoad` runs in a **separate Isolate**).
So `image.*` is not part of the main engine's sendMessage set.
| `function` | Parameters | Returns |
|---|---|---|
| `copyRange` | `key`, `x`,`y`,`width`,`height` | new key \| null |
| `copyAndRotate90` | `key` | new key \| null |
| `fillImageAt` | `key`, `x`,`y`, `image` (key) | undefined |
| `fillImageRangeAt` | `key`, `x`,`y`, `image`, `srcX`,`srcY`,`width`,`height` | undefined |
| `getWidth` / `getHeight` | `key` | int |
| `emptyImage` | `width`,`height` | new key |
Go version: Core provides a **pure in-memory bitmap** (RGBA) and this set of operations; image decoding/encoding is left to the client (decided in the previous round).
The `modifyImage` script runs in a **brand-new runtime** (MoeJS `NewRuntime` is cheap, 1.4 µs).

## 6. Environment and UI (**host**)

| method | Parameters | Returns | Owner |
|---|---|---|---|
| `getLocale` | — | string (e.g. `zh_CN`, underscore format) | host |
| `getPlatform` | — | `android`/`ios`/`windows`/`macos`/`linux` | host |
| `setClipboard` | `text` | Promise<void> | host |
| `getClipboard` | — | Promise<string> | host |
| `UI` / `showMessage` | `message` | undefined | host; empty string ignored |
| `UI` / `showDialog` | `title`,`content`,`actions:[{text,callback,style}]` | Promise<void> | host; `callback` is a **JS function handle**, called back into the runtime after a click |
| `UI` / `launchUrl` | `url` | undefined | host; empty string ignored |
| `UI` / `showLoading` | `onCancel` (function\|null) | int (id) | host |
| `UI` / `cancelLoading` | `id` | undefined | host; ignored if id is not an int |
| `UI` / `showInputDialog` | `title`,`validator` (function\|null),`image` | Promise<string\|null> | host |
| `UI` / `showSelectDialog` | `title`,`options:string[]`,`initialIndex` | Promise<int\|null> | host |
When no UI handler is injected, the official implementation returns an Exception; Go version: throw `No JavaScript UI handler provided`.
**Messages with function parameters** require the engine to be able to hand a JS function object to the host and call it later (MoeJS `Value` + `CallFunction` can do this,
but the call must happen on the goroutine that owns the runtime → go through the event loop's `RunOnLoop`).

## 7. Source loading protocol (the host calls the runtime; this is not sendMessage)

The official `ComicSourceParser` works by **concatenating JS strings and evaluating them**. The Go version should switch to structured calls but keep the behaviour:
1. Take the first line that starts with `class ` and contains `extends ComicSource` to get the class name. (A source file format constraint.)
2. Run the whole source inside an IIFE and `new ClassName()`; read `name/key/version/minAppVersion/url`.
3. `key` must match `^[a-zA-Z0-9_]+$`; `name/key/version` are required; reject if `minAppVersion` is greater than the app version.
4. Register in `ComicSource.sources[key]`; read `data`; if `init` exists, `await init()`.
5. After that every feature call has the form `ComicSource.sources[key].<path>(args)`:
   `search.load(keyword, option, page)` / `search.loadNext(keyword, option, next)`,
   `comic.loadInfo(id)`, `comic.loadEp(id, ep)`→`{images}`, `comic.loadThumbnails(id,next)`,
   `comic.onImageLoad(imageKey, comicId, ep)`, `comic.onThumbnailLoad(imageKey)`,
   `explore[i].load(page|next)`, `category*`, `favorites.*`, `account.*`, comments/likes/votes, etc.
   Arguments are passed as **JSON literals** (the official implementation uses `jsonEncode`); the Go version builds the values directly with `rt.ParseJSON`.

---

# init.js rewrite scope

The official `init.js` has **849 lines of effective code** in total (excluding comments/blank lines). Breakdown:

| Module | Lines | Nature | Rewrite difficulty |
|---|--:|---|---|
| `Convert` (22 helper functions) | 204 | Thin wrapper over the `convert` message | Low, mechanical. **Note that OFB lacks `iv`** |
| `HtmlDocument` / `HtmlElement` / `HtmlNode` | 209 | Handle objects + property getters translated into `html` messages | Low; can be generated with loops/table-driven code, **line count can be cut to about 1/3** |
| `Network` + `fetch` + `Cookie` | 93 | Request/Cookie wrappers | Low. Keep the **official quirk** where `get/delete` treat the 3rd argument as `extra` (see below) |
| `Image` | 75 | Bitmap handle wrapper | Low |
| `UI` / `APP` / clipboard / `compute` | 90 | Thin wrappers | Low |
| `ComicSource` base class | 49 | `loadData/saveData/loadSetting/isLogged/translate` | Low |
| `Comic`/`ComicDetails`/`Comment`/`ImageLoadingConfig` | 55 | **Data shapes** (the field names are the interface) | Low, but **field names must match exactly** |
| `setTimeout`/`setInterval`/`_Timer`/`uuid`/`random`/`log`/`console` | 74 | Small utilities | Low |

Conclusion: **everything can be rewritten; no part needs to be copied verbatim.** The reasons it can be rewritten:
- The external interface is the **message dictionary** of `sendMessage` (sections 1–6 above) and the **global names** (`Convert`, `Network`, `HtmlDocument`,
  `ComicSource`, `Comic`, `UI`, `APP`, `log`, `console`, `fetch`, `setTimeout`, `setInterval`, `compute`, `randomInt`, etc.).
  These are the de facto interface that source scripts depend on, and do not belong to any particular piece of expression.
- There are no complex algorithms; the vast majority is "put the arguments into a dictionary and send it".

## Interfaces that must be kept exactly (compatibility red lines)

1. Global names and method names: the complete list is in the test `comic/js/init_surface_test.go` (22 methods on Convert, 12 HtmlElement properties, etc.).
2. Message dictionary field names, especially where the official spelling is inconsistent: `http_method`, `data_key`, `setting_key`, `isEncode`, `isString`, `blockSize`, `node_toElement`.
3. The **parameter names** (destructured parameters) of the data class constructors: `Comic({id,title,subtitle,subTitle,cover,tags,description,maxPage,language,favoriteId,stars})`,
   the lookup order `subtitle ?? subTitle` in `ComicDetails`, the `Comment` fields, etc.
4. The default values of `ComicSource` instance fields (`name=""`, `key=""`, `translation={}`) and the static `ComicSource.sources = {}`.

## Official quirks (the rewrite must actively decide: keep or fix)

| # | Quirk | Recommendation |
|---|---|---|
| 1 | `Network.get(url, headers, extra)` actually calls `sendRequest('GET', url, headers, extra)`: **`extra` is treated as `data`**, and `extra` is lost. Same for `delete`. | **Keep** (source scripts may already depend on it); also add a test to pin it down |
| 2 | `setTimeout` returns no handle, and **there is no `clearTimeout`/`clearInterval`**; `setInterval` returns a `_Timer` that has only `.cancel()`. | Keep; **additionally provide** `clearTimeout`/`clearInterval` (a pure extension) |
| 3 | `setInterval` runs the callback once immediately before starting the timer; once the delay elapses it reschedules. | Keep |
| 4 | OFB does not pass the IV (section 1.5 above). | Add an optional `iv` parameter, without changing the old behaviour |
| 5 | The inconsistent `node_toElement` spelling makes `HtmlNode.toElement()` always null. | Fix: accept both spellings |
| 6 | `console.*` accepts only **one** argument. | Keep; optionally also join multiple arguments |
| 7 | The `statusText` of the object returned by `fetch()` is always an empty string, and `json()` goes through `Convert.decodeUtf8`. | Keep |
| 8 | The `key` of `HtmlDocument` is a global, incrementing static counter and is not reused. | Keep |

## Rewrite process (avoiding "rewriting = derivative work")

1. **One person/one session** reads only this specification + the behaviour of the Dart handler side, and writes `init.js`; while writing, **do not open the JS text of the official `init_js.dart`**.
2. Use the **existing real legacy sources** and `fixtures/v1/persisted-source.js` for acceptance: a missing global name causes an immediate `ReferenceError`.
3. Add a **reflection test**: enumerate the globals and method names exposed by the new `init.js` and compare them with the name table in `comic/js/init_surface_test.go`.
4. Compare the official and new implementations' return values for the same set of messages (differential testing); every difference must fall within the "quirks" table above.

## License

Decided to use **GPL-3.0** (2026-10-29). Even after the rewrite it remains compatible with the official GPL; the value of the rewrite is that
(a) there is no obstacle if we later want to switch to a permissive license, and (b) it gets rid of the official spelling bugs and structure.
