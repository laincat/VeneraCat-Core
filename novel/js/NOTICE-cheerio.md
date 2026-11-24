# cheerio 打包说明

[English](NOTICE-cheerio.en.md) | 简体中文

本目录的 `cheerio.bundle.js` 由上游 `cheerio`（MIT 许可证，见 [LICENSE-cheerio.txt](LICENSE-cheerio.txt)）通过 `esbuild` 打包生成，供 `NovelSource` 的 LNReader 适配层在 MoeJS 引擎内提供 `require("cheerio")`。

## 版本与复现命令

- `cheerio`：`1.2.0`（入口 `cheerio/slim`，不含 Node 专用 `undici` 网络层）
- `esbuild`：`0.28.2`
- 复现命令：

```sh
npm install cheerio@1.2.0 esbuild@0.28.2
printf "export * from 'cheerio/slim';\n" > entry.js
npx esbuild entry.js --bundle --format=iife --global-name=__vcCheerio --platform=browser --target=es2020 --minify --outfile=cheerio.bundle.js
```
