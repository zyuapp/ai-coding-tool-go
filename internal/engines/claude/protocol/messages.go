package protocol

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
)

// Message is one line Claude Code prints that is not part of the control protocol.
type Message interface {
	// Raw is the line as it was printed.
	Raw() jsontext.Value
	// Kind is the message's type, and its subtype when it has one ("system/init").
	Kind() string
}

type envelope struct {
	raw jsontext.Value
}

func (e envelope) Raw() jsontext.Value { return e.raw }

// MessageHeader is what every message carries.
type MessageHeader struct {
	Type      string  `json:"type"`
	Subtype   *string `json:"subtype,omitzero"`
	UUID      *string `json:"uuid,omitzero"`
	SessionID *string `json:"session_id,omitzero"`
}

// Usage is a response's usage as the API reports it.
type Usage struct {
	InputTokens              float64  `json:"input_tokens"`
	OutputTokens             float64  `json:"output_tokens"`
	CacheCreationInputTokens *float64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *float64 `json:"cache_read_input_tokens"`
	ServiceTier              *string  `json:"service_tier,omitzero"`
}

// DeltaUsage is the usage a stream's closing message_delta carries; any count may be null.
type DeltaUsage struct {
	InputTokens              *float64 `json:"input_tokens"`
	OutputTokens             *float64 `json:"output_tokens"`
	CacheCreationInputTokens *float64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *float64 `json:"cache_read_input_tokens"`
}

// ContentBlock is one block of a message: text, thinking, tool_use, tool_result, image and the
// rest, flattened. Fields a block's type does not have stay zero.
type ContentBlock struct {
	Type      string         `json:"type"`
	Text      *string        `json:"text,omitzero"`
	ID        *string        `json:"id,omitzero"`
	Name      *string        `json:"name,omitzero"`
	Input     jsontext.Value `json:"input,omitzero"`
	Thinking  *string        `json:"thinking,omitzero"`
	Signature *string        `json:"signature,omitzero"`
	ToolUseID *string        `json:"tool_use_id,omitzero"`
	Content   jsontext.Value `json:"content,omitzero"`
	IsError   *bool          `json:"is_error,omitzero"`
	Source    *ImageSource   `json:"source,omitzero"`
}

// ImageSource is an image block's bytes.
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// TextBlock is a text content block.
func TextBlock(text string) ContentBlock { return ContentBlock{Type: "text", Text: &text} }

// Base64ImageBlock is an image content block carrying its bytes.
func Base64ImageBlock(mediaType, data string) ContentBlock {
	return ContentBlock{Type: "image", Source: &ImageSource{Type: "base64", MediaType: mediaType, Data: data}}
}

// BetaMessage is an assistant response as the Messages API shapes it.
type BetaMessage struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []ContentBlock `json:"content"`
	StopReason   *string        `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        *Usage         `json:"usage"`
}

// MessageContent is a user message's content: a string, or blocks.
type MessageContent struct {
	Text   string
	Blocks []ContentBlock
	// IsBlocks says the content is blocks, even none.
	IsBlocks bool
}

// TextContent is content that is one string.
func TextContent(text string) MessageContent { return MessageContent{Text: text} }

// BlockContent is content made of blocks.
func BlockContent(blocks ...ContentBlock) MessageContent {
	return MessageContent{Blocks: blocks, IsBlocks: true}
}

func (c MessageContent) MarshalJSONTo(enc *jsontext.Encoder) error {
	if !c.IsBlocks {
		return enc.WriteToken(jsontext.String(c.Text))
	}
	blocks := c.Blocks
	if blocks == nil {
		blocks = []ContentBlock{}
	}
	data, err := marshal(blocks)
	if err != nil {
		return err
	}
	return enc.WriteValue(data)
}

func (c *MessageContent) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	value, err := dec.ReadValue()
	if err != nil {
		return err
	}
	switch value.Kind() {
	case '"':
		*c = MessageContent{}
		return unmarshal(value, &c.Text)
	case '[':
		*c = MessageContent{IsBlocks: true}
		return unmarshal(value, &c.Blocks)
	default:
		return fmt.Errorf("message content must be a string or an array, not %s", value.Kind())
	}
}

// MessageParam is a user message as the Messages API takes it.
type MessageParam struct {
	Role    string         `json:"role"`
	Content MessageContent `json:"content"`
}

// AssistantMessage is a model response, or a synthetic one the process made itself.
type AssistantMessage struct {
	envelope        `json:"-"`
	Type            string      `json:"type"`
	Message         BetaMessage `json:"message"`
	ParentToolUseID *string     `json:"parent_tool_use_id"`
	Error           *string     `json:"error,omitzero"`
	UUID            string      `json:"uuid"`
	SessionID       string      `json:"session_id"`
	AgentID         *string     `json:"agent_id,omitzero"`
	SubagentType    *string     `json:"subagent_type,omitzero"`
}

// UserMessage is a user turn: one the app sends, or one the process echoes (tool results, replays).
type UserMessage struct {
	envelope        `json:"-"`
	Type            string         `json:"type"`
	Message         MessageParam   `json:"message"`
	ParentToolUseID *string        `json:"parent_tool_use_id"`
	IsSynthetic     *bool          `json:"isSynthetic,omitzero"`
	ToolUseResult   jsontext.Value `json:"tool_use_result,omitzero"`
	Priority        *string        `json:"priority,omitzero"`
	UUID            *string        `json:"uuid,omitzero"`
	SessionID       *string        `json:"session_id,omitzero"`
	IsReplay        *bool          `json:"isReplay,omitzero"`
}

// Priorities a user message can ask for while the agent is already working.
const (
	PriorityNow   = "now"
	PriorityNext  = "next"
	PriorityLater = "later"
)

// NewUserMessage is a user turn as the app writes one: no parent tool call, and the session id the
// process fills in itself.
func NewUserMessage(content MessageContent) UserMessage {
	return UserMessage{Type: "user", Message: MessageParam{Role: "user", Content: content}, SessionID: ptr("")}
}

// ResultMessage ends a turn. Subtype is "success" or one of the error_* kinds.
type ResultMessage struct {
	envelope          `json:"-"`
	Type              string         `json:"type"`
	Subtype           string         `json:"subtype"`
	DurationMS        float64        `json:"duration_ms"`
	DurationAPIMS     float64        `json:"duration_api_ms"`
	IsError           bool           `json:"is_error"`
	NumTurns          float64        `json:"num_turns"`
	Result            *string        `json:"result,omitzero"`
	Errors            []string       `json:"errors,omitzero"`
	StopReason        *string        `json:"stop_reason"`
	TotalCostUSD      float64        `json:"total_cost_usd"`
	Usage             jsontext.Value `json:"usage,omitzero"`
	ModelUsage        jsontext.Value `json:"modelUsage,omitzero"`
	PermissionDenials jsontext.Value `json:"permission_denials,omitzero"`
	StructuredOutput  jsontext.Value `json:"structured_output,omitzero"`
	StartupFailure    *string        `json:"startup_failure_reason,omitzero"`
	UUID              string         `json:"uuid"`
	SessionID         string         `json:"session_id"`
}

// ResultSubtypeSuccess is the subtype of a result that is not an error kind.
const ResultSubtypeSuccess = "success"

// StreamEvent is one frame of a response as it streams (--include-partial-messages).
type StreamEvent struct {
	envelope        `json:"-"`
	Type            string         `json:"type"`
	Event           RawStreamEvent `json:"event"`
	ParentToolUseID *string        `json:"parent_tool_use_id"`
	UUID            string         `json:"uuid"`
	SessionID       string         `json:"session_id"`
}

// RawStreamEvent is a Messages API stream event, flattened: message_start carries Message,
// content_block_start ContentBlock, content_block_delta Delta, message_delta Delta and Usage.
type RawStreamEvent struct {
	Type         string        `json:"type"`
	Message      *BetaMessage  `json:"message,omitzero"`
	Index        *float64      `json:"index,omitzero"`
	ContentBlock *ContentBlock `json:"content_block,omitzero"`
	Delta        *StreamDelta  `json:"delta,omitzero"`
	Usage        *DeltaUsage   `json:"usage,omitzero"`
}

// StreamDelta is a content_block_delta's delta (text_delta, input_json_delta, thinking_delta,
// signature_delta) or a message_delta's (stop_reason).
type StreamDelta struct {
	Type         *string `json:"type,omitzero"`
	Text         *string `json:"text,omitzero"`
	PartialJSON  *string `json:"partial_json,omitzero"`
	Thinking     *string `json:"thinking,omitzero"`
	Signature    *string `json:"signature,omitzero"`
	StopReason   *string `json:"stop_reason,omitzero"`
	StopSequence *string `json:"stop_sequence,omitzero"`
}

// RateLimitInfo is the plan limit report the process sends whenever it changes.
type RateLimitInfo struct {
	Status          string   `json:"status"`
	ResetsAt        *float64 `json:"resetsAt,omitzero"`
	RateLimitType   *string  `json:"rateLimitType,omitzero"`
	Utilization     *float64 `json:"utilization,omitzero"`
	OverageStatus   *string  `json:"overageStatus,omitzero"`
	OverageResetsAt *float64 `json:"overageResetsAt,omitzero"`
	IsUsingOverage  *bool    `json:"isUsingOverage,omitzero"`
}

// RateLimitEvent carries the newest plan limit report.
type RateLimitEvent struct {
	envelope      `json:"-"`
	Type          string        `json:"type"`
	RateLimitInfo RateLimitInfo `json:"rate_limit_info"`
	UUID          string        `json:"uuid"`
	SessionID     string        `json:"session_id"`
}

// ActiveGoal is the goal the session is working toward.
type ActiveGoal struct {
	Condition     string  `json:"condition"`
	Iterations    float64 `json:"iterations"`
	SetAt         float64 `json:"set_at"`
	TokensAtStart float64 `json:"tokens_at_start"`
	LastReason    *string `json:"last_reason,omitzero"`
}

// ActiveGoalMessage reports the session's goal, or that it has none.
type ActiveGoalMessage struct {
	envelope  `json:"-"`
	Type      string      `json:"type"`
	Value     *ActiveGoal `json:"value"`
	UUID      string      `json:"uuid"`
	SessionID string      `json:"session_id"`
}

// McpServerStatus is one MCP server as the init message lists it.
type McpServerStatus struct {
	Name   string  `json:"name"`
	Status string  `json:"status"`
	Source *string `json:"source,omitzero"`
}

// PluginInfo is one plugin as the init message lists it.
type PluginInfo struct {
	Name    string  `json:"name"`
	Path    string  `json:"path"`
	Version *string `json:"version,omitzero"`
}

// SystemInit opens a session and names it.
type SystemInit struct {
	envelope          `json:"-"`
	Type              string            `json:"type"`
	Subtype           string            `json:"subtype"`
	Agents            []string          `json:"agents,omitzero"`
	APIKeySource      string            `json:"apiKeySource"`
	Betas             []string          `json:"betas,omitzero"`
	ClaudeCodeVersion string            `json:"claude_code_version"`
	Cwd               string            `json:"cwd"`
	Tools             []string          `json:"tools"`
	McpServers        []McpServerStatus `json:"mcp_servers"`
	Model             string            `json:"model"`
	PermissionMode    string            `json:"permissionMode"`
	SlashCommands     []string          `json:"slash_commands"`
	OutputStyle       string            `json:"output_style"`
	Skills            []string          `json:"skills"`
	Plugins           []PluginInfo      `json:"plugins"`
	Effort            *string           `json:"effort,omitzero"`
	UUID              string            `json:"uuid"`
	SessionID         string            `json:"session_id"`
}

// CompactMetadata says what a compaction did.
type CompactMetadata struct {
	Trigger    string   `json:"trigger"`
	PreTokens  float64  `json:"pre_tokens"`
	PostTokens *float64 `json:"post_tokens,omitzero"`
	DurationMS *float64 `json:"duration_ms,omitzero"`
}

// CompactBoundary marks where the conversation was summarized.
type CompactBoundary struct {
	envelope        `json:"-"`
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	CompactMetadata CompactMetadata `json:"compact_metadata"`
	UUID            string          `json:"uuid"`
	SessionID       string          `json:"session_id"`
}

// SystemStatus reports what the session is doing ("compacting", "requesting" or null) and how a
// compaction ended.
type SystemStatus struct {
	envelope       `json:"-"`
	Type           string  `json:"type"`
	Subtype        string  `json:"subtype"`
	Status         *string `json:"status"`
	PermissionMode *string `json:"permissionMode,omitzero"`
	CompactResult  *string `json:"compact_result,omitzero"`
	CompactError   *string `json:"compact_error,omitzero"`
	UUID           string  `json:"uuid"`
	SessionID      string  `json:"session_id"`
}

// APIRetry says a request failed and is being tried again.
type APIRetry struct {
	envelope     `json:"-"`
	Type         string   `json:"type"`
	Subtype      string   `json:"subtype"`
	Attempt      float64  `json:"attempt"`
	MaxRetries   float64  `json:"max_retries"`
	RetryDelayMS float64  `json:"retry_delay_ms"`
	ErrorStatus  *float64 `json:"error_status"`
	Error        string   `json:"error"`
	UUID         string   `json:"uuid"`
	SessionID    string   `json:"session_id"`
}

// TaskUsage is what a task has spent so far.
type TaskUsage struct {
	TotalTokens float64 `json:"total_tokens"`
	ToolUses    float64 `json:"tool_uses"`
	DurationMS  float64 `json:"duration_ms"`
}

// TaskStarted says a task began: a subagent, a workflow, a shell or a monitor.
type TaskStarted struct {
	envelope       `json:"-"`
	Type           string  `json:"type"`
	Subtype        string  `json:"subtype"`
	TaskID         string  `json:"task_id"`
	ToolUseID      *string `json:"tool_use_id,omitzero"`
	Description    string  `json:"description"`
	SubagentType   *string `json:"subagent_type,omitzero"`
	IsBackgrounded *bool   `json:"is_backgrounded,omitzero"`
	ParentTaskID   *string `json:"parent_task_id,omitzero"`
	TaskType       *string `json:"task_type,omitzero"`
	WorkflowName   *string `json:"workflow_name,omitzero"`
	Prompt         *string `json:"prompt,omitzero"`
	UUID           string  `json:"uuid"`
	SessionID      string  `json:"session_id"`
}

// TaskProgress reports a running task. A workflow's frames may carry its progress tree.
type TaskProgress struct {
	envelope         `json:"-"`
	Type             string         `json:"type"`
	Subtype          string         `json:"subtype"`
	TaskID           string         `json:"task_id"`
	ToolUseID        *string        `json:"tool_use_id,omitzero"`
	Description      string         `json:"description"`
	SubagentType     *string        `json:"subagent_type,omitzero"`
	Usage            TaskUsage      `json:"usage"`
	LastToolName     *string        `json:"last_tool_name,omitzero"`
	Summary          *string        `json:"summary,omitzero"`
	WorkflowProgress jsontext.Value `json:"workflow_progress,omitzero"`
	UUID             string         `json:"uuid"`
	SessionID        string         `json:"session_id"`
}

// TaskNotification says a task ended: completed, failed or stopped.
type TaskNotification struct {
	envelope   `json:"-"`
	Type       string     `json:"type"`
	Subtype    string     `json:"subtype"`
	TaskID     string     `json:"task_id"`
	ToolUseID  *string    `json:"tool_use_id,omitzero"`
	Status     string     `json:"status"`
	OutputFile string     `json:"output_file"`
	Summary    string     `json:"summary"`
	Usage      *TaskUsage `json:"usage,omitzero"`
	UUID       string     `json:"uuid"`
	SessionID  string     `json:"session_id"`
}

// BackgroundTask is one task the agent left running.
type BackgroundTask struct {
	TaskID       string  `json:"task_id"`
	TaskType     string  `json:"task_type"`
	SubagentType *string `json:"subagent_type,omitzero"`
	Description  string  `json:"description"`
	ParentTaskID *string `json:"parent_task_id,omitzero"`
}

// BackgroundTasksChanged lists everything the agent has running in the background now.
type BackgroundTasksChanged struct {
	envelope  `json:"-"`
	Type      string           `json:"type"`
	Subtype   string           `json:"subtype"`
	Tasks     []BackgroundTask `json:"tasks"`
	UUID      string           `json:"uuid"`
	SessionID string           `json:"session_id"`
}

// SessionStateChanged says whether the session is idle, running or waiting on the host.
type SessionStateChanged struct {
	envelope  `json:"-"`
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	State     string `json:"state"`
	UUID      string `json:"uuid"`
	SessionID string `json:"session_id"`
}

// SlashCommand is one command the session takes.
type SlashCommand struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	ArgumentHint string   `json:"argumentHint"`
	Aliases      []string `json:"aliases,omitzero"`
	Builtin      *bool    `json:"builtin,omitzero"`
}

// CommandsChanged lists the session's commands after they changed.
type CommandsChanged struct {
	envelope  `json:"-"`
	Type      string         `json:"type"`
	Subtype   string         `json:"subtype"`
	Commands  []SlashCommand `json:"commands"`
	UUID      string         `json:"uuid"`
	SessionID string         `json:"session_id"`
}

// UnknownMessage is any message this package has no type for.
type UnknownMessage struct {
	envelope
	Type    string
	Subtype string
}

func (m *AssistantMessage) Kind() string       { return "assistant" }
func (m *UserMessage) Kind() string            { return "user" }
func (m *ResultMessage) Kind() string          { return "result" }
func (m *StreamEvent) Kind() string            { return "stream_event" }
func (m *RateLimitEvent) Kind() string         { return "rate_limit_event" }
func (m *ActiveGoalMessage) Kind() string      { return "active_goal" }
func (m *SystemInit) Kind() string             { return "system/init" }
func (m *CompactBoundary) Kind() string        { return "system/compact_boundary" }
func (m *SystemStatus) Kind() string           { return "system/status" }
func (m *APIRetry) Kind() string               { return "system/api_retry" }
func (m *TaskStarted) Kind() string            { return "system/task_started" }
func (m *TaskProgress) Kind() string           { return "system/task_progress" }
func (m *TaskNotification) Kind() string       { return "system/task_notification" }
func (m *BackgroundTasksChanged) Kind() string { return "system/background_tasks_changed" }
func (m *SessionStateChanged) Kind() string    { return "system/session_state_changed" }
func (m *CommandsChanged) Kind() string        { return "system/commands_changed" }
func (m *UnknownMessage) Kind() string {
	if m.Subtype == "" {
		return m.Type
	}
	return m.Type + "/" + m.Subtype
}

// MarshalJSONTo writes the line the message was read from.
func (m *UnknownMessage) MarshalJSONTo(enc *jsontext.Encoder) error { return enc.WriteValue(m.raw) }

// DecodeMessage reads one printed line into its message type. A type this package does not know
// is an UnknownMessage; a known type that does not decode is an error.
func DecodeMessage(raw jsontext.Value) (Message, error) {
	var header MessageHeader
	if err := unmarshal(raw, &header); err != nil {
		return nil, err
	}
	if header.Type == "" {
		return nil, errors.New("message has no type")
	}
	subtype := ""
	if header.Subtype != nil {
		subtype = *header.Subtype
	}
	var message Message
	switch header.Type {
	case "assistant":
		message = &AssistantMessage{envelope: envelope{raw}}
	case "user":
		message = &UserMessage{envelope: envelope{raw}}
	case "result":
		message = &ResultMessage{envelope: envelope{raw}}
	case "stream_event":
		message = &StreamEvent{envelope: envelope{raw}}
	case "rate_limit_event":
		message = &RateLimitEvent{envelope: envelope{raw}}
	case "active_goal":
		message = &ActiveGoalMessage{envelope: envelope{raw}}
	case "system":
		switch subtype {
		case "init":
			message = &SystemInit{envelope: envelope{raw}}
		case "compact_boundary":
			message = &CompactBoundary{envelope: envelope{raw}}
		case "status":
			message = &SystemStatus{envelope: envelope{raw}}
		case "api_retry":
			message = &APIRetry{envelope: envelope{raw}}
		case "task_started":
			message = &TaskStarted{envelope: envelope{raw}}
		case "task_progress":
			message = &TaskProgress{envelope: envelope{raw}}
		case "task_notification":
			message = &TaskNotification{envelope: envelope{raw}}
		case "background_tasks_changed":
			message = &BackgroundTasksChanged{envelope: envelope{raw}}
		case "session_state_changed":
			message = &SessionStateChanged{envelope: envelope{raw}}
		case "commands_changed":
			message = &CommandsChanged{envelope: envelope{raw}}
		}
	}
	if message == nil {
		return &UnknownMessage{envelope: envelope{raw}, Type: header.Type, Subtype: subtype}, nil
	}
	if err := unmarshal(raw, message); err != nil {
		return nil, fmt.Errorf("%s: %w", message.Kind(), err)
	}
	return message, nil
}
