package terminal

import (
	"math"
	"unicode/utf8"
)

// Local mirrors of src/contracts/terminal.ts and src/domain/terminal.ts, to be replaced by the
// generated contracts and domain types.

// Status is TerminalSession["status"].
type Status string

const (
	StatusRunning Status = "running"
	StatusExited  Status = "exited"
)

// Update is TerminalUpdate: what the host reports about a session.
type Update struct {
	TerminalID string   `json:"terminalId"`
	Title      *string  `json:"title,omitzero"`
	Status     *Status  `json:"status,omitzero"`
	ExitCode   *float64 `json:"exitCode,omitzero"`
	Error      *string  `json:"error,omitzero"`
}

// StartOptions is TerminalStartOptions.
type StartOptions struct {
	Cwd string `json:"cwd"`
}

// ReadOptions is TerminalReadOptions.
type ReadOptions struct {
	Lines float64 `json:"lines"`
	Match *string `json:"match,omitzero"`
}

// Text is TerminalText: a terminal's lines with no escape sequences left.
type Text struct {
	Lines   []string `json:"lines"`
	Omitted float64  `json:"omitted"`
	Matched *float64 `json:"matched,omitzero"`
}

// Size is TerminalDataEvent["size"].
type Size struct {
	Cols float64 `json:"cols"`
	Rows float64 `json:"rows"`
}

// DataEvent is TerminalDataEvent, the payload of the window's terminal:data event.
type DataEvent struct {
	TerminalID string  `json:"terminalId"`
	Data       string  `json:"data"`
	Sequence   float64 `json:"sequence"`
	Size       *Size   `json:"size,omitzero"`
}

// ScreenSnapshot is TerminalScreenSnapshot.
type ScreenSnapshot struct {
	Data     string  `json:"data"`
	Cols     float64 `json:"cols"`
	Rows     float64 `json:"rows"`
	Sequence float64 `json:"sequence"`
}

// ReadKind is TerminalOutputRead["kind"].
type ReadKind string

const (
	ReadSnapshot ReadKind = "snapshot"
	ReadOutput   ReadKind = "output"
)

// OutputRead is TerminalOutputRead.
type OutputRead struct {
	ScreenSnapshot `json:",inline"`
	Kind           ReadKind `json:"kind"`
}

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
