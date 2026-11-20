package application

import (
	"strings"

	"github.com/dlclark/regexp2"
)

// A reply read as one line of plain words, as a notice or a thread's card shows what it said.

// JavaScript's \s, \S, \w and `.` as character classes, so the patterns below read as their TypeScript originals do.
const (
	replySpace    = `[\t\n\v\f\r \u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff]`
	replyNonSpace = `[^\t\n\v\f\r \u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff]`
	replyWord     = `[A-Za-z0-9_]`
	replyAny      = `[^\n\r\u2028\u2029]`
)

func replyPattern(pattern string) *regexp2.Regexp {
	pattern = strings.NewReplacer(`\s`, replySpace, `\S`, replyNonSpace, `\w`, replyWord, `.+?`, replyAny+`+?`).Replace(pattern)
	return regexp2.MustCompile(pattern, regexp2.None)
}

// A line that only fences code or rules a section off, which says nothing.
var bare = replyPattern("^(?:`{3,}|~{3,}|(?:[-*_]\\s*){3,}$)")

// What opens a markdown line rather than saying something: quote marks, then a heading's, a list item's or a task's.
var opening = replyPattern(`^(?:>\s?)*(?:#{1,6}\s+|(?:[-*+]|[0-9]+[.)])\s+(?:\[[ xX]\]\s+)?)?`)

// Inline markdown: a code span, a link or image, emphasis or strikethrough, and underscore emphasis that is not part of a word.
var inline = replyPattern("(`+)(.+?)\\1|!?\\[([^\\]]*)\\]\\([^)]*\\)|(\\*\\*|\\*|~~)(?=\\S)(.+?)(?<=\\S)\\4|(?<!\\w)(__|_)(?=\\S)(.+?)(?<=\\S)\\6(?!\\w)")

// The first line of a reply that says something, read as plain words and clipped to `max`, or false when no line does.
func ReplyLine(reply string, max int) (string, bool) {
	for raw := range strings.SplitSeq(reply, "\n") {
		if line := plainLine(raw); line != "" {
			if threadsUTF16Len(line) > max {
				return threadsUTF16Slice(line, 0, max-1) + "…", true
			}
			return line, true
		}
	}
	return "", false
}

// A markdown line as the words it reads as, or nothing when it only shapes the reply.
func plainLine(line string) string {
	trimmed := threadsTrim(line)
	if matched, _ := bare.MatchString(trimmed); matched {
		return ""
	}
	opened, _ := opening.Replace(trimmed, "", 0, 1)
	return threadsTrim(inlineText(opened))
}

// Text with its inline markdown read as words: a code span keeps what it says as written, the rest keep their words.
func inlineText(text string) string {
	replaced, _ := inline.ReplaceFunc(text, func(match regexp2.Match) string {
		group := func(number int) (string, bool) {
			captured := match.GroupByNumber(number)
			if captured == nil || len(captured.Captures) == 0 {
				return "", false
			}
			return captured.String(), true
		}
		if code, ok := group(2); ok {
			return code
		}
		for _, number := range []int{3, 5, 7} {
			if words, ok := group(number); ok {
				return inlineText(words)
			}
		}
		return inlineText("")
	}, -1, -1)
	return replaced
}
