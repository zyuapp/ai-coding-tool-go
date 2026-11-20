package claude

import (
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

var claudeBinary = agent.EngineCommand{
	Command:     "claude",
	PackageName: "@anthropic-ai/claude-code",
	Install:     "curl -fsSL https://claude.ai/install.sh | bash",
	IsNative: func(realPath string) bool {
		return strings.HasSuffix(realPath, "/.local/bin/claude") || strings.Contains(realPath, "/.local/share/claude/")
	},
}
