package agent

import (
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// What a run may do, and where it is being read: together they decide what runs without asking.
type ApprovalScope struct {
	Policy  domain.ExecutionPolicy
	Channel contracts.RunChannel
}

// What a tool reaches: only the app's own bridges, the desktop the person works at, or the workspace.
type ToolReach string

const (
	ReachApp         ToolReach = "app"
	ReachComputerUse ToolReach = "computer-use"
	ReachWorkspace   ToolReach = "workspace"
)

const computerUseToolPrefix = "mcp__cua-driver__"

// What a tool reaches, read from its mcp__server__tool name. Every engine grants app tools by this.
func ToolReachOf(toolName string) ToolReach {
	if isAppTool(toolName) {
		return ReachApp
	}
	if strings.HasPrefix(toolName, computerUseToolPrefix) {
		return ReachComputerUse
	}
	return ReachWorkspace
}

// Whether a tool runs without anybody approving it. A call with no run in scope (nil) has nobody to
// grant anything. Computer use is granted by an autonomous run on the main channel, where the person
// is watching the same desktop it drives.
func GrantsTool(reach ToolReach, scope *ApprovalScope) bool {
	if reach == ReachApp {
		return true
	}
	if scope == nil {
		return false
	}
	if scope.Policy == domain.ExecutionPolicyBypass {
		return true
	}
	return reach == ReachComputerUse && scope.Channel == contracts.RunChannelMain && scope.Policy == domain.ExecutionPolicyAutonomous
}
