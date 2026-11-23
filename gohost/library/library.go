// Package library installs comic/video sources and keeps the installed set.
//
// Every change is written back through Core's records, so the next launch
// restores the same sources. Installing takes a script URL, a script's text,
// or an entry of a comic index (index.json).
package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"sync"
	"time"

	core "github.com/laincat/veneracat-core/gohost"
	"github.com/laincat/veneracat-core/gohost/cookies"
	"github.com/laincat/veneracat-core/plugin"
	"github.com/laincat/veneracat-core/subscription"
)

// MaxScriptBytes bounds a downloaded script or index.
const MaxScriptBytes = 4 << 20

// Library is the installed sources and their indexes.
type Library struct {
	core *core.Core

	// op keeps registry mutations and their persistence/rollback together.
	op      sync.Mutex
	mu      sync.Mutex
	records []plugin.Record
}

// New wraps a Core.
func New(c *core.Core) *Library { return &Library{core: c} }

// Restore loads the saved records into Core. It returns the sources that
// could not be restored, keyed by key.
func (l *Library) Restore(ctx context.Context) (map[string]error, error) {
	l.op.Lock()
	defer l.op.Unlock()
	data, err := l.core.Store().Records()
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var recs []plugin.Record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("library: records: %w", err)
	}
	failed := l.core.Restore(ctx, recs)
	l.refresh()
	return failed, nil
}

// Sources lists installed plugins without script bodies.
//
// It reads Core every time: a source can also be installed through Core
// directly (a tool, a future import path), and the interface must not show a
// stale list.
func (l *Library) Sources() []plugin.Record { return l.core.Plugins() }

// ComicSources is the list the comic interface may open: a video provider and
// a manga source are both plugins, but only one of them answers a search for
// comics, and Core refuses the other with "not this kind of plugin".
func (l *Library) ComicSources() []plugin.Record {
	var out []plugin.Record
	for _, rec := range l.core.Plugins() {
		if rec.Type == plugin.TypeComic {
			out = append(out, rec)
		}
	}
	return out
}

// Jar is where source cookies live, one partition per source. A backup import
// needs it to restore sign-ins without inventing a shared jar.
func (l *Library) Jar() *cookies.Jar { return l.core.Jar() }

// IsInstalled reports whether key is installed.
func (l *Library) IsInstalled(key string) bool {
	_, ok := l.core.Record(key)
	return ok
}

// InstallText installs a script given as text (paste or file import).
func (l *Library) InstallText(ctx context.Context, body, origin string) (*plugin.Record, error) {
	return l.InstallTypedText(ctx, plugin.TypeComic, body, origin)
}

// InstallTypedText installs or updates the explicit kind selected by the user.
// Core owns validation and type checks; the library persists the resulting set.
func (l *Library) InstallTypedText(ctx context.Context, typ plugin.Type, body, origin string) (*plugin.Record, error) {
	return l.installOrReplace(ctx, typ, body, origin, "")
}

// InstallExpecting checks a catalogue entry's identity before binding storage or cookies.
func (l *Library) InstallExpecting(ctx context.Context, key string, typ plugin.Type, body, origin, digest string) (*plugin.Record, error) {
	if key == "" && digest == "" {
		return l.InstallTypedText(ctx, typ, body, origin)
	}
	l.op.Lock()
	defer l.op.Unlock()
	if l.IsInstalled(key) {
		return l.replaceTypedLocked(ctx, key, typ, body, origin, "", digest, true)
	}
	rec, err := l.core.InstallExpecting(ctx, key, typ, body, origin, "", digest)
	if err != nil {
		if key == "" {
			if existing := keyOf(err); existing != "" && l.IsInstalled(existing) {
				return l.replaceTypedLocked(ctx, existing, typ, body, origin, "", digest, true)
			}
		}
		return nil, err
	}
	return l.persistInstalled(ctx, rec)
}

// InstallURL downloads and installs a comic script, updating an existing key.
func (l *Library) InstallURL(ctx context.Context, rawURL string) (*plugin.Record, error) {
	return l.InstallTypedURL(ctx, plugin.TypeComic, rawURL)
}

// InstallTypedURL uses the same download bound and persistence for video and
// comic providers. Source type is selected, never guessed from script text.
func (l *Library) InstallTypedURL(ctx context.Context, typ plugin.Type, rawURL string) (*plugin.Record, error) {
	body, err := l.fetch(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return l.installOrReplace(ctx, typ, string(body), rawURL, "")
}

// InstallFile imports a local script/rule through the same path as URL installs.
func (l *Library) InstallFile(ctx context.Context, typ plugin.Type, path string) (*plugin.Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, MaxScriptBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxScriptBytes {
		return nil, errors.New("library: source file exceeds 4 MiB")
	}
	return l.installOrReplace(ctx, typ, string(body), path, "")
}

// InstallEntry installs or updates the source an index entry names.
func (l *Library) InstallEntry(ctx context.Context, entry subscription.Entry, indexURL string) (*plugin.Record, error) {
	body, err := l.fetch(ctx, entry.URL)
	if err != nil {
		return nil, err
	}
	l.op.Lock()
	defer l.op.Unlock()
	if l.IsInstalled(entry.Key) {
		return l.replaceTypedLocked(ctx, entry.Key, plugin.TypeComic, string(body), entry.URL, indexURL, "", true)
	}
	// The index selected this key. A downloaded body cannot redirect the
	// install into updating another source through a duplicate-key fallback.
	rec, err := l.core.InstallComicEntry(ctx, entry, string(body), indexURL)
	if err != nil {
		return nil, err
	}
	return l.persistInstalled(ctx, rec)
}

// Remove uninstalls a source and forgets what it stored.
func (l *Library) Remove(ctx context.Context, key string) error {
	l.op.Lock()
	defer l.op.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !l.IsInstalled(key) {
		return plugin.ErrNotFound
	}
	records := l.core.Records()
	for index, record := range records {
		if record.Key == key {
			records = append(records[:index], records[index+1:]...)
			break
		}
	}
	if err := l.persistRecords(records); err != nil {
		return err
	}
	if err := l.core.RemoveRecord(ctx, key); err != nil {
		return err
	}
	return l.core.ClearSource(key)
}

// SetEnabled turns a source on or off.
func (l *Library) SetEnabled(ctx context.Context, key string, on bool) error {
	l.op.Lock()
	defer l.op.Unlock()
	previous, exists := l.core.Record(key)
	if !exists {
		return plugin.ErrNotFound
	}
	if previous.Enabled == on {
		return nil
	}
	if err := l.core.SetEnabled(ctx, key, on); err != nil {
		return err
	}
	if err := l.persist(); err != nil {
		rollback, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		restoreErr := l.core.SetEnabled(rollback, key, previous.Enabled)
		l.refresh()
		return errors.Join(err, restoreErr)
	}
	return nil
}

// ImportSourceData writes the values of a source's data object, as a backup
// carried them. Values are stored exactly as written, so large integers and
// key order survive.
func (l *Library) ImportSourceData(key string, data map[string]json.RawMessage) (int, error) {
	n := 0
	for name, value := range data {
		if err := l.core.Store().SaveData(key, name, value); err != nil {
			return n, fmt.Errorf("library: %s/%s: %w", key, name, err)
		}
		n++
	}
	return n, nil
}

// ImportSourceValues writes one source's values from a backup, splitting them
// the way this client keeps them.
//
// The archive has a single object per source, while this client has two buckets:
// data (what a source stores for itself) and settings (what the user chose in the
// settings panel). The split cannot be read back out of the archive, so the
// source itself decides: a key the source declares in its settings schema is a
// setting, everything else is data. A source that fails to load leaves every key
// as data, which is what the old importer did for all of them.
func (l *Library) ImportSourceValues(ctx context.Context, key string, values map[string]json.RawMessage) (data, settings int, err error) {
	declared := map[string]bool{}
	if list, err := l.core.Settings(ctx, key); err == nil {
		for _, s := range list {
			declared[s.Key] = true
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := values[name]
		if declared[name] {
			if err := l.core.Store().SaveSetting(key, name, value); err != nil {
				return data, settings, fmt.Errorf("library: %s/%s: %w", key, name, err)
			}
			settings++
			continue
		}
		if err := l.core.Store().SaveData(key, name, value); err != nil {
			return data, settings, fmt.Errorf("library: %s/%s: %w", key, name, err)
		}
		data++
	}
	return data, settings, nil
}

// Index downloads an index.json and compares it with the installed sources.
func (l *Library) Index(ctx context.Context, rawURL string) (*subscription.ComicIndex, []subscription.Row, error) {
	body, err := l.fetch(ctx, rawURL)
	if err != nil {
		return nil, nil, err
	}
	idx, err := subscription.ParseComic(body, rawURL)
	if err != nil {
		return nil, nil, err
	}
	return idx, idx.Compare(rawURL, l.core.AppVersion(), l.installed()), nil
}

// IndexFrom parses index text the app already has.
func (l *Library) IndexFrom(body []byte, rawURL string) (*subscription.ComicIndex, []subscription.Row, error) {
	idx, err := subscription.ParseComic(body, rawURL)
	if err != nil {
		return nil, nil, err
	}
	return idx, idx.Compare(rawURL, l.core.AppVersion(), l.installed()), nil
}

// --- internals ---

func (l *Library) installOrReplace(ctx context.Context, typ plugin.Type, body, origin, indexURL string) (*plugin.Record, error) {
	l.op.Lock()
	defer l.op.Unlock()
	rec, err := l.core.InstallTyped(ctx, typ, body, origin, indexURL)
	if err != nil {
		// Core is the only one that knows a script's key, so a taken key is
		// only discovered here; update that source instead.
		if errors.Is(err, plugin.ErrExists) {
			if key := keyOf(err); key != "" && l.IsInstalled(key) {
				return l.replaceTypedLocked(ctx, key, typ, body, origin, indexURL, "", false)
			}
		}
		return nil, err
	}
	return l.persistInstalled(ctx, rec)
}

// persistInstalled is called under op for a newly installed source.
func (l *Library) persistInstalled(ctx context.Context, rec *plugin.Record) (*plugin.Record, error) {
	err := ctx.Err()
	if err == nil {
		err = l.persist()
	}
	if err != nil {
		// Core now holds a source the disk refused. Undo it: the window would
		// otherwise show a source that the next launch silently loses, while the
		// user has already been told the install failed.
		if rec != nil && rec.Key != "" {
			err = errors.Join(err, l.core.RemoveRecord(context.WithoutCancel(ctx), rec.Key))
			l.refresh()
		}
		return nil, err
	}
	return rec, nil
}

func (l *Library) replaceTypedLocked(ctx context.Context, key string, typ plugin.Type, body, origin, indexURL, digest string, expected bool) (*plugin.Record, error) {
	previous, ok := l.core.Record(key)
	if !ok {
		return nil, plugin.ErrNotFound
	}
	var rec *plugin.Record
	var err error
	if expected {
		rec, err = l.core.ReplaceExpecting(ctx, key, typ, body, origin, indexURL, digest)
	} else {
		rec, err = l.core.ReplaceTyped(ctx, key, typ, body, origin, indexURL)
	}
	if err != nil {
		return nil, err
	}
	if err := l.persist(); err != nil {
		// Never uninstall to roll back: Remove also erases this source's data
		// and sign-in cookies. Replace preserves both and its enabled state.
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, restoreErr := l.core.ReplaceTyped(rctx, key, previous.Type, previous.Body, previous.Origin, previous.IndexURL)
		cancel()
		if restoreErr != nil {
			// If old init cannot run again, stop the unpersisted replacement and
			// report recovery failure rather than silently running the new code.
			dctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			disableErr := l.core.SetEnabled(dctx, key, false)
			stop()
			l.refresh()
			return nil, errors.Join(err, fmt.Errorf("library: previous source could not be restored: %w", restoreErr), disableErr)
		}
		l.refresh()
		return nil, err
	}
	return rec, nil
}

// keyOf only accepts a registry-produced collision; script text cannot choose
// which installed source the update path will replace.
func keyOf(err error) string {
	var collision *plugin.KeyExistsError
	if errors.As(err, &collision) {
		return collision.Key
	}
	return ""
}

func (l *Library) refresh() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = l.core.Records()
}

func (l *Library) persist() error {
	return l.persistRecords(l.core.Records())
}

func (l *Library) persistRecords(recs []plugin.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	if err := l.core.Store().SaveRecords(data); err != nil {
		return err
	}
	l.records = recs
	return nil
}

func (l *Library) installed() []subscription.Installed {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []subscription.Installed
	for _, r := range l.records {
		if r.Type == plugin.TypeComic {
			out = append(out, subscription.Installed{Key: r.Key, Version: r.Version, IndexURL: r.IndexURL})
		}
	}
	return out
}

func (l *Library) fetch(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("library: only http and https URLs")
	}
	body, _, err := l.core.Transport().Fetch(ctx, rawURL, MaxScriptBytes)
	return body, err
}
