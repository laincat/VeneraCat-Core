# The official `.venera` backup format and how Core reads and restores it

English | [简体中文](VENERA-BACKUP.md)

Date: 2026-10-29. Source: the official `venera-app/venera@a0eba914` (GPL-3.0), `exportAppData` and `importAppData` in `lib/utils/data.dart`. The table schemas come from `foundation/history.dart`, `favorites.dart`, `image_favorites.dart`, `network/cookie_jar.dart` and `foundation/appdata.dart`. The hash algorithm comes from the Dart SDK: `StringHasher` in `runtime/vm/object.h` and `runtime/vm/hash.h`.

## 1. Container

A `.venera` file is a plain ZIP. The official export writes these members (all at the root except the source files):

| Member | Contents |
|---|---|
| `history.db` | SQLite, reading history |
| `local_favorite.db` | SQLite, local favourite folders (one table per folder) |
| `appdata.json` | Settings and search history (taken from `syncdata.json`, with device-specific fields removed) |
| `cookie.db` | SQLite, cookies |
| `comic_source/*` | Comic source scripts `<key>.js` and source data `<key>.data` (JSON) |

The official import **overwrites** the local files of the same names: no merge, no preview, no rollback. Core instead previews first, restores what the user selects, and never runs source scripts during a restore.

## 2. Table schemas

`history.db`:

```sql
history(id text primary key, title text, subtitle text, cover text,
        time int, type int, ep int, page int, readEpisode text,
        max_page int, chapter_group int)   -- chapter_group was added later; old backups may lack it
```

`readEpisode` is a comma-separated set; `ep` and `page` start at 1; `time` is a millisecond timestamp.

`local_favorite.db` has the metadata tables `folder_order(folder_name, order_value)` and `folder_sync(folder_name, source_key, source_folder)`; every other table is a folder:

```sql
"<folder>"(id text, name TEXT, author TEXT, type int, tags TEXT,
           cover_path TEXT, time TEXT, display_order int,
           translated_tags TEXT, primary key (id, type))  -- translated_tags was added later
```

`history.db` also holds `image_favorites(id, title, sub_title, author, tags, translated_tags, time, max_page, source_key, image_favorites_ep, other)`, where `image_favorites_ep` and `other` are JSON text.

`cookie.db`: `cookies(name, value, domain, path, expires, secure, httpOnly)`.

## 3. The `type` column: a Dart hash of the source key

`type` is not the source key but `key.hashCode` (the Dart VM's `String.hashCode`); `0` means the local comic `local`. The hash is **one-way**, so it can only be resolved against a list of known keys.

The Dart VM string hash (native platforms; the web build differs and is out of scope):

```
h = 0
for each UTF-16 code unit c:
    h = h + c;  h = h + (h << 10);  h = h ^ (h >> 6)      // uint32 arithmetic
h = h + (h << 3);  h = h ^ (h >> 11);  h = h + (h << 15)
h = h & (1<<30 - 1)                                     // kHashBits = 30
if h == 0: h = 1
```

Keys are tried in this order:

1. The file-name keys of every `<key>.js` and `<key>.data` in the backup's `comic_source/`;
2. The keys of the sources installed in the client;
3. The historical official built-in keys (`picacg ehentai jm hitomi wnacg nhentai`).

Rows that cannot be resolved are **kept unchanged** and listed in the preview; they must not be treated as empty data or as deletions.

## 4. Core behaviour

- **Read-only parsing**: limits the number of entries and the size of each file and of the whole decompressed archive; rejects absolute paths, `..` and duplicate names; parses only the known members and lists the rest as "not recognised, kept unchanged". Databases are streamed to temporary files instead of being held in memory.
- **Preview**: history, favourite folders, image favourites, the number of cookies, sources, setting keys, unresolved `type` values, and unrecognised members and columns.
- **Restore**: the caller selects items and Core returns structured data; it does not write the client's databases. Source scripts are handed to the client's install flow as text and are **not executed** during restore.
- Cookies are sensitive and are not restored unless explicitly selected.
- Restoring cookies does not recreate a shared jar: the original app had one jar, this one has a
  partition per source. The plan's `cookieTargets` comes from `backup.CookieTargets` — the sources
  the backup itself carries, because the import installs them. "Already installed here" cannot be
  the condition: a new device has nothing installed, and that is the case this migration is for.
  A backup with no source scripts falls back to every installed partition. The cookies themselves are
  not filtered per source: a jar only sends a cookie to a matching domain, so handing one to a
  source that never visits that host is inert. Rows without a name, without a value, or with a
  domain that is not a host are dropped rather than stored as records that can never match.
- Settings drop the official device-specific fields: `proxy authorizationRequired customImageProcessing webdav disableSyncFields deviceId`.
