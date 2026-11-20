package contracts

import (
	"encoding/json/jsontext"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Validator checks one field of a command as sent. An absent field is an empty Value (TypeScript's undefined);
// JSON null is the Value "null".
type Validator func(value jsontext.Value) bool

// ShapeField is one entry of a Shape.
type ShapeField struct {
	Name  string
	Check Validator
}

// Shape is a validator per field, in declaration order (the order the wire advertises them in).
type Shape []ShapeField

// AgentRule is what an agent's command must also satisfy. It reads the command as sent, after its fields passed.
type AgentRule func(command map[string]jsontext.Value) bool

// Declaration is one command's fields, where it is carried out, and whether an agent or a paired phone may send it.
type Declaration struct {
	Type   string
	Fields Shape
	At     CommandPlacement
	// Leaves moves this window to one of its own threads, which takes a paired computer's thread off screen.
	Leaves bool
	// Agent is what an agent's command must also satisfy. A command without one is the window's alone.
	Agent AgentRule
	// Phone is what a paired phone's command must satisfy. A command without one is never a phone's.
	Phone PhoneRule
}

// Declarations is one feature's table, in declaration order.
type Declarations []Declaration

func CommandDeclarationRecord(value any) bool {
	_, ok := declRecordOf(value)
	return ok
}

var Text Validator = func(value jsontext.Value) bool {
	s, ok := declString(value)
	return ok && declUTF16Len(s) <= 16_000_000
}

var PathText Validator = func(value jsontext.Value) bool {
	s, ok := declString(value)
	return ok && declUTF16Len(s) <= 4096 && !strings.Contains(s, "\x00")
}

var DeviceID Validator = func(value jsontext.Value) bool {
	s, ok := declString(value)
	return ok && len(s) > 0 && declUTF16Len(s) <= 256
}

var Boolean Validator = func(value jsontext.Value) bool {
	_, ok := declBool(value)
	return ok
}

var Number Validator = func(value jsontext.Value) bool {
	_, ok := declNumber(value)
	return ok
}

func Optional(check Validator) Validator {
	return func(value jsontext.Value) bool { return declUndefined(value) || check(value) }
}

func Nullable(check Validator) Validator {
	return func(value jsontext.Value) bool { return declNull(value) || check(value) }
}

func Array(check Validator) Validator {
	return func(value jsontext.Value) bool {
		items, ok := declArray(value)
		if !ok || len(items) > 10_000 {
			return false
		}
		for _, item := range items {
			if !check(item) {
				return false
			}
		}
		return true
	}
}

// Literals accepts a value equal to one of choices: strings (or named string types), numbers, or booleans.
func Literals(choices ...any) Validator {
	normalized := make([]any, len(choices))
	for i, choice := range choices {
		normalized[i] = declLiteral(choice)
	}
	return func(value jsontext.Value) bool {
		var decoded any
		switch value.Kind() {
		case '"':
			decoded, _ = declString(value)
		case '0':
			n, ok := declNumber(value)
			if !ok {
				return false
			}
			decoded = n
		case 't', 'f':
			decoded, _ = declBool(value)
		default:
			return false
		}
		for _, choice := range normalized {
			if choice == decoded {
				return true
			}
		}
		return false
	}
}

func HasFields(value map[string]jsontext.Value, fields Shape) bool {
	for _, field := range fields {
		if !field.Check(value[field.Name]) {
			return false
		}
	}
	return true
}

func Object(fields Shape) Validator {
	return func(value jsontext.Value) bool {
		record, ok := declRecord(value)
		return ok && HasFields(record, fields)
	}
}

var OptionalText = Optional(Text)
var OptionalBoolean = Optional(Boolean)
var NullableText = Nullable(Text)
var Step = Literals(-1, 1)

// AgentID: agents name what they act on by a non-empty id.
func AgentID(value jsontext.Value) bool {
	return declUndefined(value) || PhoneCommandsIsString(value)
}

func AgentThread(command map[string]jsontext.Value) bool {
	return AgentID(command["taskId"])
}

// CommandDeclarationNamesThread: an agent acts on the thread it names, never on whichever one is on screen.
func CommandDeclarationNamesThread(command map[string]jsontext.Value) bool {
	return PhoneCommandsIsString(command["taskId"])
}

// declRaw turns a value into JSON as sent: a jsontext.Value or []byte as is, anything else encoded.
// A nil interface is null.
func declRaw(value any) (jsontext.Value, bool) {
	switch v := value.(type) {
	case jsontext.Value:
		return v, true
	case []byte:
		return jsontext.Value(v), true
	case nil:
		return jsontext.Value("null"), true
	}
	data, err := jsonx.Marshal(value)
	if err != nil {
		return nil, false
	}
	return jsontext.Value(data), true
}

func declUndefined(value jsontext.Value) bool { return len(value) == 0 }

func declNull(value jsontext.Value) bool { return value.Kind() == 'n' }

func declString(value jsontext.Value) (string, bool) {
	if value.Kind() != '"' {
		return "", false
	}
	var s string
	if jsonx.Unmarshal(value, &s) != nil {
		return "", false
	}
	return s, true
}

// declNumber reads a finite number; a literal past float64's range is JavaScript's Infinity.
func declNumber(value jsontext.Value) (float64, bool) {
	if value.Kind() != '0' {
		return 0, false
	}
	n, err := strconv.ParseFloat(string(value), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return n, !math.IsInf(n, 0) && !math.IsNaN(n)
}

func declBool(value jsontext.Value) (bool, bool) {
	switch value.Kind() {
	case 't':
		return true, true
	case 'f':
		return false, true
	}
	return false, false
}

func declArray(value jsontext.Value) ([]jsontext.Value, bool) {
	if value.Kind() != '[' {
		return nil, false
	}
	var items []jsontext.Value
	if jsonx.Unmarshal(value, &items) != nil {
		return nil, false
	}
	return items, true
}

func declRecord(value jsontext.Value) (map[string]jsontext.Value, bool) {
	if value.Kind() != '{' {
		return nil, false
	}
	var record map[string]jsontext.Value
	if jsonx.Unmarshal(value, &record) != nil {
		return nil, false
	}
	if record == nil {
		record = map[string]jsontext.Value{}
	}
	return record, true
}

// declRecordOf reads a JSON object from a raw value, a Go map, or any value that encodes as an object.
func declRecordOf(value any) (map[string]jsontext.Value, bool) {
	switch v := value.(type) {
	case map[string]jsontext.Value:
		return v, v != nil
	case jsontext.Value:
		return declRecord(v)
	case []byte:
		return declRecord(jsontext.Value(v))
	case map[string]any:
		if v == nil {
			return nil, false
		}
	}
	raw, ok := declRaw(value)
	if !ok {
		return nil, false
	}
	return declRecord(raw)
}

// declAny decodes a present value into plain Go values (map[string]any, []any, string, float64, bool, nil)
// for the guards other files declare on any.
func declAny(value jsontext.Value) (any, bool) {
	if declUndefined(value) {
		return nil, false
	}
	var decoded any
	if jsonx.Unmarshal(value, &decoded) != nil {
		return nil, false
	}
	return decoded, true
}

// declGuard adapts a guard on plain Go values. An absent field fails it, as every such guard refuses undefined.
func declGuard(check func(any) bool) Validator {
	return func(value jsontext.Value) bool {
		decoded, ok := declAny(value)
		return ok && check(decoded)
	}
}

func declLiteral(choice any) any {
	switch c := choice.(type) {
	case string, bool, float64:
		return c
	case int:
		return float64(c)
	}
	v := reflect.ValueOf(choice)
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int())
	case reflect.Float32, reflect.Float64:
		return v.Float()
	}
	return choice
}

// declUTF16Len is JavaScript's string length: UTF-16 code units.
func declUTF16Len(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] < utf8.RuneSelf {
			n++
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
		i += size
	}
	return n
}

// declBlank is JavaScript's s.trim().length === 0.
func declBlank(s string) bool {
	return strings.TrimFunc(s, declJSSpace) == ""
}

func declJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// declIsKind reports whether a record's "kind" is the string kind.
func declIsKind(record map[string]jsontext.Value, kind string) bool {
	value, ok := declString(record["kind"])
	return ok && value == kind
}

// declLiteralsOf is Literals over a list of literal constants.
func declLiteralsOf[T any](choices []T) Validator {
	values := make([]any, len(choices))
	for i, choice := range choices {
		values[i] = choice
	}
	return Literals(values...)
}
