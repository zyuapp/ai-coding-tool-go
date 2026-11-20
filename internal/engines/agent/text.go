package agent

import "strings"

// JavaScript's String.prototype.trim.
func jsTrim(text string) string {
	return strings.TrimFunc(text, isJSSpace)
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// The first limit UTF-16 code units of text, as JavaScript's slice(0, limit) cuts it.
func utf16Prefix(text string, limit int) string {
	units := 0
	for index, r := range text {
		width := 1
		if r > 0xffff {
			width = 2
		}
		// A surrogate pair the limit cuts in half is dropped whole: Go strings cannot hold a lone surrogate.
		if units+width > limit {
			return text[:index]
		}
		units += width
	}
	return text
}
