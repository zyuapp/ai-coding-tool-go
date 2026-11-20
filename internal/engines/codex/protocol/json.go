// Package protocol speaks the Codex app server's JSON-RPC protocol over stdio: the client, and Go
// types for the requests, responses, notifications and server requests the app uses.
package protocol

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

// decodeOptions read what the server writes the way JSON.parse does.
var decodeOptions = json.JoinOptions(jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true))

// Marshal encodes a value as the TypeScript client would.
func Marshal(value any) ([]byte, error) { return json.Marshal(value) }

// Unmarshal decodes a value the server sent.
func Unmarshal(data []byte, value any) error { return json.Unmarshal(data, value, decodeOptions) }

// tagName names the constant a Tag encodes as.
type tagName interface{ tagValue() string }

// Tag is a zero-size discriminator field: it always encodes as its constant, and decodes only
// that constant.
type Tag[N tagName] struct{}

func (Tag[N]) MarshalJSONTo(enc *jsontext.Encoder) error {
	var name N
	return enc.WriteToken(jsontext.String(name.tagValue()))
}

func (*Tag[N]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	token, err := dec.ReadToken()
	if err != nil {
		return err
	}
	var name N
	if token.Kind() != '"' || token.String() != name.tagValue() {
		return fmt.Errorf("discriminator is %s, not %q", token, name.tagValue())
	}
	return nil
}

// Empty is a result that carries nothing (Record<string, never>).
type Empty struct{}
