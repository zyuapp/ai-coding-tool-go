package codex

import (
	"errors"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

var Binary = agent.EngineCommand{
	Command:     "codex",
	PackageName: "@openai/codex",
	Install:     "brew install --cask codex",
	IsNative:    func(realPath string) bool { return strings.Contains(realPath, "/packages/standalone/") },
}

// Finds the installed command; replaced in tests.
var engineBinaryPath = agent.EngineBinaryPath

// The Codex the user installed. The app carries no copy of its own, so a machine without the command
// cannot run Codex; readiness reports that before a run is ever started.
func CodexExecutable() (string, error) {
	executable, ok := engineBinaryPath(Binary)
	if !ok {
		return "", errors.New("Codex is not installed.")
	}
	return executable, nil
}
