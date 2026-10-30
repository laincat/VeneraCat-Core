# 官方 `.venera` 备份格式与 Core 的读入和恢复

[English](VENERA-BACKUP.en.md) | 简体中文

日期:2026-10-29。来源:官方 `venera-app/venera@a0eba914`(GPL-3.0)
`lib/utils/data.dart` 的 `exportAppData` / `importAppData`,表结构取自
`foundation/history.dart`、`favorites.dart`、`image_favorites.dart`、
`network/cookie_jar.dart`、`foundation/appdata.dart`。哈希算法取自
Dart SDK `runtime/vm/object.h`(`StringHasher`)与 `runtime/vm/hash.h`。

## 1. 容器

`.venera` 是普通 ZIP。官方导出写入这些成员(都在根目录,只有源文件在子目录):

| 成员 | 内容 |
|---|---|
| `history.db` | SQLite,阅读历史 |
| `local_favorite.db` | SQLite,本地收藏夹(每个文件夹一张表) |
| `appdata.json` | 设置与搜索历史(实际取自 `syncdata.json`,已去掉设备相关字段) |
| `cookie.db` | SQLite,Cookie |
| `comic_source/*` | 漫画源脚本 `<key>.js` 与源数据 `<key>.data`(JSON) |

官方导入**整体覆盖**本地同名文件,不合并、不预览、不回滚。Core 改为:先预览,
再按项选择恢复,并且永不在恢复中执行源脚本。

## 2. 表结构

`history.db`:

```sql
history(id text primary key, title text, subtitle text, cover text,
        time int, type int, ep int, page int, readEpisode text,
        max_page int, chapter_group int)   -- chapter_group 为后加列,旧备份可能没有
```

`readEpisode` 是逗号连接的集合;`ep`、`page` 从 1 开始;`time` 为毫秒时间戳。

`local_favorite.db`:元数据表 `folder_order(folder_name, order_value)`、
`folder_sync(folder_name, source_key, source_folder)`;其余每张表都是一个文件夹:

```sql
"<folder>"(id text, name TEXT, author TEXT, type int, tags TEXT,
           cover_path TEXT, time TEXT, display_order int,
           translated_tags TEXT, primary key (id, type))  -- translated_tags 后加
```

`history.db` 里还有 `image_favorites(id, title, sub_title, author, tags,
translated_tags, time, max_page, source_key, image_favorites_ep, other)`,
其中 `image_favorites_ep` 和 `other` 是 JSON 文本。

`cookie.db`:`cookies(name, value, domain, path, expires, secure, httpOnly)`。

## 3. `type` 列:来源 key 的 Dart 哈希

`type` 不是来源 key,而是 `key.hashCode`(Dart VM 的 `String.hashCode`);
`0` 表示本地漫画 `local`。这个哈希**单向**,只能用已知 key 列表反查。

Dart VM 字符串哈希(原生平台;Web 端不同,不在兼容范围):

```
h = 0
for each UTF-16 code unit c:
    h = h + c;  h = h + (h << 10);  h = h ^ (h >> 6)      // uint32 运算
h = h + (h << 3);  h = h ^ (h >> 11);  h = h + (h << 15)
h = h & (1<<30 - 1)                                     // kHashBits = 30
if h == 0: h = 1
```

反查顺序:
1. 备份 `comic_source/` 里每个 `<key>.js`/`<key>.data` 的文件名 key;
2. 客户端已安装源的 key;
3. 官方历史内置 key(`picacg ehentai jm hitomi wnacg nhentai`)。

反查不到的行**原样保留**并在预览里列出,不能当成空数据或删除。

## 4. Core 行为

- **只读解析**:限制条目数、单个文件和整体解压后的大小;拒绝绝对路径、`..` 和重复名称;只解析已知成员,其余成员列为"未识别,原样保留"。数据库流式写入临时文件,不整个读进内存。
- **预览**:历史、收藏夹、图片收藏、Cookie 数量、源列表、设置键、
  无法反查的 `type`、未识别的成员和列。
- **恢复**:调用方选择项目,Core 输出结构化数据(不写客户端数据库)。
  源脚本只作为文本交给客户端安装流程,**恢复时不执行**。
- Cookie 属于敏感数据,默认不恢复,需要显式选择。
- 恢复 Cookie 不会退回共享 jar:官方只有一个 jar,这里每个源一个分区。计划里的 `cookieTargets` 由
  `backup.CookieTargets` 决定 —— 取**备份自己带的源**,因为导入会把这些源装上;
  「本机已装」不能作为条件:新设备上什么都没装,而那正是这次迁移最要紧的场景。
  备份没有携带源脚本时,退化为所有已装分区。Cookie 本身不按源筛选:jar 只在域名匹配时送出,
  送给不访问该域的源等于没送。缺名字、缺值、或域名不是主机的行直接丢弃,免得存下永远匹配不到的记录。
- 设置去掉官方的设备相关字段:`proxy authorizationRequired
  customImageProcessing webdav disableSyncFields deviceId`。
