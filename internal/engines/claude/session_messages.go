package claude

import (
	"encoding/json/jsontext"
	"fmt"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/claude/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// syntheticModel is the model the agent process stamps on replies it produced itself: slash
// commands, interrupts, error notices.
const syntheticModel = "<synthetic>"

// compactCommand is Claude Code's own command for summarizing the session so far. It answers with no model turn.
const compactCommand = "/compact"

// responseUsage is a response's usage as the API reports it, whole on a message or in part on a stream frame.
type responseUsage struct {
	InputTokens              *float64
	CacheCreationInputTokens *float64
	CacheReadInputTokens     *float64
	OutputTokens             *float64
}

func orZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// tokensOf is what a response spent: all it read, cached or not, and all it wrote, as Codex counts its totalTokens.
func tokensOf(usage responseUsage) float64 {
	return orZero(usage.InputTokens) + orZero(usage.CacheCreationInputTokens) + orZero(usage.CacheReadInputTokens) + orZero(usage.OutputTokens)
}

func messageUsage(usage *protocol.Usage) responseUsage {
	if usage == nil {
		return responseUsage{}
	}
	return responseUsage{
		InputTokens:              &usage.InputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
		OutputTokens:             &usage.OutputTokens,
	}
}

// retryReason is why a request is being retried, in the user's terms.
func retryReason(message *protocol.APIRetry) string {
	switch {
	case message.Error == "overloaded":
		return "Claude is overloaded."
	case message.Error == "rate_limit":
		return "Claude is rate limited."
	case message.ErrorStatus == nil:
		return "Cannot reach Claude."
	}
	return fmt.Sprintf("Claude API error %s.", jsNumber(*message.ErrorStatus))
}

// usageLimitOf is the plan limit the account is held at, if the latest report says requests are being turned away.
func usageLimitOf(info *protocol.RateLimitInfo) *domain.UsageLimit {
	if info == nil || info.Status != "rejected" || info.ResetsAt == nil || *info.ResetsAt == 0 {
		return nil
	}
	// Extra usage keeps requests going past the plan's limit.
	if info.OverageStatus != nil && (*info.OverageStatus == "allowed" || *info.OverageStatus == "allowed_warning") {
		return nil
	}
	// Reported in seconds. Only the five-hour window lifts soon enough to resume on its own.
	window := domain.UsageLimitWindowWeekly
	if info.RateLimitType == nil || *info.RateLimitType == "five_hour" {
		window = domain.UsageLimitWindowSession
	}
	return &domain.UsageLimit{ResetsAt: *info.ResetsAt * 1_000, Window: window}
}

// backgroundProcessKinds are the task types that are processes of their own. Subagents and
// workflows have a panel already.
var backgroundProcessKinds = map[string]domain.BackgroundProcessKind{
	"local_bash":  domain.BackgroundProcessKindShell,
	"monitor_mcp": domain.BackgroundProcessKindMonitor,
	"monitor_ws":  domain.BackgroundProcessKindMonitor,
}

func backgroundProcesses(tasks []protocol.BackgroundTask) []*domain.BackgroundProcess {
	processes := []*domain.BackgroundProcess{}
	for _, task := range tasks {
		if kind, ok := backgroundProcessKinds[task.TaskType]; ok {
			processes = append(processes, &domain.BackgroundProcess{ID: task.TaskID, Kind: kind, Description: task.Description})
		}
	}
	return processes
}

// effortFlag is the effort a run asks the SDK to hold, or nil to clear it for a model that takes none.
func effortFlag(model domain.AgentModel, effort domain.AgentEffort) *domain.ClaudeEffort {
	if !domain.ModelTakesEffort(domain.BuiltInModels, domain.AgentEngineClaude, model) {
		return nil
	}
	flag := domain.AgentEngineClaudeEffort(effort)
	return &flag
}

// ClaudePermissionMode is the permission mode a policy runs Claude under.
func ClaudePermissionMode(policy domain.ExecutionPolicy) string {
	switch policy {
	case domain.ExecutionPolicyConfirm:
		return protocol.PermissionModeDefault
	case domain.ExecutionPolicyPlan:
		return protocol.PermissionModePlan
	case domain.ExecutionPolicyAllowEdits:
		return protocol.PermissionModeAcceptEdits
	case domain.ExecutionPolicyAutonomous:
		return protocol.PermissionModeAuto
	case domain.ExecutionPolicyBypass:
		return protocol.PermissionModeBypassPermissions
	}
	return ""
}

// userMessage is what the run says, as the agent process takes it. A message that arrives while the
// agent is already working is held for the next turn unless it says otherwise, so a steered one asks
// to be folded into the turn already going — which is the whole of what steering means.
func userMessage(prompt string, priority string) protocol.UserMessage {
	message := protocol.NewUserMessage(protocol.TextContent(prompt))
	if priority != "" {
		message.Priority = &priority
	}
	return message
}

func inputRecord(input jsontext.Value) map[string]any {
	var record map[string]any
	if len(input) == 0 || jsonx.Unmarshal(input, &record) != nil {
		return nil
	}
	return record
}

func writePathFor(toolName string, input jsontext.Value) *string {
	if toolName != "Write" && toolName != "Edit" && toolName != "NotebookEdit" {
		return nil
	}
	record := inputRecord(input)
	value := record["file_path"]
	if value == nil {
		value = record["notebook_path"]
	}
	if path, ok := value.(string); ok {
		return &path
	}
	return nil
}

// ToolDisplayName names a tool call. Every skill runs through one tool, so the name carries the skill
// that was asked for.
func ToolDisplayName(toolName string, input jsontext.Value) string {
	if toolName != "Skill" {
		return toolName
	}
	if skill, ok := inputRecord(input)["skill"].(string); ok && skill != "" {
		return "Skill: " + skill
	}
	return toolName
}

func normalizeToolIntent(toolName string, input jsontext.Value, toolUseID string) domain.ToolIntent {
	return domain.ToolIntent{ToolID: toolUseID, Name: ToolDisplayName(toolName, input), Input: input, WritePath: writePathFor(toolName, input)}
}
