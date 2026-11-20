package tools

import (
	"encoding/json/jsontext"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// The subset of zod the tool inputs are written in. A Field emits the JSON Schema zod's
// toJSONSchema writes for it (draft-07, input side, the same key order), and parses arguments the
// way zod's safeParse does: the same trims and transforms, the same issues in the same order, and
// the same messages, so an agent is refused in the words the TypeScript app refuses it in.

type fieldKind int

const (
	kindString fieldKind = iota
	kindNumber
	kindBool
	kindEnum
	kindArray
	kindObject
)

// Field is one zod schema. Builders return copies, so a field is shared freely.
type Field struct {
	kind fieldKind
	// strings
	trim           bool
	minLen, maxLen int
	transform      func(string) string
	// numbers
	integer  bool
	min, max *float64
	// enums
	values []string
	// arrays
	items              *Field
	minItems, maxItems int
	// objects
	props Shape
	// a .refine on the value, after any transform
	refine    func(any) bool
	refineMsg string

	optional, nullable bool
	description        string
	// A description given after .optional() is written before the inner schema's keys.
	descriptionFirst bool
}

// Prop is one key of an object, in the order zod declares it.
type Prop struct {
	Name  string
	Field Field
}

// Shape is a zod raw shape: an object's keys in order.
type Shape []Prop

func String() Field  { return Field{kind: kindString, minLen: -1, maxLen: -1} }
func Number() Field  { return Field{kind: kindNumber} }
func Boolean() Field { return Field{kind: kindBool} }

func Enum[T ~string](values ...T) Field {
	field := Field{kind: kindEnum}
	for _, value := range values {
		field.values = append(field.values, string(value))
	}
	return field
}

func Array(items Field) Field {
	return Field{kind: kindArray, items: &items, minItems: -1, maxItems: -1}
}

func Object(shape Shape) Field { return Field{kind: kindObject, props: shape} }

func (f Field) Trim() Field { f.trim = true; return f }

// Min is a string's or array's least length, or a number's least value.
func (f Field) Min(n float64) Field {
	switch f.kind {
	case kindString:
		f.minLen = int(n)
	case kindArray:
		f.minItems = int(n)
	default:
		f.min = &n
	}
	return f
}

// Max is a string's or array's greatest length, or a number's greatest value.
func (f Field) Max(n float64) Field {
	switch f.kind {
	case kindString:
		f.maxLen = int(n)
	case kindArray:
		f.maxItems = int(n)
	default:
		f.max = &n
	}
	return f
}

func (f Field) Nonnegative() Field { return f.Min(0) }
func (f Field) Int() Field         { f.integer = true; return f }

func (f Field) Transform(transform func(string) string) Field { f.transform = transform; return f }

func (f Field) Refine(check func(any) bool, message string) Field {
	f.refine, f.refineMsg = check, message
	return f
}

func (f Field) Optional() Field { f.optional = true; return f }
func (f Field) Nullable() Field { f.nullable = true; return f }

func (f Field) Describe(description string) Field {
	f.description = description
	f.descriptionFirst = f.optional
	return f
}

// With is the shape with name set to field: in its place when the shape has it, else last.
func (s Shape) With(name string, field Field) Shape {
	out := slices.Clone(s)
	for index := range out {
		if out[index].Name == name {
			out[index].Field = field
			return out
		}
	}
	return append(out, Prop{Name: name, Field: field})
}

// Get is the field the shape holds under name.
func (s Shape) Get(name string) (Field, bool) {
	for _, prop := range s {
		if prop.Name == name {
			return prop.Field, true
		}
	}
	return Field{}, false
}

// JSONSchema is the tool's input schema as the MCP SDK lists it.
func (s Shape) JSONSchema() jsontext.Value {
	var buffer []byte
	enc := jsontext.NewEncoder(sliceWriter{&buffer})
	writeObject(enc, s, true)
	return jsontext.Value(strings.TrimSuffix(string(buffer), "\n"))
}

type sliceWriter struct{ buffer *[]byte }

func (w sliceWriter) Write(p []byte) (int, error) {
	*w.buffer = append(*w.buffer, p...)
	return len(p), nil
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func writeKey(enc *jsontext.Encoder, key string) { must(enc.WriteToken(jsontext.String(key))) }

func writeNumber(enc *jsontext.Encoder, key string, value float64) {
	writeKey(enc, key)
	must(enc.WriteToken(jsontext.Float(value)))
}

func writeString(enc *jsontext.Encoder, key, value string) {
	writeKey(enc, key)
	must(enc.WriteToken(jsontext.String(value)))
}

func writeObject(enc *jsontext.Encoder, shape Shape, root bool) {
	must(enc.WriteToken(jsontext.BeginObject))
	writeString(enc, "type", "object")
	writeKey(enc, "properties")
	must(enc.WriteToken(jsontext.BeginObject))
	var required []string
	for _, prop := range shape {
		writeKey(enc, prop.Name)
		writeField(enc, prop.Field)
		if !prop.Field.optional {
			required = append(required, prop.Name)
		}
	}
	must(enc.WriteToken(jsontext.EndObject))
	if len(required) > 0 {
		writeKey(enc, "required")
		must(enc.WriteToken(jsontext.BeginArray))
		for _, name := range required {
			must(enc.WriteToken(jsontext.String(name)))
		}
		must(enc.WriteToken(jsontext.EndArray))
	}
	if root {
		writeString(enc, "$schema", "http://json-schema.org/draft-07/schema#")
	}
	must(enc.WriteToken(jsontext.EndObject))
}

func writeField(enc *jsontext.Encoder, f Field) {
	if f.kind == kindObject {
		writeObject(enc, f.props, false)
		return
	}
	must(enc.WriteToken(jsontext.BeginObject))
	if f.description != "" && f.descriptionFirst {
		writeString(enc, "description", f.description)
	}
	if f.nullable {
		writeKey(enc, "anyOf")
		must(enc.WriteToken(jsontext.BeginArray))
		must(enc.WriteToken(jsontext.BeginObject))
		writeBase(enc, f)
		must(enc.WriteToken(jsontext.EndObject))
		must(enc.WriteToken(jsontext.BeginObject))
		writeString(enc, "type", "null")
		must(enc.WriteToken(jsontext.EndObject))
		must(enc.WriteToken(jsontext.EndArray))
	} else {
		writeBase(enc, f)
	}
	if f.description != "" && !f.descriptionFirst {
		writeString(enc, "description", f.description)
	}
	must(enc.WriteToken(jsontext.EndObject))
}

const maxSafeInteger = 9007199254740991

func writeBase(enc *jsontext.Encoder, f Field) {
	switch f.kind {
	case kindString:
		writeString(enc, "type", "string")
		if f.minLen >= 0 {
			writeNumber(enc, "minLength", float64(f.minLen))
		}
		if f.maxLen >= 0 {
			writeNumber(enc, "maxLength", float64(f.maxLen))
		}
	case kindNumber:
		if f.integer {
			writeString(enc, "type", "integer")
			writeNumber(enc, "minimum", max(-maxSafeInteger, valueOr(f.min, -maxSafeInteger)))
			writeNumber(enc, "maximum", min(maxSafeInteger, valueOr(f.max, maxSafeInteger)))
			return
		}
		writeString(enc, "type", "number")
		if f.min != nil {
			writeNumber(enc, "minimum", *f.min)
		}
		if f.max != nil {
			writeNumber(enc, "maximum", *f.max)
		}
	case kindBool:
		writeString(enc, "type", "boolean")
	case kindEnum:
		writeString(enc, "type", "string")
		writeKey(enc, "enum")
		must(enc.WriteToken(jsontext.BeginArray))
		for _, value := range f.values {
			must(enc.WriteToken(jsontext.String(value)))
		}
		must(enc.WriteToken(jsontext.EndArray))
	case kindArray:
		if f.minItems >= 0 {
			writeNumber(enc, "minItems", float64(f.minItems))
		}
		if f.maxItems >= 0 {
			writeNumber(enc, "maxItems", float64(f.maxItems))
		}
		writeString(enc, "type", "array")
		writeKey(enc, "items")
		writeField(enc, *f.items)
	}
}

func valueOr(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

// Issue is one thing zod found wrong with the arguments.
type Issue struct {
	Path    []any
	Message string
	// Whether later checks still run, as zod's `continue`.
	continues bool
}

// Text is the issue as the MCP SDK reports it: the message, and where it is.
func (i Issue) Text() string {
	if len(i.Path) == 0 {
		return i.Message
	}
	var b strings.Builder
	for index, segment := range i.Path {
		switch segment := segment.(type) {
		case int:
			if index == 0 {
				b.WriteString(strconv.Itoa(segment))
			} else {
				fmt.Fprintf(&b, "[%d]", segment)
			}
		default:
			if index > 0 {
				b.WriteByte('.')
			}
			fmt.Fprint(&b, segment)
		}
	}
	return i.Message + " at " + b.String()
}

// Args are parsed arguments: what zod hands a tool, with absent keys absent. Values are string,
// float64, bool, nil (an explicit null), []any and Args.
type Args map[string]any

// Parse reads raw arguments against the shape as z.object(shape).safeParse does.
func (s Shape) Parse(value any) (Args, []Issue) {
	var issues []Issue
	out := parseObject(s, value, nil, &issues)
	if len(issues) > 0 {
		return nil, issues
	}
	return out.(Args), nil
}

func received(value any) string {
	switch value := value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if math.IsNaN(value) {
			return "NaN"
		}
		if math.IsInf(value, 1) {
			return "Infinity"
		}
		if math.IsInf(value, -1) {
			return "-Infinity"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any, Args:
		return "object"
	}
	return "unknown"
}

func receivedOf(value any, present bool) string {
	if !present {
		return "undefined"
	}
	return received(value)
}

func invalidType(expected string, value any, present bool, path []any) Issue {
	return Issue{Path: path, Message: fmt.Sprintf("Invalid input: expected %s, received %s", expected, receivedOf(value, present))}
}

func extend(path []any, segment any) []any {
	return append(slices.Clip(path), segment)
}

func aborted(issues []Issue, from int) bool {
	for _, issue := range issues[from:] {
		if !issue.continues {
			return true
		}
	}
	return false
}

func parseObject(shape Shape, value any, path []any, issues *[]Issue) any {
	record, ok := value.(map[string]any)
	if !ok {
		if args, isArgs := value.(Args); isArgs {
			record, ok = args, true
		}
	}
	if !ok {
		*issues = append(*issues, invalidType("object", value, true, path))
		return value
	}
	out := Args{}
	for _, prop := range shape {
		raw, present := record[prop.Name]
		parsed, keep := parseField(prop.Field, raw, present, extend(path, prop.Name), issues)
		if keep {
			out[prop.Name] = parsed
		}
	}
	return out
}

// parseField answers the parsed value, and whether the key is kept in the output.
func parseField(f Field, value any, present bool, path []any, issues *[]Issue) (any, bool) {
	if !present && f.optional {
		return nil, false
	}
	if present && value == nil && f.nullable {
		return nil, true
	}
	start := len(*issues)
	var out any
	switch f.kind {
	case kindString:
		out = parseString(f, value, present, path, issues)
	case kindNumber:
		out = parseNumber(f, value, present, path, issues)
	case kindBool:
		if _, ok := value.(bool); !ok || !present {
			*issues = append(*issues, invalidType("boolean", value, present, path))
		}
		out = value
	case kindEnum:
		text, ok := value.(string)
		if !ok || !present || !slices.Contains(f.values, text) {
			*issues = append(*issues, Issue{Path: path, Message: enumMessage(f.values)})
		}
		out = value
	case kindArray:
		out = parseArray(f, value, present, path, issues)
	case kindObject:
		if !present {
			*issues = append(*issues, invalidType("object", value, false, path))
			out = value
		} else {
			out = parseObject(f.props, value, path, issues)
		}
	}
	if f.refine != nil && !aborted(*issues, start) && (f.transform == nil || len(*issues) == start) {
		if !f.refine(out) {
			*issues = append(*issues, Issue{Path: path, Message: f.refineMsg})
		}
	}
	return out, true
}

func enumMessage(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = strconv.Quote(value)
	}
	if len(values) == 1 {
		return "Invalid input: expected " + quoted[0]
	}
	return "Invalid option: expected one of " + strings.Join(quoted, "|")
}

// lengthOf is the length zod's size checks read: a string's code points, or an array's items.
func lengthOf(value any, present bool) (length int, origin, unit string, ok bool) {
	if !present {
		return 0, "", "", false
	}
	switch value := value.(type) {
	case string:
		return utf8.RuneCountInString(value), "string", "characters", true
	case []any:
		return len(value), "array", "items", true
	}
	return 0, "", "", false
}

func sizeChecks(minimum, maximum int, value any, present bool, path []any, issues *[]Issue) {
	length, origin, unit, ok := lengthOf(value, present)
	if !ok {
		return
	}
	if minimum >= 0 && length < minimum {
		*issues = append(*issues, Issue{Path: path, Message: fmt.Sprintf("Too small: expected %s to have >=%d %s", origin, minimum, unit), continues: true})
	}
	if maximum >= 0 && length > maximum {
		*issues = append(*issues, Issue{Path: path, Message: fmt.Sprintf("Too big: expected %s to have <=%d %s", origin, maximum, unit), continues: true})
	}
}

func parseString(f Field, value any, present bool, path []any, issues *[]Issue) any {
	text, ok := value.(string)
	if !ok || !present {
		*issues = append(*issues, invalidType("string", value, present, path))
		sizeChecks(f.minLen, f.maxLen, value, present, path, issues)
		return value
	}
	if f.trim {
		text = jsTrim(text)
	}
	start := len(*issues)
	sizeChecks(f.minLen, f.maxLen, text, true, path, issues)
	if f.transform != nil && len(*issues) == start {
		text = f.transform(text)
	}
	return text
}

func parseNumber(f Field, value any, present bool, path []any, issues *[]Issue) any {
	number, ok := value.(float64)
	if !ok || !present || math.IsNaN(number) || math.IsInf(number, 0) {
		*issues = append(*issues, invalidType("number", value, present, path))
		return value
	}
	if f.integer {
		if number != math.Trunc(number) {
			*issues = append(*issues, Issue{Path: path, Message: "Invalid input: expected int, received number"})
			return value
		}
		if number > maxSafeInteger {
			*issues = append(*issues, Issue{Path: path, Message: "Too big: expected int to be <=9007199254740991", continues: true})
		} else if number < -maxSafeInteger {
			*issues = append(*issues, Issue{Path: path, Message: "Too small: expected int to be >=-9007199254740991", continues: true})
		}
	}
	if f.min != nil && number < *f.min {
		*issues = append(*issues, Issue{Path: path, Message: "Too small: expected number to be >=" + jsNumber(*f.min), continues: true})
	}
	if f.max != nil && number > *f.max {
		*issues = append(*issues, Issue{Path: path, Message: "Too big: expected number to be <=" + jsNumber(*f.max), continues: true})
	}
	return number
}

func parseArray(f Field, value any, present bool, path []any, issues *[]Issue) any {
	items, ok := value.([]any)
	if !ok || !present {
		*issues = append(*issues, invalidType("array", value, present, path))
		sizeChecks(f.minItems, f.maxItems, value, present, path, issues)
		return value
	}
	// An oversized array fails its size cap before any item is read.
	if f.maxItems >= 0 && len(items) > f.maxItems {
		*issues = append(*issues, Issue{Path: path, Message: fmt.Sprintf("Too big: expected array to have <=%d items", f.maxItems)})
		return value
	}
	out := make([]any, len(items))
	for index, item := range items {
		parsed, _ := parseField(*f.items, item, true, extend(path, index), issues)
		out[index] = parsed
	}
	if f.minItems >= 0 && len(items) < f.minItems {
		*issues = append(*issues, Issue{Path: path, Message: fmt.Sprintf("Too small: expected array to have >=%d items", f.minItems), continues: true})
	}
	return out
}

// isJSSpace is JavaScript's \s and what String.prototype.trim removes.
func isJSSpace(r rune) bool {
	return r == 0xFEFF || r != 0x85 && unicode.IsSpace(r)
}

// jsTrim is String.prototype.trim.
func jsTrim(text string) string {
	return strings.TrimFunc(text, isJSSpace)
}

// utf16Len is a string's JavaScript length.
func utf16Len(text string) int {
	n := 0
	for _, r := range text {
		n += utf16.RuneLen(r)
	}
	return n
}

// jsNumber is String(value) for a finite number.
func jsNumber(value float64) string {
	if value == 0 {
		return "0"
	}
	if abs := math.Abs(value); abs >= 1e21 || abs < 1e-6 {
		mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(value, 'e', -1, 64), "e")
		sign, digits := exponent[:1], strings.TrimLeft(exponent[1:], "0")
		return mantissa + "e" + sign + digits
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}
