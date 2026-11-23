package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type keyedLocks struct {
	mu      sync.Mutex
	entries map[string]*lockEntry
}

type lockEntry struct {
	mu    sync.Mutex
	users int
}

func (locks *keyedLocks) lock(key string) func() {
	locks.mu.Lock()
	if locks.entries == nil {
		locks.entries = make(map[string]*lockEntry)
	}
	entry := locks.entries[key]
	if entry == nil {
		entry = &lockEntry{}
		locks.entries[key] = entry
	}
	entry.users++
	locks.mu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		locks.mu.Lock()
		entry.users--
		if entry.users == 0 {
			delete(locks.entries, key)
		}
		locks.mu.Unlock()
	}
}

func absoluteDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return filepath.Clean(dir)
}

var storageLocks keyedLocks

// Storage keeps a service's ordinary values separately from its installed package.
type Storage struct {
	dir string
	id  string
}

// OpenStorage constructs the storage callbacks for an installed or local service.
func OpenStorage(dataDir, id string) (*Storage, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	return &Storage{dir: absoluteDir(dataDir), id: id}, nil
}

func (s *Storage) lock() func() {
	return storageLocks.lock(PackageDir(s.dir, s.id))
}

func (s *Storage) path() string {
	return PackageDir(s.dir, s.id) + ".json"
}

// Get reads a value, migrating ordinary values from older profiles on demand.
func (s *Storage) Get(ctx context.Context, key string) (string, bool, error) {
	defer s.lock()()
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	values, err := s.load()
	value, found := values[key]
	return value, found, err
}

// Set updates one value while preserving concurrent updates to other keys.
func (s *Storage) Set(ctx context.Context, key, value string) error {
	defer s.lock()()
	if err := ctx.Err(); err != nil {
		return err
	}
	values, err := s.load()
	if err != nil {
		return err
	}
	values[key] = value
	return s.write(values)
}

func (s *Storage) load() (map[string]string, error) {
	values, err := readStorage(s.path())
	if err != nil {
		return nil, err
	}
	if values == nil {
		values = make(map[string]string)
	}
	legacy := filepath.Join(PackageDir(s.dir, s.id), "storage.json")
	old, err := readStorage(legacy)
	if err != nil || old == nil {
		return values, err
	}
	for key, value := range old {
		if _, exists := values[key]; !exists {
			values[key] = value
		}
	}
	if err := s.write(values); err != nil {
		return nil, err
	}
	return values, os.Remove(legacy)
}

func readStorage(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("service: storage %s is unreadable: %w", path, err)
		}
		if values == nil {
			values = make(map[string]string)
		}
	}
	return values, nil
}

func (s *Storage) write(values map[string]string) error {
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return writeAtomic(s.path(), raw, os.Rename)
}

func writeAtomic(path string, data []byte, rename func(string, string) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return rename(file.Name(), path)
}
