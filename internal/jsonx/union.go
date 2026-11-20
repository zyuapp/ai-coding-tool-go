package jsonx

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
)

// RegisterUnion makes the interface type U decodable wherever it appears: an object is decoded
// into the member its key names. Members are keyed by the discriminator's value (a string,
// bool or number) and given as zero values; each must implement U. JSON null decodes to nil.
// Call it from init.
func RegisterUnion[U any](key string, members map[any]U) {
	union := reflect.TypeFor[U]()
	if union.Kind() != reflect.Interface {
		panic(fmt.Sprintf("jsonx: union %v is not an interface", union))
	}
	cases := make(map[any]reflect.Type, len(members))
	for value, member := range members {
		if any(member) == nil {
			panic(fmt.Sprintf("jsonx: union %v: nil member for %#v", union, value))
		}
		cases[discriminator(union, value)] = reflect.TypeOf(member)
	}
	registry.Lock()
	defer registry.Unlock()
	if _, ok := unions[union]; ok {
		panic(fmt.Sprintf("jsonx: union %v registered twice", union))
	}
	u := &unionCases{key: key, cases: cases}
	unions[union] = u
	decode := func(dec *jsontext.Decoder, out *U) error {
		value, err := u.decode(dec, union)
		if err != nil || value == nil {
			var zero U
			*out = zero
			return err
		}
		*out = value.(U)
		return nil
	}
	unmarshals = append(unmarshals, json.UnmarshalFromFunc(decode))
	rebuild()
}

// RegisterNested routes the members of U whose discriminator is value to the union N, which tells
// them apart by a key of its own. Every member of N must implement U.
func RegisterNested[U, N any](value any) {
	union, nested := reflect.TypeFor[U](), reflect.TypeFor[N]()
	if nested.Kind() != reflect.Interface {
		panic(fmt.Sprintf("jsonx: nested union %v is not an interface", nested))
	}
	registry.Lock()
	defer registry.Unlock()
	u, ok := unions[union]
	if !ok {
		panic(fmt.Sprintf("jsonx: union %v is not registered", union))
	}
	u.cases[discriminator(union, value)] = nested
}

type unionCases struct {
	key   string
	cases map[any]reflect.Type
}

var unions = map[reflect.Type]*unionCases{}

func discriminator(union reflect.Type, value any) any {
	value = normal(value)
	switch value.(type) {
	case string, bool, float64:
		return value
	}
	panic(fmt.Sprintf("jsonx: union %v: discriminator %#v is not a string, bool or number", union, value))
}

func (u *unionCases) decode(dec *jsontext.Decoder, union reflect.Type) (any, error) {
	raw, err := dec.ReadValue()
	if err != nil {
		return nil, err
	}
	if raw.Kind() == 'n' {
		return nil, nil
	}
	if raw.Kind() != '{' {
		return nil, fmt.Errorf("jsonx: %v: want an object, got %s", union, raw.Kind())
	}
	value, found, err := member(raw, u.key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("jsonx: %v: no %q member", union, u.key)
	}
	registry.RLock()
	target, ok := u.cases[value]
	registry.RUnlock()
	if !ok {
		return nil, fmt.Errorf("jsonx: %v: unknown %s %v", union, u.key, describe(value))
	}
	ptr := reflect.New(target)
	if err := json.Unmarshal(raw, ptr.Interface(), dec.Options()); err != nil {
		return nil, err
	}
	return ptr.Elem().Interface(), nil
}

func describe(value any) string {
	if s, ok := value.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprint(value)
}

func normal(value any) any {
	switch v := value.(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float32:
		return float64(v)
	}
	if v := reflect.ValueOf(value); v.Kind() == reflect.String {
		return v.String()
	}
	return value
}

// member reads the scalar value of the top-level member key of the object raw.
func member(raw jsontext.Value, key string) (any, bool, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(raw), jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true))
	if _, err := dec.ReadToken(); err != nil {
		return nil, false, err
	}
	var found any
	ok := false
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return nil, false, err
		}
		if name.String() != key {
			if err := dec.SkipValue(); err != nil {
				return nil, false, err
			}
			continue
		}
		token, err := dec.ReadToken()
		if err != nil {
			return nil, false, err
		}
		switch token.Kind() {
		case '"':
			found, ok = token.String(), true
		case 't', 'f':
			found, ok = token.Bool(), true
		case '0':
			f, err := token.Float()
			if err != nil {
				return nil, false, err
			}
			found, ok = f, true
		case 'n':
			found, ok = nil, false
		default:
			return nil, false, fmt.Errorf("jsonx: discriminator %q is not a scalar", key)
		}
	}
	return found, ok, nil
}
