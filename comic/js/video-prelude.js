/*
 * VeneraCat Core: VideoSource prelude.
 *
 * Original work, VeneraCat extension; not part of the official ComicSource
 * API. Loaded after init.js. It reuses every init.js global (Network,
 * HtmlDocument, Convert, UI, ...) and adds a VideoSource base class plus
 * the result shapes of docs/VIDEO-PLUGIN-DESIGN.md section 4.
 *
 * A video plugin is written like a comic source:
 *
 *   class Demo extends VideoSource {
 *     name = "Demo"; key = "demo"; version = "1.0.0"
 *     author = {name: "Alice", url: "https://github.com/alice"}
 *     homepage = "https://github.com/alice/demo-source"
 *     search = { load: async (keyword, options, page) => ({videos: [new Video({...})], maxPage: 1}) }
 *     video = {
 *       loadInfo: async (id) => new VideoDetails({title, lines: [{id, name, episodes: [{id, title}]}]}),
 *       loadPlay: async (id, lineId, episodeId) => new PlayInfo({url, format: "hls", headers})
 *     }
 *   }
 *
 * Video sources register in VideoSource.sources, separate from
 * ComicSource.sources, so comic UI never lists a video plugin.
 */
(function (root) {
  'use strict';

  const pick = (obj, keys) => {
    const out = {};
    for (const k of keys) {
      if (obj[k] !== undefined) out[k] = obj[k];
    }
    return out;
  };

  /** A search or catalogue result. */
  class Video {
    constructor(fields = {}) {
      Object.assign(this, pick(fields, ['id', 'title', 'cover', 'subtitle', 'tags', 'extra']));
    }
  }

  /** The detail page: playback lines, each with episodes. */
  class VideoDetails {
    constructor(fields = {}) {
      Object.assign(this, pick(fields, ['title', 'cover', 'description', 'tags', 'lines']));
      if (!Array.isArray(this.lines)) this.lines = [];
    }
  }

  /**
   * How to play an episode. Give either `url` (with optional format,
   * headers, variants, subtitles, hls) or `sniff: {page, match,
   * followIframes, timeoutMs}` when only a WebView can find the media.
   */
  class PlayInfo {
    constructor(fields = {}) {
      Object.assign(this, pick(fields, ['url', 'format', 'headers', 'variants', 'subtitles', 'hls', 'sniff', 'encrypted']));
    }
  }

  class VideoSource {
    name = '';
    key = '';
    version;
    minCoreVersion;
    url;
    /** "Name", {name, url} or a list of those. Optional. */
    author;
    /** Project or source page, http(s). Optional. */
    homepage;
    translation = {};

    static sources = {};

    // Storage and settings use the same host messages as comic sources;
    // the host keeps video plugin data in its own namespace by key.
    loadData(dataKey) {
      return sendMessage({ method: 'load_data', key: this.key, data_key: dataKey });
    }
    loadSetting(settingKey) {
      return sendMessage({ method: 'load_setting', key: this.key, setting_key: settingKey });
    }
    saveData(dataKey, data) {
      const msg = { method: 'save_data', key: this.key, data_key: dataKey };
      if (data !== undefined) msg.data = data;
      return sendMessage(msg);
    }
    deleteData(dataKey) {
      return sendMessage({ method: 'delete_data', key: this.key, data_key: dataKey });
    }
    get isLogged() {
      return sendMessage({ method: 'isLogged', key: this.key });
    }
    translate(text) {
      const table = this.translation ? this.translation[APP.locale] : undefined;
      return (table && table[text]) ?? text;
    }
    init() {}
  }

  Object.assign(root, { Video, VideoDetails, PlayInfo, VideoSource });
})(globalThis);
