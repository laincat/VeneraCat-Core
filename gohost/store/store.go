// Package store persists what sources keep: their data, their settings and
// the installed plugin records.
//
// Layout under the app data directory:
//
//	plugins.json          Core records with script bodies
//	sources/<key>.json    {"data": {...}, "settings": {...}}
//
// Values are stored as the exact JSON text Core passed in, so large integers
// and key order survive. Writes are atomic.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store is safe for concurrent use; Core calls it from several goroutines.
type Store struct {
	mu   sync.Mutex
	root string
	// sources caches the parsed file of each source.
	sources map[string]*sourceFile
}

type sourceFile struct {
	Data     json.RawMessage `json:"data"`
	Settings json.RawMessage `json:"settings"`
}

// Open prepares the directory.
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "sources"), 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root, sources: map[string]*sourceFile{}}, nil
}

// Root is the directory the store writes to.
func (s *Store) Root() string { return s.root }

// Records returns the saved plugin records, or nil when none were saved.
func (s *Store) Records() ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(s.root, "plugins.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

// SaveRecords writes the plugin records.
func (s *Store) SaveRecords(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFile(filepath.Join(s.root, "plugins.json"), data)
}

// RemoveSource deletes everything a source stored.
func (s *Store) RemoveSource(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(s.sources, key)
	return nil
}

// --- js.SourceStorage ---

// LoadData returns the stored value of dataKey.
func (s *Store) LoadData(key, dataKey string) (json.RawMessage, bool, error) {
	return s.load(key, dataKey, false)
}

// SaveData stores a value.
func (s *Store) SaveData(key, dataKey string, data json.RawMessage) error {
	return s.save(key, dataKey, data, false)
}

// DeleteData removes a value.
func (s *Store) DeleteData(key, dataKey string) error {
	return s.delete(key, dataKey, false)
}

// LoadSetting returns the stored value of a setting. Core resolves the
// declared default for settings never changed.
func (s *Store) LoadSetting(key, settingKey string) (json.RawMessage, bool, error) {
	return s.load(key, settingKey, true)
}

// SaveSetting stores a setting value. Core has no setting writer, so the
// app calls this directly for its own settings UI.
func (s *Store) SaveSetting(key, settingKey string, data json.RawMessage) error {
	return s.save(key, settingKey, data, true)
}

// IsLogged reports whether the source stored an account, as the official
// app defines being logged in.
func (s *Store) IsLogged(key string) bool {
	v, ok, err := s.LoadData(key, "account")
	if err != nil || !ok {
		return false
	}
	return string(v) != "null"
}

// --- internals ---

func (s *Store) load(key, field string, setting bool) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.file(key)
	if err != nil {
		return nil, false, err
	}
	var obj map[string]json.RawMessage
	if setting {
		obj = parseObject(f.Settings)
	} else {
		obj = parseObject(f.Data)
	}
	v, ok := obj[field]
	return v, ok, nil
}

// AllData returns every stored data key of one source, as one object. It is what
// a .venera export writes: the archive keeps a source's data as a single
// comic_source/<key>.data file, so a restore can hand back the whole thing.
func (s *Store) AllData(key string) (map[string]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.file(key)
	if err != nil {
		return nil, err
	}
	return parseObject(f.Data), nil
}

// AllSettings is AllData for the settings side.
func (s *Store) AllSettings(key string) (map[string]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.file(key)
	if err != nil {
		return nil, err
	}
	return parseObject(f.Settings), nil
}

func (s *Store) save(key, field string, data json.RawMessage, setting bool) error {
	if len(data) == 0 || !json.Valid(data) {
		return errors.New("store: value must be JSON")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.file(key)
	if err != nil {
		return err
	}
	obj := parseObject(f.Data)
	if setting {
		obj = parseObject(f.Settings)
	}
	obj[field] = data
	// encoding/json sorts keys, so a rewrite cannot reorder what a source
	// reads back as an object; values themselves stay byte-identical.
	raw, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	next := *f
	if setting {
		next.Settings = raw
	} else {
		next.Data = raw
	}
	if err := s.write(key, &next); err != nil {
		return err
	}
	*f = next
	return nil
}

func (s *Store) delete(key, field string, setting bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.file(key)
	if err != nil {
		return err
	}
	obj := parseObject(f.Data)
	if setting {
		obj = parseObject(f.Settings)
	}
	if _, ok := obj[field]; !ok {
		return nil
	}
	delete(obj, field)
	raw, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	next := *f
	if setting {
		next.Settings = raw
	} else {
		next.Data = raw
	}
	if err := s.write(key, &next); err != nil {
		return err
	}
	*f = next
	return nil
}

// file returns the cached source file, reading it once.
func (s *Store) file(key string) (*sourceFile, error) {
	if f, ok := s.sources[key]; ok {
		return f, nil
	}
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	f := &sourceFile{Data: json.RawMessage("{}"), Settings: json.RawMessage("{}")}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("store: read source: %w", err)
	}
	if len(data) > 0 {
		if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
			return nil, errors.New("store: source file must be an object")
		}
		var parsed sourceFile
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("store: invalid source file: %w", err)
		}
		f = &parsed
		for _, raw := range []json.RawMessage{f.Data, f.Settings} {
			if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
				continue
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				return nil, errors.New("store: source data and settings must be objects")
			}
		}
	}
	s.sources[key] = f
	return f, nil
}

func (s *Store) write(key string, f *sourceFile) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// path maps a source key to its file. Core validates keys, but a key is
// never trusted as a path part.
func (s *Store) path(key string) (string, error) {
	if key == "" {
		return "", errors.New("store: empty source key")
	}
	for _, r := range key {
		if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "", fmt.Errorf("store: invalid source key %q", key)
		}
	}
	return filepath.Join(s.root, "sources", key+".json"), nil
}

func parseObject(raw json.RawMessage) map[string]json.RawMessage {
	obj := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &obj); err != nil {
			return map[string]json.RawMessage{}
		}
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	return obj
}

// writeFile writes through a temporary file in the same directory, so an
// interrupted write never leaves half a file.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
