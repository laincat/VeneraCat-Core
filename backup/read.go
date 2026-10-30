package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// Identity is a resolved work identity.
type Identity struct {
	// SourceKey is "" when the type hash could not be resolved.
	SourceKey string `json:"sourceKey"`
	ID        string `json:"id"`
	// Type is the original Dart hash, kept so unresolved rows can be restored
	// later when the user installs the source.
	Type int64 `json:"type"`
}

// HistoryItem is one row of history.db.
type HistoryItem struct {
	Identity
	Title        string   `json:"title"`
	Subtitle     string   `json:"subtitle,omitempty"`
	Cover        string   `json:"cover,omitempty"`
	TimeMillis   int64    `json:"time"`
	Episode      int64    `json:"ep"`
	Page         int64    `json:"page"`
	ReadEpisodes []string `json:"readEpisode,omitempty"`
	MaxPage      *int64   `json:"maxPage,omitempty"`
	ChapterGroup *int64   `json:"chapterGroup,omitempty"`
}

// FavoriteItem is one row of a favourite folder.
type FavoriteItem struct {
	Identity
	Name           string   `json:"name"`
	Author         string   `json:"author,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	TranslatedTags []string `json:"translatedTags,omitempty"`
	Cover          string   `json:"cover,omitempty"`
	Time           string   `json:"time,omitempty"`
	DisplayOrder   *int64   `json:"displayOrder,omitempty"`
}

// Folder is a favourite folder.
type Folder struct {
	Name  string         `json:"name"`
	Order *int64         `json:"order,omitempty"`
	Sync  *FolderSync    `json:"sync,omitempty"`
	Items []FavoriteItem `json:"items"`
}

// FolderSync links a folder to a network favourite folder of a source.
type FolderSync struct {
	SourceKey    string `json:"sourceKey"`
	SourceFolder string `json:"sourceFolder"`
}

// ImageFavorite is a row of the image_favorites table. The two JSON columns
// are kept verbatim: their shape changed between Venera versions.
type ImageFavorite struct {
	ID             string          `json:"id"`
	SourceKey      string          `json:"sourceKey"`
	Title          string          `json:"title"`
	SubTitle       string          `json:"subTitle,omitempty"`
	Author         string          `json:"author,omitempty"`
	Tags           string          `json:"tags,omitempty"`
	TranslatedTags string          `json:"translatedTags,omitempty"`
	Time           *int64          `json:"time,omitempty"`
	MaxPage        *int64          `json:"maxPage,omitempty"`
	Episodes       json.RawMessage `json:"episodes"`
	Other          json.RawMessage `json:"other"`
}

// Cookie is a row of cookie.db.
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path,omitempty"`
	Expires  *int64 `json:"expires,omitempty"`
	Secure   bool   `json:"secure"`
	HTTPOnly bool   `json:"httpOnly"`
}

// Source is a comic source found in the backup.
type Source struct {
	Key string `json:"key"`
	// Script is the source text. It is not executed during restore.
	Script string `json:"-"`
	// Data is the per-source JSON (<key>.data), kept as raw text.
	Data       json.RawMessage `json:"-"`
	HasScript  bool            `json:"hasScript"`
	HasData    bool            `json:"hasData"`
	ScriptSize int             `json:"scriptSize"`
}

// Unresolved describes rows whose source could not be identified.
type Unresolved struct {
	Type       int64    `json:"type"`
	Rows       int      `json:"rows"`
	Candidates []string `json:"candidates,omitempty"` // colliding keys, if any
}

// Content is everything Core understood from an archive.
type Content struct {
	History        []HistoryItem              `json:"history"`
	Folders        []Folder                   `json:"folders"`
	ImageFavorites []ImageFavorite            `json:"imageFavorites"`
	Cookies        []Cookie                   `json:"-"`
	CookieCount    int                        `json:"cookieCount"`
	Settings       map[string]json.RawMessage `json:"-"`
	SettingKeys    []string                   `json:"settingKeys"`
	SearchHistory  []string                   `json:"searchHistory"`
	Sources        []Source                   `json:"sources"`
	Unresolved     []Unresolved               `json:"unresolved,omitempty"`
	// Unknown lists members, tables and columns Core did not interpret.
	Unknown  []string `json:"unknown,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// DeviceSettings are dropped from restored settings, as the official client
// does when syncing between devices.
var DeviceSettings = []string{"proxy", "authorizationRequired", "customImageProcessing", "webdav", "disableSyncFields", "deviceId"}

// Options for Read.
type Options struct {
	// KnownKeys are source keys installed in the client; they help resolve
	// type hashes in addition to the keys found in the backup.
	KnownKeys []string
	// TempDir receives extracted database files; os.TempDir() when empty.
	TempDir string
}

// Read parses an archive. It never fails because of one bad row: such rows
// are skipped with a warning. It fails on corrupt databases.
func (a *Archive) Read(ctx context.Context, opts Options) (*Content, error) {
	_ = ctx
	c := &Content{History: []HistoryItem{}, Folders: []Folder{}, ImageFavorites: []ImageFavorite{},
		Sources: []Source{}, SearchHistory: []string{}, SettingKeys: []string{}}
	c.Unknown = append(c.Unknown, a.Unknown...)

	sources, err := a.readSources()
	if err != nil {
		return nil, err
	}
	c.Sources = sources
	keys := make([]string, 0, len(sources))
	for _, s := range sources {
		keys = append(keys, s.Key)
	}
	resolver := NewTypeResolver(keys, opts.KnownKeys, LegacyBuiltinKeys)
	unresolved := map[int64]*Unresolved{}
	resolve := func(t int64, id string) Identity {
		key, ok, cand := resolver.Resolve(t)
		if !ok {
			u := unresolved[t]
			if u == nil {
				u = &Unresolved{Type: t, Candidates: cand}
				unresolved[t] = u
			}
			u.Rows++
		}
		return Identity{SourceKey: key, ID: id, Type: t}
	}

	dir, err := os.MkdirTemp(opts.TempDir, "venera-backup-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	if a.Has(MemberHistory) {
		db, err := a.openDB(dir, MemberHistory)
		if err != nil {
			return nil, err
		}
		err = readHistory(db, c, resolve)
		if err == nil {
			err = readImageFavorites(db, c)
		}
		db.Close()
		if err != nil {
			return nil, fmt.Errorf("backup: history.db: %w", err)
		}
	}
	if a.Has(MemberFavorites) {
		db, err := a.openDB(dir, MemberFavorites)
		if err != nil {
			return nil, err
		}
		err = readFavorites(db, c, resolve)
		db.Close()
		if err != nil {
			return nil, fmt.Errorf("backup: local_favorite.db: %w", err)
		}
	}
	if a.Has(MemberCookies) {
		db, err := a.openDB(dir, MemberCookies)
		if err != nil {
			return nil, err
		}
		err = readCookies(db, c)
		db.Close()
		if err != nil {
			return nil, fmt.Errorf("backup: cookie.db: %w", err)
		}
	}
	if a.Has(MemberAppData) {
		if err := a.readAppData(c); err != nil {
			return nil, err
		}
	}
	for _, u := range unresolved {
		c.Unresolved = append(c.Unresolved, *u)
	}
	sort.Slice(c.Unresolved, func(i, j int) bool { return c.Unresolved[i].Type < c.Unresolved[j].Type })
	sort.Strings(c.Unknown)
	return c, nil
}

func (a *Archive) readSources() ([]Source, error) {
	byKey := map[string]*Source{}
	for _, name := range a.Sources {
		var key string
		var script bool
		switch {
		case strings.HasSuffix(name, ".js"):
			key, script = strings.TrimSuffix(name, ".js"), true
		case strings.HasSuffix(name, ".data"):
			key = strings.TrimSuffix(name, ".data")
		default:
			continue
		}
		if key == "" {
			continue
		}
		b, err := a.Member(SourceDir + name)
		if err != nil {
			return nil, err
		}
		s := byKey[key]
		if s == nil {
			s = &Source{Key: key}
			byKey[key] = s
		}
		if script {
			s.Script, s.HasScript, s.ScriptSize = string(b), true, len(b)
		} else {
			if !json.Valid(b) {
				return nil, fmt.Errorf("backup: %s%s is not valid JSON", SourceDir, name)
			}
			s.Data, s.HasData = append(json.RawMessage(nil), b...), true
		}
	}
	out := make([]Source, 0, len(byKey))
	for _, s := range byKey {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// openDB extracts a database member straight to a temporary file and opens
// it read-only. The member is streamed, never held in memory, so a large
// history database does not get a mobile app killed. Write-ahead and
// journal files are not part of official exports and are ignored.
func (a *Archive) openDB(dir, name string) (*sql.DB, error) {
	p := filepath.Join(dir, filepath.Base(name))
	if err := a.extract(name, p); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(p) + "?mode=ro&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var check string
	if err := db.QueryRow("pragma quick_check").Scan(&check); err != nil || check != "ok" {
		db.Close()
		if err == nil {
			err = errors.New(check)
		}
		return nil, fmt.Errorf("backup: %s is not a valid SQLite database: %w", name, err)
	}
	return db, nil
}

func tables(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query("select name from sqlite_master where type='table'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

func columns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("select name from pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// selectRows reads every row of table as a map of column -> value, so older
// schemas with missing columns still parse.
func selectRows(db *sql.DB, table string) ([]map[string]any, error) {
	rows, err := db.Query("select * from " + quoteIdent(table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func integer(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		return int64(x), x == float64(int64(x))
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	case []byte:
		n, err := strconv.ParseInt(strings.TrimSpace(string(x)), 10, 64)
		return n, err == nil
	}
	return 0, false
}

func optInt(v any) *int64 {
	if n, ok := integer(v); ok {
		return &n
	}
	return nil
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var historyColumns = map[string]bool{"id": true, "title": true, "subtitle": true, "cover": true, "time": true, "type": true,
	"ep": true, "page": true, "readEpisode": true, "max_page": true, "chapter_group": true}

func readHistory(db *sql.DB, c *Content, resolve func(int64, string) Identity) error {
	ts, err := tables(db)
	if err != nil {
		return err
	}
	for t := range ts {
		if t != "history" && t != "image_favorites" && !strings.HasPrefix(t, "sqlite_") {
			c.Unknown = append(c.Unknown, MemberHistory+": table "+t)
		}
	}
	if !ts["history"] {
		return nil
	}
	cols, err := columns(db, "history")
	if err != nil {
		return err
	}
	for col := range cols {
		if !historyColumns[col] {
			c.Unknown = append(c.Unknown, MemberHistory+": history."+col)
		}
	}
	rows, err := selectRows(db, "history")
	if err != nil {
		return err
	}
	for i, r := range rows {
		t, ok := integer(r["type"])
		id := text(r["id"])
		if !ok || id == "" {
			c.Warnings = append(c.Warnings, fmt.Sprintf("history row %d skipped: missing id or type", i+1))
			continue
		}
		h := HistoryItem{Identity: resolve(t, id), Title: text(r["title"]), Subtitle: text(r["subtitle"]),
			Cover: text(r["cover"]), ReadEpisodes: splitList(text(r["readEpisode"])),
			MaxPage: optInt(r["max_page"]), ChapterGroup: optInt(r["chapter_group"])}
		h.TimeMillis, _ = integer(r["time"])
		h.Episode, _ = integer(r["ep"])
		h.Page, _ = integer(r["page"])
		c.History = append(c.History, h)
	}
	sort.SliceStable(c.History, func(i, j int) bool { return c.History[i].TimeMillis > c.History[j].TimeMillis })
	return nil
}

func readImageFavorites(db *sql.DB, c *Content) error {
	ts, err := tables(db)
	if err != nil || !ts["image_favorites"] {
		return err
	}
	rows, err := selectRows(db, "image_favorites")
	if err != nil {
		return err
	}
	for i, r := range rows {
		ep, other := text(r["image_favorites_ep"]), text(r["other"])
		if !json.Valid([]byte(ep)) || (other != "" && !json.Valid([]byte(other))) {
			c.Warnings = append(c.Warnings, fmt.Sprintf("image favourite row %d skipped: invalid JSON", i+1))
			continue
		}
		if other == "" {
			other = "{}"
		}
		c.ImageFavorites = append(c.ImageFavorites, ImageFavorite{
			ID: text(r["id"]), SourceKey: text(r["source_key"]), Title: text(r["title"]),
			SubTitle: text(r["sub_title"]), Author: text(r["author"]), Tags: text(r["tags"]),
			TranslatedTags: text(r["translated_tags"]), Time: optInt(r["time"]), MaxPage: optInt(r["max_page"]),
			Episodes: json.RawMessage(ep), Other: json.RawMessage(other),
		})
	}
	return nil
}

var favoriteMeta = map[string]bool{"folder_order": true, "folder_sync": true}

func readFavorites(db *sql.DB, c *Content, resolve func(int64, string) Identity) error {
	ts, err := tables(db)
	if err != nil {
		return err
	}
	order := map[string]int64{}
	if ts["folder_order"] {
		rows, err := selectRows(db, "folder_order")
		if err != nil {
			return err
		}
		for _, r := range rows {
			if n, ok := integer(r["order_value"]); ok {
				order[text(r["folder_name"])] = n
			}
		}
	}
	syncs := map[string]*FolderSync{}
	if ts["folder_sync"] {
		rows, err := selectRows(db, "folder_sync")
		if err != nil {
			return err
		}
		for _, r := range rows {
			syncs[text(r["folder_name"])] = &FolderSync{SourceKey: text(r["source_key"]), SourceFolder: text(r["source_folder"])}
		}
	}
	var names []string
	for t := range ts {
		if !favoriteMeta[t] && !strings.HasPrefix(t, "sqlite_") {
			names = append(names, t)
		}
	}
	for _, name := range names {
		cols, err := columns(db, name)
		if err != nil {
			return err
		}
		if !cols["id"] || !cols["type"] {
			c.Unknown = append(c.Unknown, MemberFavorites+": table "+name)
			continue
		}
		rows, err := selectRows(db, name)
		if err != nil {
			return err
		}
		f := Folder{Name: name, Sync: syncs[name], Items: []FavoriteItem{}}
		if o, ok := order[name]; ok {
			f.Order = &o
		}
		for i, r := range rows {
			t, ok := integer(r["type"])
			id := text(r["id"])
			if !ok || id == "" {
				c.Warnings = append(c.Warnings, fmt.Sprintf("folder %q row %d skipped: missing id or type", name, i+1))
				continue
			}
			f.Items = append(f.Items, FavoriteItem{Identity: resolve(t, id), Name: text(r["name"]),
				Author: text(r["author"]), Tags: splitList(text(r["tags"])), TranslatedTags: splitList(text(r["translated_tags"])),
				Cover: text(r["cover_path"]), Time: text(r["time"]), DisplayOrder: optInt(r["display_order"])})
		}
		sort.SliceStable(f.Items, func(i, j int) bool {
			a, b := f.Items[i].DisplayOrder, f.Items[j].DisplayOrder
			if a == nil || b == nil {
				return a != nil
			}
			return *a < *b
		})
		c.Folders = append(c.Folders, f)
	}
	sort.SliceStable(c.Folders, func(i, j int) bool {
		a, b := c.Folders[i].Order, c.Folders[j].Order
		switch {
		case a != nil && b != nil && *a != *b:
			return *a < *b
		case (a == nil) != (b == nil):
			return a != nil
		}
		return c.Folders[i].Name < c.Folders[j].Name
	})
	return nil
}

func readCookies(db *sql.DB, c *Content) error {
	ts, err := tables(db)
	if err != nil || !ts["cookies"] {
		return err
	}
	rows, err := selectRows(db, "cookies")
	if err != nil {
		return err
	}
	for _, r := range rows {
		ck := Cookie{Name: text(r["name"]), Value: text(r["value"]), Domain: text(r["domain"]), Path: text(r["path"]),
			Expires: optInt(r["expires"])}
		if n, _ := integer(r["secure"]); n != 0 {
			ck.Secure = true
		}
		if n, _ := integer(r["httpOnly"]); n != 0 {
			ck.HTTPOnly = true
		}
		if ck.Name == "" || ck.Domain == "" {
			continue
		}
		c.Cookies = append(c.Cookies, ck)
	}
	c.CookieCount = len(c.Cookies)
	return nil
}

func (a *Archive) readAppData(c *Content) error {
	b, err := a.Member(MemberAppData)
	if err != nil {
		return err
	}
	var doc struct {
		Settings      map[string]json.RawMessage `json:"settings"`
		SearchHistory []json.RawMessage          `json:"searchHistory"`
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("backup: appdata.json: %w", err)
	}
	drop := map[string]bool{}
	for _, k := range DeviceSettings {
		drop[k] = true
	}
	c.Settings = map[string]json.RawMessage{}
	for k, v := range doc.Settings {
		if drop[k] {
			continue
		}
		c.Settings[k] = v
		c.SettingKeys = append(c.SettingKeys, k)
	}
	sort.Strings(c.SettingKeys)
	for _, v := range doc.SearchHistory {
		var s string
		if json.Unmarshal(v, &s) == nil {
			c.SearchHistory = append(c.SearchHistory, s)
		}
	}
	return nil
}
