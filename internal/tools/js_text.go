package tools

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// jsonString is JSON.stringify of a string.
func jsonString(text string) string {
	var b strings.Builder
	b.WriteByte('"')
	for index := 0; index < len(text); {
		r, size := utf8.DecodeRuneInString(text[index:])
		index += size
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// isoString is new Date(ms).toISOString().
func isoString(ms float64) string {
	at := time.UnixMilli(int64(ms)).UTC()
	year := at.Year()
	prefix := fmt.Sprintf("%04d", year)
	if year < 0 || year > 9999 {
		sign := "+"
		if year < 0 {
			sign, year = "-", -year
		}
		prefix = fmt.Sprintf("%s%06d", sign, year)
	}
	return prefix + at.Format("-01-02T15:04:05.000Z")
}

// errorText is what `error instanceof Error ? error.message : String(error)` reads.
func errorText(err error) string {
	return err.Error()
}
