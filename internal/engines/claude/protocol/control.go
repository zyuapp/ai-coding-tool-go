package protocol

import (
	"encoding/json/jsontext"
)

// controlRequest is a request either side sends over the control channel.
type controlRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Request   any    `json:"request"`
}

// incomingControlRequest is a control request Claude Code sent.
type incomingControlRequest struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id"`
	Request   jsontext.Value `json:"request"`
}

// controlResponse answers a control request.
type controlResponse struct {
	Type     string              `json:"type"`
	Response controlResponseBody `json:"response"`
}

type controlResponseBody struct {
	Subtype   string         `json:"subtype"`
	RequestID string         `json:"request_id"`
	Response  jsontext.Value `json:"response,omitzero"`
	Error     *string        `json:"error,omitzero"`
	ErrorCode *string        `json:"error_code,omitzero"`
}

type controlCancelRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

// ControlError is a control request Claude Code answered with an error.
type ControlError struct {
	Subtype string
	Message string
	// Code is the machine-readable code the error came with, if any.
	Code string
}

func (e *ControlError) Error() string { return e.Message }

// HookMatcherRegistration is one matcher of the initialize request's hooks.
type HookMatcherRegistration struct {
	Matcher         *string  `json:"matcher,omitzero"`
	HookCallbackIDs []string `json:"hookCallbackIds"`
	Timeout         *float64 `json:"timeout,omitzero"`
}

// InitializeRequest is the first control request a process is sent.
type InitializeRequest struct {
	Subtype               string                               `json:"subtype"`
	Hooks                 map[string][]HookMatcherRegistration `json:"hooks,omitzero"`
	SdkMcpServers         []string                             `json:"sdkMcpServers,omitzero"`
	SdkMcpServerManifests map[string]McpManifest               `json:"sdkMcpServerManifests,omitzero"`
	JSONSchema            jsontext.Value                       `json:"jsonSchema,omitzero"`
	SystemPrompt          []string                             `json:"systemPrompt,omitzero"`
	AppendSystemPrompt    *string                              `json:"appendSystemPrompt,omitzero"`
	ExcludeDynamic        *bool                                `json:"excludeDynamicSections,omitzero"`
	Skills                []string                             `json:"skills,omitzero"`
	ForwardSubagentText   *bool                                `json:"forwardSubagentText,omitzero"`
}

// InitializeResponse is what Claude Code answers initialize with.
type InitializeResponse struct {
	Commands              []SlashCommand `json:"commands"`
	Agents                []AgentInfo    `json:"agents"`
	OutputStyle           string         `json:"output_style"`
	AvailableOutputStyles []string       `json:"available_output_styles"`
	Models                []ModelInfo    `json:"models"`
	Account               AccountInfo    `json:"account"`
	HooksApplied          *bool          `json:"hooks_applied,omitzero"`
	PluginsApplied        *bool          `json:"plugins_applied,omitzero"`
	ClaudeCodeVersion     *string        `json:"claude_code_version,omitzero"`
}

// AgentInfo is one subagent the session offers.
type AgentInfo struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Model       *string `json:"model,omitzero"`
}

// ModelInfo is one model the installed Claude Code runs.
type ModelInfo struct {
	Value                    string   `json:"value"`
	ResolvedModel            *string  `json:"resolvedModel,omitzero"`
	DisplayName              string   `json:"displayName"`
	Description              string   `json:"description"`
	SupportsEffort           *bool    `json:"supportsEffort,omitzero"`
	SupportedEffortLevels    []string `json:"supportedEffortLevels,omitzero"`
	SupportsAdaptiveThinking *bool    `json:"supportsAdaptiveThinking,omitzero"`
	SupportsFastMode         *bool    `json:"supportsFastMode,omitzero"`
	SupportsAutoMode         *bool    `json:"supportsAutoMode,omitzero"`
}

// AccountInfo is the account the session runs under.
type AccountInfo struct {
	Email            *string `json:"email,omitzero"`
	Organization     *string `json:"organization,omitzero"`
	SubscriptionType *string `json:"subscriptionType,omitzero"`
	TokenSource      *string `json:"tokenSource,omitzero"`
	APIKeySource     *string `json:"apiKeySource,omitzero"`
	APIProvider      *string `json:"apiProvider,omitzero"`
}

// PermissionRequest is a can_use_tool control request: Claude Code asking whether a tool may run.
type PermissionRequest struct {
	Subtype                 string         `json:"subtype"`
	ToolName                string         `json:"tool_name"`
	Input                   jsontext.Value `json:"input"`
	PermissionSuggestions   jsontext.Value `json:"permission_suggestions,omitzero"`
	BlockedPath             *string        `json:"blocked_path,omitzero"`
	DecisionReason          *string        `json:"decision_reason,omitzero"`
	DecisionReasonType      *string        `json:"decision_reason_type,omitzero"`
	Title                   *string        `json:"title,omitzero"`
	DisplayName             *string        `json:"display_name,omitzero"`
	Description             *string        `json:"description,omitzero"`
	DefaultToNo             *bool          `json:"default_to_no,omitzero"`
	SuppressAlwaysAllowRule *bool          `json:"suppress_always_allow_rule,omitzero"`
	ToolUseID               string         `json:"tool_use_id"`
	AgentID                 *string        `json:"agent_id,omitzero"`
	RequiresUserInteraction *bool          `json:"requires_user_interaction,omitzero"`
	McpServer               jsontext.Value `json:"mcp_server,omitzero"`
	// RequestID is the control request's id.
	RequestID string `json:"-"`
}

// Permission behaviours.
const (
	BehaviorAllow = "allow"
	BehaviorDeny  = "deny"
)

// PermissionResult answers a PermissionRequest. The tool call's id is added when it is sent.
type PermissionResult struct {
	Behavior           string         `json:"behavior"`
	UpdatedInput       jsontext.Value `json:"updatedInput,omitzero"`
	UpdatedPermissions jsontext.Value `json:"updatedPermissions,omitzero"`
	Message            *string        `json:"message,omitzero"`
	Interrupt          *bool          `json:"interrupt,omitzero"`
	ToolUseID          string         `json:"toolUseID,omitzero"`
}

// Allow lets a tool call run with the input given.
func Allow(input jsontext.Value) *PermissionResult {
	return &PermissionResult{Behavior: BehaviorAllow, UpdatedInput: input}
}

// Deny refuses a tool call, telling the model why.
func Deny(message string) *PermissionResult {
	return &PermissionResult{Behavior: BehaviorDeny, Message: &message}
}

// HookEffort is the effort a hook input reports the agent running at.
type HookEffort struct {
	Level string `json:"level"`
}

// HookInput is what a hook callback is called with. Fields an event does not have stay zero; the
// whole input is in Raw.
type HookInput struct {
	HookEventName  string         `json:"hook_event_name"`
	SessionID      string         `json:"session_id"`
	TranscriptPath string         `json:"transcript_path"`
	Cwd            string         `json:"cwd"`
	PermissionMode *string        `json:"permission_mode,omitzero"`
	AgentID        *string        `json:"agent_id,omitzero"`
	AgentType      *string        `json:"agent_type,omitzero"`
	Effort         *HookEffort    `json:"effort,omitzero"`
	ToolName       *string        `json:"tool_name,omitzero"`
	ToolInput      jsontext.Value `json:"tool_input,omitzero"`
	ToolUseID      *string        `json:"tool_use_id,omitzero"`
	Raw            jsontext.Value `json:"-"`
}

type hookCallbackRequest struct {
	CallbackID string         `json:"callback_id"`
	Input      jsontext.Value `json:"input"`
	ToolUseID  *string        `json:"tool_use_id,omitzero"`
}

type mcpMessageRequest struct {
	ServerName string         `json:"server_name"`
	Message    jsontext.Value `json:"message"`
}

type mcpMessageResponse struct {
	McpResponse jsontext.Value `json:"mcp_response"`
}

type subtypeOnly struct {
	Subtype string `json:"subtype"`
}

// The requests the app sends.

type interruptRequest struct {
	Subtype string `json:"subtype"`
}

type setPermissionModeRequest struct {
	Subtype string `json:"subtype"`
	Mode    string `json:"mode"`
}

type setModelRequest struct {
	Subtype string  `json:"subtype"`
	Model   *string `json:"model,omitzero"`
}

type applyFlagSettingsRequest struct {
	Subtype  string `json:"subtype"`
	Settings any    `json:"settings"`
}

type stopTaskRequest struct {
	Subtype string `json:"subtype"`
	TaskID  string `json:"task_id"`
}

// Permission modes, as --permission-mode and set_permission_mode name them.
const (
	PermissionModeDefault           = "default"
	PermissionModeAcceptEdits       = "acceptEdits"
	PermissionModeBypassPermissions = "bypassPermissions"
	PermissionModePlan              = "plan"
	PermissionModeDontAsk           = "dontAsk"
	PermissionModeAuto              = "auto"
)
