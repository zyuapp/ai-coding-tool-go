package domain

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// hunkHeader's tail stops at the line terminators JavaScript's `.` refuses.
var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@([^\n\r\x{2028}\x{2029}]*)$`)

// ParseFilePatch reads the patch for one file, as `git diff` writes it. Only the first file's headers are read.
func ParseFilePatch(patch string, fallbackPath string) *DiffFile {
	lines := strings.Split(patch, "\n")
	hunks := []*DiffHunk{}
	path := fallbackPath
	previousPath := ""
	var current *DiffHunk
	hunkIndex := -1
	var oldLine, newLine float64
	for _, line := range lines {
		if strings.HasPrefix(line, "--- ") && line != "--- /dev/null" {
			named := strings.TrimPrefix(line[4:], "a/")
			if named != path {
				previousPath = named
			}
			continue
		}
		if strings.HasPrefix(line, "+++ ") && line != "+++ /dev/null" {
			path = strings.TrimPrefix(line[4:], "b/")
			continue
		}
		if header := hunkHeader.FindStringSubmatch(line); header != nil {
			oldLine = diffNumber(header[1])
			newLine = diffNumber(header[3])
			hunkIndex = len(hunks)
			// A range without a count is one line long, which is how Git writes `@@ -1 +1 @@`.
			oldLines, newLines := 1.0, 1.0
			if header[2] != "" {
				oldLines = diffNumber(header[2])
			}
			if header[4] != "" {
				newLines = diffNumber(header[4])
			}
			current = &DiffHunk{Header: featuresJSTrim(header[5]), OldStart: oldLine, OldLines: oldLines, NewStart: newLine, NewLines: newLines, Rows: []*DiffRowContext{}}
			hunks = append(hunks, current)
			continue
		}
		if current == nil {
			continue
		}
		// A patch's last line is empty after the split, and "\ No newline" annotates rather than adds.
		if line == "" || line[0] == '\\' {
			continue
		}
		index := strconv.Itoa(hunkIndex)
		text := line[1:]
		switch line[0] {
		case '+':
			at := newLine
			current.Rows = append(current.Rows, &DiffRowContext{Kind: DiffLineKindAdd, Key: index + ":n" + featuresJSNumber(at), Text: text, NewLine: &at})
			newLine++
		case '-':
			at := oldLine
			current.Rows = append(current.Rows, &DiffRowContext{Kind: DiffLineKindDelete, Key: index + ":o" + featuresJSNumber(at), Text: text, OldLine: &at})
			oldLine++
		case ' ':
			before, after := oldLine, newLine
			current.Rows = append(current.Rows, &DiffRowContext{Kind: DiffLineKindContext, Key: index + ":c" + featuresJSNumber(before) + ":" + featuresJSNumber(after), Text: text, OldLine: &before, NewLine: &after})
			oldLine++
			newLine++
		}
	}
	file := &DiffFile{Path: path, Hunks: hunks}
	if previousPath != "" && previousPath != path {
		file.PreviousPath = &previousPath
	}
	return file
}

// DiffRows is the flat row list the panel draws, hunk headers included. Line rows are DiffRowContext values.
func DiffRows(file *DiffFile) []DiffRow {
	rows := []DiffRow{}
	for index, hunk := range file.Hunks {
		rows = append(rows, DiffRowHunk{Key: "h" + strconv.Itoa(index), Text: hunkLabel(hunk)})
		for _, row := range hunk.Rows {
			rows = append(rows, *row)
		}
	}
	return rows
}

// hunkLabel is the header Git itself writes, counts and all.
func hunkLabel(hunk *DiffHunk) string {
	label := "@@ -" + featuresJSNumber(hunk.OldStart) + "," + featuresJSNumber(hunk.OldLines) + " +" + featuresJSNumber(hunk.NewStart) + "," + featuresJSNumber(hunk.NewLines) + " @@"
	if hunk.Header != "" {
		return label + " " + hunk.Header
	}
	return label
}

// HunkText is one side of a hunk as the text it was cut from.
func HunkText(hunk *DiffHunk, side DiffSide) string {
	lines := []string{}
	for _, row := range hunk.Rows {
		if onSide(row, side) {
			lines = append(lines, row.Text)
		}
	}
	return strings.Join(lines, "\n")
}

// HunkTextIndex is where each of a hunk's rows lands in HunkText, by row key.
func HunkTextIndex(hunk *DiffHunk, side DiffSide) map[string]int {
	index := make(map[string]int, len(hunk.Rows))
	line := 0
	for _, row := range hunk.Rows {
		if !onSide(row, side) {
			continue
		}
		index[row.Key] = line
		line++
	}
	return index
}

var languages = map[string]string{
	"ts":       "typescript",
	"tsx":      "tsx",
	"mts":      "typescript",
	"cts":      "typescript",
	"js":       "javascript",
	"jsx":      "jsx",
	"mjs":      "javascript",
	"cjs":      "javascript",
	"json":     "json",
	"css":      "css",
	"html":     "html",
	"md":       "markdown",
	"markdown": "markdown",
	"sh":       "shellscript",
	"bash":     "shellscript",
	"zsh":      "shellscript",
	"py":       "python",
	"rs":       "rust",
	"go":       "go",
	"yml":      "yaml",
	"yaml":     "yaml",
	"toml":     "toml",
	"sql":      "sql",
}

// LanguageForPath is the grammar a path asks for; false for one nothing here can read.
func LanguageForPath(path string) (string, bool) {
	name := path[strings.LastIndex(path, "/")+1:]
	extension := strings.ToLower(name[strings.LastIndex(name, ".")+1:])
	language, ok := languages[extension]
	return language, ok
}

// LineOn is the line number a row carries on a side; false for a hunk row or a line not on that side.
func LineOn(row DiffRow, side DiffSide) (float64, bool) {
	line, ok := diffLine(row)
	if !ok {
		return 0, false
	}
	at := line.NewLine
	if side == DiffSideOld {
		at = line.OldLine
	}
	if at == nil {
		return 0, false
	}
	return *at, true
}

// CommentQuote is how a selected range reaches the agent: the file and the lines it names, then the lines themselves.
func CommentQuote(path string, rows []DiffRow, side DiffSide) string {
	var lines []float64
	body := []string{}
	for _, row := range rows {
		line, ok := diffLine(row)
		if !ok {
			continue
		}
		if at, ok := LineOn(row, side); ok {
			lines = append(lines, at)
		}
		marker := " "
		switch line.Kind {
		case DiffLineKindAdd:
			marker = "+"
		case DiffLineKindDelete:
			marker = "-"
		}
		body = append(body, marker+line.Text)
	}
	location := path
	if len(lines) > 0 {
		first, last := lines[0], lines[len(lines)-1]
		if first == last {
			location += ":L" + featuresJSNumber(first)
		} else {
			location += ":L" + featuresJSNumber(first) + "-L" + featuresJSNumber(last)
		}
	}
	return strings.Join(append([]string{location}, body...), "\n")
}

// FileFingerprint is a file's counts, used to notice that a file marked viewed has changed since.
func FileFingerprint(file *DiffFileSummary) string {
	return string(file.Status) + ":" + featuresJSNumber(file.Additions) + ":" + featuresJSNumber(file.Deletions)
}

// DrawnLineBudget is how many changed lines a review draws before it folds the rest away.
const DrawnLineBudget = 200_000

// DrawnFileLimit is the most one file may take of that budget.
const DrawnFileLimit = 20_000

// FoldedForSize is the paths of the files too large to draw.
func FoldedForSize(files []*DiffFileSummary) jsonx.Set[string] {
	folded := jsonx.Set[string]{}
	drawn := 0.0
	for _, file := range files {
		lines := file.Additions + file.Deletions
		if lines > DrawnFileLimit || drawn+lines > DrawnLineBudget {
			folded[file.Path] = struct{}{}
		} else {
			drawn += lines
		}
	}
	return folded
}

// ModeForRange is a DiffMode or "commit" (application.DiffStateMode's values).
func ModeForRange(diffRange DiffRange) string {
	switch diffRange.(type) {
	case DiffRangeBranches:
		return string(DiffModeBranch)
	case DiffRangeCommit:
		return "commit"
	default:
		return string(DiffModeUncommitted)
	}
}

var DefaultBranchRange = DiffRangeBranches{Base: "HEAD", Compare: nil}

var Uncommitted DiffRange = DiffRangeUncommitted{}

func RangeKey(diffRange DiffRange) string {
	switch diffRange := diffRange.(type) {
	case DiffRangeCommit:
		return "commit:" + diffRange.Commit
	case DiffRangeBranches:
		return "branches:" + diffRange.Base + ":" + featuresDeref(diffRange.Compare)
	case *DiffRangeCommit:
		return "commit:" + diffRange.Commit
	case *DiffRangeBranches:
		return "branches:" + diffRange.Base + ":" + featuresDeref(diffRange.Compare)
	}
	return "uncommitted"
}

// IsDiffRange checks a JSON-decoded value (map[string]any, …).
func IsDiffRange(value any) bool {
	candidate, ok := value.(map[string]any)
	if !ok {
		return false
	}
	switch candidate["kind"] {
	case "uncommitted":
		return true
	case "commit":
		commit, ok := candidate["commit"].(string)
		return ok && commitHash.MatchString(commit)
	case "branches":
		base, ok := candidate["base"].(string)
		if !ok || base == "" {
			return false
		}
		compare, present := candidate["compare"]
		if !present {
			return false
		}
		_, isString := compare.(string)
		return compare == nil || isString
	}
	return false
}

// SplitRows is the same rows in two columns. Rows keep their parsed keys.
func SplitRows(file *DiffFile) []SplitRow {
	rows := []SplitRow{}
	for index, hunk := range file.Hunks {
		rows = append(rows, DiffRowHunk{Key: "h" + strconv.Itoa(index), Text: hunkLabel(hunk)})
		cursor := 0
		for cursor < len(hunk.Rows) {
			source := hunk.Rows[cursor]
			if source.Kind == DiffLineKindContext {
				rows = append(rows, DiffPair{Key: source.Key, Left: *source, Right: *source})
				cursor++
				continue
			}
			var deletions, additions []*DiffRowContext
			for cursor < len(hunk.Rows) && hunk.Rows[cursor].Kind == DiffLineKindDelete {
				deletions = append(deletions, hunk.Rows[cursor])
				cursor++
			}
			for cursor < len(hunk.Rows) && hunk.Rows[cursor].Kind == DiffLineKindAdd {
				additions = append(additions, hunk.Rows[cursor])
				cursor++
			}
			for offset := range max(len(deletions), len(additions)) {
				pair := DiffPair{}
				leftKey, rightKey := "", ""
				if offset < len(deletions) {
					pair.Left = *deletions[offset]
					leftKey = deletions[offset].Key
				}
				if offset < len(additions) {
					pair.Right = *additions[offset]
					rightKey = additions[offset].Key
				}
				pair.Key = leftKey + "|" + rightKey
				rows = append(rows, pair)
			}
		}
	}
	return rows
}

var commitHash = regexp.MustCompile(`(?i)^[a-f\d]{7,40}$`)

func diffNumber(digits string) float64 {
	number, _ := strconv.ParseFloat(digits, 64)
	return number
}

// onSide reports whether a row's text belongs to one side of the hunk.
func onSide(row *DiffRowContext, side DiffSide) bool {
	if side == DiffSideOld {
		return row.Kind != DiffLineKindAdd
	}
	return row.Kind != DiffLineKindDelete
}

// diffLine is the line a row draws; false for a hunk header.
func diffLine(row DiffRow) (*DiffRowContext, bool) {
	switch row := row.(type) {
	case *DiffRowContext:
		return row, row != nil
	case DiffRowContext:
		return &row, true
	}
	return nil, false
}
