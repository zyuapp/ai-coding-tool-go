package mobile

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// isJSSpace is JavaScript's \s and what String.prototype.trim removes.
func isJSSpace(r rune) bool {
	return r == 0xFEFF || r != 0x85 && unicode.IsSpace(r)
}

func jsTrim(text string) string { return strings.TrimFunc(text, isJSSpace) }

func jsTrimEnd(text string) string { return strings.TrimRightFunc(text, isJSSpace) }

// utf16Length is String.prototype.length.
func utf16Length(text string) int {
	length := 0
	for _, r := range text {
		length += utf16.RuneLen(r)
	}
	return length
}

// utf16Prefix is text.slice(0, end) on UTF-16 code units. A surrogate pair it would cut in half is
// left out, where JavaScript keeps a lone surrogate.
func utf16Prefix(text string, end int) string {
	units := 0
	for index, r := range text {
		units += utf16.RuneLen(r)
		if units > end {
			return text[:index]
		}
	}
	return text
}

// collapseSpaces is text.replace(/\s+/g, " ").
func collapseSpaces(text string) string {
	var out strings.Builder
	space := false
	for _, r := range text {
		if isJSSpace(r) {
			space = true
			continue
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
	}
	if space {
		out.WriteByte(' ')
	}
	return out.String()
}

// errorMessage is `error instanceof Error ? error.message : String(error)`.
func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// closeReason fits a close frame's reason into the 123 bytes a frame allows.
func closeReason(reason string) string {
	if len(reason) <= 123 {
		return reason
	}
	cut := 123
	for cut > 0 && !utf8.RuneStart(reason[cut]) {
		cut--
	}
	return reason[:cut]
}

var errNotReady = errors.New("The phone bridge is not ready.")
