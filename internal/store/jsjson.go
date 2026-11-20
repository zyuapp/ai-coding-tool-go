package store

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"math"
	"slices"
	"strconv"
	"unicode"
	"unicode/utf8"
)

// The store keeps every record as the bytes JSON.stringify wrote. These helpers read and edit
// those bytes as JavaScript would, without re-encoding members they do not touch.

// newLenientDecoder reads JSON as JSON.parse does: a repeated name keeps its first place and its
// last value, and escaped lone surrogates are accepted.
func newLenientDecoder(data []byte) *jsontext.Decoder {
	return jsontext.NewDecoder(bytes.NewReader(data), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
}

// errNotJSON stands in for JSON.parse's SyntaxError.
var errNotJSON = errors.New("Unexpected token in JSON")

// parseJSON checks that data is one JSON value, as JSON.parse would accept it.
func parseJSON(data []byte) (jsontext.Value, error) {
	decoder := newLenientDecoder(data)
	if _, err := decoder.ReadValue(); err != nil {
		return nil, syntaxError(err)
	}
	if _, err := decoder.ReadToken(); err == nil || !isEOF(err) {
		return nil, errNotJSON
	}
	return jsontext.Value(data), nil
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF)
}

func syntaxError(err error) error {
	return &SyntaxError{Err: err}
}

// SyntaxError is a stored record JSON.parse would have refused.
type SyntaxError struct{ Err error }

func (e *SyntaxError) Error() string { return "SyntaxError: " + e.Err.Error() }
func (e *SyntaxError) Unwrap() error { return e.Err }

type member struct {
	name    string
	rawName []byte
	value   jsontext.Value
}

// object is a JSON object's members in order.
type object []member

// parseObject reads data as an object. ok is false for any other JSON value.
func parseObject(data []byte) (members object, ok bool, err error) {
	decoder := newLenientDecoder(data)
	kind := decoder.PeekKind()
	if kind != '{' {
		if _, err := parseJSON(data); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, false, syntaxError(err)
	}
	index := map[string]int{}
	for decoder.PeekKind() != '}' {
		rawName, err := decoder.ReadValue()
		if err != nil {
			return nil, false, syntaxError(err)
		}
		nameEnd := int(decoder.InputOffset())
		rawName = data[nameEnd-len(rawName) : nameEnd]
		name, err := jsontext.AppendUnquote(nil, rawName)
		if err != nil && name == nil {
			return nil, false, syntaxError(err)
		}
		value, err := decoder.ReadValue()
		if err != nil {
			return nil, false, syntaxError(err)
		}
		end := int(decoder.InputOffset())
		value = data[end-len(value) : end]
		if at, seen := index[string(name)]; seen {
			members[at].value = value
			continue
		}
		index[string(name)] = len(members)
		members = append(members, member{name: string(name), rawName: rawName, value: value})
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, false, syntaxError(err)
	}
	if _, err := decoder.ReadToken(); !isEOF(err) {
		return nil, false, errNotJSON
	}
	if members == nil {
		members = object{}
	}
	return members, true, nil
}

// get is the member's value, or nil when the object has none.
func (o object) get(name string) jsontext.Value {
	for _, m := range o {
		if m.name == name {
			return m.value
		}
	}
	return nil
}

func (o object) has(name string) bool { return o.get(name) != nil }

// with sets a member as object spread does: in place when present, last when new.
func (o object) with(name string, value jsontext.Value) object {
	next := slices.Clone(o)
	for i := range next {
		if next[i].name == name {
			next[i].value = value
			return next
		}
	}
	return append(next, member{name: name, value: value})
}

// without drops a member as rest destructuring does.
func (o object) without(name string) object {
	return slices.DeleteFunc(slices.Clone(o), func(m member) bool { return m.name == name })
}

// encode writes the object as JSON.stringify would, keeping each member's bytes.
func (o object) encode() jsontext.Value {
	size := 2
	for _, m := range o {
		size += len(m.name) + len(m.value) + 4
	}
	out := make([]byte, 0, size)
	out = append(out, '{')
	for i, m := range o {
		if i > 0 {
			out = append(out, ',')
		}
		if m.rawName != nil {
			out = append(out, m.rawName...)
		} else {
			out = appendJSString(out, m.name)
		}
		out = append(out, ':')
		out = append(out, m.value...)
	}
	return append(out, '}')
}

// stringValue is the string a value holds.
func stringValue(value jsontext.Value) (string, bool) {
	if len(value) == 0 || value[0] != '"' {
		return "", false
	}
	text, err := jsontext.AppendUnquote(nil, value)
	if err != nil && text == nil {
		return "", false
	}
	return string(text), true
}

// numberValue is the number a value holds.
func numberValue(value jsontext.Value) (float64, bool) {
	if len(value) == 0 || (value[0] != '-' && (value[0] < '0' || value[0] > '9')) {
		return 0, false
	}
	number, err := strconv.ParseFloat(string(value), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return number, true
}

// truthy is JavaScript's truthiness of a parsed value; an absent one is undefined.
func truthy(value jsontext.Value) bool {
	if len(value) == 0 {
		return false
	}
	switch value[0] {
	case 'n', 'f':
		return false
	case 't', '{', '[':
		return true
	case '"':
		return len(value) > 2
	default:
		number, ok := numberValue(value)
		return ok && number != 0 && !math.IsNaN(number)
	}
}

// arrayItems splits a JSON array into its items.
func arrayItems(value jsontext.Value) ([]jsontext.Value, bool) {
	if len(value) == 0 || value[0] != '[' {
		return nil, false
	}
	decoder := newLenientDecoder(value)
	if _, err := decoder.ReadToken(); err != nil {
		return nil, false
	}
	items := []jsontext.Value{}
	for decoder.PeekKind() != ']' {
		item, err := decoder.ReadValue()
		if err != nil {
			return nil, false
		}
		end := int(decoder.InputOffset())
		items = append(items, value[end-len(item):end])
	}
	return items, true
}

// joinArray writes values as one JSON array.
func joinArray(values []jsontext.Value) jsontext.Value {
	size := 2
	for _, value := range values {
		size += len(value) + 1
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for i, value := range values {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, value...)
	}
	return append(out, ']')
}

// jsString quotes s as JSON.stringify does.
func jsString(s string) jsontext.Value { return appendJSString(nil, s) }

func jsStrings(values []string) jsontext.Value {
	items := make([]jsontext.Value, len(values))
	for i, value := range values {
		items[i] = jsString(value)
	}
	return joinArray(items)
}

const hexDigits = "0123456789abcdef"

func appendJSString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				dst = append(dst, '\\', '"')
			case '\\':
				dst = append(dst, '\\', '\\')
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				if c < 0x20 {
					dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
				} else {
					dst = append(dst, c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, "�"...)
		} else {
			dst = append(dst, s[i:i+size]...)
		}
		i += size
	}
	return append(dst, '"')
}

// utf16Length is a JavaScript string's length.
func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// jsTrimmedEmpty is whether String.prototype.trim leaves nothing.
func jsTrimmedEmpty(s string) bool {
	for _, r := range s {
		if !jsSpace(r) {
			return false
		}
	}
	return true
}

func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// JSLower is String.prototype.toLowerCase: Unicode's full lowercase mapping, including the
// dotted capital I and the final sigma.
func JSLower(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf || ('A' <= s[i] && s[i] <= 'Z') {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for i, r := range runes {
		switch r {
		case 0x130:
			out = append(out, 'i', 0x307)
		case 0x3A3:
			if finalSigma(runes, i) {
				out = append(out, 0x3C2)
			} else {
				out = append(out, 0x3C3)
			}
		default:
			out = append(out, unicode.ToLower(r))
		}
	}
	return string(out)
}

func finalSigma(runes []rune, at int) bool {
	before := false
	for i := at - 1; i >= 0; i-- {
		if caseIgnorable(runes[i]) {
			continue
		}
		before = cased(runes[i])
		break
	}
	if !before {
		return false
	}
	for i := at + 1; i < len(runes); i++ {
		if caseIgnorable(runes[i]) {
			continue
		}
		return !cased(runes[i])
	}
	return true
}

func cased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

func caseIgnorable(r rune) bool {
	switch r {
	case '\'', '.', ':', '^', '`', 0xA8, 0xAD, 0xAF, 0xB4, 0xB7, 0xB8, 0x2018, 0x2019, 0x2024, 0x2027:
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}
