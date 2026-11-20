package domain

import (
	"regexp"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// toolCallNaming is how one engine names its tools: which name is which family, and which argument keys name a call.
type toolCallNaming struct {
	families map[string]ToolFamily
	// namingKeys are argument keys in the order they identify a call, so the first one present is the one to show.
	namingKeys []string
}

var naming = map[AgentEngine]toolCallNaming{
	AgentEngineClaude: {
		families: map[string]ToolFamily{
			"Bash": ToolFamilyShell, "BashOutput": ToolFamilyShell, "KillShell": ToolFamilyShell,
			"Read": ToolFamilyRead, "NotebookRead": ToolFamilyRead,
			"Write": ToolFamilyWrite, "Edit": ToolFamilyWrite, "MultiEdit": ToolFamilyWrite, "NotebookEdit": ToolFamilyWrite,
			"Grep": ToolFamilySearch, "Glob": ToolFamilySearch, "ToolSearch": ToolFamilySearch,
			"WebFetch": ToolFamilyWeb, "WebSearch": ToolFamilyWeb,
			"Agent": ToolFamilyAgent, "Task": ToolFamilyAgent, "Skill": ToolFamilyAgent, "Workflow": ToolFamilyAgent, "SendMessage": ToolFamilyAgent,
		},
		namingKeys: []string{"command", "file_path", "notebook_path", "pattern", "query", "url", "path", "description", "prompt", "skill", "name"},
	},
	// Codex reports item kinds rather than tool names; its session emits them in snake case.
	AgentEngineCodex: {
		families: map[string]ToolFamily{
			"command_execution": ToolFamilyShell,
			"file_change":       ToolFamilyWrite,
			"web_search":        ToolFamilyWeb,
			"mcp_tool_call":     ToolFamilyOther,
		},
		namingKeys: []string{"command", "path", "query"},
	},
	AgentEngineOpencode: {
		families: map[string]ToolFamily{
			"shell": ToolFamilyShell,
			"read":  ToolFamilyRead,
			"write": ToolFamilyWrite, "edit": ToolFamilyWrite, "apply_patch": ToolFamilyWrite, "patch": ToolFamilyWrite, "multiedit": ToolFamilyWrite,
			"glob": ToolFamilySearch, "grep": ToolFamilySearch, "list": ToolFamilySearch,
			"webfetch": ToolFamilyWeb, "websearch": ToolFamilyWeb,
			"subagent": ToolFamilyAgent, "task": ToolFamilyAgent, "skill": ToolFamilyAgent,
		},
		namingKeys: []string{"command", "filePath", "pattern", "url", "query", "path", "description", "prompt", "name"},
	},
}

// genericNaming reads a thread on an engine only a newer build on a paired computer knows.
var genericNaming = toolCallNaming{families: map[string]ToolFamily{}, namingKeys: []string{"command", "file_path", "path", "pattern", "url", "query"}}

func namingOf(engine EngineID) toolCallNaming {
	if IsAgentEngine(engine) {
		return naming[AgentEngine(engine)]
	}
	return genericNaming
}

// argumentLimit is longer than any row can show, and short enough that a heredoc cannot bloat the transcript.
const argumentLimit = 240

// toolCallInput is a tool call's JSON-decoded input object.
type toolCallInput = map[string]any

// toolCallOwnTool is how one of the app's own tools reads: a verb, and the input that says what it acted on.
type toolCallOwnTool struct {
	family   ToolFamily
	label    string
	argument func(input toolCallInput) (string, bool)
	thread   bool
}

// toolCallText reads a string input that is not blank.
func toolCallText(key string) func(input toolCallInput) (string, bool) {
	return func(input toolCallInput) (string, bool) {
		value, ok := input[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return "", false
		}
		return value, true
	}
}

// ownTools carry the same names whichever engine calls them.
var ownTools = map[string]toolCallOwnTool{
	"list_threads":    {family: ToolFamilyThread, label: "List threads", argument: toolCallText("search")},
	"read_thread":     {family: ToolFamilyThread, label: "Read thread", thread: true},
	"wait_for_thread": {family: ToolFamilyThread, label: "Wait for thread", thread: true},
	"start_thread": {family: ToolFamilyThread, label: "Start thread", argument: func(input toolCallInput) (string, bool) {
		if title, ok := toolCallText("title")(input); ok {
			return title, true
		}
		return toolCallText("prompt")(input)
	}},
	"message_thread": {family: ToolFamilyThread, label: "Message thread", thread: true, argument: toolCallText("text")},
	"set_thread_role": {family: ToolFamilyThread, label: "Set role", thread: true, argument: func(input toolCallInput) (string, bool) {
		if role, ok := toolCallText("role")(input); ok {
			return role, true
		}
		return "No role", true
	}},
	"dismiss_thread": {family: ToolFamilyThread, label: "Dismiss thread", thread: true},
	"archive_thread": {family: ToolFamilyThread, label: "Archive thread", thread: true},
	"stop_thread":    {family: ToolFamilyThread, label: "Stop thread", thread: true},
	"move_to_worktree": {family: ToolFamilyThread, label: "Move to worktree", thread: true, argument: func(input toolCallInput) (string, bool) {
		if input["worktreeId"] == "new" {
			return "New worktree", true
		}
		if worktree, ok := toolCallText("worktreeId")(input); ok {
			return worktree, true
		}
		return "Project checkout", true
	}},
	"delete_worktree": {family: ToolFamilyThread, label: "Delete worktree", argument: toolCallText("worktreeId")},
	"report_status": {family: ToolFamilyThread, label: "Report status", argument: func(input toolCallInput) (string, bool) {
		parts := []string{}
		for _, key := range []string{"state", "summary"} {
			if part, ok := toolCallText(key)(input); ok {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, " · "), true
	}},
	"raise_decision": {family: ToolFamilyThread, label: "Ask for a decision", argument: toolCallText("question")},
	"remember":       {family: ToolFamilyThread, label: "Remember", argument: toolCallText("text")},
	"forget":         {family: ToolFamilyThread, label: "Forget", argument: toolCallText("noteId")},
	"set_purpose":    {family: ToolFamilyThread, label: "Set purpose", argument: toolCallText("purpose")},
	"propose_threads": {family: ToolFamilyThread, label: "Propose threads", argument: func(input toolCallInput) (string, bool) {
		threads, ok := input["threads"].([]any)
		if !ok {
			return "", false
		}
		titles := []string{}
		for _, thread := range threads {
			if record, ok := thread.(map[string]any); ok {
				if title, ok := toolCallText("title")(record); ok {
					titles = append(titles, title)
				}
			}
		}
		return strings.Join(titles, ", "), true
	}},
	"notify":             {family: ToolFamilyAutomation, label: "Notify", argument: toolCallText("headline")},
	"nothing_to_report":  {family: ToolFamilyAutomation, label: "Nothing to report", argument: toolCallText("checked")},
	"schedule":           {family: ToolFamilyAutomation, label: "Schedule", argument: toolCallText("schedule")},
	"status":             {family: ToolFamilyAutomation, label: "Automation status"},
	"update":             {family: ToolFamilyAutomation, label: "Update automation"},
	"stop":               {family: ToolFamilyAutomation, label: "Stop automation"},
	"list_all":           {family: ToolFamilyAutomation, label: "List automations"},
	"terminal_list":      {family: ToolFamilyRead, label: "List terminals"},
	"terminal_read":      {family: ToolFamilyRead, label: "Read terminal", argument: toolCallText("match")},
	"browser_open":       {family: ToolFamilyWeb, label: "Open page", argument: toolCallText("url")},
	"browser_search":     {family: ToolFamilyWeb, label: "Search", argument: toolCallText("query")},
	"browser_read":       {family: ToolFamilyWeb, label: "Read page"},
	"browser_screenshot": {family: ToolFamilyWeb, label: "Screenshot"},
	"browser_console":    {family: ToolFamilyWeb, label: "Console"},
	"browser_network":    {family: ToolFamilyWeb, label: "Network"},
	"browser_wait":       {family: ToolFamilyWeb, label: "Wait", argument: toolCallText("value")},
	"browser_click":      {family: ToolFamilyWeb, label: "Click"},
	"browser_type":       {family: ToolFamilyWeb, label: "Type", argument: toolCallText("text")},
	"browser_back":       {family: ToolFamilyWeb, label: "Back"},
	"browser_tabs":       {family: ToolFamilyWeb, label: "List tabs"},
	"browser_close_tab":  {family: ToolFamilyWeb, label: "Close tab"},
	"simulator_open":     {family: ToolFamilyOther, label: "Open simulator", argument: toolCallText("device")},
	"simulator_read":     {family: ToolFamilyRead, label: "Read simulator"},
	"simulator_tap":      {family: ToolFamilyOther, label: "Tap", argument: toolCallText("ref")},
	"simulator_swipe":    {family: ToolFamilyOther, label: "Swipe"},
	"simulator_type":     {family: ToolFamilyOther, label: "Type", argument: toolCallText("text")},
	"simulator_button":   {family: ToolFamilyOther, label: "Press button", argument: toolCallText("button")},
	"simulator_rotate":   {family: ToolFamilyOther, label: "Rotate", argument: toolCallText("orientation")},
	"request_setup":      {family: ToolFamilyOther, label: "Set up computer use"},
}

var mcpName = regexp.MustCompile(`^mcp__(.+?)__(.+)$`)
var ownServer = regexp.MustCompile(`^aicodingtool(-|$)`)

// ownTool is the app's own tool a name refers to, prefixed by its server or bare.
func ownTool(name string) (toolCallOwnTool, bool) {
	bare := name
	if mcp := mcpName.FindStringSubmatch(name); mcp != nil {
		if !ownServer.MatchString(mcp[1]) {
			return toolCallOwnTool{}, false
		}
		bare = mcp[2]
	}
	own, ok := ownTools[bare]
	return own, ok
}

// ToolLabel is what a person reads a tool as.
func ToolLabel(name string) string {
	if own, ok := ownTool(name); ok {
		return own.label
	}
	if mcp := mcpName.FindStringSubmatch(name); mcp != nil {
		return mcp[1] + " · " + strings.ReplaceAll(mcp[2], "_", " ")
	}
	return name
}

func ToolCallToolFamily(engine EngineID, name string) ToolFamily {
	if family, ok := namingOf(engine).families[name]; ok {
		return family
	}
	if own, ok := ownTool(name); ok {
		return own.family
	}
	if strings.HasPrefix(name, "browser_") {
		return ToolFamilyWeb
	}
	return ToolFamilyOther
}

// shortPath drops path segments beyond the last two.
func shortPath(value string) string {
	segments := strings.FieldsFunc(value, func(r rune) bool { return r == '/' })
	if !strings.HasPrefix(value, "/") || len(segments) <= 2 {
		return value
	}
	return "…/" + strings.Join(segments[len(segments)-2:], "/")
}

func oneLine(value string) string {
	flat := featuresJSTrim(strings.Join(strings.FieldsFunc(value, featuresIsJSSpace), " "))
	if featuresUTF16Len(flat) > argumentLimit {
		return featuresUTF16Prefix(flat, argumentLimit-1) + "…"
	}
	return flat
}

// namingValue is the first named key the input carries, else its only string value; false for nothing.
func namingValue(namingKeys []string, input toolCallInput) (key string, value string, ok bool) {
	for _, key := range namingKeys {
		if value, ok := input[key].(string); ok {
			if trimmed := featuresJSTrim(value); trimmed != "" {
				return key, trimmed, true
			}
		}
	}
	found := 0
	for name, value := range input {
		if text, ok := value.(string); ok && featuresJSTrim(text) != "" {
			found++
			key, value = name, featuresJSTrim(text)
		}
	}
	if found != 1 {
		return "", "", false
	}
	return key, value, true
}

// DescribeToolCall reads a call from its tool name and the JSON text it was given (detail nil or empty for none).
func DescribeToolCall(engine EngineID, name string, detail *string) ToolCall {
	input := parseInput(detail)
	if own, ok := ownTool(name); ok {
		if input == nil {
			input = toolCallInput{}
		}
		return describeOwnCall(own, input)
	}
	family := ToolCallToolFamily(engine, name)
	label := ToolLabel(name)
	if input == nil {
		return ToolCall{Family: family, Label: label}
	}
	key, value, ok := namingValue(namingOf(engine).namingKeys, input)
	if !ok {
		return ToolCall{Family: family, Label: label}
	}
	if strings.HasSuffix(strings.ToLower(key), "path") {
		value = shortPath(value)
	}
	argument := oneLine(value)
	// A search says what it looked for and where, which two rows of the same pattern cannot.
	if path, ok := input["path"].(string); ok && family == ToolFamilySearch && featuresJSTrim(path) != "" {
		argument += " in " + shortPath(featuresJSTrim(path))
	}
	call := ToolCall{Family: family, Label: label, Argument: argument}
	if family == ToolFamilyShell {
		sigil := "$"
		call.Sigil = &sigil
	}
	return call
}

func describeOwnCall(own toolCallOwnTool, input toolCallInput) ToolCall {
	verb := true
	call := ToolCall{Family: own.family, Label: own.label, Verb: &verb}
	if own.argument != nil {
		if argument, ok := own.argument(input); ok && argument != "" {
			call.Argument = oneLine(argument)
		}
	}
	if own.thread {
		if thread, ok := toolCallText("threadId")(input); ok {
			if thread = featuresJSTrim(thread); thread != "" {
				call.Thread = &thread
			}
		}
	}
	return call
}

// parseInput is nil unless detail is JSON text of an object.
func parseInput(detail *string) toolCallInput {
	if detail == nil || *detail == "" {
		return nil
	}
	var parsed any
	if err := jsonx.Unmarshal([]byte(*detail), &parsed); err != nil {
		return nil
	}
	input, _ := parsed.(map[string]any)
	return input
}

// featuresUTF16Prefix is text.slice(0, units), backing off a surrogate pair it would split.
func featuresUTF16Prefix(text string, units int) string {
	count := 0
	for index, r := range text {
		width := 1
		if r >= 0x10000 {
			width = 2
		}
		if count+width > units {
			return text[:index]
		}
		count += width
	}
	return text
}
