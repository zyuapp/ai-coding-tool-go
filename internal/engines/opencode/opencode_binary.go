// Package opencode runs OpenCode sessions, each on a private `opencode serve` of its own, and answers
// what the app asks of OpenCode outside a run. Ports src/main/opencode/*.
package opencode

import (
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

var Binary = agent.EngineCommand{
	Command:     "opencode",
	PackageName: "@opencode/cli",
	Install:     "curl -fsSL https://opencode.ai/v2/install | bash",
	IsNative:    func(realPath string) bool { return strings.Contains(realPath, "/.opencode/bin/") },
}
