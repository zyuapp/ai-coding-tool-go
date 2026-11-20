package application

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// setKey: JSON has no sets, so a set travels as an object no state would otherwise hold.
const setKey = "$set"

// encoded writes every jsonx.Set as {"$set": [...]}.
var encoded = json.MarshalToFunc(func(encoder *jsontext.Encoder, set jsonx.Set[string]) error {
	if err := encoder.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	if err := encoder.WriteToken(jsontext.String(setKey)); err != nil {
		return err
	}
	if err := json.MarshalEncode(encoder, set.Sorted()); err != nil {
		return err
	}
	return encoder.WriteToken(jsontext.EndObject)
})

// decoded reads {"$set": [...]} back into a jsonx.Set, and still takes a bare array.
var decoded = json.UnmarshalFromFunc(func(decoder *jsontext.Decoder, set *jsonx.Set[string]) error {
	var members []string
	if decoder.PeekKind() == '{' {
		var wrapped map[string][]string
		if err := json.UnmarshalDecode(decoder, &wrapped); err != nil {
			return err
		}
		held, ok := wrapped[setKey]
		if !ok || len(wrapped) != 1 {
			return errNotASet
		}
		members = held
	} else if err := json.UnmarshalDecode(decoder, &members); err != nil {
		return err
	}
	*set = jsonx.SetOf(members...)
	return nil
})

// StringifyWorkspaceJSON is value on a wire to a paired computer or phone, sets as {"$set": [...]}.
func StringifyWorkspaceJSON(value any) ([]byte, error) {
	options := jsonx.Options()
	marshalers := encoded
	if held, ok := json.GetOption(options, json.WithMarshalers); ok && held != nil {
		marshalers = json.JoinMarshalers(encoded, held)
	}
	return json.Marshal(value, options, json.WithMarshalers(marshalers))
}

// ParseWorkspaceJSON decodes text into into (a pointer), reading {"$set": [...]} wherever a set is held.
func ParseWorkspaceJSON(text []byte, into any) error {
	options := jsonx.Options()
	unmarshalers := decoded
	if unions, ok := json.GetOption(options, json.WithUnmarshalers); ok && unions != nil {
		unmarshalers = json.JoinUnmarshalers(decoded, unions)
	}
	return json.Unmarshal(text, into, options, json.WithUnmarshalers(unmarshalers))
}

var errNotASet = errors.New(`application: a set is written {"$set": [...]}`)
