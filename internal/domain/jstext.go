package domain

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

// isJSSpace is JavaScript's \s and what String.prototype.trim removes.
func isJSSpace(r rune) bool {
	return r == 0xFEFF || r != 0x85 && unicode.IsSpace(r)
}

// jsTrim is String.prototype.trim.
func jsTrim(text string) string {
	return strings.TrimFunc(text, isJSSpace)
}

// jsBlank is text.trim() === "".
func jsBlank(text string) bool {
	return jsTrim(text) == ""
}

// utf16Slice is String.prototype.slice(start, end) on UTF-16 code units, with both in range.
// A surrogate pair it cuts in half becomes U+FFFD, where JavaScript keeps a lone surrogate.
func utf16Slice(text string, start, end int) string {
	if start >= end {
		return ""
	}
	units := utf16.Encode([]rune(text))
	start, end = max(0, min(start, len(units))), max(0, min(end, len(units)))
	if start >= end {
		return ""
	}
	return string(utf16.Decode(units[start:end]))
}

// jsNumber is String(value) for a number.
func jsNumber(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	case value == 0:
		return "0"
	}
	if abs := math.Abs(value); abs >= 1e21 || abs < 1e-6 {
		mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(value, 'e', -1, 64), "e")
		sign, digits := exponent[:1], strings.TrimLeft(exponent[1:], "0")
		return mantissa + "e" + sign + digits
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// jsEncodeURIComponent is encodeURIComponent.
func jsEncodeURIComponent(text string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			encoded.WriteByte(c)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hex[c>>4])
		encoded.WriteByte(hex[c&15])
	}
	return encoded.String()
}

// jsRecord is a value decoded from JSON that is a plain object.
func jsRecord(value any) (map[string]any, bool) {
	record, ok := value.(map[string]any)
	return record, ok && record != nil
}

// jsSafeInteger is Number.isSafeInteger.
func jsSafeInteger(value any) bool {
	number, ok := value.(float64)
	return ok && number == math.Trunc(number) && math.Abs(number) <= 1<<53-1
}

// jsFinite is typeof value === "number" && Number.isFinite(value).
func jsFinite(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsInf(number, 0) && !math.IsNaN(number)
}
