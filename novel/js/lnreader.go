package js

import (
	_ "embed"

	comicjs "github.com/laincat/veneracat-core/comic/js"
)

//go:embed cheerio.bundle.js
var cheerioBundle string

//go:embed lnreader-prelude.js
var lnreaderPrelude string

func init() {
	comicjs.RegisterKindPrelude(comicjs.KindNovel, "<cheerio-bundle>", cheerioBundle)
	comicjs.RegisterKindPrelude(comicjs.KindNovel, "<lnreader-prelude>", lnreaderPrelude)
}
