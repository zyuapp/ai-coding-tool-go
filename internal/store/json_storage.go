package store

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
)

// WindowStorageFile is the file a profile keeps its window's storage in.
const WindowStorageFile = "window.v1.json"

// KeyValueStorage is application KeyValueStorage: string values by key, as a window's own
// storage holds them. GetItem's false is getItem's null.
type KeyValueStorage interface {
	GetItem(key string) (string, bool)
	SetItem(key, value string) error
}

// JSONStorage is what a window keeps between launches, as one JSON file of the same keys it kept
// in its own storage. Every write lands whole: the file is replaced, never appended to, so a crash
// mid-write leaves the last one.
type JSONStorage struct {
	file   string
	mu     sync.Mutex
	keys   []string
	values map[string]string
}

var _ KeyValueStorage = (*JSONStorage)(nil)

// NewJSONStorage reads file, treating one that is missing or unreadable as empty.
func NewJSONStorage(file string) *JSONStorage {
	storage := &JSONStorage{file: file, values: map[string]string{}}
	storage.read()
	return storage
}

func (s *JSONStorage) read() {
	data, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	record, ok, err := parseObject(data)
	if err != nil || !ok {
		return
	}
	for _, m := range record {
		if value, isString := stringValue(m.value); isString {
			s.set(m.name, value)
		}
	}
}

func (s *JSONStorage) set(key, value string) {
	if _, ok := s.values[key]; !ok {
		s.keys = insertKey(s.keys, key)
	}
	s.values[key] = value
}

// insertKey places a key where a JavaScript object would: integer keys first in ascending order,
// then the rest in insertion order.
func insertKey(keys []string, key string) []string {
	index, isIndex := arrayIndex(key)
	if !isIndex {
		return append(keys, key)
	}
	at := 0
	for at < len(keys) {
		other, otherIsIndex := arrayIndex(keys[at])
		if !otherIsIndex || other > index {
			break
		}
		at++
	}
	return slices.Insert(keys, at, key)
}

func arrayIndex(key string) (uint64, bool) {
	index, err := strconv.ParseUint(key, 10, 32)
	if err != nil || index == 1<<32-1 || strconv.FormatUint(index, 10) != key {
		return 0, false
	}
	return index, true
}

func (s *JSONStorage) GetItem(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	return value, ok
}

func (s *JSONStorage) SetItem(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set(key, value)
	return s.write()
}

// Adopt takes on values a window still holds from hosting the runtime itself, without overwriting
// any it already has. It reports whether anything was taken.
func (s *JSONStorage) Adopt(values map[string]string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if _, ok := s.values[key]; ok {
			continue
		}
		s.set(key, values[key])
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, s.write()
}

func (s *JSONStorage) write() error {
	if err := os.MkdirAll(filepath.Dir(s.file), 0o777); err != nil {
		return err
	}
	data := []byte{'{'}
	for i, key := range s.keys {
		if i > 0 {
			data = append(data, ',')
		}
		data = appendJSString(data, key)
		data = append(data, ':')
		data = appendJSString(data, s.values[key])
	}
	data = append(data, '}')
	draft := fmt.Sprintf("%s.%d.tmp", s.file, os.Getpid())
	if err := os.WriteFile(draft, data, 0o666); err != nil {
		return err
	}
	return os.Rename(draft, s.file)
}
