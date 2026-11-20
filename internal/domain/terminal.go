package domain

import (
	"math"
	"strings"
)

const DefaultTerminalLines = 100
const MaxTerminalLines = 2_000
const MaxTerminalBytes = 100_000

// TerminalLineLimit takes nil for a read that names no count.
func TerminalLineLimit(lines *float64) int {
	if lines == nil {
		return DefaultTerminalLines
	}
	return int(max(1, min(math.Floor(*lines), MaxTerminalLines)))
}

func TerminalTitle(cwd string) string {
	parts := strings.Split(cwd, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return cwd
}

func DescribeTerminal(session *TerminalSession) string {
	state := "running"
	if session.Status != TerminalSessionStatusRunning {
		state = "exited"
		if session.ExitCode != nil {
			state += " (" + jsNumber(*session.ExitCode) + ")"
		}
	}
	return session.Title + " [" + session.ID + "] · " + session.Cwd + " · " + state
}

// WithinReadBudget trims a read to the byte ceiling from the end. Lengths count UTF-16 code units, as JavaScript does.
func WithinReadBudget(lines []string) []string {
	bytes := 0
	for index := len(lines) - 1; index >= 0; index-- {
		bytes += utf16Length(lines[index]) + 1
		if bytes > MaxTerminalBytes {
			return lines[index+1:]
		}
	}
	return lines
}
