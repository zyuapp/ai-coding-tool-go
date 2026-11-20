package domain

import (
	"runtime"
	"sync"
	"weak"
)

// weakMemo is TypeScript's WeakMap: values keyed by a pointer's identity, dropped once the key is
// collected. A value must not reach its key, or the key is never collected.
type weakMemo[K, V any] struct {
	mu      sync.Mutex
	entries map[weak.Pointer[K]]V
}

func (m *weakMemo[K, V]) get(key *K) (V, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.entries[weak.Make(key)]
	return value, ok
}

func (m *weakMemo[K, V]) set(key *K, value V) {
	handle := weak.Make(key)
	m.mu.Lock()
	if m.entries == nil {
		m.entries = map[weak.Pointer[K]]V{}
	}
	_, held := m.entries[handle]
	m.entries[handle] = value
	m.mu.Unlock()
	if !held {
		runtime.AddCleanup(key, m.forget, handle)
	}
}

func (m *weakMemo[K, V]) forget(handle weak.Pointer[K]) {
	m.mu.Lock()
	delete(m.entries, handle)
	m.mu.Unlock()
}
