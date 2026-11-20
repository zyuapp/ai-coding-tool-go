package terminal

import (
	"math"
	"unicode/utf8"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The contracts and domain shapes the host reports in.
type (
	Status         = domain.TerminalUpdateStatus
	Update         = domain.TerminalUpdate
	StartOptions   = contracts.TerminalStartOptions
	ReadOptions    = contracts.TerminalReadOptions
	Text           = contracts.TerminalText
	Size           = contracts.TerminalDataEventSize
	DataEvent      = contracts.TerminalDataEvent
	ScreenSnapshot = contracts.TerminalScreenSnapshot
	ReadKind       = contracts.TerminalOutputReadKind
	OutputRead     = contracts.TerminalOutputRead
)

const (
	StatusRunning = domain.TerminalUpdateStatusRunning
	StatusExited  = domain.TerminalUpdateStatusExited
	ReadSnapshot  = contracts.TerminalOutputReadKindSnapshot
	ReadOutput    = contracts.TerminalOutputReadKindOutput
)

const (
	// MaxInput is MAX_TERMINAL_INPUT, in UTF-16 code units.
	MaxInput = 64 * 1024
	// MaxDimension is MAX_TERMINAL_DIMENSION.
	MaxDimension = 1_000

	defaultTerminalLines = 100
	maxTerminalLines     = 2_000
	maxTerminalBytes     = 100_000
)

// IsDimension is isTerminalDimension.
func IsDimension(value float64) bool {
	return math.Trunc(value) == value && value >= 1 && value <= MaxDimension
}

// lineLimit is terminalLineLimit from src/domain/terminal.ts.
func lineLimit(lines *float64) int {
	if lines == nil {
		return defaultTerminalLines
	}
	value := *lines
	if value != value {
		return 1
	}
	if value >= maxTerminalLines {
		return maxTerminalLines
	}
	if value < 1 {
		return 1
	}
	return int(value)
}

// title is terminalTitle from src/domain/terminal.ts.
func title(cwd string) string {
	last := ""
	start := 0
	for i := 0; i <= len(cwd); i++ {
		if i == len(cwd) || cwd[i] == '/' {
			if i > start {
				last = cwd[start:i]
			}
			start = i + 1
		}
	}
	if last == "" {
		return cwd
	}
	return last
}

// withinReadBudget is withinReadBudget from src/domain/terminal.ts.
func withinReadBudget(lines []string) []string {
	bytes := 0
	for index := len(lines) - 1; index >= 0; index-- {
		bytes += utf16Len(lines[index]) + 1
		if bytes > maxTerminalBytes {
			return lines[index+1:]
		}
	}
	return lines
}

// utf16Len is a JavaScript string's length.
func utf16Len(text string) int {
	length := 0
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		if r > 0xFFFF {
			length += 2
		} else {
			length++
		}
	}
	return length
}
