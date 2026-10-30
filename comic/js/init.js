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
  };
  for (const name of Object.keys(GLOBALS)) publish(name, GLOBALS[name]);
})(globalThis);
