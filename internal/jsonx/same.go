package jsonx

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"slices"
	"unsafe"
)

// Same is TypeScript's `a === b` for state: slices are the same when they share a backing array
// and length, maps when they are the same map, pointers, funcs and channels when equal.
// Interfaces compare their dynamic values the same way; other comparable values compare with ==,
// and values that are none of these are never the same.
//
// Go backs every zero-capacity slice with one shared address, so two empty slices built apart
// (`[]T{}`, `make([]T, 0)`, slices.Clone of an empty slice) are the same here although TypeScript's
// `[]` literals are not. A new list whose identity is checked against one that may also be empty is
// built with Fresh.
func Same[T any](a, b T) bool {
	return same(reflect.ValueOf(&a).Elem(), reflect.ValueOf(&b).Elem())
}

func same(a, b reflect.Value) bool {
	if a.Kind() == reflect.Interface {
		if a.IsNil() || b.IsNil() {
			return a.IsNil() && b.IsNil()
		}
		a, b = a.Elem(), b.Elem()
		if a.Type() != b.Type() {
			return false
		}
	}
	switch a.Kind() {
	case reflect.Slice:
		return a.Len() == b.Len() && a.UnsafePointer() == b.UnsafePointer()
	case reflect.Map, reflect.Pointer, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return a.UnsafePointer() == b.UnsafePointer()
	}
	if a.Comparable() {
		return a.Equal(b)
	}
	return false
}

// Fresh is a new slice of the given length with a backing array of its own even when empty, so it is
// never Same as any other slice.
func Fresh[E any](length int) []E {
	return make([]E, length, max(length, 1))
}

// SameSlice is Same for slices without reflection.
func SameSlice[E any](a, b []E) bool {
	return len(a) == len(b) && unsafe.SliceData(a) == unsafe.SliceData(b)
}

// SameMap is Same for maps.
func SameMap[K comparable, V any](a, b map[K]V) bool {
	return reflect.ValueOf(a).UnsafePointer() == reflect.ValueOf(b).UnsafePointer()
}

// Set is TypeScript's Set as a value that crosses a boundary: an array in JSON, sorted on encode.
type Set[T cmp.Ordered] map[T]struct{}

// SetOf builds a set from values.
func SetOf[T cmp.Ordered](values ...T) Set[T] {
	set := make(Set[T], len(values))
	for _, v := range values {
		set[v] = struct{}{}
	}
	return set
}

func (s Set[T]) Has(v T) bool {
	_, ok := s[v]
	return ok
}

// Sorted lists the members in order.
func (s Set[T]) Sorted() []T {
	out := make([]T, 0, len(s))
	for v := range s {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

func (s Set[T]) MarshalJSONTo(enc *jsontext.Encoder) error {
	return json.MarshalEncode(enc, s.Sorted())
}

func (s *Set[T]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var values []T
	if err := json.UnmarshalDecode(dec, &values); err != nil {
		return err
	}
	if values == nil {
		*s = nil
		return nil
	}
	*s = SetOf(values...)
	return nil
}

// NullSlice is a slice TypeScript declares `T[] | null`: nil encodes as null, an empty slice as [].
type NullSlice[E any] []E

func (s NullSlice[E]) MarshalJSONTo(enc *jsontext.Encoder) error {
	if s == nil {
		return enc.WriteToken(jsontext.Null)
	}
	return json.MarshalEncode(enc, []E(s))
}

func (s *NullSlice[E]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var values []E
	if err := json.UnmarshalDecode(dec, &values); err != nil {
		return err
	}
	*s = values
	return nil
}

// NullMap is a record TypeScript declares `Record<K, V> | null`: nil encodes as null, an empty map as {}.
type NullMap[K comparable, V any] map[K]V

func (m NullMap[K, V]) MarshalJSONTo(enc *jsontext.Encoder) error {
	if m == nil {
		return enc.WriteToken(jsontext.Null)
	}
	return json.MarshalEncode(enc, map[K]V(m))
}

func (m *NullMap[K, V]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var values map[K]V
	if err := json.UnmarshalDecode(dec, &values); err != nil {
		return err
	}
	*m = values
	return nil
}
