package contracts

import (
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var viewCommandsReadingPoint = Nullable(Object(Shape{{"anchor", Text}, {"depth", Number}}))
var automationFields = Object(Shape{{"schedule", Text}, {"prompt", Text}, {"surfaceWhen", Text}, {"endsWhen", Text}})

// IsFindTarget takes a raw JSON value, or any value that encodes as one.
func IsFindTarget(value any) bool {
	raw, ok := declRaw(value)
	if !ok {
		return false
	}
	record, ok := declRecord(raw)
	if !ok {
		return false
	}
	kind, _ := declString(record["kind"])
	switch kind {
	case "thread":
		return NullableText(record["taskId"])
	case "browser":
		return Text(record["tabId"])
	case "terminal":
		return Text(record["terminalId"])
	case "review":
		return Text(record["owner"])
	case "panel":
		return Text(record["owner"]) && Text(record["panel"])
	default:
		return false
	}
}

var setsPrompt PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && PhoneCommandsIsBlankable(command["prompt"], PhoneCommandsMaxPromptLength)
}

func viewCommandsFindTarget(value jsontext.Value) bool { return IsFindTarget(value) }

var ViewCommands = Declarations{
	{Type: "view.mounted", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.set-prompt", Fields: Shape{{"taskId", OptionalText}, {"prompt", Text}}, At: CommandPlacementLocal, Phone: setsPrompt},
	{Type: "view.reading-point", Fields: Shape{{"taskId", Text}, {"point", viewCommandsReadingPoint}}, At: CommandPlacementLocal},
	{Type: "view.dismiss-action-error", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.dismiss-hidden-tasks", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.dismiss-toast", Fields: Shape{{"id", Number}}, At: CommandPlacementLocal},
	{Type: "view.edit-automation", Fields: Shape{{"taskId", Text}, {"base", automationFields}, {"fields", automationFields}}, At: CommandPlacementLocal},
	{Type: "view.hold-toasts", Fields: Shape{{"held", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.edit-project", Fields: Shape{{"projectId", NullableText}}, At: CommandPlacementLocal},
	{Type: "view.move-worktree", Fields: Shape{{"worktree", Nullable(Boolean)}}, At: CommandPlacementLocal},
	{Type: "view.fork-name", Fields: Shape{{"sourceId", NullableText}, {"worktree", OptionalBoolean}}, At: CommandPlacementLocal},
	{Type: "view.fork-confirm", Fields: Shape{{"title", StartTitle}}, At: CommandPlacementLocal},
	{Type: "view.archive-prompt", Fields: Shape{{"threadId", NullableText}}, At: CommandPlacementLocal},
	{Type: "view.archive-confirm", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.set-section-open", Fields: Shape{{"section", declGuard(domain.IsSidebarSection)}, {"open", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.set-subagent-group", Fields: Shape{{"group", declGuard(domain.IsSubagentGroup)}, {"open", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.set-overview-group", Fields: Shape{{"taskId", Text}, {"group", declGuard(domain.IsOverviewSection)}, {"open", OptionalBoolean}, {"all", OptionalBoolean}}, At: CommandPlacementLocal},
	{Type: "view.set-model-favorite", Fields: Shape{{"engine", declGuard(domain.IsAgentEngine)}, {"model", declGuard(domain.IsModelID)}, {"favorite", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.remember-project", Fields: Shape{{"projectId", Text}}, At: CommandPlacementLocal},
	{Type: "view.set-theme", Fields: Shape{{"theme", Text}}, At: CommandPlacementLocal},
	{Type: "view.set-theme-family", Fields: Shape{{"family", Text}, {"systemDark", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.set-theme-mode", Fields: Shape{{"mode", declGuard(domain.IsThemeMode)}, {"systemDark", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.system-scheme", Fields: Shape{{"dark", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.set-ui-font", Fields: Shape{{"font", Text}}, At: CommandPlacementLocal},
	{Type: "view.set-mono-font", Fields: Shape{{"font", Text}}, At: CommandPlacementLocal},
	{Type: "view.set-reading-size", Fields: Shape{{"size", Number}}, At: CommandPlacementLocal},
	{Type: "view.set-terminal-size", Fields: Shape{{"size", Number}}, At: CommandPlacementLocal},
	{Type: "view.set-activity-project", Fields: Shape{{"projectId", NullableText}}, At: CommandPlacementLocal},
	{Type: "view.set-sidebar-open", Fields: Shape{{"open", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.set-session-panel-open", Fields: Shape{{"open", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.set-zen", Fields: Shape{{"zen", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.set-capture-options", Fields: Shape{{"options", declGuard(domain.IsCaptureOptions)}}, At: CommandPlacementLocal},
	{Type: "view.set-chrome-browser", Fields: Shape{{"enabled", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.set-computer-use", Fields: Shape{{"enabled", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.set-browser-tools", Fields: Shape{{"enabled", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.set-memory-protection", Fields: Shape{{"enabled", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.set-notifications", Fields: Shape{{"enabled", Boolean}}, At: CommandPlacementLocal},
	{Type: "view.inspect-subagent", Fields: Shape{{"taskId", OptionalText}, {"subagentId", Text}}, At: CommandPlacementThread},
	{Type: "view.set-settings-open", Fields: Shape{{"open", Boolean}, {"section", Optional(declGuard(domain.IsSettingsSection))}, {"settingId", OptionalText}}, At: CommandPlacementLocal},
	{Type: "view.set-usage-computer", Fields: Shape{{"computerId", NullableText}}, At: CommandPlacementLocal},
	{Type: "view.close-tab", Fields: Shape{}, At: CommandPlacementThread},
	{Type: "view.new-tab", Fields: Shape{}, At: CommandPlacementThread},
	{Type: "view.set-dock-open", Fields: Shape{{"open", Boolean}}, At: CommandPlacementThread},
	{Type: "view.set-dock-expanded", Fields: Shape{{"expanded", Boolean}}, At: CommandPlacementThread},
	{Type: "view.open-dock-panel", Fields: Shape{{"panel", Text}, {"taskId", OptionalText}}, At: CommandPlacementThread},
	{Type: "view.close-dock-panel", Fields: Shape{{"panel", Text}}, At: CommandPlacementThread},
	{Type: "view.close-thread-tab", Fields: Shape{{"taskId", Text}}, At: CommandPlacementThread},
	{Type: "view.open-workflow", Fields: Shape{{"workflowId", Text}}, At: CommandPlacementThread},
	{Type: "view.select-dock-tab", Fields: Shape{{"tab", Text}}, At: CommandPlacementThread},
	{Type: "view.select-dock-index", Fields: Shape{{"index", Number}}, At: CommandPlacementThread},
	{Type: "view.set-menu", Fields: Shape{{"menu", NullableText}}, At: CommandPlacementLocal},
	{Type: "view.go-back", Fields: Shape{}, At: CommandPlacementLocal, Leaves: true},
	{Type: "view.go-forward", Fields: Shape{}, At: CommandPlacementLocal, Leaves: true},
	{Type: "view.set-focused", Fields: Shape{{"focused", Boolean}}, At: CommandPlacementOwn},
	{Type: "view.focus-composer", Fields: Shape{{"recover", OptionalBoolean}}, At: CommandPlacementLocal},
	{Type: "view.shortcut", Fields: Shape{{"action", Text}, {"surface", Literals("any", "browser", "desktop")}}, At: CommandPlacementThread},
	{Type: "view.escape", Fields: Shape{}, At: CommandPlacementThread},
	{Type: "view.set-shortcut", Fields: Shape{{"action", Text}, {"binding", NullableText}}, At: CommandPlacementLocal},
	{Type: "view.reset-shortcuts", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.capture-shortcut", Fields: Shape{{"action", NullableText}}, At: CommandPlacementLocal},
	{Type: "view.dismiss-computer-use-setup", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.refresh-environment", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.dock-keys", Fields: Shape{{"tab", NullableText}}, At: CommandPlacementThread},
	{Type: "view.find-open", Fields: Shape{{"target", Optional(viewCommandsFindTarget)}}, At: CommandPlacementLocal},
	{Type: "view.find-query", Fields: Shape{{"query", Text}}, At: CommandPlacementLocal},
	{Type: "view.find-step", Fields: Shape{{"delta", Step}}, At: CommandPlacementLocal},
	{Type: "view.find-close", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.jump-open", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.jump-query", Fields: Shape{{"query", Text}}, At: CommandPlacementLocal},
	{Type: "view.jump-step", Fields: Shape{{"delta", Step}}, At: CommandPlacementLocal},
	{Type: "view.jump-choose", Fields: Shape{{"taskId", Text}}, At: CommandPlacementSelect, Leaves: true},
	{Type: "view.jump-choose-setting", Fields: Shape{{"section", declGuard(domain.IsSettingsSection)}, {"settingId", OptionalText}}, At: CommandPlacementLocal},
	{Type: "view.jump-close", Fields: Shape{}, At: CommandPlacementLocal},
}
