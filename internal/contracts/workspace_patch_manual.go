package contracts

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strconv"
)

// PathKey is one step of a WorkspacePatch path: an object key, or an array index.
type PathKey struct {
	Key   string
	Index float64
	// IsIndex says the step is an array index.
	IsIndex bool
}

// KeyStep is a path step into an object.
func KeyStep(key string) PathKey { return PathKey{Key: key} }

// IndexStep is a path step into an array.
func IndexStep(index int) PathKey { return PathKey{Index: float64(index), IsIndex: true} }

func (k PathKey) String() string {
	if k.IsIndex {
		return strconv.FormatFloat(k.Index, 'g', -1, 64)
	}
	return k.Key
}

func (k PathKey) MarshalJSONTo(enc *jsontext.Encoder) error {
	if k.IsIndex {
		return json.MarshalEncode(enc, k.Index)
	}
	return enc.WriteToken(jsontext.String(k.Key))
}

func (k *PathKey) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	token, err := dec.ReadToken()
	if err != nil {
		return err
	}
	switch token.Kind() {
	case '"':
		*k = PathKey{Key: token.String()}
	case '0':
		f, err := token.Float()
		if err != nil {
			return err
		}
		*k = PathKey{Index: f, IsIndex: true}
	default:
		return fmt.Errorf("contracts: patch path step must be a string or number, got %s", token.Kind())
	}
	return nil
}
