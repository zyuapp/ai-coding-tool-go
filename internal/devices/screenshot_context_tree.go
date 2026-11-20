package devices

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// jsSpaces is JavaScript's \s, which also trim() and trimStart() take off.
const jsSpaces = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"

// jsSpace is a character class matching one of jsSpaces.
const jsSpace = "[" + jsSpaces + "]"

// ScreenshotTreeRow is one node of a driver's Markdown tree, with whatever lines its text ran on to.
type ScreenshotTreeRow struct {
	Text  string
	Depth int
	Role  string
	// Index is the element's index in the driver's structured list, or -1 for none.
	Index int
}

var (
	treeTruncated = regexp.MustCompile(`(?i)^⚠.*\b(?:AX tree|accessibility tree) truncated at \d+ nodes\b`)
	macTreeRow    = regexp.MustCompile(`^(` + jsSpace + `*)- (?:\[(?:element_index )?(\d+)\] )?(AX[A-Za-z0-9]+)(?:` + jsSpace + `|$)`)
	linuxTreeRow  = regexp.MustCompile(`^(` + jsSpace + `*)- (?:\[(\d+)\] ([a-z][a-z\d -]*?) "|([a-z][a-z\d -]*?) = ")`)
	documentRole  = regexp.MustCompile(`^(?:AXWebArea|AXDocument|AXDocumentWeb|document(?: web| frame| text)?)$`)
	protectedRole = regexp.MustCompile(`(?i)^(?:AXSecureTextField|password(?: text| field)?)$`)
)

// ScreenshotTree reads a driver's Markdown tree. Only native role prefixes introduce nodes;
// paragraphs and list items can span physical lines.
func ScreenshotTree(markdown string, platform domain.ScreenshotPlatform) (rows []*ScreenshotTreeRow, truncated bool) {
	pattern := linuxTreeRow
	if platform == domain.ScreenshotPlatformMacos {
		pattern = macTreeRow
	}
	for line := range strings.SplitSeq(markdown, "\n") {
		if treeTruncated.MatchString(strings.TrimLeft(line, jsSpaces)) {
			truncated = true
			break
		}
		if match := pattern.FindStringSubmatchIndex(line); match != nil {
			group := func(index int) (string, bool) {
				if match[2*index] < 0 {
					return "", false
				}
				return line[match[2*index]:match[2*index+1]], true
			}
			indent, _ := group(1)
			row := &ScreenshotTreeRow{Text: line, Depth: utf16Length(indent), Index: -1}
			if role, ok := group(3); ok {
				row.Role = role
			} else {
				row.Role, _ = group(4)
			}
			if digits, ok := group(2); ok {
				if index, err := strconv.Atoi(digits); err == nil {
					row.Index = index
				}
			}
			rows = append(rows, row)
		} else if trimmed := strings.Trim(line, jsSpaces); len(rows) > 0 && trimmed != "" {
			rows[len(rows)-1].Text += `\n` + trimmed
		}
	}
	return rows, truncated
}

func IsDocumentRole(role string) bool { return documentRole.MatchString(role) }

func IsProtectedRole(role string) bool { return protectedRole.MatchString(role) }
