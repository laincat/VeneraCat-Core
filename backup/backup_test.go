package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Hash values computed from the Dart VM algorithm (runtime/vm/object.h
// StringHasher + runtime/vm/hash.h FinalizeHash, kHashBits = 30). No Dart SDK
// was available to cross-check by running `'key'.hashCode`; the vectors pin
// this implementation and the edge cases of the algorithm.
func TestDartStringHash(t *testing.T) {
	if DartStringHash("") != 1 {
		t.Fatalf("empty string must hash to 1 (FinalizeHash maps 0 to 1), got %d", DartStringHash(""))
	}
	seen := map[int64]string{}
	for _, k := range append(LegacyBuiltinKeys, "copy_manga", "baozi", "local_x", "绅士", "😀") {
		h := DartStringHash(k)
		if h <= 0 || h >= 1<<30 {
			t.Fatalf("%q -> %d out of 30-bit range", k, h)
		}
		if prev, dup := seen[h]; dup {
			t.Fatalf("collision %q and %q", prev, k)
		}
		seen[h] = k
		if DartStringHash(k) != h {
			t.Fatal("not deterministic")
		}
	}
	// Cross-checked against an independent Python implementation of the same
	// algorithm (uint32 arithmetic, UTF-16 code units, 30-bit mask).
	for k, want := range map[string]int64{"a": 170824770, "wnacg": 823512256, "picacg": 553570794, "jm": 769844263} {
		if got := DartStringHash(k); got != want {
			t.Fatalf("DartStringHash(%q) = %d, want %d", k, got, want)
		}
	}
	// Non-BMP characters hash as two UTF-16 code units, as in Dart.
	if DartStringHash("😀") == DartStringHash("\uFFFD") {
		t.Fatal("surrogate pair handling")
	}
}

func TestTypeResolver(t *testing.T) {
	r := NewTypeResolver([]string{"wnacg", "copy_manga"}, LegacyBuiltinKeys)
	if k, ok, _ := r.Resolve(DartStringHash("copy_manga")); !ok || k != "copy_manga" {
		t.Fatalf("copy_manga -> %q %v", k, ok)
	}
	if k, ok, _ := r.Resolve(0); !ok || k != "local" {
		t.Fatalf("0 -> %q %v", k, ok)
	}
	if _, ok, _ := r.Resolve(DartStringHash("not_installed")); ok {
		t.Fatal("unknown key resolved")
	}
}

// --- fixture ---------------------------------------------------------------

type fixture struct {
	t     *testing.T
	dir   string
	files map[string][]byte
}

func newFixture(t *testing.T) *fixture {
	return &fixture{t: t, dir: t.TempDir(), files: map[string][]byte{}}
}

func (f *fixture) db(name string, stmts ...string) {
	f.t.Helper()
	p := filepath.Join(f.dir, name)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(p))
	if err != nil {
		f.t.Fatal(err)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			f.t.Fatalf("%s: %v", s, err)
		}
	}
	db.Close()
	b, err := os.ReadFile(p)
	if err != nil {
		f.t.Fatal(err)
	}
	f.files[name] = b
}

func (f *fixture) zip(extra ...[2]string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	add := func(name string, b []byte) {
		fw, err := w.Create(name)
		if err != nil {
			f.t.Fatal(err)
		}
		fw.Write(b)
	}
	for n, b := range f.files {
		add(n, b)
	}
	for _, e := range extra {
		add(e[0], []byte(e[1]))
	}
	w.Close()
	return buf.Bytes()
}

func hashSQL(key string) string { return strconv.FormatInt(DartStringHash(key), 10) }

// officialBackup builds an archive with the schema of venera@a0eba914.
func officialBackup(t *testing.T) []byte {
	f := newFixture(t)
	wn, cm, missing := hashSQL("wnacg"), hashSQL("copy_manga"), hashSQL("gone_source")
	f.db("history.db",
		`create table history (id text primary key, title text, subtitle text, cover text, time int, type int,
			ep int, page int, readEpisode text, max_page int, chapter_group int)`,
		`insert into history values ('111','作品一','作者','https://c/1.jpg',1700000000000,`+wn+`,2,5,'1,2',30,null)`,
		`insert into history values ('c-9','Copy','','',1700000005000,`+cm+`,1,1,'',null,1)`,
		`insert into history values ('lost','Lost','','',1600000000000,`+missing+`,1,1,'1',null,null)`,
		`insert into history values ('local1','Local','','',1500000000000,0,1,3,'',10,null)`,
		`create table image_favorites (id TEXT, title TEXT NOT NULL, sub_title TEXT, author TEXT, tags TEXT,
			translated_tags TEXT, time int, max_page int, source_key TEXT NOT NULL, image_favorites_ep TEXT NOT NULL,
			other TEXT NOT NULL, PRIMARY KEY (id,source_key))`,
		`insert into image_favorites values ('111','作品一','','','a,b','',1700000000000,30,'wnacg','[{"ep":1,"pages":[3]}]','{}')`,
	)
	f.db("local_favorite.db",
		`create table folder_order (folder_name text primary key, order_value int)`,
		`create table folder_sync (folder_name text primary key, source_key text, source_folder text)`,
		`insert into folder_order values ('默认', 1), ('Later', 0)`,
		`insert into folder_sync values ('默认', 'wnacg', '0')`,
		`create table "默认" (id text, name TEXT, author TEXT, type int, tags TEXT, cover_path TEXT, time TEXT,
			display_order int, translated_tags TEXT, primary key (id, type))`,
		`insert into "默认" values ('111','作品一','作者',`+wn+`,'tag1,tag2','https://c/1.jpg','2023-11-14 22:13:20',2,null)`,
		`insert into "默认" values ('lost','Lost','',`+missing+`,'','','',1,null)`,
		// An older folder without translated_tags and display_order.
		`create table "Later" (id text, name TEXT, author TEXT, type int, tags TEXT, cover_path TEXT, time TEXT,
			primary key (id, type))`,
		`insert into "Later" values ('c-9','Copy','',`+cm+`,'','','')`,
	)
	f.db("cookie.db",
		`create table cookies (name TEXT NOT NULL, value TEXT NOT NULL, domain TEXT NOT NULL, path TEXT,
			expires INTEGER, secure INTEGER, httpOnly INTEGER, PRIMARY KEY (name, domain, path))`,
		`insert into cookies values ('sid','secret','.wnacg.com','/',1900000000000,1,1)`,
	)
	return f.zip(
		[2]string{"appdata.json", `{"settings":{"theme":"dark","proxy":"http://p:1","webdav":["x"],"dataVersion":12345678901234567890},"searchHistory":["a","b"]}`},
		[2]string{"comic_source/wnacg.js", "class W extends ComicSource { key = 'wnacg' }"},
		[2]string{"comic_source/wnacg.data", `{"token":"t","n":12345678901234567890}`},
		[2]string{"comic_source/copy_manga.data", `{}`},
		[2]string{"extra/notes.txt", "kept"},
	)
}

func open(t *testing.T, b []byte) *Archive {
	t.Helper()
	a, err := Open(bytes.NewReader(b), int64(len(b)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestReadOfficialBackup(t *testing.T) {
	a := open(t, officialBackup(t))
	c, err := a.Read(context.Background(), Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.History) != 4 || c.History[0].ID != "c-9" || c.History[0].SourceKey != "copy_manga" {
		t.Fatalf("history = %+v", c.History)
	}
	byID := map[string]HistoryItem{}
	for _, h := range c.History {
		byID[h.ID] = h
	}
	if h := byID["111"]; h.SourceKey != "wnacg" || h.Episode != 2 || h.Page != 5 || strings.Join(h.ReadEpisodes, ",") != "1,2" || *h.MaxPage != 30 {
		t.Fatalf("history 111 = %+v", h)
	}
	if byID["local1"].SourceKey != "local" || byID["lost"].SourceKey != "" {
		t.Fatalf("local/lost = %+v / %+v", byID["local1"], byID["lost"])
	}
	if len(c.Unresolved) != 1 || c.Unresolved[0].Type != DartStringHash("gone_source") || c.Unresolved[0].Rows != 2 {
		t.Fatalf("unresolved = %+v", c.Unresolved)
	}
	if len(c.Folders) != 2 || c.Folders[0].Name != "Later" || c.Folders[1].Name != "默认" {
		t.Fatalf("folder order = %+v", c.Folders)
	}
	def := c.Folders[1]
	if def.Sync == nil || def.Sync.SourceKey != "wnacg" || len(def.Items) != 2 || def.Items[0].ID != "lost" {
		t.Fatalf("默认 = %+v", def)
	}
	if def.Items[1].Tags[1] != "tag2" {
		t.Fatalf("tags = %+v", def.Items[1].Tags)
	}
	if len(c.ImageFavorites) != 1 || string(c.ImageFavorites[0].Episodes) != `[{"ep":1,"pages":[3]}]` {
		t.Fatalf("image favourites = %+v", c.ImageFavorites)
	}
	if c.CookieCount != 1 || !c.Cookies[0].HTTPOnly {
		t.Fatalf("cookies = %+v", c.Cookies)
	}
	if _, ok := c.Settings["proxy"]; ok {
		t.Fatal("device setting proxy was kept")
	}
	if string(c.Settings["dataVersion"]) != "12345678901234567890" {
		t.Fatalf("dataVersion lost precision: %s", c.Settings["dataVersion"])
	}
	if strings.Join(c.SearchHistory, ",") != "a,b" {
		t.Fatalf("search history = %v", c.SearchHistory)
	}
	if len(c.Sources) != 2 || !c.Sources[1].HasScript || string(c.Sources[1].Data) != `{"token":"t","n":12345678901234567890}` {
		t.Fatalf("sources = %+v", c.Sources)
	}
	if len(c.Unknown) != 1 || c.Unknown[0] != "extra/notes.txt" {
		t.Fatalf("unknown = %v", c.Unknown)
	}
}

func TestPlan(t *testing.T) {
	a := open(t, officialBackup(t))
	c, err := a.Read(context.Background(), Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Plan(DefaultSelection())
	if err != nil {
		t.Fatal(err)
	}
	s := p.Summary()
	if s.History != 3 || s.PendingHistory != 1 || s.FavoriteItems != 2 || s.PendingFavorites != 1 || s.Cookies != 0 || s.Sources != 2 {
		t.Fatalf("summary = %+v", s)
	}
	p, _ = c.Plan(Selection{Cookies: true})
	if len(p.Cookies) != 1 || len(p.History) != 0 {
		t.Fatal("cookie-only selection")
	}
	p, _ = c.Plan(Selection{Favorites: true, Folders: []string{"Later"}})
	if len(p.Folders) != 1 || p.Folders[0].Name != "Later" {
		t.Fatalf("folder selection = %+v", p.Folders)
	}
	if _, err := c.Plan(Selection{Favorites: true, Folders: []string{"nope"}}); err == nil {
		t.Fatal("unknown folder accepted")
	}
	// Restore never executes scripts: the plan only carries their text. With
	// scripts only, copy_manga (data but no script) is left out.
	p, _ = c.Plan(Selection{Sources: true})
	if len(p.Sources) != 1 || p.Sources[0].Key != "wnacg" || p.Sources[0].Script == "" || p.Sources[0].Data != nil {
		t.Fatalf("source-only selection = %+v", p.Sources)
	}
	// KnownKeys resolves a source the backup does not contain.
	c2, _ := open(t, officialBackup(t)).Read(context.Background(), Options{TempDir: t.TempDir(), KnownKeys: []string{"gone_source"}})
	if len(c2.Unresolved) != 0 {
		t.Fatalf("KnownKeys did not resolve: %+v", c2.Unresolved)
	}
}

func zipOf(t *testing.T, entries ...[2]string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		fw, err := w.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte(e[1]))
	}
	w.Close()
	return buf.Bytes()
}

func TestOpenRejects(t *testing.T) {
	cases := map[string][]byte{
		"not zip":      []byte("hello"),
		"no data":      zipOf(t, [2]string{"readme.txt", "x"}),
		"traversal":    zipOf(t, [2]string{"../evil.js", "x"}, [2]string{"appdata.json", "{}"}),
		"nested trav":  zipOf(t, [2]string{"comic_source/../../x", "x"}, [2]string{"appdata.json", "{}"}),
		"absolute":     zipOf(t, [2]string{"/etc/passwd", "x"}, [2]string{"appdata.json", "{}"}),
		"drive letter": zipOf(t, [2]string{"C:/x", "x"}, [2]string{"appdata.json", "{}"}),
		"backslash":    zipOf(t, [2]string{`comic_source\x.js`, "x"}, [2]string{"appdata.json", "{}"}),
		"duplicate":    zipOf(t, [2]string{"appdata.json", "{}"}, [2]string{"appdata.json", "{}"}),
	}
	for name, b := range cases {
		if _, err := Open(bytes.NewReader(b), int64(len(b)), Limits{}); !errors.Is(err, ErrInvalidArchive) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	big := zipOf(t, [2]string{"appdata.json", "{}"}, [2]string{"comic_source/a.js", strings.Repeat("x", 2000)})
	if _, err := Open(bytes.NewReader(big), int64(len(big)), Limits{MaxSourceBytes: 1000}); err == nil {
		t.Error("oversized source accepted")
	}
	if _, err := Open(bytes.NewReader(big), int64(len(big)), Limits{MaxTotalBytes: 100}); err == nil {
		t.Error("archive over the total limit accepted")
	}
}

func TestReadRejectsCorruptData(t *testing.T) {
	b := zipOf(t, [2]string{"history.db", "this is not sqlite"})
	a := open(t, b)
	if _, err := a.Read(context.Background(), Options{TempDir: t.TempDir()}); err == nil {
		t.Fatal("corrupt database accepted")
	}
	b = zipOf(t, [2]string{"comic_source/x.data", "{not json"})
	a = open(t, b)
	if _, err := a.Read(context.Background(), Options{TempDir: t.TempDir()}); err == nil {
		t.Fatal("invalid source data accepted")
	}
}

func TestOldSchemaWithoutOptionalTables(t *testing.T) {
	f := newFixture(t)
	f.db("history.db", `create table history (id text primary key, title text, subtitle text, cover text, time int,
		type int, ep int, page int, readEpisode text, max_page int)`,
		`insert into history values ('1','t','','',1,`+hashSQL("jm")+`,1,1,'',null)`)
	a := open(t, f.zip())
	c, err := a.Read(context.Background(), Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.History) != 1 || c.History[0].SourceKey != "jm" || c.History[0].ChapterGroup != nil {
		t.Fatalf("history = %+v", c.History)
	}
}

func TestDecompressedSizeIsCounted(t *testing.T) {
	// Open checks the sizes the central directory declares; Read must also
	// count the bytes the decompressor actually produces, because those
	// headers can lie. Lowering the limit after Open stands in for a lying
	// header: two 600 KiB sources must exceed a 1 MiB total.
	big := strings.Repeat("x", 600<<10)
	b := zipOf(t, [2]string{"comic_source/a.js", big}, [2]string{"comic_source/b.js", big})
	a, err := Open(bytes.NewReader(b), int64(len(b)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	a.limits.MaxTotalBytes = 1 << 20
	if _, err := a.Read(context.Background(), Options{TempDir: t.TempDir()}); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("expanded size not enforced: %v", err)
	}
}
