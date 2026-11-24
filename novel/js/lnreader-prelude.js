/*
 * VeneraCat Core: LNReader compatibility prelude.
 *
 * Original work, VeneraCat extension. Loaded in NovelSource engines after
 * init.js, novel-prelude.js, and cheerio.bundle.js.
 *
 * Provides:
 *   - LNReaderAdapter.createRequire(source): CommonJS require() loader for
 *     cheerio, @libs/fetch, @libs/novelStatus, @libs/filterInputs,
 *     @libs/defaultCover, and @libs/storage.
 *   - LNReaderAdapter.attach(source, factory): wraps a compiled LNReader
 *     CommonJS plugin into NovelSource's four methods (popular, search,
 *     detail, chapter), including parsePage pagination.
 */
(function (root) {
  'use strict';

  const NovelStatus = Object.freeze({
    Unknown: 'Unknown',
    Ongoing: 'Ongoing',
    Completed: 'Completed',
    Licensed: 'Licensed',
    PublishingFinished: 'Publishing Finished',
    Cancelled: 'Cancelled',
    OnHiatus: 'On Hiatus',
    STUB: 'STUB',
    Inactive: 'Inactive',
  });

  const FilterTypes = Object.freeze({
    TextInput: 'Text',
    Picker: 'Picker',
    CheckboxGroup: 'Checkbox',
    Switch: 'Switch',
    ExcludableCheckboxGroup: 'XCheckbox',
  });

  const defaultCover = '';

  if (typeof root.FormData === 'undefined') {
    class FormDataShim {
      constructor() {
        this._entries = [];
      }
      append(name, value) {
        this._entries.push([String(name), String(value)]);
      }
      set(name, value) {
        const key = String(name);
        this._entries = this._entries.filter((e) => e[0] !== key);
        this._entries.push([key, String(value)]);
      }
      get(name) {
        const key = String(name);
        const found = this._entries.find((e) => e[0] === key);
        return found ? found[1] : null;
      }
      has(name) {
        const key = String(name);
        return this._entries.some((e) => e[0] === key);
      }
      delete(name) {
        const key = String(name);
        this._entries = this._entries.filter((e) => e[0] !== key);
      }
      entries() {
        return this._entries[Symbol.iterator]();
      }
      [Symbol.iterator]() {
        return this._entries[Symbol.iterator]();
      }
      forEach(cb) {
        for (const [k, v] of this._entries) cb(v, k, this);
      }
    }
    root.FormData = FormDataShim;
  }


  function normalizeHeaders(input) {
    const out = {};
    if (!input) return out;
    if (typeof input.forEach === 'function') {
      input.forEach((v, k) => {
        out[String(k)] = String(v);
      });
      return out;
    }
    if (Array.isArray(input)) {
      for (const pair of input) {
        if (Array.isArray(pair) && pair.length >= 2) {
          out[String(pair[0])] = String(pair[1]);
        }
      }
      return out;
    }
    if (typeof input === 'object') {
      for (const k of Object.keys(input)) {
        if (input[k] !== undefined && input[k] !== null) {
          out[k] = String(input[k]);
        }
      }
    }
    return out;
  }

  function wrapResponseHeaders(raw) {
    const map = {};
    if (raw && typeof raw === 'object') {
      for (const k of Object.keys(raw)) {
        const v = raw[k];
        map[k.toLowerCase()] = Array.isArray(v) ? v.join(', ') : String(v);
      }
    }
    return Object.assign({}, raw || {}, {
      get(name) {
        const key = String(name || '').toLowerCase();
        return Object.prototype.hasOwnProperty.call(map, key) ? map[key] : null;
      },
      has(name) {
        return Object.prototype.hasOwnProperty.call(map, String(name || '').toLowerCase());
      },
    });
  }

  async function fetchApi(url, init) {
    const opts = init ? Object.assign({}, init) : {};
    const headers = normalizeHeaders(opts.headers);
    let body = opts.body;
    if (body !== undefined && body !== null) {
      if (typeof root.FormData !== 'undefined' && body instanceof root.FormData) {
        const boundary = '----vcForm' + Math.random().toString(36).slice(2);
        const parts = [];
        for (const [k, v] of body.entries()) {
          parts.push('--' + boundary + '\r\nContent-Disposition: form-data; name="' + k.replace(/"/g, '%22') + '"\r\n\r\n' + v + '\r\n');
        }
        parts.push('--' + boundary + '--\r\n');
        body = parts.join('');
        if (!Object.keys(headers).some((k) => k.toLowerCase() === 'content-type')) {
          headers['Content-Type'] = 'multipart/form-data; boundary=' + boundary;
        }
      } else if (typeof URLSearchParams !== 'undefined' && body instanceof URLSearchParams) {
        body = body.toString();
        if (!Object.keys(headers).some((k) => k.toLowerCase() === 'content-type')) {
          headers['Content-Type'] = 'application/x-www-form-urlencoded';
        }
      } else if (typeof body !== 'string' && !(body instanceof Uint8Array)) {
        body = String(body);
      }
    }
    const res = await fetch(String(url), {
      method: opts.method || 'GET',
      headers,
      body,
    });
    return {
      ok: res.ok,
      status: res.status,
      statusText: res.statusText || '',
      url: String(url),
      headers: wrapResponseHeaders(res.headers),
      text: () => res.text(),
      json: () => res.json(),
      arrayBuffer: () => res.arrayBuffer(),
    };
  }

  async function fetchText(url, init) {
    const res = await fetchApi(url, init);
    return res.text();
  }

  function createStorage(source, prefix) {
    const mem = Object.create(null);
    const keyOf = (k) => prefix + String(k);
    return {
      set(k, value, expires) {
        const exp = expires instanceof Date ? expires.getTime() : typeof expires === 'number' ? expires : undefined;
        const item = { created: Date.now(), value, expires: exp };
        mem[k] = item;
        if (source && typeof source.saveData === 'function') {
          try {
            source.saveData(keyOf(k), item);
          } catch (_) {}
        }
      },
      get(k, raw) {
        let item = mem[k];
        if (item === undefined && source && typeof source.loadData === 'function') {
          try {
            const stored = source.loadData(keyOf(k));
            if (stored && typeof stored === 'object' && 'value' in stored) {
              item = stored;
              mem[k] = item;
            }
          } catch (_) {}
        }
        if (!item) return undefined;
        if (item.expires && Date.now() > item.expires) {
          this.delete(k);
          return undefined;
        }
        return raw ? item : item.value;
      },
      delete(k) {
        delete mem[k];
        if (source && typeof source.deleteData === 'function') {
          try {
            source.deleteData(keyOf(k));
          } catch (_) {}
        }
      },
      clearAll() {
        for (const k of Object.keys(mem)) {
          this.delete(k);
        }
      },
      getAllKeys() {
        return Object.keys(mem);
      },
    };
  }

  function createRequire(source) {
    const storage = createStorage(source, 'ln_storage:');
    const localStorage = createStorage(source, 'ln_local:');
    const sessionStorage = createStorage(null, 'ln_session:');
    const modules = {
      cheerio: root.__vcCheerio || {},
      '@libs/fetch': {
        fetchApi,
        fetchText,
        fetchProto() {
          throw new Error('lnreader: fetchProto is not supported');
        },
        fetchFile: fetchText,
      },
      '@libs/novelStatus': { NovelStatus },
      '@libs/filterInputs': { FilterTypes },
      '@libs/defaultCover': { defaultCover },
      '@libs/storage': { storage, localStorage, sessionStorage },
    };
    return function require(id) {
      if (Object.prototype.hasOwnProperty.call(modules, id)) {
        return modules[id];
      }
      throw new Error('lnreader: unsupported module "' + String(id) + '"');
    };
  }

  function resolveMaybeURL(raw, site) {
    if (!raw) return '';
    const s = String(raw).trim();
    if (!s) return '';
    if (/^https?:\/\//i.test(s) || /^data:/i.test(s)) return s;
    if (!site) return s;
    try {
      return new URL(s, site).toString();
    } catch (_) {
      return s;
    }
  }

  function defaultFiltersOf(plugin) {
    const out = {};
    const defs = plugin && plugin.filters;
    if (!defs || typeof defs !== 'object') return out;
    for (const k of Object.keys(defs)) {
      const def = defs[k];
      if (!def || typeof def !== 'object') continue;
      out[k] = { type: def.type, value: def.value };
    }
    return out;
  }

  function mapNovelItems(list, site) {
    if (!Array.isArray(list)) return [];
    const items = [];
    for (const n of list) {
      if (!n || typeof n !== 'object') continue;
      const id = n.path !== undefined && n.path !== null ? String(n.path) : '';
      const title = n.name !== undefined && n.name !== null ? String(n.name).trim() : '';
      if (!id || !title) continue;
      const item = { id, title };
      const cover = resolveMaybeURL(n.cover, site);
      if (cover) item.cover = cover;
      if (n.author) item.author = String(n.author).trim();
      items.push(item);
    }
    return items;
  }

  function normalizeTags(genres) {
    if (Array.isArray(genres)) {
      return genres.map((g) => String(g).trim()).filter(Boolean);
    }
    if (typeof genres === 'string' && genres.trim()) {
      return genres
        .split(/[,，/]/)
        .map((g) => g.trim())
        .filter(Boolean);
    }
    return [];
  }

  function mapChapters(list) {
    if (!Array.isArray(list)) return [];
    const out = [];
    for (const c of list) {
      if (!c || typeof c !== 'object') continue;
      const id = c.path !== undefined && c.path !== null ? String(c.path) : '';
      if (!id) continue;
      const title = c.name !== undefined && c.name !== null ? String(c.name).trim() : '';
      const entry = { id, title };
      if (c.releaseTime) entry.time = String(c.releaseTime).trim();
      out.push(entry);
    }
    return out;
  }

  function attach(source, factory) {
    let instance = null;
    function getPlugin() {
      if (instance) return instance;
      const module = { exports: {} };
      const req = createRequire(source);
      factory(req, module.exports, module);
      const exp = module.exports;
      instance = exp && exp.default ? exp.default : exp;
      if (!instance || typeof instance !== 'object') {
        throw new Error('lnreader: plugin did not export a default instance');
      }
      return instance;
    }

    return {
      async popular(page) {
        const plugin = getPlugin();
        if (typeof plugin.popularNovels !== 'function') {
          throw new Error('lnreader: plugin does not implement popularNovels');
        }
        const list = await plugin.popularNovels(page, {
          showLatestNovels: false,
          filters: defaultFiltersOf(plugin),
        });
        return { items: mapNovelItems(list, plugin.site || source.url) };
      },

      async search(keyword, page) {
        const plugin = getPlugin();
        if (typeof plugin.searchNovels !== 'function') {
          throw new Error('lnreader: plugin does not implement searchNovels');
        }
        const list = await plugin.searchNovels(String(keyword || ''), page);
        return { items: mapNovelItems(list, plugin.site || source.url) };
      },

      async detail(id) {
        const plugin = getPlugin();
        const parseFn =
          typeof plugin.parseNovel === 'function'
            ? plugin.parseNovel
            : typeof plugin.parseNovelAndChapters === 'function'
              ? plugin.parseNovelAndChapters
              : null;
        if (!parseFn) {
          throw new Error('lnreader: plugin does not implement parseNovel');
        }
        const info = await parseFn.call(plugin, String(id));
        if (!info || typeof info !== 'object') {
          throw new Error('lnreader: parseNovel returned empty result');
        }
        let chapters = Array.isArray(info.chapters) ? info.chapters.slice() : [];
        const totalPages = Number(info.totalPages || 0);
        if (typeof plugin.parsePage === 'function' && totalPages > 0 && (chapters.length === 0 || totalPages > 1)) {
          const limit = Math.min(totalPages, 50);
          chapters = [];
          for (let p = 1; p <= limit; p++) {
            const pageRes = await plugin.parsePage(String(id), String(p));
            if (pageRes && Array.isArray(pageRes.chapters)) {
              for (const ch of pageRes.chapters) chapters.push(ch);
            }
          }
        }
        return {
          id: info.path ? String(info.path) : String(id),
          title: info.name ? String(info.name).trim() : '',
          cover: resolveMaybeURL(info.cover, plugin.site || source.url),
          author: info.author ? String(info.author).trim() : '',
          summary: info.summary ? String(info.summary).trim() : '',
          status: info.status ? String(info.status).trim() : '',
          tags: normalizeTags(info.genres),
          chapters: mapChapters(chapters),
        };
      },

      async chapter(novelId, chapId) {
        const plugin = getPlugin();
        if (typeof plugin.parseChapter !== 'function') {
          throw new Error('lnreader: plugin does not implement parseChapter');
        }
        const html = await plugin.parseChapter(String(chapId));
        return { html: html === undefined || html === null ? '' : String(html) };
      },
    };
  }

  root.LNReaderAdapter = Object.freeze({
    createRequire,
    attach,
    NovelStatus,
    FilterTypes,
  });
})(globalThis);
