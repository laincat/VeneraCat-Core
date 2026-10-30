# sendMessage 接口规格(Go 版 Core 与 init.js 重写依据)

[English](SENDMESSAGE-SPEC.en.md) | 简体中文

日期:2026-10-29。来源:官方 `venera-app/venera_core@50711c98`
(`lib/src/comic_source/init_js.dart` SHA-256 `f037d902…ada94`,以及同目录
`js_engine.dart` 的 `_messageReceiver` / `_convert` / `handleHtmlCallback` /
`handleCookieCallback`)。所有"行号"指 `init_js.dart` 去掉 Dart 包装后的 JS 文本。

本文件是**行为规格**,不是官方代码的拷贝。"实现状态"一栏:
**Core** = 由 Go 版 Core 实现;**宿主** = 由各客户端(Swift/Go UI)实现,Core 只给请求计划或回调。

## 0. 总则

- `sendMessage(msg)` 是引擎注入的全局函数。`msg` 是对象,`msg.method` 选择处理器。
- 返回值可以是**同步值**或 **Promise**。官方里只有 `delay`、`http`、`compute`、
  需要用户交互的 `UI.*` 和剪贴板返回 Promise,其余同步。
- 字节数据在 JS 侧是 `ArrayBuffer`(也接受 `Uint8Array`、整数数组、字符串=UTF-8 字节)。
- 处理器返回一个 `Exception` 对象时,JS 侧**得到该对象作为值**,不会抛出
  (官方 `_missingMessageHandler` 的行为)。Go 版应明确改为 **抛出 Error**,并在兼容测试里记录这个偏差。
- 未知 `method`:官方返回 Exception(见上)。Go 版:抛出 `Error("No JavaScript message handler provided for method: X")`。
- 全局还有 `appVersion`(字符串),由引擎在 init 之前设置。

## 1. 纯 Core 方法(无外部依赖,Go 直接实现)

### 1.1 `delay` → Promise<void>
| 参数 | 类型 | 说明 |
|---|---|---|
| `time` | number | 毫秒,缺省 0 |

### 1.2 `uuid` → string
UUID v1(官方用 `Uuid().v1()`)。Go 版建议 v4,源脚本一般不依赖版本;兼容测试只检查格式。

### 1.3 `random` → number
| 参数 | 类型 | 说明 |
|---|---|---|
| `type` | `"int"` \| `"double"` | 缺省 `int` |
| `min`,`max` | number | 缺省 0 和 1 |
`min + (max-min)*rand()`;`int` 截断取整(**含 min、不含 max**)。

### 1.4 `log` → undefined
`level`(`info`/`warning`/`error`)、`title`、`content`。官方 Dart 端**忽略 level**,统一按 error 记录。
Go 版:保留 level,交宿主日志回调。

### 1.5 `convert` → 视 `type`
公共参数:`type`、`value`、`isEncode`(true=编码/加密/哈希)。出错时官方**吞掉异常并返回 null**。
| `type` | 参数 | 编码返回 | 解码返回 |
|---|---|---|---|
| `utf8` | value | ArrayBuffer | string |
| `gbk` | value | ArrayBuffer | string |
| `base64` | value | string | ArrayBuffer |
| `md5` `sha1` `sha256` `sha512` | value(字节) | ArrayBuffer | (无解码) |
| `hmac` | value, key, `hash`∈md5/sha1/sha256/sha512, `isString` | ArrayBuffer;`isString=true` 时为十六进制 string | — |
| `aes-ecb` | value, key | ArrayBuffer | ArrayBuffer |
| `aes-cbc` | value, key, iv | 同上 | 同上 |
| `aes-cfb` | value, key, iv, `blockSize` | 同上 | 同上 |
| `aes-ofb` | value, key, **iv**, `blockSize` | 同上 | 同上 |
| `rsa` | value, key(Base64 PKCS#8 私钥) | **null**(仅解密) | ArrayBuffer(PKCS#1 v1.5,按块) |
| 其他 | | 原样返回 `value` | |

已知偏差,必须在规格里钉死:
1. **AES 无填充。** 官方逐块处理,`result` 长度等于输入;输入不是块倍数时官方行为未定义。Go 版:要求块对齐,否则返回 null 并记日志。
2. **OFB 的 IV 缺口。** JS 辅助函数 `encryptAesOfb/decryptAesOfb(value, key, blockSize)` **不传 `iv`**,
   而 Dart 端对 `iv` 调 `_bytes()`,缺失时抛错并被吞为 null。即官方 OFB 实际上**不可用**。
   旧 Core 文档 `HOST-AES.md` 记录 Swift 当前用全零 IV。Go 版决定:保持"缺 IV → null",**不**悄悄用零 IV;
   同时给 JS 辅助函数增加可选第 4 个参数 `iv`(扩展,不破坏旧调用)。
3. **RSA 只有私钥解密**,加密永远 null。
4. **GBK** 官方用 `enough_convert`;Go 用 `golang.org/x/text/encoding/simplifiedchinese.GBK`。非法字节的替换行为需用夹具对比。
5. `hmac` 的未知 `hash` 官方抛错→null。

### 1.6 `html` → 见第 4 节(DOM 句柄协议)
### 1.7 `image` → 见第 5 节(像素缓冲协议)
### 1.8 `compute` → Promise<any>
| 参数 | 类型 | 说明 |
|---|---|---|
| `function` | string | **JS 源码字符串**,求值得到函数 |
| `args` | array | 展开为函数参数 |
官方在**同一引擎**的 microtask 里求值 `(${function})` 并调用,**并不是**独立线程
(注释"engine pool"与实现不符)。Go 版:先同样在同一 runtime 做;独立 worker 另行设计。
`function` 不是字符串/`args` 不是数组 → 抛 `Function must be a string` / `Args must be a list`。

## 2. 存储与设置(Core 定义语义,数据落地由宿主)

| method | 参数 | 返回 | 官方语义 |
|---|---|---|---|
| `load_data` | `key`(源 key), `data_key` | any \| null | 读 `source.data[data_key]`;源不存在 → null |
| `save_data` | `key`, `data_key`, `data` | undefined | **`data_key === "setting"` 时抛错**;否则写入并异步落盘 |
| `delete_data` | `key`, `data_key` | undefined | 删除并异步落盘 |
| `load_setting` | `key`, `setting_key` | any | 顺序:`data.settings[setting_key]` → `settings[setting_key].default` → 抛 `Setting not found: …` |
| `isLogged` | `key` | boolean | `data["account"] != null` |

存储数据是单个 JSON 对象/源(`<dataPath>/comic_source/<key>.data`)。**数字精度**:
旧 Rust Core 因为 JSON 数字被舍入出过缺陷(超 u64、长小数)。Go 版必须用 `json.RawMessage`/`json.Number`
保留数字文本,不得经 `float64` 中转。

## 3. 网络(**剥离给客户端**,Core 只定义信封)

### 3.1 `http` → Promise<{status, headers, body, error}>
请求参数:
| 参数 | 类型 | 说明 |
|---|---|---|
| `http_method` | string | 缺省 GET,转大写 |
| `url` | string | |
| `headers` | object | 缺 User-Agent(大小写不敏感)时由宿主补默认 UA |
| `data` | any | 数组→字节;其余原样 |
| `bytes` | boolean | true → `body` 为 ArrayBuffer,否则 UTF-8 字符串(宽松解码) |
| `extra` | object | 透传给宿主拦截器 |
响应:`status`(int)、`headers`(同名多值用 `,` 连接的单值)、`body`、`error`(字符串或 null)。
**不因 4xx/5xx 抛错**(`validateStatus: true`)。宿主出错时返回 `{status:null, error:"…"}`,JS 辅助函数再 `throw result.error`。

Go 版 Core 边界:Core 不发请求,调用宿主注入的 `Transport` 接口(Go 接口 / Swift 回调),
Core 负责:参数校验、默认 UA 注入点、`bytes` 模式、响应信封规范化、取消和超时。

### 3.2 `cookie` → 视 `function`
| `function` | 参数 | 返回 |
|---|---|---|
| `set` | `url`, `cookies:[{name,value,domain?,path?}]` | undefined |
| `get` | `url` | `[{name,value,domain,path,expires,max-age,secure,httpOnly,session}]` |
| `delete` | `url` | undefined(删除**该 URI 下全部** cookie) |
Cookie 存储与匹配归客户端;Core 只做信封校验。

## 4. HTML DOM 句柄协议(`method: "html"`)

文档以整数 `key` 标识;元素/节点是文档内的整数句柄(**只增不减**,`dispose` 才整体释放)。
官方限制:同时最多约 9 个文档(超出淘汰最早的)。
| `function` | 入参 | 返回 | 备注 |
|---|---|---|---|
| `parse` | `key`, `data`(html string) | null | 创建文档 |
| `dispose` | `key` | null | |
| `querySelector` / `querySelectorAll` | `key`, `query` | 句柄 / 句柄数组 / null | CSS 选择器;文档级用 `key` |
| `getElementById` | `key`, `id` | 句柄 \| null | |
| `dom_querySelector` / `dom_querySelectorAll` | `doc`, `key`(元素), `query` | 同上 | 元素级 |
| `getText` | `doc`, `key` | string | `element.text` = 递归文本 |
| `getAttributes` | `doc`, `key` | {name: value} | |
| `getInnerHTML` | `doc`, `key` | string | |
| `getChildren` | `doc`, `key` | 元素句柄数组 | 仅元素子节点 |
| `getNodes` | `doc`, `key` | **节点**句柄数组 | 含文本/注释节点;节点句柄与元素句柄是**两个独立编号空间** |
| `getParent` / `getPreviousSibling` / `getNextSibling` | `doc`, `key` | 句柄 \| null | 兄弟只看元素兄弟 |
| `getClassNames` | `doc`, `key` | string[] | |
| `getId` / `getLocalName` | `doc`, `key` | string \| null | |
| `node_text` | `doc`, `key`(节点) | string | |
| `node_type` | `doc`, `key` | `element`/`text`/`comment`/`document`/`unknown` | |
| `node_to_element` | `doc`, `key` | 元素句柄 \| null | |

**官方缺陷(必须保留兼容):** JS 辅助函数 `HtmlNode.toElement()` 发送的是 **`node_toElement`**(驼峰),
而 Dart 只处理 **`node_to_element`**(下划线)。所以官方这个 API 实际返回 null(落入 `return null`)。
旧 Core 已把两种拼写都当作别名。Go 版:**两种都接受**。
另一个缺口:`html` 消息中未知的 `function`,官方静默返回 null。Core 默认保持这一行为;`html.Store` 开启 `Strict` 时改为报错。

Go 实现:`golang.org/x/net/html` + `github.com/andybalholm/cascadia`(已验证可 `go get`)。
注意 Dart `html` 包与 x/net/html 在**畸形 HTML 的容错树**和 `.text` 对 `<script>/<style>` 的处理上可能有差异,
需要夹具对比,不能默认等价。

## 5. 像素缓冲协议(`method: "image"`,仅用于 `modifyImage`)

官方 JS 侧 `Image` 类包装整数 `key`;**Dart 的 `_messageReceiver` 没有 `image` 分支**,
实现在宿主的另一条路径(`onImageLoad` 返回的 `modifyImage` 脚本在**独立 Isolate**里运行)。
即 `image.*` 不属于主引擎的 sendMessage 集合。
| `function` | 参数 | 返回 |
|---|---|---|
| `copyRange` | `key`, `x`,`y`,`width`,`height` | 新 key \| null |
| `copyAndRotate90` | `key` | 新 key \| null |
| `fillImageAt` | `key`, `x`,`y`, `image`(key) | undefined |
| `fillImageRangeAt` | `key`, `x`,`y`, `image`, `srcX`,`srcY`,`width`,`height` | undefined |
| `getWidth` / `getHeight` | `key` | int |
| `emptyImage` | `width`,`height` | 新 key |
Go 版:Core 提供**纯内存位图**(RGBA)和这组操作;图片解码/编码留给客户端(上一轮已决定)。
`modifyImage` 脚本在一个**全新的 runtime** 执行(MoeJS `NewRuntime` 很便宜,1.4 µs)。

## 6. 环境与 UI(**宿主**)

| method | 参数 | 返回 | 归属 |
|---|---|---|---|
| `getLocale` | — | string(如 `zh_CN`,下划线格式) | 宿主 |
| `getPlatform` | — | `android`/`ios`/`windows`/`macos`/`linux` | 宿主 |
| `setClipboard` | `text` | Promise<void> | 宿主 |
| `getClipboard` | — | Promise<string> | 宿主 |
| `UI` / `showMessage` | `message` | undefined | 宿主;空串忽略 |
| `UI` / `showDialog` | `title`,`content`,`actions:[{text,callback,style}]` | Promise<void> | 宿主;`callback` 是 **JS 函数句柄**,点击后回调进 runtime |
| `UI` / `launchUrl` | `url` | undefined | 宿主;空串忽略 |
| `UI` / `showLoading` | `onCancel`(函数\|null) | int(id) | 宿主 |
| `UI` / `cancelLoading` | `id` | undefined | 宿主;id 非 int 忽略 |
| `UI` / `showInputDialog` | `title`,`validator`(函数\|null),`image` | Promise<string\|null> | 宿主 |
| `UI` / `showSelectDialog` | `title`,`options:string[]`,`initialIndex` | Promise<int\|null> | 宿主 |
没有注入 UI 处理器时,官方返回 Exception;Go 版:抛 `No JavaScript UI handler provided`。
**含函数参数的消息**要求引擎能把 JS 函数对象交给宿主并稍后调用(MoeJS 的 `Value` + `CallFunction` 可以做到,
但必须在 runtime 所属 goroutine 上调用 → 走 event loop 的 `RunOnLoop`)。

## 7. 源加载协议(宿主调用 runtime,不是 sendMessage)

官方 `ComicSourceParser` 的做法,是**拼接 JS 字符串求值**。Go 版应改为结构化调用,但保持行为:
1. 取第一行以 `class ` 开头且含 `extends ComicSource` 的行,得到类名。(源文件格式约束)
2. 在一个 IIFE 里执行整份源码并 `new ClassName()`;读 `name/key/version/minAppVersion/url`。
3. `key` 必须匹配 `^[a-zA-Z0-9_]+$`;`name/key/version` 必填;`minAppVersion` 大于应用版本则拒绝。
4. 注册到 `ComicSource.sources[key]`;读 `data`;若存在 `init` 则 `await init()`。
5. 之后所有功能调用形如 `ComicSource.sources[key].<path>(args)`:
   `search.load(keyword, option, page)` / `search.loadNext(keyword, option, next)`、
   `comic.loadInfo(id)`、`comic.loadEp(id, ep)`→`{images}`、`comic.loadThumbnails(id,next)`、
   `comic.onImageLoad(imageKey, comicId, ep)`、`comic.onThumbnailLoad(imageKey)`、
   `explore[i].load(page|next)`、`category*`、`favorites.*`、`account.*`、评论/点赞/投票等。
   参数以 **JSON 字面量**传入(官方用 `jsonEncode`);Go 版用 `rt.ParseJSON` 直接构造值。

---

# init.js 重写范围

官方 `init.js` 共 **849 行有效代码**(去注释/空行)。分类如下:

| 模块 | 行 | 性质 | 重写难度 |
|---|--:|---|---|
| `Convert`(22 个辅助函数) | 204 | 对 `convert` 消息的薄包装 | 低,机械。**注意 OFB 缺 `iv`** |
| `HtmlDocument` / `HtmlElement` / `HtmlNode` | 209 | 句柄对象 + 属性 getter 转 `html` 消息 | 低,可用循环/表驱动生成,**行数可压到约 1/3** |
| `Network` + `fetch` + `Cookie` | 93 | 请求/Cookie 包装 | 低。保留 `get/delete` 把第 3 参数当 `extra` 的**官方怪癖**(见下) |
| `Image` | 75 | 位图句柄包装 | 低 |
| `UI` / `APP` / 剪贴板 / `compute` | 90 | 薄包装 | 低 |
| `ComicSource` 基类 | 49 | `loadData/saveData/loadSetting/isLogged/translate` | 低 |
| `Comic`/`ComicDetails`/`Comment`/`ImageLoadingConfig` | 55 | **数据形状**(字段名即接口) | 低,但**字段名必须逐字一致** |
| `setTimeout`/`setInterval`/`_Timer`/`uuid`/`random`/`log`/`console` | 74 | 小工具 | 低 |

结论:**全部可以重写,没有一块需要照抄。** 可以重写的原因是:
- 对外接口是 `sendMessage` 的**消息字典**(上面第 1–6 节)和**全局名字**(`Convert`、`Network`、`HtmlDocument`、
  `ComicSource`、`Comic`、`UI`、`APP`、`log`、`console`、`fetch`、`setTimeout`、`setInterval`、`compute`、`randomInt` 等),
  这些是源脚本依赖的事实接口,不属于某段表达。
- 没有复杂算法,绝大部分是"把参数装进字典再发出去"。

## 必须逐字保持的接口(兼容红线)

1. 全局名与方法名:完整列表见测试 `comic/js/init_surface_test.go`(Convert 22 个方法、HtmlElement 12 个属性等)。
2. 消息字典字段名,尤其是官方拼写不一致处:`http_method`、`data_key`、`setting_key`、`isEncode`、`isString`、`blockSize`、`node_toElement`。
3. 数据类构造器的**参数名**(解构参数):`Comic({id,title,subtitle,subTitle,cover,tags,description,maxPage,language,favoriteId,stars})`,
   `ComicDetails` 里 `subtitle ?? subTitle` 的取值顺序,`Comment` 字段等。
4. `ComicSource` 的实例字段默认值(`name=""`、`key=""`、`translation={}`)和静态 `ComicSource.sources = {}`。

## 官方怪癖(重写时要主动决定:保留还是修)

| # | 怪癖 | 建议 |
|---|---|---|
| 1 | `Network.get(url, headers, extra)` 实际调用 `sendRequest('GET', url, headers, extra)`:**把 `extra` 当成了 `data`**,`extra` 丢失。`delete` 同。 | **保留**(源脚本可能已依赖);同时加测试固定 |
| 2 | `setTimeout` 不返回句柄,**没有 `clearTimeout`/`clearInterval`**;`setInterval` 返回 `_Timer`,只有 `.cancel()`。 | 保留;另外**额外提供** `clearTimeout`/`clearInterval`(纯扩展) |
| 3 | `setInterval` 先立即执行一次回调再计时;延迟到点后再重排。 | 保留 |
| 4 | OFB 不传 IV(上文 1.5)。 | 加可选 `iv` 参数,不改旧行为 |
| 5 | `node_toElement` 拼写不一致导致 `HtmlNode.toElement()` 恒为 null。 | 修复:两种拼写都接受 |
| 6 | `console.*` 只接受**一个**参数。 | 保留,可额外 join 多参数 |
| 7 | `fetch()` 返回对象的 `statusText` 恒为空串,`json()` 经 `Convert.decodeUtf8`。 | 保留 |
| 8 | `HtmlDocument` 的 `key` 是全局递增静态计数,不复用。 | 保留 |

## 重写流程(避免"改写即派生")

1. 由**一个人/一个会话**只读本规格 + Dart 处理端的行为,写 `init.js`;写的过程**不打开官方 `init_js.dart` 的 JS 文本**。
2. 用**现有真实 legacy 源**和 `fixtures/v1/persisted-source.js` 做验收:全局名缺失会直接 `ReferenceError`。
3. 加一个**反射测试**:枚举新 `init.js` 暴露的全局和方法名,与 `comic/js/init_surface_test.go` 中的名字表比对。
4. 对比官方与新实现对同一组消息的返回(差分测试),差异必须落在上表"怪癖"列。

## 许可证

决定用 **GPL-3.0**(2026-10-29)。即使重写,仍与官方 GPL 兼容;重写的价值在于
(a) 将来想换宽松许可证时没有阻碍,(b) 摆脱官方的拼写缺陷和结构。
