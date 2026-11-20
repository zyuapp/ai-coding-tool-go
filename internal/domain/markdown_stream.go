package domain

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// jsSpaceClass is the inside of a character class matching JavaScript's \s.
const jsSpaceClass = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var blockMarker = regexp.MustCompile("^(?:[" + jsSpaceClass + "]*>[" + jsSpaceClass + "]*)*[" + jsSpaceClass + "]*(?:(?:[-+*]|\\d+[.)])[" + jsSpaceClass + "]+)?(`{3,}|~{3,})((?s:.)*)$")

func EmptyScan() BlockScan {
	return BlockScan{}
}

// ScanBlocks reads the lines added since the last scan. Offsets in BlockScan are byte offsets into text.
func ScanBlocks(text string, previous BlockScan) BlockScan {
	safeEnd, fence := previous.SafeEnd, previous.Fence
	lineStart := int(previous.Scanned)
	for lineStart < len(text) {
		newline := strings.IndexByte(text[lineStart:], '\n')
		if newline == -1 {
			break
		}
		newline += lineStart
		line := text[lineStart:newline]
		marker := blockMarker.FindStringSubmatch(line)
		switch {
		case fence == nil && marker != nil:
			fence = &BlockScanFence{Marker: marker[1][:1], Length: float64(len(marker[1]))}
		case fence != nil && marker != nil && marker[1][:1] == fence.Marker && float64(len(marker[1])) >= fence.Length && jsBlank(marker[2]):
			fence = nil
			safeEnd = float64(newline + 1)
		case fence == nil && jsBlank(line):
			safeEnd = float64(newline + 1)
		}
		lineStart = newline + 1
	}
	return BlockScan{SafeEnd: safeEnd, Scanned: float64(lineStart), Fence: fence}
}

// markdownStreamPunctuation is CommonMark's punctuation class.
var markdownStreamPunctuation = regexp.MustCompile("[!-/:-@\\[-`{-~]")

// markersOnly matches a line that carries markers but no content yet.
var markersOnly = regexp.MustCompile("^[" + jsSpaceClass + ">#*+\\-_=~`|.:0-9]*$")

// tableRow and tableDelimiter tell a GFM table's rows from its delimiter row.
var tableRow = regexp.MustCompile(`^[` + jsSpaceClass + `]*\|`)
var tableDelimiter = regexp.MustCompile(`^[|` + jsSpaceClass + `:-]*-[|` + jsSpaceClass + `:-]*$`)
var inlineMarker = regexp.MustCompile("[\\\\`<\\[!*_~]")

func closingRun(text string, from int, char byte, length int) int {
	for index := markdownIndexByte(text, from, char); index != -1; index = markdownIndexByte(text, index+1, char) {
		run := 0
		for index+run < len(text) && text[index+run] == char {
			run++
		}
		if run == length {
			return index
		}
		index += run - 1
	}
	return -1
}

// markdownIndexByte is text.indexOf(char, from).
func markdownIndexByte(text string, from int, char byte) int {
	if from >= len(text) {
		return -1
	}
	index := strings.IndexByte(text[from:], char)
	if index == -1 {
		return -1
	}
	return from + index
}

func markdownPunctuation(r rune) bool {
	return r < utf8.RuneSelf && markdownStreamPunctuation.MatchString(string(r))
}

// flanks reports whether a delimiter run leans against the text before or after it.
func flanks(text string, start, end int, char byte) (canOpen, canClose bool) {
	before, after := ' ', ' '
	if start > 0 {
		before, _ = utf8.DecodeLastRuneInString(text[:start])
	}
	if end < len(text) {
		after, _ = utf8.DecodeRuneInString(text[end:])
	}
	left := !isJSSpace(after) && (!markdownPunctuation(after) || isJSSpace(before) || markdownPunctuation(before))
	right := !isJSSpace(before) && (!markdownPunctuation(before) || isJSSpace(after) || markdownPunctuation(after))
	if char != '_' {
		return left, right
	}
	return left && (!right || markdownPunctuation(before)), right && (!left || markdownPunctuation(after))
}

// InlineSafeEnd is the byte offset up to which text renders as finished Markdown.
func InlineSafeEnd(text string) int {
	type opened struct {
		char byte
		at   int
	}
	var open []opened
	earliest := func() int {
		if len(open) > 0 {
			return open[0].at
		}
		return len(text)
	}
	index := 0
	for index < len(text) {
		found := inlineMarker.FindStringIndex(text[index:])
		if found == nil {
			break
		}
		index += found[0]
		char := text[index]
		switch {
		case char == '\\':
			index++
			if index < len(text) {
				_, size := utf8.DecodeRuneInString(text[index:])
				index += size
			}
		case char == '`':
			start := index
			for index < len(text) && text[index] == '`' {
				index++
			}
			close := closingRun(text, index, '`', index-start)
			if close == -1 {
				return min(start, earliest())
			}
			index = close + (index - start)
		case char == '<':
			close := markdownIndexByte(text, index, '>')
			if close == -1 {
				return min(index, earliest())
			}
			index = close + 1
		case char == '[' || char == '!' && index+1 < len(text) && text[index+1] == '[':
			start := index
			label := markdownIndexByte(text, start+1, ']')
			if label == -1 {
				return min(start, earliest())
			}
			if label+1 < len(text) && text[label+1] == '(' && markdownIndexByte(text, label+1, ')') == -1 {
				return min(start, earliest())
			}
			index = label + 1
		case char == '*' || char == '_' || char == '~':
			start := index
			for index < len(text) && text[index] == char {
				index++
			}
			var canOpen, canClose bool
			if index >= len(text) {
				canOpen = true
				if start > 0 {
					before, _ := utf8.DecodeLastRuneInString(text[:start])
					canClose = !isJSSpace(before)
				}
			} else {
				canOpen, canClose = flanks(text, start, index, char)
			}
			if canClose && len(open) > 0 && open[len(open)-1].char == char {
				open = open[:len(open)-1]
			} else if canOpen {
				open = append(open, opened{char, start})
			}
		default:
			index++
		}
	}
	return earliest()
}

func RepairCut(text string) string {
	cut := strings.LastIndexByte(text, '\n') + 1
	complete := text[:cut]
	scan := ScanBlocks(complete, EmptyScan())
	if scan.Fence != nil {
		close := strings.Repeat(scan.Fence.Marker, int(scan.Fence.Length))
		separator := "\n"
		if strings.HasSuffix(text, "\n") {
			separator = ""
		}
		return text + separator + close + "\n"
	}
	partial := text[cut:]
	held := partial
	if markersOnly.MatchString(partial) || blockMarker.MatchString(partial) {
		held = ""
	}
	lines := strings.Split(complete, "\n")
	lines = lines[:len(lines)-1]
	rows := len(lines)
	for rows > 0 && tableRow.MatchString(lines[rows-1]) {
		rows--
	}
	established := false
	for _, line := range lines[rows:] {
		if tableDelimiter.MatchString(line) {
			established = true
			break
		}
	}
	var above strings.Builder
	if established {
		above.WriteString(complete)
	} else {
		for _, line := range lines[:rows] {
			above.WriteString(line)
			above.WriteByte('\n')
		}
	}
	body := above.String()
	if !tableRow.MatchString(held) || established {
		body += held
	}
	return body[:InlineSafeEnd(body)]
}
