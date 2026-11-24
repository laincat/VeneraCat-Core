# cheerio bundle notice

English | [简体中文](NOTICE-cheerio.md)

`cheerio.bundle.js` in this directory is bundled from upstream `cheerio` (MIT License, see [LICENSE-cheerio.txt](LICENSE-cheerio.txt)) using `esbuild` so the `NovelSource` LNReader adapter can provide `require("cheerio")` inside MoeJS.

## Version and rebuild command

- `cheerio`: `1.2.0` (entry `cheerio/slim`, without the Node-only `undici` HTTP client)
- `esbuild`: `0.28.2`
- Rebuild command:

```sh
npm install cheerio@1.2.0 esbuild@0.28.2
printf "export * from 'cheerio/slim';\n" > entry.js
npx esbuild entry.js --bundle --format=iife --global-name=__vcCheerio --platform=browser --target=es2020 --minify --outfile=cheerio.bundle.js
```
