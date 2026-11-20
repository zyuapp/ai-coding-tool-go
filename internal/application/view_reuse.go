package application

import "reflect"

// HeldViews holds a surface's views still while the state around it moves: a view is handed back
// only when every field of the new one matches the last (SameFields), and the list is handed back
// when every view in it is. `id` names a view (TS: T extends { id: string }). Not safe for
// concurrent use.
func HeldViews[V any](id func(view *V) string) func(views []*V) []*V {
	var held []*V
	return func(views []*V) []*V {
		previous := held
		reused := make([]*V, len(views))
		for index, view := range views {
			reused[index] = view
			key := id(view)
			for _, before := range previous {
				if id(before) == key {
					if SameFields(before, view) {
						reused[index] = before
					}
					break
				}
			}
		}
		if len(reused) == len(previous) {
			kept := true
			for index, view := range reused {
				if previous[index] != view {
					kept = false
					break
				}
			}
			if kept {
				return previous
			}
		}
		held = reused
		return reused
	}
}

// SameFields is whether two flat records hold the same values under the same fields. Both are
// pointers to structs of one type (or nil); fields compare as TypeScript's === would: optional
// scalars by value, objects, arrays and records by identity.
func SameFields(left, right any) bool {
	a, b := reflect.ValueOf(left), reflect.ValueOf(right)
	if a.Type() != b.Type() {
		return false
	}
	if a.Kind() == reflect.Pointer {
		if a.IsNil() || b.IsNil() {
			return a.IsNil() && b.IsNil()
		}
		a, b = a.Elem(), b.Elem()
	}
	return sameRecordFields(a, b)
}

func sameRecordFields(a, b reflect.Value) bool {
	for index := range a.NumField() {
		if !sameField(a.Field(index), b.Field(index)) {
			return false
		}
	}
	return true
}

func sameField(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() && b.IsNil()
		}
		a, b = a.Elem(), b.Elem()
		if a.Type() != b.Type() {
			return false
		}
		return sameField(a, b)
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() && b.IsNil()
		}
		if a.Pointer() == b.Pointer() {
			return true
		}
		if elem := a.Elem(); elem.Kind() != reflect.Struct && elem.Comparable() {
			return elem.Equal(b.Elem())
		}
		return false
	case reflect.Slice:
		return a.Len() == b.Len() && a.UnsafePointer() == b.UnsafePointer()
	case reflect.Map, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return a.UnsafePointer() == b.UnsafePointer()
	case reflect.Struct:
		return sameRecordFields(a, b)
	}
	if a.Comparable() {
		return a.Equal(b)
	}
	return false
}
