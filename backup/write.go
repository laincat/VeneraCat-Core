package backup

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// Writing a .venera backup.
//
// Core could read this format but never write it, so a user could move into this
// client and never out of it: sources and settings had no way back to the
// official app or to another device. The reader above documents the members, and
// the writer follows it:
//
//	comic_source/<name.js>   one entry per source script
//	appdata.json             {"settings": {...}, "searchHistory": [...]}
//
// Two things are deliberately not written:
//
//   - cookie.db: the cookies this client holds are per-source credentials, and
//     an export file is a thing people mail to themselves.
//   - history.db / local_favorite.db: those are the official app's SQLite
//     schemas. This client's shelf is its own format, and writing a half-filled
//     database of someone else's schema would look like data and read as
//     corruption.
//
// The result is a backup another Venera-compatible client can read for sources
// and settings; it is not a full clone of the official app's data.

// WriteSource is one script to include in an export. The reader has its own
// Source type for what it found in an archive, hence the different name.
type WriteSource struct {
	// Name is the entry's file name inside comic_source/. A name with a path
	// separator or a parent reference is rejected by the writer.
	Name string
	Body []byte
	// Data is the source's own stored object, written beside the script as
	// <key>.data. It is what a source keeps between runs — tokens, a chosen
	// domain — and without it a restore brings back code that has lost its
	// state. Empty means "write no .data entry".
	Data map[string]json.RawMessage
}

// WriteOptions is what goes into a backup.
type WriteOptions struct {
	Sources []WriteSource
	// Settings is written as appdata.json's "settings" object. Device settings
	// are dropped: the reader drops them on the way in for the same reason.
	Settings map[string]json.RawMessage
	// SearchHistory is a list of keywords, oldest first.
	SearchHistory []string
}

// MaxWriteSourceBytes bounds one script in a backup, matching the reader.
const MaxWriteSourceBytes = 16 << 20

// MaxWriteDataBytes bounds one source's data entry. It matches the reader's
// MaxSourceBytes, which applies to every comic_source file: without the same cap
// here, a source with a lot of stored data would export a file this very package
// refuses to open.
const MaxWriteDataBytes = 16 << 20

// Write builds a .venera archive.
//
// The order of entries is fixed (appdata.json first, then sources by name) so
// two exports of the same data are byte-identical, which makes a "did anything
// change" question answerable.
func Write(w io.Writer, opts WriteOptions) error {
	if len(opts.Sources) == 0 && len(opts.Settings) == 0 && len(opts.SearchHistory) == 0 {
		return fmt.Errorf("backup: nothing to write")
	}
	zw := zip.NewWriter(w)

	appData, err := buildAppData(opts)
	if err != nil {
		return err
	}
	if err := writeEntry(zw, MemberAppData, appData); err != nil {
		return err
	}

	sources := append([]WriteSource(nil), opts.Sources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	seen := map[string]bool{}
	for _, source := range sources {
		name, err := cleanSourceName(source.Name)
		if err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("backup: two sources named %q", name)
		}
		seen[name] = true
		if len(source.Body) == 0 {
			// An empty script is a broken source; refuse rather than export it.
			return fmt.Errorf("backup: source %q is empty", name)
		}
		if int64(len(source.Body)) > MaxWriteSourceBytes {
			return fmt.Errorf("backup: source %q is larger than %d bytes", name, MaxWriteSourceBytes)
		}
		if err := writeEntry(zw, SourceDir+name, source.Body); err != nil {
			return err
		}
		if len(source.Data) > 0 {
			raw, err := json.Marshal(source.Data)
			if err != nil {
				return fmt.Errorf("backup: %s: %w", name, err)
			}
			if int64(len(raw)) > MaxWriteDataBytes {
				return fmt.Errorf("backup: source %q has more than %d bytes of data", name, MaxWriteDataBytes)
			}
			if err := writeEntry(zw, SourceDir+dataName(name), raw); err != nil {
				return err
			}
		}
	}
	return zw.Close()
}

// buildAppData renders appdata.json with keys in a fixed order.
func buildAppData(opts WriteOptions) ([]byte, error) {
	settings := map[string]json.RawMessage{}
	drop := map[string]bool{}
	for _, k := range DeviceSettings {
		drop[k] = true
	}
	for k, v := range opts.Settings {
		if drop[k] || len(v) == 0 {
			continue
		}
		settings[k] = v
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(`{"settings":{`)
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		name, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteString(":")
		b.Write(settings[k])
	}
	b.WriteString(`},"searchHistory":[`)
	history := append([]string(nil), opts.SearchHistory...)
	// The official reader keeps the list as it finds it; the newest entry is
	// last, which is how this client stores it.
	for i, keyword := range history {
		if i > 0 {
			b.WriteString(",")
		}
		quoted, err := json.Marshal(keyword)
		if err != nil {
			return nil, err
		}
		b.Write(quoted)
	}
	b.WriteString("]}")
	return []byte(b.String()), nil
}

// dataName turns comic_source/<key>.js into comic_source/<key>.data: the reader
// pairs the two entries by key, so the names have to match that way.
func dataName(script string) string {
	return strings.TrimSuffix(script, ".js") + ".data"
}

// cleanSourceName keeps a source from escaping comic_source/.
func cleanSourceName(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) {
		return "", fmt.Errorf("backup: invalid source name %q", name)
	}
	if strings.Contains(name, "/") {
		return "", fmt.Errorf("backup: source name %q contains a path", name)
	}
	c := path.Clean(name)
	if c == "." || c == ".." {
		return "", fmt.Errorf("backup: invalid source name %q", name)
	}
	return c, nil
}

// writeEntry adds one member with a fixed timestamp, so exports of unchanged
// data stay byte-identical.
func writeEntry(zw *zip.Writer, name string, body []byte) error {
	entry, err := zw.CreateHeader(&zip.FileHeader{
		Name:   name,
		Method: zip.Deflate,
	})
	if err != nil {
		return fmt.Errorf("backup: writing %s: %w", name, err)
	}
	if _, err := entry.Write(body); err != nil {
		return fmt.Errorf("backup: writing %s: %w", name, err)
	}
	return nil
}
