package codex

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// One entry of a TOML table that keeps the order its entries were written in.
type TomlEntry struct {
	Key   string
	Value any
}

// A TOML table in the order it is written. A map[string]any is written with its keys sorted.
type TomlTable []TomlEntry

var tomlEscapes = map[rune]string{'\\': `\\`, '"': `\"`, '\n': `\n`, '\r': `\r`, '\t': `\t`}

func tomlString(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for index := 0; index < len(value); index++ {
		char := value[index]
		if escaped, ok := tomlEscapes[rune(char)]; ok {
			out.WriteString(escaped)
		} else if char < 0x20 || char == 0x7f {
			fmt.Fprintf(&out, `\u%04x`, char)
		} else {
			out.WriteByte(char)
		}
	}
	out.WriteByte('"')
	return out.String()
}

// One TOML value, as `-c key=value` takes it: a string, bool, number, slice, TomlTable or map.
func Toml(value any) string {
	switch value := value.(type) {
	case string:
		return tomlString(value)
	case bool:
		return strconv.FormatBool(value)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case float64:
		return jsNumber(value)
	case []string:
		parts := make([]string, len(value))
		for index, entry := range value {
			parts[index] = Toml(entry)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []any:
		parts := make([]string, len(value))
		for index, entry := range value {
			parts[index] = Toml(entry)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case TomlTable:
		if len(value) == 0 {
			return "{}"
		}
		parts := make([]string, len(value))
		for index, entry := range value {
			parts[index] = tomlString(entry.Key) + " = " + Toml(entry.Value)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case map[string]string:
		table := TomlTable{}
		for _, key := range slices.Sorted(maps.Keys(value)) {
			table = append(table, TomlEntry{key, value[key]})
		}
		return Toml(table)
	case map[string]any:
		table := TomlTable{}
		for _, key := range slices.Sorted(maps.Keys(value)) {
			table = append(table, TomlEntry{key, value[key]})
		}
		return Toml(table)
	case time.Time:
		return value.Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(value)
	}
}

// A number as JavaScript's String writes it, for the integers and plain decimals config holds.
func jsNumber(value float64) string {
	if math.IsInf(value, 1) {
		return "Infinity"
	}
	if math.IsInf(value, -1) {
		return "-Infinity"
	}
	if math.IsNaN(value) {
		return "NaN"
	}
	if value == 0 {
		return "0"
	}
	if magnitude := math.Abs(value); magnitude >= 1e21 || magnitude < 1e-6 {
		text := strconv.FormatFloat(value, 'e', -1, 64)
		mantissa, exponent, _ := strings.Cut(text, "e")
		if strings.HasPrefix(exponent, "+") || strings.HasPrefix(exponent, "-") {
			sign := exponent[:1]
			exponent = strings.TrimLeft(exponent[1:], "0")
			return mantissa + "e" + sign + exponent
		}
		return text
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}
