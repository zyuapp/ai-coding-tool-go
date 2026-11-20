package codex

import (
	"bytes"
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type jsonMember struct {
	key   string
	value jsontext.Value
}

// A JSON object that keeps its members in the order they were written, as a JavaScript object does.
type jsonObject []jsonMember

// The members of value when it is an object; false for anything else.
func readObject(value jsontext.Value) (jsonObject, bool) {
	if value.Kind() != '{' {
		return nil, false
	}
	decoder := jsontext.NewDecoder(bytes.NewReader(value), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	if _, err := decoder.ReadToken(); err != nil {
		return nil, false
	}
	var object jsonObject
	for decoder.PeekKind() != '}' {
		key, err := decoder.ReadToken()
		if err != nil {
			return nil, false
		}
		name := key.String()
		member, err := decoder.ReadValue()
		if err != nil {
			return nil, false
		}
		object = object.with(name, append(jsontext.Value(nil), member...))
	}
	return object, true
}

// The object with key set to value, in the place it already holds or else at the end.
func (object jsonObject) with(key string, value jsontext.Value) jsonObject {
	for index := range object {
		if object[index].key == key {
			next := append(jsonObject(nil), object...)
			next[index].value = value
			return next
		}
	}
	return append(object[:len(object):len(object)], jsonMember{key, value})
}

func (object jsonObject) withValue(key string, value any) jsonObject {
	encoded, err := jsonx.Marshal(value)
	if err != nil {
		return object
	}
	return object.with(key, encoded)
}

func (object jsonObject) MarshalJSONTo(encoder *jsontext.Encoder) error {
	if err := encoder.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	for _, member := range object {
		if err := encoder.WriteToken(jsontext.String(member.key)); err != nil {
			return err
		}
		if err := encoder.WriteValue(member.value); err != nil {
			return err
		}
	}
	return encoder.WriteToken(jsontext.EndObject)
}

func (object jsonObject) value() jsontext.Value {
	encoded, err := jsonx.Marshal(object)
	if err != nil {
		return jsontext.Value("{}")
	}
	return encoded
}

// Whether a JSON value is truthy as JavaScript reads it.
func truthy(value jsontext.Value) bool {
	switch value.Kind() {
	case 0, 'n', 'f':
		return false
	case '"':
		return string(value) != `""`
	case '0':
		var number float64
		return jsonx.Unmarshal(value, &number) == nil && number != 0 && finite(number)
	}
	return true
}
