package contracts

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The guards in this package take a decoded JSON value (map[string]any, []any, float64, string, bool or nil)
// and report whether it holds the named shape; callers decode it into the typed value afterwards.

const (
	// IpcMaxIDLength is the longest id a request names a thread, a tab, a terminal or a worktree by.
	IpcMaxIDLength = 256
	// MaxURLLength is the longest address a run may open in the browser panel.
	MaxURLLength = 8_192
	// MaxSearchText is the longest text a read looks for: a thread search, a terminal match, or what a browser wait watches for.
	MaxSearchText = 1_000
	// MaxThreadWaitMs bounds a wait, which holds a tool call open.
	MaxThreadWaitMs = 15 * 60 * 1_000
	// MaxBrowserWaitMs bounds how long a page read waits for the tab to settle.
	MaxBrowserWaitMs   = 2 * 60 * 1_000
	IpcMaxPromptLength = 1_000_000
	ipcMaxTitleLength  = 64
)

// ipcLength is a string's length in UTF-16 code units, as JavaScript counts it.
func ipcLength(text string) int {
	length := 0
	for _, r := range text {
		if r >= 0x10000 {
			length += 2
		} else {
			length++
		}
	}
	return length
}

// IpcIsString reports a non-empty string of at most maxLength UTF-16 units; maxLength defaults to IpcMaxIDLength.
func IpcIsString(value any, maxLength ...int) bool {
	limit := IpcMaxIDLength
	if len(maxLength) > 0 {
		limit = maxLength[0]
	}
	text, ok := value.(string)
	return ok && text != "" && ipcLength(text) <= limit
}

func IpcIsBlankable(value any, maxLength int) bool {
	text, ok := value.(string)
	return ok && ipcLength(text) <= maxLength
}

func IpcIsCount(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsInf(number, 0) && !math.IsNaN(number) && number >= 0
}

func isRunChannel(value any) bool {
	return value == "main" || value == "side"
}

// ipcRecord is value as an object. An array is refused, which no guard here could tell from an object
// lacking every field it requires.
func ipcRecord(value any) (map[string]any, bool) {
	record, ok := value.(map[string]any)
	return record, ok && record != nil
}

// ipcHas is JavaScript's key !== undefined on a decoded object: a key holding null is present.
func ipcHas(record map[string]any, key string) bool {
	_, ok := record[key]
	return ok
}

func ipcFinite(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsInf(number, 0) && !math.IsNaN(number)
}

// ipcSafeInteger is Number.isSafeInteger.
func ipcSafeInteger(value any) bool {
	number, ok := value.(float64)
	return ok && number == math.Trunc(number) && math.Abs(number) <= 1<<53-1
}

// ipcInteger is Number.isInteger.
func ipcInteger(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsInf(number, 0) && number == math.Trunc(number)
}

func ipcNumber(value any) float64 {
	number, _ := value.(float64)
	return number
}

// ipcJSString is JavaScript's String(value) for a decoded JSON value; nil is null.
func ipcJSString(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return ipcJSNumber(v)
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item != nil {
				parts[i] = ipcJSString(item)
			}
		}
		return strings.Join(parts, ",")
	case map[string]any:
		return "[object Object]"
	case ipcUndefinedValue:
		return "undefined"
	}
	return fmt.Sprint(value)
}

// ipcJSNumber is how JavaScript writes a number.
func ipcJSNumber(number float64) string {
	switch {
	case math.IsNaN(number):
		return "NaN"
	case math.IsInf(number, 1):
		return "Infinity"
	case math.IsInf(number, -1):
		return "-Infinity"
	case number == 0:
		return "0"
	}
	if magnitude := math.Abs(number); magnitude >= 1e-6 && magnitude < 1e21 {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	text := strconv.FormatFloat(number, 'e', -1, 64)
	mantissa, exponent, _ := strings.Cut(text, "e")
	sign := exponent[0]
	exponent = strings.TrimLeft(exponent[1:], "0")
	return mantissa + "e" + string(sign) + exponent
}

// ipcUndefinedValue stands for a property an object does not have, where null (nil) is a value.
type ipcUndefinedValue struct{}

var ipcUndefined any = ipcUndefinedValue{}

func isContinuation(value any) bool {
	continuation, ok := ipcRecord(value)
	return ok && IpcIsString(continuation["provider"]) && IpcIsString(continuation["value"], 100_000)
}

func isBackgroundProcessKind(value any) bool {
	return value == "shell" || value == "monitor"
}

func isBackgroundProcess(value any) bool {
	process, ok := ipcRecord(value)
	return ok && IpcIsString(process["id"]) && isBackgroundProcessKind(process["kind"]) && IpcIsString(process["description"], 100_000)
}

func isWorkflowPhase(value any) bool {
	phase, ok := ipcRecord(value)
	return ok && IpcIsCount(phase["index"]) && IpcIsString(phase["title"], 100_000)
}

func isWorkflowAgentState(value any) bool {
	return value == "queued" || value == "running" || value == "done" || value == "error"
}

var (
	workflowAgentStrings = []string{"phaseTitle", "agentId", "agentType", "model", "lastAttemptReason", "lastToolName", "lastToolSummary", "promptPreview", "resultPreview", "error"}
	workflowAgentCounts  = []string{"phaseIndex", "attempt", "queuedAt", "startedAt", "durationMs", "lastProgressAt", "tokens", "toolCalls"}
)

func isWorkflowAgent(value any) bool {
	agent, ok := ipcRecord(value)
	if !ok {
		return false
	}
	_, cached := agent["cached"].(bool)
	return IpcIsCount(agent["index"]) &&
		IpcIsString(agent["label"], 100_000) &&
		isWorkflowAgentState(agent["state"]) &&
		(!ipcHas(agent, "isolation") || agent["isolation"] == "worktree" || agent["isolation"] == "remote") &&
		(!ipcHas(agent, "cached") || cached) &&
		!slices.ContainsFunc(workflowAgentStrings, func(key string) bool { return ipcHas(agent, key) && !IpcIsString(agent[key], 100_000) }) &&
		!slices.ContainsFunc(workflowAgentCounts, func(key string) bool { return ipcHas(agent, key) && !IpcIsCount(agent[key]) })
}

func isIntent(value any) bool {
	intent, ok := ipcRecord(value)
	if !ok {
		return false
	}
	_, path := intent["writePath"].(string)
	return IpcIsString(intent["toolId"]) && IpcIsString(intent["name"]) && (!ipcHas(intent, "writePath") || path)
}

func isComputerUseRunConfig(value any) bool {
	config, ok := ipcRecord(value)
	if !ok {
		return false
	}
	if config["status"] == "setup-required" {
		return true
	}
	if config["status"] == "unavailable" {
		return IpcIsString(config["message"], 10_000)
	}
	mcp, ok := ipcRecord(config["mcp"])
	if config["status"] != "available" || !ok {
		return false
	}
	args, ok := mcp["args"].([]any)
	if !IpcIsString(mcp["command"], 4_096) || !ok || slices.ContainsFunc(args, ipcNotString) {
		return false
	}
	env, ok := ipcRecord(mcp["env"])
	if !ok {
		return false
	}
	for _, item := range env {
		if ipcNotString(item) {
			return false
		}
	}
	return true
}

func ipcNotString(value any) bool {
	_, ok := value.(string)
	return !ok
}

func isCoordinationRole(command map[string]any) bool {
	return !ipcHas(command, "coordinationRole") || command["coordinationRole"] == "coordinator" || command["coordinationRole"] == "member"
}

func IsRunCommand(value any) bool {
	command, ok := ipcRecord(value)
	if !ok {
		return false
	}
	switch command["type"] {
	case "start":
		return isStartCommand(command, false)
	case "memory-protection":
		_, ok := command["enabled"].(bool)
		return ok
	case "reload-settings":
		return true
	case "cancel":
		return IpcIsString(command["taskId"]) && IpcIsString(command["runId"])
	case "answer-question":
		text, _ := command["text"].(string)
		return IpcIsString(command["taskId"]) && IpcIsString(command["runId"]) && IpcIsString(command["requestId"]) && IpcIsString(command["questionId"]) && IpcIsString(text, IpcMaxPromptLength) && !declBlank(text)
	case "approval":
		_, allow := command["allow"].(bool)
		return IpcIsString(command["taskId"]) && IpcIsString(command["runId"]) && IpcIsString(command["approvalId"]) && allow
	case "steer":
		return IpcIsString(command["taskId"]) && IpcIsString(command["runId"]) && IpcIsString(command["messageId"]) && IpcIsString(command["prompt"], IpcMaxPromptLength)
	case "stop-process":
		return IpcIsString(command["taskId"]) && IpcIsString(command["processId"])
	case "label":
		return IpcIsString(command["taskId"]) && IpcIsString(command["title"], ipcMaxTitleLength)
	case "archive":
		_, archived := command["archived"].(bool)
		return IpcIsString(command["taskId"]) && isRunChannel(command["channel"]) && (!ipcHas(command, "continuation") || isContinuation(command["continuation"])) && archived
	case "role":
		return IpcIsString(command["taskId"]) && isCoordinationRole(command)
	}
	return false
}

func IsInternalRunCommand(value any) bool {
	if command, ok := ipcRecord(value); ok && command["type"] == "start" {
		return isStartCommand(command, true)
	}
	return IsRunCommand(value)
}

var externalStartRefuses = []string{"workspaceRoot", "projectless", "computerUse", "projectRoot", "cwd", "folder", "sessionId", "mode", "requestId"}

func isStartCommand(command map[string]any, internal bool) bool {
	if !IpcIsString(command["title"], ipcMaxTitleLength) {
		return false
	}
	operation, _ := ipcRecord(command["operation"])
	engine, _ := command["engine"].(string)
	continuation, _ := ipcRecord(command["continuation"])
	// An operation is work the engine performs itself; whether the thread's model offers it is the reducer's to decide.
	compact := operation["type"] == "compact" &&
		ipcFinite(operation["preTokens"]) && ipcNumber(operation["preTokens"]) >= 0 &&
		command["prompt"] == "" && domain.IsAgentEngine(command["engine"]) && domain.IsModelID(command["model"]) &&
		isContinuation(command["continuation"]) && continuation["provider"] == command["engine"] &&
		!ipcHas(command, "forkContinuation")
	review := operation["type"] == "review" &&
		domain.IsReviewTarget(operation["target"]) &&
		command["channel"] == "main" && command["prompt"] == "" && domain.IsAgentEngine(command["engine"]) && domain.CapabilitiesFor(engine).Review &&
		isContinuation(command["continuation"]) && continuation["provider"] == command["engine"] &&
		(!ipcHas(command, "forkContinuation") || command["forkContinuation"] == true)
	operationOnly := compact || review
	_, memoryProtection := command["memoryProtection"].(bool)
	prompt := IpcIsString(command["prompt"], IpcMaxPromptLength)
	if operationOnly {
		prompt = IpcIsBlankable(command["prompt"], IpcMaxPromptLength)
	}
	base := (!ipcHas(command, "memoryProtection") || memoryProtection) &&
		isRunChannel(command["channel"]) &&
		IpcIsString(command["taskId"]) &&
		IpcIsString(command["runId"]) &&
		prompt &&
		IpcIsString(command["workspaceId"]) &&
		domain.IsExecutionPolicy(command["policy"]) &&
		domain.IsAgentEngine(command["engine"]) &&
		domain.IsModelID(command["model"]) &&
		domain.IsAgentEffort(command["effort"]) &&
		(!ipcHas(command, "operation") || operationOnly) &&
		(!ipcHas(command, "chromeBrowser") || (command["chromeBrowser"] == true && domain.CapabilitiesFor(engine).ChromeBrowser)) &&
		(!ipcHas(command, "computerUseTools") || command["computerUseTools"] == false) &&
		(!ipcHas(command, "browserTools") || command["browserTools"] == false) &&
		(!ipcHas(command, "simulatorTools") || command["simulatorTools"] == false) &&
		(!ipcHas(command, "continuation") || isContinuation(command["continuation"])) &&
		(!ipcHas(command, "forkContinuation") || (command["forkContinuation"] == true && isContinuation(command["continuation"]))) &&
		(!ipcHas(command, "unattended") || command["unattended"] == true) &&
		isCoordinationRole(command) &&
		(!ipcHas(command, "projectWorkspaceId") || (command["coordinationRole"] == "member" && IpcIsString(command["projectWorkspaceId"])))
	if !base {
		return false
	}
	if ipcHas(command, "fastMode") {
		_, fastMode := command["fastMode"].(bool)
		if !(domain.IsAgentEngine(command["engine"]) && domain.CapabilitiesFor(engine).FastMode && fastMode) {
			return false
		}
	}
	if !internal {
		return !slices.ContainsFunc(externalStartRefuses, func(key string) bool { return ipcHas(command, key) })
	}
	_, projectless := command["projectless"].(bool)
	return IpcIsString(command["workspaceRoot"], 4_096) && projectless && isComputerUseRunConfig(command["computerUse"]) &&
		(!ipcHas(command, "projectRoot") || IpcIsString(command["projectRoot"], 4_096))
}

func IsAutomationRequest(value any) bool {
	request, ok := ipcRecord(value)
	if !ok || request["type"] != "automation.request" || !IpcIsString(request["requestId"]) || !IpcIsString(request["taskId"]) {
		return false
	}
	switch request["op"] {
	case "read", "list", "delete":
		return true
	case "save":
		if !IpcIsString(request["runId"]) {
			return false
		}
		draft, _ := ipcRecord(request["draft"])
		spread := make(map[string]any, len(draft)+1)
		for key, item := range draft {
			spread[key] = item
		}
		spread["taskId"] = request["taskId"]
		return domain.IsAutomationDraft(spread)
	case "update":
		return IpcIsString(request["runId"]) && domain.IsAutomationPatch(request["patch"])
	}
	return false
}

func IsBrowserAction(value any) bool {
	action, ok := ipcRecord(value)
	if !ok {
		return false
	}
	switch action["kind"] {
	case "click":
		return IpcIsString(action["ref"])
	case "type":
		text, isText := action["text"].(string)
		_, submit := action["submit"].(bool)
		return IpcIsString(action["ref"]) && isText && ipcLength(text) <= IpcMaxPromptLength &&
			(!ipcHas(action, "submit") || submit)
	}
	return false
}

func IsBrowserBounds(value any) bool {
	box, ok := ipcRecord(value)
	return ok && ipcFinite(box["x"]) && ipcFinite(box["y"]) && ipcFinite(box["width"]) && ipcFinite(box["height"])
}

var (
	browserConsoleLevels  = []string{"debug", "info", "warning", "error"}
	browserWaitConditions = []string{"text", "text-gone", "element", "element-gone", "url", "network-idle"}
)

func IsBrowserRead(value any) bool {
	read, ok := ipcRecord(value)
	if !ok {
		return false
	}
	if read["op"] == "tabs" {
		return true
	}
	tabbed := !ipcHas(read, "tabId") || IpcIsString(read["tabId"])
	limited := !ipcHas(read, "limit") || IpcIsCount(read["limit"]) && ipcNumber(read["limit"]) <= 200
	since := !ipcHas(read, "since") || IpcIsCount(read["since"])
	waited := IpcIsCount(read["timeoutMs"]) && ipcNumber(read["timeoutMs"]) <= MaxBrowserWaitMs
	switch read["op"] {
	case "console":
		return tabbed && since && limited &&
			(!ipcHas(read, "minimumLevel") || slices.Contains(browserConsoleLevels, ipcJSString(read["minimumLevel"])))
	case "network":
		_, failuresOnly := read["failuresOnly"].(bool)
		return tabbed && since && limited && (!ipcHas(read, "failuresOnly") || failuresOnly)
	case "wait":
		condition := ipcUndefinedString(read, "condition")
		needsValue := condition != "network-idle"
		return tabbed &&
			slices.Contains(browserWaitConditions, condition) &&
			(!needsValue && !ipcHas(read, "value") || needsValue && IpcIsString(read["value"], MaxSearchText)) &&
			waited
	case "screenshot":
		_, fullPage := read["fullPage"].(bool)
		return tabbed && waited && (!ipcHas(read, "fullPage") || fullPage)
	case "snapshot":
		return tabbed && waited && (!ipcHas(read, "textLimit") || IpcIsCount(read["textLimit"]))
	}
	return false
}

// ipcUndefinedString is String(record[key]), which writes an absent key as "undefined".
func ipcUndefinedString(record map[string]any, key string) string {
	if !ipcHas(record, key) {
		return ipcJSString(ipcUndefined)
	}
	return ipcJSString(record[key])
}

// MaxSimulatorWaitMs is the longest a run waits on a device to boot, which a first boot of a fresh device can take.
const MaxSimulatorWaitMs = 300_000

func IsSimulatorRead(value any) bool {
	read, ok := ipcRecord(value)
	if !ok {
		return false
	}
	if read["op"] == "status" {
		return IpcIsCount(read["timeoutMs"]) && ipcNumber(read["timeoutMs"]) <= MaxSimulatorWaitMs
	}
	_, screenshot := read["screenshot"].(bool)
	return read["op"] == "snapshot" && screenshot
}

func IsTerminalRead(value any) bool {
	read, ok := ipcRecord(value)
	if !ok {
		return false
	}
	if read["op"] == "terminals" {
		return true
	}
	if read["op"] != "snapshot" {
		return false
	}
	return (!ipcHas(read, "terminalId") || IpcIsString(read["terminalId"])) &&
		(!ipcHas(read, "lines") || IpcIsCount(read["lines"])) &&
		(!ipcHas(read, "match") || IpcIsString(read["match"], MaxSearchText))
}

const unreadableRefusal = "AI Coding Tool could not read that request: one of its fields is not what the request allows."

// UnreadableRequest is what a request no guard could read is answered with: a ThreadResponseFalse,
// an AutomationResponseFalse, or nil when value names neither. The caller is a tool call held open, so a
// message that is dropped reaches it as a timeout saying nothing about what was wrong with it.
func UnreadableRequest(value any) any {
	message, ok := ipcRecord(value)
	if !ok || !IpcIsString(message["requestId"]) {
		return nil
	}
	requestID := message["requestId"].(string)
	switch message["type"] {
	case "thread.request":
		return ThreadResponseFalse{RequestID: requestID, Message: unreadableRefusal}
	case "automation.request":
		return AutomationResponseFalse{RequestID: requestID, Message: unreadableRefusal}
	}
	return nil
}

func isResponse(value any, typ string) bool {
	response, ok := ipcRecord(value)
	if !ok || response["type"] != typ || !IpcIsString(response["requestId"]) {
		return false
	}
	return response["ok"] == true || (response["ok"] == false && IpcIsString(response["message"], 100_000))
}

func IsThreadResponse(value any) bool {
	return isResponse(value, "thread.response")
}

func IsAutomationResponse(value any) bool {
	return isResponse(value, "automation.response")
}

func IsAutomationAck(value any) bool {
	ack, ok := ipcRecord(value)
	_, started := ack["started"].(bool)
	return ok && IpcIsString(ack["automationId"]) && IpcIsString(ack["runId"]) && started
}

// More than any one model response could spend, so a larger count is a mistake rather than spend.
const maxSpentTokens = 1_000_000_000

func IsRunEvent(value any) bool {
	event, ok := ipcRecord(value)
	if !ok || !IpcIsString(event["taskId"]) || !IpcIsString(event["runId"]) || !ipcSafeInteger(event["sequence"]) || ipcNumber(event["sequence"]) < 1 {
		return false
	}
	switch event["type"] {
	case "question.requested":
		return IpcIsString(event["requestId"]) && domain.IsQuestionRequest(event["request"])
	case "question.answered":
		return IpcIsString(event["requestId"]) && IpcIsString(event["questionId"]) && IpcIsString(event["text"], IpcMaxPromptLength)
	case "question.closed":
		return IpcIsString(event["requestId"])
	case "run.started":
		return !ipcHas(event, "agentInitiated") || event["agentInitiated"] == true
	case "run.status":
		status := event["status"]
		return (status == "running" || status == "awaiting-approval" || status == "succeeded" || status == "failed" || status == "cancelled") &&
			(!ipcHas(event, "message") || IpcIsString(event["message"], 100_000)) &&
			(!ipcHas(event, "limit") || status == "failed" && domain.IsUsageLimit(event["limit"]))
	case "assistant.delta":
		_, text := event["text"].(string)
		return IpcIsString(event["messageId"]) && text &&
			(!ipcHas(event, "append") || event["append"] == true) &&
			(!ipcHas(event, "artifact") || event["artifact"] == true)
	case "assistant.tail":
		text, ok := event["text"].(string)
		return IpcIsString(event["messageId"]) && ok && ipcLength(text) <= IpcMaxPromptLength
	case "context.usage":
		return ipcFinite(event["tokens"]) && ipcNumber(event["tokens"]) >= 0 &&
			ipcFinite(event["limit"]) && ipcNumber(event["limit"]) > 0 && IpcIsString(event["model"])
	case "context.spent":
		return ipcSafeInteger(event["tokens"]) && ipcNumber(event["tokens"]) >= 1 && ipcNumber(event["tokens"]) <= maxSpentTokens
	case "run.retrying":
		return IpcIsString(event["message"], 100_000) &&
			(!ipcHas(event, "attempt") || IpcIsCount(event["attempt"])) &&
			(!ipcHas(event, "maxRetries") || IpcIsCount(event["maxRetries"]))
	case "context.compaction-status":
		_, compacting := event["compacting"].(bool)
		return compacting && (!ipcHas(event, "error") || IpcIsString(event["error"], 100_000))
	case "context.compacted":
		return (event["trigger"] == "manual" || event["trigger"] == "auto") &&
			ipcFinite(event["preTokens"]) && ipcNumber(event["preTokens"]) >= 0 &&
			(!ipcHas(event, "postTokens") || ipcFinite(event["postTokens"]) && ipcNumber(event["postTokens"]) >= 0)
	case "tool.intent":
		return isIntent(event["intent"])
	case "computer-use.setup-required":
		return true
	case "approval.requested":
		return IpcIsString(event["approvalId"]) && isIntent(event["intent"]) && IpcIsString(event["title"]) && IpcIsString(event["description"], 100_000)
	case "continuation.updated":
		return isContinuation(event["continuation"])
	case "continuation.lost":
		return true
	case "queued.delivered":
		return IpcIsString(event["messageId"])
	case "queued.steer-failed":
		return IpcIsString(event["messageId"]) && IpcIsString(event["message"], 100_000)
	}
	return false
}

func IsBackgroundEvent(value any) bool {
	event, ok := ipcRecord(value)
	if !ok || !IpcIsString(event["taskId"]) || event["type"] != "background.changed" {
		return false
	}
	processes, ok := event["processes"].([]any)
	return ok && !slices.ContainsFunc(processes, func(process any) bool { return !isBackgroundProcess(process) })
}

func IsSubagentEvent(value any) bool {
	event, ok := ipcRecord(value)
	if !ok || !IpcIsString(event["taskId"]) || !IpcIsString(event["id"]) {
		return false
	}
	typ := event["type"]
	if typ == "subagent.started" || typ == "subagent.metadata" {
		_, prompt := event["prompt"].(string)
		if !(!ipcHas(event, "model") || IpcIsString(event["model"])) ||
			!(!ipcHas(event, "effort") || IpcIsString(event["effort"])) ||
			!(!ipcHas(event, "prompt") || prompt) {
			return false
		}
		if typ == "subagent.metadata" {
			return true
		}
	}
	switch typ {
	case "subagent.started":
		return IpcIsString(event["description"], 100_000) &&
			(!ipcHas(event, "agentType") || IpcIsString(event["agentType"])) &&
			(!ipcHas(event, "sessionScoped") || event["sessionScoped"] == true)
	case "subagent.status":
		return (event["status"] == "working" || event["status"] == "idle") &&
			(!ipcHas(event, "summary") || IpcIsString(event["summary"], 100_000))
	case "subagent.progress":
		return IpcIsString(event["description"], 100_000) &&
			(!ipcHas(event, "agentType") || IpcIsString(event["agentType"])) &&
			(!ipcHas(event, "lastToolName") || IpcIsString(event["lastToolName"])) &&
			(!ipcHas(event, "summary") || IpcIsString(event["summary"], 100_000)) &&
			IpcIsCount(event["totalTokens"])
	case "subagent.activity":
		_, text := event["text"].(string)
		return IpcIsString(event["activityId"]) &&
			(event["kind"] == "text" || event["kind"] == "tool") &&
			(!ipcHas(event, "title") || IpcIsString(event["title"])) &&
			text
	case "subagent.finished":
		_, summary := event["summary"].(string)
		return (event["status"] == "completed" || event["status"] == "failed" || event["status"] == "stopped") && summary
	}
	return false
}

func IsGoalEvent(value any) bool {
	event, ok := ipcRecord(value)
	if !ok || !IpcIsString(event["taskId"]) || event["type"] != "goal.changed" {
		return false
	}
	if ipcHas(event, "goal") && event["goal"] == nil {
		return true
	}
	goal, ok := ipcRecord(event["goal"])
	return ok && IpcIsString(goal["objective"], 100_000) &&
		(goal["status"] == "active" || goal["status"] == "blocked") &&
		(!ipcHas(goal, "iterations") || IpcIsCount(goal["iterations"])) &&
		(!ipcHas(goal, "lastReason") || IpcIsString(goal["lastReason"], 100_000))
}

func IsWorkflowEvent(value any) bool {
	event, ok := ipcRecord(value)
	if !ok || !IpcIsString(event["taskId"]) || !IpcIsString(event["id"]) {
		return false
	}
	switch event["type"] {
	case "workflow.started":
		return IpcIsString(event["name"], 100_000) && IpcIsString(event["description"], 100_000)
	case "workflow.progress":
		phases, ok := event["phases"].([]any)
		if !ok || slices.ContainsFunc(phases, func(phase any) bool { return !isWorkflowPhase(phase) }) {
			return false
		}
		agents, ok := event["agents"].([]any)
		return ok && !slices.ContainsFunc(agents, func(agent any) bool { return !isWorkflowAgent(agent) }) &&
			IpcIsCount(event["totalTokens"]) && IpcIsCount(event["totalToolCalls"])
	case "workflow.finished":
		_, summary := event["summary"].(string)
		return (event["status"] == "completed" || event["status"] == "failed" || event["status"] == "stopped") && summary
	}
	return false
}

var windowThemeCanvas = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// IsWindowTheme checks a theme arriving from the renderer, which the window trusts no further than its own three fields.
func IsWindowTheme(value any) bool {
	theme, ok := ipcRecord(value)
	if !ok {
		return false
	}
	canvas, isCanvas := theme["canvas"].(string)
	_, follow := theme["follow"].(bool)
	return (theme["variant"] == "dark" || theme["variant"] == "light") &&
		isCanvas && windowThemeCanvas.MatchString(canvas) &&
		(!ipcHas(theme, "follow") || follow)
}
