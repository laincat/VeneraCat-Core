/*
 * VeneraCat Core: ComicSource runtime prelude.
 *
 * This file is original work. It was written from the interface specification
 * in docs/SENDMESSAGE-SPEC.md only; see NOTICE-init.md for provenance.
 *
 * The engine loads it before any ComicSource script. It expects the host to
 * have injected the global function `sendMessage(msg)` and the string
 * `appVersion`; neither is defined here. Every other global that sources rely
 * on is defined below, and all private helpers stay inside the closure.
 *
 * Conventions:
 *  - Optional message fields that the caller did not supply (undefined) are
 *    left out of the message instead of being sent as undefined.
 *  - `sendMessage` is looked up at call time, never captured.
 */
(function (root) {
  'use strict';

  // ---------------------------------------------------------------------
  // plumbing
  // ---------------------------------------------------------------------

  /** Send a message to the host. Looked up lazily so the host may swap it. */
  function post(msg) {
    return sendMessage(msg);
  }

  /** Copy `fields` onto `msg`, skipping keys whose value is undefined. */
  function fill(msg, fields) {
    for (const k of Object.keys(fields)) {
      if (fields[k] !== undefined) msg[k] = fields[k];
    }
    return msg;
  }

  /**
   * Define a global property. It stays configurable so that a source script
   * may still declare a top-level binding of the same name.
   */
  function publish(name, value) {
    Object.defineProperty(root, name, {
      value,
      writable: true,
      configurable: true,
      enumerable: false,
    });
  }

  /** Render any value as log text. */
  function asText(v) {
    if (typeof v === 'string') return v;
    if (v instanceof Error) return v.stack || String(v);
    if (v !== null && typeof v === 'object') {
      try {
        const s = JSON.stringify(v);
        if (s !== undefined) return s;
      } catch (_) {
        // fall through to String()
      }
    }
    return String(v);
  }

  // ---------------------------------------------------------------------
  // log, console
  // ---------------------------------------------------------------------

  function log(level, title, content) {
    post({ method: 'log', level, title, content });
  }

  const CONSOLE_TITLE = 'console';

  function consoleWriter(level) {
    // One argument is the contract; extra arguments are joined with a space.
    return function (...args) {
      post({
        method: 'log',
        level,
        title: CONSOLE_TITLE,
        content: args.map(asText).join(' '),
      });
    };
  }

  const consoleObject = {
    log: consoleWriter('info'),
    warn: consoleWriter('warning'),
    error: consoleWriter('error'),
  };

  // ---------------------------------------------------------------------
  // uuid, random
  // ---------------------------------------------------------------------

  function createUuid() {
    return post({ method: 'uuid' });
  }

  function randomInt(min, max) {
    return post(fill({ method: 'random', type: 'int' }, { min, max }));
  }

  function randomDouble(min, max) {
    return post(fill({ method: 'random', type: 'double' }, { min, max }));
  }

  // ---------------------------------------------------------------------
  // timers (built on the host `delay` message)
  // ---------------------------------------------------------------------

  let nextTimerId = 1;
  const liveTimers = new Map();

  class TimerHandle {
    constructor() {
      this.id = nextTimerId++;
      this.cancelled = false;
      liveTimers.set(this.id, this);
    }

    cancel() {
      this.cancelled = true;
      liveTimers.delete(this.id);
    }

    valueOf() {
      return this.id;
    }
  }

  function requireCallback(cb) {
    if (typeof cb !== 'function') {
      throw new TypeError('The callback must be a function');
    }
  }

  /** Run a timer callback; a throw is reported to the host log instead. */
  function guarded(cb) {
    try {
      cb();
    } catch (e) {
      log('error', 'timer callback', asText(e));
    }
  }

  /** Wait `ms` on the host, then call `then` unless the timer was cancelled. */
  function waitThen(timer, ms, then) {
    Promise.resolve(post({ method: 'delay', time: Number(ms) || 0 })).then(
      () => {
        if (!timer.cancelled) then();
      },
      (e) => {
        log('error', 'timer delay', asText(e));
      },
    );
  }

  function setTimeout(callback, delay) {
    requireCallback(callback);
    const timer = new TimerHandle();
    waitThen(timer, delay, () => {
      liveTimers.delete(timer.id);
      guarded(callback);
    });
    return timer;
  }

  function setInterval(callback, delay) {
    requireCallback(callback);
    const timer = new TimerHandle();
    // The first run happens now; the delay only starts after it.
    const tick = () => {
      guarded(callback);
      if (!timer.cancelled) waitThen(timer, delay, tick);
    };
    tick();
    return timer;
  }

  /** Shared by clearTimeout and clearInterval: accepts a handle or its id. */
  function clearTimer(handle) {
    const timer =
      handle instanceof TimerHandle ? handle : liveTimers.get(Number(handle));
    if (timer) timer.cancel();
  }

  // ---------------------------------------------------------------------
  // Convert
  // ---------------------------------------------------------------------

  // Each row: [mode suffix, convert type, parameter names after (value, key)].
  const AES_MODES = [
    ['Ecb', 'aes-ecb', []],
    ['Cbc', 'aes-cbc', ['iv']],
    ['Cfb', 'aes-cfb', ['iv', 'blockSize']],
    // OFB helpers historically took (value, key, blockSize); iv is an
    // optional trailing extra so that old calls keep their old meaning.
    ['Ofb', 'aes-ofb', ['blockSize', 'iv']],
  ];

  // Each row: [name, convert type, isEncode, parameter names, fixed fields].
  // Parameter names are the message field names, in call order.
  const CONVERT_TABLE = [
    ['encodeUtf8', 'utf8', true, ['value']],
    ['decodeUtf8', 'utf8', false, ['value']],
    ['encodeGbk', 'gbk', true, ['value']],
    ['decodeGbk', 'gbk', false, ['value']],
    ['encodeBase64', 'base64', true, ['value']],
    ['decodeBase64', 'base64', false, ['value']],
    ['md5', 'md5', true, ['value']],
    ['sha1', 'sha1', true, ['value']],
    ['sha256', 'sha256', true, ['value']],
    ['sha512', 'sha512', true, ['value']],
    ['hmac', 'hmac', true, ['key', 'value', 'hash'], { isString: false }],
    ['hmacString', 'hmac', true, ['key', 'value', 'hash'], { isString: true }],
    ['decryptRsa', 'rsa', false, ['value', 'key']],
  ];
  for (const [suffix, type, extra] of AES_MODES) {
    CONVERT_TABLE.push(['encryptAes' + suffix, type, true, ['value', 'key', ...extra]]);
    CONVERT_TABLE.push(['decryptAes' + suffix, type, false, ['value', 'key', ...extra]]);
  }

  function convertCall(type, isEncode, params, fixed) {
    return function (...args) {
      const msg = { method: 'convert', type, isEncode };
      params.forEach((name, i) => {
        if (args[i] !== undefined) msg[name] = args[i];
      });
      if (fixed) Object.assign(msg, fixed);
      return post(msg);
    };
  }

  /** View any byte container as a Uint8Array without copying when possible. */
  function byteView(v) {
    if (v instanceof ArrayBuffer) return new Uint8Array(v);
    if (ArrayBuffer.isView(v)) return new Uint8Array(v.buffer, v.byteOffset, v.byteLength);
    if (Array.isArray(v)) return Uint8Array.from(v);
    throw new TypeError('hexEncode expects an ArrayBuffer, a typed array or an array of bytes');
  }

  function hexEncode(value) {
    const bytes = byteView(value);
    let out = '';
    for (let i = 0; i < bytes.length; i++) {
      out += (bytes[i] < 16 ? '0' : '') + bytes[i].toString(16);
    }
    return out;
  }

  const Convert = { hexEncode };
  for (const [name, type, isEncode, params, fixed] of CONVERT_TABLE) {
    Convert[name] = convertCall(type, isEncode, params, fixed);
  }

  // ---------------------------------------------------------------------
  // Network, Cookie, fetch
  // ---------------------------------------------------------------------

  class Cookie {
    constructor({ name, value, domain } = {}) {
      this.name = name;
      this.value = value;
      this.domain = domain;
    }
  }

  /** Send one `http` message; a host-side failure is thrown as-is. */
  async function httpCall(bytes, method, url, headers, data, extra) {
    const res = await post(
      fill(
        { method: 'http', http_method: method, url, bytes },
        { headers, data, extra },
      ),
    );
    if (res && res.error) throw res.error;
    return res;
  }

  const sendRequest = (method, url, headers, data, extra) =>
    httpCall(false, method, url, headers, data, extra);

  const fetchBytes = (method, url, headers, data, extra) =>
    httpCall(true, method, url, headers, data, extra);

  const Network = {
    fetchBytes,
    sendRequest,
    // Compatibility quirk: for get and delete the third argument lands in the
    // `data` slot of the request, and `extra` is not forwarded.
    get: (url, headers, extra) => sendRequest('GET', url, headers, extra),
    delete: (url, headers, extra) => sendRequest('DELETE', url, headers, extra),
    post: (url, headers, data, extra) => sendRequest('POST', url, headers, data, extra),
    put: (url, headers, data, extra) => sendRequest('PUT', url, headers, data, extra),
    patch: (url, headers, data, extra) => sendRequest('PATCH', url, headers, data, extra),
    setCookies(url, cookies) {
      // Plain objects only: drop undefined members and any extra properties.
      const list = (cookies || []).map((c) =>
        fill({}, { name: c.name, value: c.value, domain: c.domain, path: c.path }),
      );
      return post({ method: 'cookie', function: 'set', url, cookies: list });
    },
    getCookies(url) {
      return post({ method: 'cookie', function: 'get', url });
    },
    deleteCookies(url) {
      return post({ method: 'cookie', function: 'delete', url });
    },
  };

  /** Minimal fetch: statusText is always empty, json() goes via decodeUtf8. */
  async function fetch(url, options) {
    const opts = options || {};
    const res = await fetchBytes(opts.method || 'GET', url, opts.headers, opts.body);
    const status = res.status;
    const body = res.body;
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: '',
      headers: res.headers,
      arrayBuffer: async () => body,
      text: async () => Convert.decodeUtf8(body),
      json: async () => JSON.parse(Convert.decodeUtf8(body)),
    };
  }

  // ---------------------------------------------------------------------
  // HTML: HtmlDocument, HtmlElement, HtmlNode
  // ---------------------------------------------------------------------

  function htmlCall(fn, fields) {
    return post(Object.assign({ method: 'html', function: fn }, fields));
  }

  // Handles are small integers; 0 is valid, so only null/undefined mean "none".
  const isNone = (h) => h === null || h === undefined;

  const asElement = (doc) => (h) => (isNone(h) ? null : new HtmlElement(doc, h));
  const asElements = (doc) => (hs) => (isNone(hs) ? [] : hs.map((h) => new HtmlElement(doc, h)));
  const asNodes = (doc) => (hs) => (isNone(hs) ? [] : hs.map((h) => new HtmlNode(doc, h)));
  const asIs = () => (v) => v;

  let documentCounter = 0; // never reused

  class HtmlDocument {
    constructor(html) {
      this.key = documentCounter++;
      htmlCall('parse', { key: this.key, data: html });
    }

    dispose() {
      htmlCall('dispose', { key: this.key });
    }

    querySelector(query) {
      return asElement(this.key)(htmlCall('querySelector', { key: this.key, query }));
    }

    querySelectorAll(query) {
      return asElements(this.key)(htmlCall('querySelectorAll', { key: this.key, query }));
    }

    getElementById(id) {
      return asElement(this.key)(htmlCall('getElementById', { key: this.key, id }));
    }
  }

  class HtmlElement {
    constructor(doc, key) {
      this.doc = doc;
      this.key = key;
    }

    querySelector(query) {
      return asElement(this.doc)(
        htmlCall('dom_querySelector', { doc: this.doc, key: this.key, query }),
      );
    }

    querySelectorAll(query) {
      return asElements(this.doc)(
        htmlCall('dom_querySelectorAll', { doc: this.doc, key: this.key, query }),
      );
    }
  }

  class HtmlNode {
    constructor(doc, key) {
      this.doc = doc;
      this.key = key;
    }

    toElement() {
      return asElement(this.doc)(htmlCall('node_to_element', { doc: this.doc, key: this.key }));
    }
  }

  // [property names, host function, result wrapper]
  const ELEMENT_GETTERS = [
    [['text'], 'getText', asIs],
    [['attributes'], 'getAttributes', asIs],
    [['innerHTML'], 'getInnerHTML', asIs],
    [['children'], 'getChildren', asElements],
    [['nodes'], 'getNodes', asNodes],
    [['parent'], 'getParent', asElement],
    [['previousElementSibling'], 'getPreviousSibling', asElement],
    [['nextElementSibling'], 'getNextSibling', asElement],
    [['classNames'], 'getClassNames', asIs],
    [['id'], 'getId', asIs],
    [['localName'], 'getLocalName', asIs],
  ];

  const NODE_GETTERS = [
    [['text'], 'node_text', asIs],
    [['type'], 'node_type', asIs],
  ];

  function defineHandleGetters(cls, table) {
    for (const [names, fn, wrap] of table) {
      const read = function () {
        return wrap(this.doc)(htmlCall(fn, { doc: this.doc, key: this.key }));
      };
      for (const name of names) {
        Object.defineProperty(cls.prototype, name, {
          get: read,
          configurable: true,
          enumerable: false,
        });
      }
    }
  }
  defineHandleGetters(HtmlElement, ELEMENT_GETTERS);
  defineHandleGetters(HtmlNode, NODE_GETTERS);

  // ---------------------------------------------------------------------
  // Data shapes: Comic, ComicDetails, Comment, ImageLoadingConfig
  //
  // Field names are the interface. ComicDetails, Comment and
  // ImageLoadingConfig copy any field they do not list (`unlisted`) so that
  // a field missing from these lists is not silently dropped.
  // ---------------------------------------------------------------------

  class Comic {
    constructor({
      id,
      title,
      subtitle,
      subTitle,
      cover,
      tags,
      description,
      maxPage,
      language,
      favoriteId,
      stars,
    } = {}) {
      this.id = id;
      this.title = title;
      this.subtitle = subtitle;
      this.subTitle = subTitle;
      this.cover = cover;
      this.tags = tags;
      this.description = description;
      this.maxPage = maxPage;
      this.language = language;
      this.favoriteId = favoriteId;
      this.stars = stars;
    }
  }

  class ComicDetails {
    constructor({
      title,
      subtitle,
      subTitle,
      cover,
      description,
      tags,
      chapters,
      isFavorite,
      subId,
      thumbnails,
      recommend,
      commentCount,
      likesCount,
      isLiked,
      uploader,
      updateTime,
      uploadTime,
      url,
      stars,
      maxPage,
      comments,
      ...unlisted
    } = {}) {
      this.title = title;
      this.subtitle = subtitle ?? subTitle;
      this.cover = cover;
      this.description = description;
      this.tags = tags;
      this.chapters = chapters;
      this.isFavorite = isFavorite;
      this.subId = subId;
      this.thumbnails = thumbnails;
      this.recommend = recommend;
      this.commentCount = commentCount;
      this.likesCount = likesCount;
      this.isLiked = isLiked;
      this.uploader = uploader;
      this.updateTime = updateTime;
      this.uploadTime = uploadTime;
      this.url = url;
      this.stars = stars;
      this.maxPage = maxPage;
      this.comments = comments;
      Object.assign(this, unlisted);
    }
  }

  class Comment {
    constructor({
      userName,
      avatar,
      content,
      time,
      replyCount,
      id,
      isLiked,
      score,
      voteStatus,
      ...unlisted
    } = {}) {
      this.userName = userName;
      this.avatar = avatar;
      this.content = content;
      this.time = time;
      this.replyCount = replyCount;
      this.id = id;
      this.isLiked = isLiked;
      this.score = score;
      this.voteStatus = voteStatus;
      Object.assign(this, unlisted);
    }
  }

  class ImageLoadingConfig {
    constructor({ url, method, data, headers, onResponse, modifyImage, onLoadFailed, ...unlisted } = {}) {
      this.url = url;
      this.method = method;
      this.data = data;
      this.headers = headers;
      this.onResponse = onResponse;
      this.modifyImage = modifyImage;
      this.onLoadFailed = onLoadFailed;
      Object.assign(this, unlisted);
    }
  }

  // ---------------------------------------------------------------------
  // APP, clipboard, compute
  // ---------------------------------------------------------------------

  const APP = {
    get version() {
      return typeof appVersion === 'undefined' ? undefined : appVersion;
    },
    get locale() {
      return post({ method: 'getLocale' });
    },
    get platform() {
      return post({ method: 'getPlatform' });
    },
  };

  function setClipboard(text) {
    return post({ method: 'setClipboard', text });
  }

  function getClipboard() {
    return post({ method: 'getClipboard' });
  }

  /**
   * Run a function on the host. The function travels as source text, so it
   * cannot capture variables from the caller.
   */
  function compute(fn, ...args) {
    const source = typeof fn === 'function' ? Function.prototype.toString.call(fn) : fn;
    return post({ method: 'compute', function: source, args });
  }

  // ---------------------------------------------------------------------
  // UI
  // ---------------------------------------------------------------------

  function uiCall(fn, fields) {
    return post(Object.assign({ method: 'UI', function: fn }, fields));
  }

  const UI = {
    showMessage(message) {
      return uiCall('showMessage', { message });
    },
    showDialog(title, content, actions) {
      const list = (actions || []).map((a) =>
        fill({}, { text: a.text, callback: a.callback, style: a.style }),
      );
      return uiCall('showDialog', { title, content, actions: list });
    },
    launchUrl(url) {
      return uiCall('launchUrl', { url });
    },
    showLoading(onCancel) {
      return uiCall('showLoading', { onCancel: onCancel ?? null });
    },
    cancelLoading(id) {
      return uiCall('cancelLoading', { id });
    },
    showInputDialog(title, validator, image) {
      return uiCall('showInputDialog', fill({ title, validator: validator ?? null }, { image }));
    },
    showSelectDialog(title, options, initialIndex) {
      return uiCall('showSelectDialog', fill({ title, options }, { initialIndex }));
    },
  };

  // ---------------------------------------------------------------------
  // Image (pixel buffer handle)
  // ---------------------------------------------------------------------

  function imageCall(fn, fields) {
    return post(Object.assign({ method: 'image', function: fn }, fields));
  }

  const wrapImage = (key) => (isNone(key) ? null : new Image(key));

  class Image {
    constructor(key) {
      this.key = key;
    }

    copyRange(x, y, width, height) {
      return wrapImage(imageCall('copyRange', { key: this.key, x, y, width, height }));
    }

    copyAndRotate90() {
      return wrapImage(imageCall('copyAndRotate90', { key: this.key }));
    }

    fillImageAt(x, y, image) {
      imageCall('fillImageAt', { key: this.key, x, y, image: image.key });
    }

    fillImageRangeAt(x, y, image, srcX, srcY, width, height) {
      imageCall('fillImageRangeAt', {
        key: this.key,
        x,
        y,
        image: image.key,
        srcX,
        srcY,
        width,
        height,
      });
    }

    get width() {
      return imageCall('getWidth', { key: this.key });
    }

    get height() {
      return imageCall('getHeight', { key: this.key });
    }

    static empty(width, height) {
      return wrapImage(imageCall('emptyImage', { width, height }));
    }
  }

  // ---------------------------------------------------------------------
  // ComicSource base class
  // ---------------------------------------------------------------------

  class ComicSource {
    name = '';
    key = '';
    version;
    minAppVersion;
    url;
    translation = {};

    static sources = {};

    loadData(dataKey) {
      return post({ method: 'load_data', key: this.key, data_key: dataKey });
    }

    loadSetting(settingKey) {
      return post({ method: 'load_setting', key: this.key, setting_key: settingKey });
    }

    saveData(dataKey, data) {
      return post(fill({ method: 'save_data', key: this.key, data_key: dataKey }, { data }));
    }

    deleteData(dataKey) {
      return post({ method: 'delete_data', key: this.key, data_key: dataKey });
    }

    get isLogged() {
      return post({ method: 'isLogged', key: this.key });
    }

    /** Look up `text` in the table of the current locale; fall back to it. */
    translate(text) {
      const table = this.translation ? this.translation[APP.locale] : undefined;
      return (table && table[text]) ?? text;
    }

    /** Sources override this; the loader awaits it once after registration. */
    init() {}
  }

  // ---------------------------------------------------------------------
  // URL and URLSearchParams
  // ---------------------------------------------------------------------
  //
  // The pinned engine has no URL class of its own, and one official source builds
  // a Referer from `new URL(imageUrl).host` (falling back to a regex when it
  // throws). A parser for the parts sources actually use is better than telling
  // every author to write their own.
  //
  // What this is not: a full WHATWG URL. It handles absolute http(s) addresses,
  // relative references against a base, and the usual accessors; it does not
  // normalise percent escapes, resolve every edge of dot segments, or implement
  // IDNA. A source that needs those has to do them itself.

  function searchParamsOf(query) {
    const pairs = [];
    if (query) {
      for (const part of query.split('&')) {
        if (!part) continue;
        const eq = part.indexOf('=');
        const rawName = eq === -1 ? part : part.slice(0, eq);
        const rawValue = eq === -1 ? '' : part.slice(eq + 1);
        pairs.push([decodeQuery(rawName), decodeQuery(rawValue)]);
      }
    }
    return pairs;
  }

  function decodeQuery(text) {
    try { return decodeURIComponent(String(text).replace(/\+/g, ' ')); } catch (_) { return String(text); }
  }

  function encodeQuery(text) {
    // application/x-www-form-urlencoded leaves `*`, `-`, `.` and `_` alone and
    // escapes `!`, `'`, `(`, `)` and `~`; encodeURIComponent does the opposite
    // for that second set. A query rebuilt by the browser must have the same
    // bytes, or a signed parameter changes meaning.
    return encodeURIComponent(String(text))
      .replace(/[!'()~]/g, c => '%' + c.charCodeAt(0).toString(16).toUpperCase())
      .replace(/%20/g, '+');
  }

  class URLSearchParams {
    // A URL owns this object and wants to know when it was edited: until then
    // the URL keeps the exact query string it was given, including escapes and
    // a bare `?`. Any edit switches the URL over to the encoded form.
    constructor(init, onEdit) {
      this._pairs = [];
      this._onEdit = typeof onEdit === 'function' ? onEdit : null;
      if (init == null) return;
      if (typeof init === 'string') {
        this._pairs = searchParamsOf(init.replace(/^\?/, ''));
      } else if (init instanceof URLSearchParams) {
        // Copying another parameter list has to copy its pairs, not its own
        // fields: enumerating the object handed back `_pairs` and `_onEdit`
        // as two parameters named after the implementation.
        this._pairs = init._pairs.map(pair => [pair[0], pair[1]]);
      } else if (Array.isArray(init)) {
        for (const pair of init) this._pairs.push([String(pair[0]), String(pair[1])]);
      } else if (typeof init === 'object') {
        for (const key of Object.keys(init)) this._pairs.push([key, String(init[key])]);
      }
    }

    get(name) {
      for (const [key, value] of this._pairs) if (key === String(name)) return value;
      return null;
    }

    getAll(name) {
      return this._pairs.filter(([key]) => key === String(name)).map(([, value]) => value);
    }

    has(name) {
      return this._pairs.some(([key]) => key === String(name));
    }

    // The number of pairs. A source that checks whether a parameter exists
    // with `size` would otherwise read `undefined` and treat the object as
    // a non-empty one.
    get size() {
      return this._pairs.length;
    }

    edited() {
      if (this._onEdit) this._onEdit();
    }

    append(name, value) {
      this._pairs.push([String(name), String(value)]);
      this.edited();
    }

    set(name, value) {
      const key = String(name);
      const at = this._pairs.findIndex(([k]) => k === key);
      if (at === -1) { this._pairs.push([key, String(value)]); this.edited(); return; }
      this._pairs[at] = [key, String(value)];
      this._pairs = this._pairs.filter(([k], i) => k !== key || i === at);
      this.edited();
    }

    delete(name) {
      this._pairs = this._pairs.filter(([key]) => key !== String(name));
      this.edited();
    }

    sort() {
      this._pairs.sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
      this.edited();
    }

    forEach(fn, thisArg) {
      for (const [key, value] of this._pairs) fn.call(thisArg, value, key, this);
    }

    // The parameter list is iterable as [name, value] pairs, and the three
    // views are what a source walks when it turns a query into a form. Without
    // them `for (const [k, v] of params)` threw `not iterable`, which a source
    // sees as an engine fault rather than a missing method.
    *entries() {
      for (const pair of this._pairs) yield [pair[0], pair[1]];
    }

    *keys() {
      for (const pair of this._pairs) yield pair[0];
    }

    *values() {
      for (const pair of this._pairs) yield pair[1];
    }

    [Symbol.iterator]() {
      return this.entries();
    }

    toString() {
      return this._pairs.map(([key, value]) => encodeQuery(key) + '=' + encodeQuery(value)).join('&');
    }
  }

  const URL_PATTERN = /^([a-zA-Z][a-zA-Z0-9+.-]*:)(?:\/\/([^/?#]*))?([^?#]*)(?:\?([^#]*))?(?:#(.*))?$/;
  // The schemes whose host is case-insensitive in the URL standard. Sources
  // only use these; a non-special scheme keeps its host as written.
  const SPECIAL_SCHEMES = ['http:', 'https:', 'ws:', 'wss:', 'ftp:'];
  // The port a special scheme leaves out of the address. The standard drops it
  // from `port` and from `href`, so `new URL('https://a.com:443/p').port` is
  // empty and the address is `https://a.com/p`. A source that compares the
  // address it built with the one the server sees would otherwise see a port
  // the request never carries.
  const DEFAULT_PORTS = { 'http:': '80', 'https:': '443', 'ws:': '80', 'wss:': '443', 'ftp:': '21' };
  // The characters a path percent-encodes. The query has its own set, and the
  // fragment has none: a space in a path is `a%20b`, while `?` and `#` are the
  // path's own separators and never reach it.
  //
  // Bytes that are already escaped stay as they are. `%2f` is a slash inside
  // one path segment and must not become a separator, and `%41` is an `A` the
  // source wrote down — re-encoding either changes the address the server
  // receives. The path is therefore walked one character at a time: an
  // escape is copied through, everything else is encoded by its own byte.
  const PATH_UNRESERVED = /[A-Za-z0-9\-._~!$&'()*+,;=:@]/;
  function encodePath(path) {
    let out = '';
    for (let i = 0; i < path.length; i++) {
      const ch = path[i];
      // An escape the source wrote down is kept exactly as it is.
      if (ch === '%' && /^[0-9a-fA-F]{2}$/.test(path.slice(i + 1, i + 3))) {
        out += path.slice(i, i + 3);
        i += 2;
        continue;
      }
      // A slash separates the segments and stays a slash.
      if (ch === '/') {
        out += ch;
        continue;
      }
      if (PATH_UNRESERVED.test(ch)) {
        out += ch;
        continue;
      }
      // A character outside ASCII is encoded from its UTF-8 bytes.
      out += encodeURIComponent(ch);
    }
    return out;
  }

  // Removes `.` and `..` path segments the way the URL standard does for a
  // special scheme. Without it `new URL('../img/1.jpg', page)` asked the server
  // for the literal `/b/c/../img/1.jpg`, which a signed CDN link or a strict
  // server refuses even when a browser would have fetched `/b/img/1.jpg`.
  // `%2e` counts as a dot, as it does in the standard; `..` never climbs above
  // the root; and a trailing `.` or `..` leaves the directory's slash behind.
  function removeDotSegments(path) {
    const input = path.split('/');
    const output = [];
    for (let i = 0; i < input.length; i++) {
      const segment = input[i];
      const lower = segment.toLowerCase();
      const single = segment === '.' || lower === '%2e';
      const double = segment === '..' || lower === '.%2e' || lower === '%2e.' || lower === '%2e%2e';
      const last = i === input.length - 1;
      if (double) {
        if (output.length > 1) output.pop();
        if (last) output.push('');
      } else if (single) {
        if (last) output.push('');
      } else {
        output.push(segment);
      }
    }
    const joined = output.join('/');
    return joined.startsWith('/') ? joined : '/' + joined;
  }

  // Resolves a relative reference against a base address, as a string the
  // constructor can parse again. The shapes a source actually writes are a
  // protocol-relative address (`//cdn.example/host`), a bare query, a bare
  // fragment and an empty reference; each keeps a different part of the base.
  function resolveReference(reference, base) {
    const ref = String(reference);
    // What precedes the path: the scheme and, when the address has one, the
    // host with any credentials.
    const prefix = base._hasHost
      ? base.protocol + '//'
        + (base.username
          ? base.username + (base.password ? ':' + base.password : '') + '@'
          : '')
        + base.host
      : base.protocol;
    const withoutHash = prefix + base.pathname + base.search;

    // A fragment-only reference keeps the query, and an empty one keeps the
    // whole address but the fragment.
    if (ref === '' || ref.startsWith('#')) return withoutHash + ref;

    // An opaque path has no directories to merge with: only a fragment can be
    // added to it, and the standard refuses the rest with a TypeError.
    if (base._opaquePath) throw new TypeError('Invalid URL: ' + ref);

    // A reference that starts with a query replaces the query and keeps the
    // path; a protocol-relative and an absolute one carry their own address.
    if (ref.startsWith('?')) return prefix + base.pathname + ref;
    if (ref.startsWith('//')) return base.protocol + ref;
    if (ref.startsWith('/')) return prefix + ref;

    // A relative path merges with the directory of the base path. A base path
    // that ends in a slash is already a directory; a base address with no
    // path merges against the root.
    const path = base.pathname || '/';
    const directory = path.endsWith('/') ? path : path.replace(/[^/]*$/, '');
    return prefix + directory + ref;
  }

  class URL {
    constructor(input, base) {
      const text = String(input);
      const absolute = URL_PATTERN.exec(text);
      if (!absolute) {
        if (base === undefined) throw new TypeError('Invalid URL: ' + text);
        return new URL(resolveReference(text, new URL(String(base), undefined)), undefined);
      }
      this.protocol = absolute[1].toLowerCase();
      const special = SPECIAL_SCHEMES.indexOf(this.protocol) !== -1;
      // A special scheme treats a backslash as a path separator everywhere,
      // so `https:\\a.com\p` is the address `https://a.com/p` rather than a
      // host of `\a.com`. The replacement reaches the authority and the path
      // only: a backslash in a query or a fragment is part of the text and
      // stays as it is.
      //
      // A single separator after the scheme is not enough to say "there is a
      // host": the standard reads `https:/a.com` as a host of `a.com`, so the
      // separator is doubled when the scheme is special and the address does
      // not already carry two of them.
      let raw = text;
      if (special) {
        const cut = raw.search(/[?#]/);
        const head = cut === -1 ? raw : raw.slice(0, cut);
        const tail = cut === -1 ? '' : raw.slice(cut);
        raw = head.replace(/\\/g, '/') + tail;
        raw = raw.replace(/^([a-zA-Z][a-zA-Z0-9+.-]*:)\/(?!\/)/, '$1//');
      }
      const parsed = URL_PATTERN.exec(raw);
      if (!parsed) throw new TypeError('Invalid URL: ' + text);
      const authority = parsed[2] || '';
      // A scheme that writes an authority has a host; one that does not keeps
      // its path as written, which is what `mailto:` and `data:` need.
      this._hasHost = parsed[2] !== undefined;
      const at = authority.lastIndexOf('@');
      this.username = '';
      this.password = '';
      if (at !== -1) {
        const credentials = authority.slice(0, at).split(':');
        this.username = credentials[0] || '';
        this.password = credentials[1] || '';
      }
      const hostport = at === -1 ? authority : authority.slice(at + 1);
      const portAt = hostport.lastIndexOf(':');
      const hasPort = portAt !== -1 && /^\d*$/.test(hostport.slice(portAt + 1));
      this.hostname = hasPort ? hostport.slice(0, portAt) : hostport;
      // Addresses with a special scheme parse their host case-insensitively:
      // a source comparing `new URL(imageUrl).host` against "example.test" must
      // not fail on an uppercase link. Other schemes keep the host opaque.
      if (special) {
        this.hostname = this.hostname.toLowerCase();
      }
      // The port a special scheme leaves out of the address is dropped, so
      // `new URL('https://a.com:443/p').port` is empty and the address reads
      // `https://a.com/p`. The request never carries that port, and a source
      // that rebuilds the address from `port` would otherwise send `:443` to
      // a host that did not ask for it. A scheme that is not special keeps
      // its port as written.
      const port = hasPort ? hostport.slice(portAt + 1) : '';
      this.port = (special && port === DEFAULT_PORTS[this.protocol]) ? '' : port;
      // A path is percent-encoded, and the tabs and newlines the standard
      // removes are removed first. The bytes that are already escaped stay
      // as they are: `%2e` is a dot to the dot-segment rule but a literal
      // `%2e` to the address, and re-encoding it would change the request.
      let path = parsed[3] || '/';
      path = path.replace(/[\t\n\r]/g, '');
      if (special) {
        path = encodePath(path);
      }
      this.pathname = path;
      // Dot segments are resolved for every scheme that has a host, and for
      // the ones that write an absolute path. A scheme whose path is opaque
      // — `mailto:` and `data:` — keeps it as written, because there the
      // text is the message rather than a route.
      const opaque = !special && authority === '' && !path.startsWith('/');
      if (!opaque) {
        this.pathname = removeDotSegments(this.pathname);
      }
      this._opaquePath = opaque;
      // The query is kept exactly as written. A signed URL's `?sig=a/b` is a
      // different request from `?sig=a%2Fb`, and pre-escaped bytes such as
      // `%2f` must not be decoded and re-encoded. Only an edit through
      // searchParams replaces the string with its encoded form. `null` means
      // there was no `?` at all; `''` means a bare `?`.
      this._query = parsed[4] === undefined ? null : parsed[4];
      this._queryEdited = false;
      this.searchParams = new URLSearchParams(parsed[4] || '');
      // `hash` is a getter below; `_hash` keeps the marker even for an empty
      // fragment, because `https://a.com/p#` is a different href from
      // `https://a.com/p` even though both report `hash === ''`.
      this._hash = parsed[5] === undefined ? '' : '#' + parsed[5];
    }

    get searchParams() { return this._params; }

    set searchParams(value) {
      this._params = value;
      // Rebind when a source replaces the object, so edits are still observed.
      if (value && typeof value.edited === 'function') {
        value._onEdit = () => { this._queryEdited = true; };
      }
    }

    get hash() { return this._hash === '#' ? '' : this._hash; }

    set hash(value) {
      const text = String(value == null ? '' : value);
      this._hash = text === '' ? '' : (text.startsWith('#') ? text : '#' + text);
    }

    // `search` and `searchParams` are one value in the browser: an edit through
    // searchParams has to show up in `search`, while an untouched URL keeps the
    // string it was given.
    get search() {
      if (this._queryEdited) {
        const query = this._params.toString();
        return query ? '?' + query : '';
      }
      return !this._query ? '' : '?' + this._query;
    }

    set search(value) {
      const text = String(value == null ? '' : value);
      this._query = text === '' ? null
        : (text === '?' ? '' : text.replace(/^\?/, ''));
      this._queryEdited = false;
      this.searchParams = new URLSearchParams(this._query || '');
    }

    get host() {
      return this.port ? this.hostname + ':' + this.port : this.hostname;
    }

    get origin() {
      // Only a special scheme has an origin. `foo://a.com` and `mailto:` do
      // not, and the standard answers "null" for them rather than a string
      // that looks like an address.
      if (SPECIAL_SCHEMES.indexOf(this.protocol) === -1) return 'null';
      return this.protocol + '//' + this.host;
    }

    get href() {
      const credentials = this.username
        ? this.username + (this.password ? ':' + this.password : '') + '@'
        : '';
      const query = this._queryEdited
        ? (this._params.toString() ? '?' + this._params.toString() : '')
        : (this._query === null ? '' : '?' + this._query);
      // The address is built the way the standard writes it: a scheme that
      // has a host is followed by `//`, and one that does not is followed by
      // its path straight away. `mailto:a@b` and `data:text/plain,x` keep
      // their shape here — writing `mailto://a@b` would hand a source that
      // reads `.href` an address the server never sees.
      const prefix = this._hasHost
        ? this.protocol + '//' + credentials + this.host
        : this.protocol;
      return prefix + this.pathname + query + this._hash;
    }

    toString() { return this.href; }
    toJSON() { return this.href; }
  }

  URL.canParse = function (input, base) {
    try { new URL(input, base); return true; } catch (_) { return false; }
  };

  // The non-throwing form: an address the standard rejects answers null rather
  // than an exception a source has to wrap in try/catch.
  URL.parse = function (input, base) {
    try { return new URL(input, base); } catch (_) { return null; }
  };

  // ---------------------------------------------------------------------
  // publish the globals
  // ---------------------------------------------------------------------

  const GLOBALS = {
    log,
    console: consoleObject,
    createUuid,
    randomInt,
    randomDouble,
    setTimeout,
    setInterval,
    clearTimeout: clearTimer,
    clearInterval: clearTimer,
    Convert,
    Cookie,
    Network,
    fetch,
    HtmlDocument,
    HtmlElement,
    HtmlNode,
    Comic,
    ComicDetails,
    Comment,
    ImageLoadingConfig,
    APP,
    setClipboard,
    getClipboard,
    compute,
    UI,
    Image,
    ComicSource,
    URL,
    URLSearchParams,
  };
  for (const name of Object.keys(GLOBALS)) publish(name, GLOBALS[name]);
})(globalThis);
