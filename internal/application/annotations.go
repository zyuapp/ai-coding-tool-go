package application

import (
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// A quote longer than this is cut when the annotation is made, so a select-all cannot flood the prompt.
const AnnotationQuoteLimit = 2000

func ClampQuote(quote string) string {
	trimmed := threadsTrim(quote)
	if threadsUTF16Len(trimmed) > AnnotationQuoteLimit {
		return threadsUTF16Slice(trimmed, 0, AnnotationQuoteLimit-1) + "…"
	}
	return trimmed
}

const annotationHeading = "Annotations on your earlier output (each quotes your words, then my note on them):"

// How annotations reach the agent. The stored message keeps them structured; only the prompt is flat.
func PromptWithAnnotations(text string, annotations []*domain.Annotation) string {
	if len(annotations) == 0 {
		return text
	}
	blocks := make([]string, len(annotations))
	for index, annotation := range annotations {
		lines := strings.Split(annotation.Quote, "\n")
		for at, line := range lines {
			lines[at] = "> " + line
		}
		quote := strings.Join(lines, "\n")
		if note := threadsTrim(annotation.Note); note != "" {
			blocks[index] = quote + "\n" + note
		} else {
			blocks[index] = quote
		}
	}
	return threadsJoinNonEmpty([]string{text, annotationHeading + "\n\n" + strings.Join(blocks, "\n\n")}, "\n\n")
}

// threadsTrim is String.prototype.trim.
func threadsTrim(text string) string {
	return strings.TrimFunc(text, threadsIsSpace)
}

// threadsTrimStart is String.prototype.trimStart.
func threadsTrimStart(text string) string {
	return strings.TrimLeftFunc(text, threadsIsSpace)
}

// threadsIsSpace is JavaScript's \s.
func threadsIsSpace(r rune) bool {
	return r == 0xFEFF || r != 0x85 && unicode.IsSpace(r)
}

// threadsUTF16Len is a string's JavaScript length.
func threadsUTF16Len(text string) int {
	units := 0
	for _, r := range text {
		units++
		if r >= 0x10000 {
			units++
		}
	}
	return units
}

// threadsUTF16Slice is String.prototype.slice(start, end) on UTF-16 code units.
func threadsUTF16Slice(text string, start, end int) string {
	units := utf16.Encode([]rune(text))
	start, end = max(0, min(start, len(units))), max(0, min(end, len(units)))
	if start >= end {
		return ""
	}
	return string(utf16.Decode(units[start:end]))
}

// threadsJoinNonEmpty is parts.filter(Boolean).join(separator).
func threadsJoinNonEmpty(parts []string, separator string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, separator)
}
