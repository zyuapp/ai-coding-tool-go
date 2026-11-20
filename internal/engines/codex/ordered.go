package codex

import "slices"

// A map that remembers the order keys were first set in, as a JavaScript Map does.
type ordered[K comparable, V any] struct {
	keys   []K
	values map[K]V
}

func newOrdered[K comparable, V any]() *ordered[K, V] {
	return &ordered[K, V]{values: map[K]V{}}
}

func (m *ordered[K, V]) get(key K) (V, bool) {
	value, ok := m.values[key]
	return value, ok
}

func (m *ordered[K, V]) has(key K) bool {
	_, ok := m.values[key]
	return ok
}

func (m *ordered[K, V]) set(key K, value V) {
	if _, ok := m.values[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

func (m *ordered[K, V]) delete(key K) bool {
	if _, ok := m.values[key]; !ok {
		return false
	}
	delete(m.values, key)
	m.keys = slices.DeleteFunc(m.keys, func(held K) bool { return held == key })
	return true
}

func (m *ordered[K, V]) len() int { return len(m.keys) }

func (m *ordered[K, V]) clear() {
	m.keys = nil
	m.values = map[K]V{}
}

// The keys in order, copied so the map may change while they are walked.
func (m *ordered[K, V]) orderedKeys() []K { return slices.Clone(m.keys) }
