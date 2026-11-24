/*
 * VeneraCat Core: NovelSource prelude.
 *
 * Original work, VeneraCat extension; not part of the official ComicSource
 * API. Loaded after init.js. It reuses every init.js global (Network, fetch,
 * HtmlDocument, Convert, UI, APP, log, sendMessage, ...) and adds a
 * NovelSource base class.
 *
 * Novel sources register in NovelSource.sources, separate from
 * ComicSource.sources and VideoSource.sources.
 */
(function (root) {
  'use strict';

  class NovelSource {
    name = '';
    key = '';
    version;
    minAppVersion;
    url;
    author;
    homepage;
    translation = {};

    static sources = {};

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

  Object.assign(root, { NovelSource });
})(globalThis);
