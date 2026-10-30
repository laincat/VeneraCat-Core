package js

import (
	"encoding/json"
	"sync"
)

// scratchStorage is the storage of an engine whose source is not yet
// validated. Reads see only what the script itself wrote; nothing reaches
// the client's storage. When the source is registered, what it wrote while
// loading is copied into the real storage, so a constructor that saves a
// default still works.
type scratchStorage struct {
	mu   sync.Mutex
	data map[string]json.RawMessage
	gone map[string]bool
}

func newScratchStorage() *scratchStorage {
	return &scratchStorage{data: map[string]json.RawMessage{}, gone: map[string]bool{}}
}

// LoadData serves a source that has no host storage: a source test runs against a
// scratch store, so it behaves the same without an app behind it.
func (s *scratchStorage) LoadData(_, dataKey string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[dataKey]
	return v, ok, nil
}

// SaveData keeps the exact bytes: a source's numbers and key order survive.
func (s *scratchStorage) SaveData(_, dataKey string, data json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[dataKey] = append(json.RawMessage(nil), data...)
	delete(s.gone, dataKey)
	return nil
}

func (s *scratchStorage) DeleteData(_, dataKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, dataKey)
	s.gone[dataKey] = true
	return nil
}

// LoadSetting finds nothing, so declared defaults apply while loading.
func (s *scratchStorage) LoadSetting(string, string) (json.RawMessage, bool, error) {
	return nil, false, nil
}

func (s *scratchStorage) IsLogged(string) bool { return false }

// flush replays the writes made while loading into st under key.
func (s *scratchStorage) flush(st SourceStorage, key string) error {
	if st == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.gone {
		if err := st.DeleteData(key, k); err != nil {
			return err
		}
	}
	for k, v := range s.data {
		if err := st.SaveData(key, k, v); err != nil {
			return err
		}
	}
	return nil
}
