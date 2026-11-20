package protocol

import "encoding/json/jsontext"

// Notification methods the app listens to.
const (
	NotifyError                 = "error"
	NotifyWarning               = "warning"
	NotifyThreadStarted         = "thread/started"
	NotifyThreadStatusChanged   = "thread/status/changed"
	NotifyThreadClosed          = "thread/closed"
	NotifyThreadGoalUpdated     = "thread/goal/updated"
	NotifyThreadGoalCleared     = "thread/goal/cleared"
	NotifyThreadTokenUsage      = "thread/tokenUsage/updated"
	NotifyThreadCompacted       = "thread/compacted"
	NotifySkillsChanged         = "skills/changed"
	NotifyTurnStarted           = "turn/started"
	NotifyTurnCompleted         = "turn/completed"
	NotifyItemStarted           = "item/started"
	NotifyItemCompleted         = "item/completed"
	NotifyAgentMessageDelta     = "item/agentMessage/delta"
	NotifyServerRequestResolved = "serverRequest/resolved"
	NotifyAccountLoginCompleted = "account/login/completed"
)

// Server request methods.
const (
	RequestCommandApproval     = "item/commandExecution/requestApproval"
	RequestFileChangeApproval  = "item/fileChange/requestApproval"
	RequestPermissionsApproval = "item/permissions/requestApproval"
	RequestToolUserInput       = "item/tool/requestUserInput"
	RequestMcpElicitation      = "mcpServer/elicitation/request"
	RequestDynamicToolCall     = "item/tool/call"
	RequestAuthTokensRefresh   = "account/chatgptAuthTokens/refresh"
)

// ThreadItem types.
const (
	ItemUserMessage         = "userMessage"
	ItemHookPrompt          = "hookPrompt"
	ItemAgentMessage        = "agentMessage"
	ItemFunctionCallOutput  = "functionCallOutput"
	ItemPlan                = "plan"
	ItemReasoning           = "reasoning"
	ItemCommandExecution    = "commandExecution"
	ItemFileChange          = "fileChange"
	ItemMcpToolCall         = "mcpToolCall"
	ItemDynamicToolCall     = "dynamicToolCall"
	ItemCollabAgentToolCall = "collabAgentToolCall"
	ItemSubAgentActivity    = "subAgentActivity"
	ItemWebSearch           = "webSearch"
	ItemImageView           = "imageView"
	ItemSleep               = "sleep"
	ItemImageGeneration     = "imageGeneration"
	ItemEnteredReviewMode   = "enteredReviewMode"
	ItemExitedReviewMode    = "exitedReviewMode"
	ItemContextCompaction   = "contextCompaction"
)

// PatchChangeKind is how a file changed: add, delete, or update (with where it moved).
type PatchChangeKind struct {
	Type     string  `json:"type"`
	MovePath *string `json:"move_path,omitzero"`
}

// FileUpdateChange is one file a patch touches.
type FileUpdateChange struct {
	Path string          `json:"path"`
	Kind PatchChangeKind `json:"kind"`
	Diff string          `json:"diff"`
}

// CommandAction is what a command does, as Codex reads it: read, listFiles, search or unknown.
type CommandAction struct {
	Type    string  `json:"type"`
	Command string  `json:"command"`
	Name    *string `json:"name,omitzero"`
	Path    *string `json:"path,omitzero"`
	Query   *string `json:"query,omitzero"`
}

// McpToolCallError is why an MCP tool call failed.
type McpToolCallError struct {
	Message string `json:"message"`
}

// CollabAgentState is one subagent as a collab tool call last saw it.
type CollabAgentState struct {
	Status  string  `json:"status"`
	Message *string `json:"message"`
}

// AsyncUserInputQuestion is a question an agent message asks.
type AsyncUserInputQuestion struct {
	Title   string   `json:"title"`
	Options []string `json:"options"`
}

// ThreadItem is one item of a turn, any kind, flattened as it is on the wire. Fields an item's
// type does not have stay zero. Fields whose shape differs by type are kept raw, with accessors.
type ThreadItem struct {
	Type          string   `json:"type"`
	ID            string   `json:"id"`
	StartedAtMs   *float64 `json:"startedAtMs,omitzero"`
	CompletedAtMs *float64 `json:"completedAtMs,omitzero"`

	// userMessage
	ClientID *string `json:"clientId,omitzero"`
	// Content is a userMessage's inputs or a reasoning item's texts.
	Content jsontext.Value `json:"content,omitzero"`
	// hookPrompt
	Fragments jsontext.Value `json:"fragments,omitzero"`
	// agentMessage, plan
	Text           *string                  `json:"text,omitzero"`
	Phase          *string                  `json:"phase,omitzero"`
	Moderation     jsontext.Value           `json:"moderation,omitzero"`
	MemoryCitation jsontext.Value           `json:"memoryCitation,omitzero"`
	Delivery       *string                  `json:"delivery,omitzero"`
	Questions      []AsyncUserInputQuestion `json:"questions,omitzero"`
	// functionCallOutput
	Output jsontext.Value `json:"output,omitzero"`
	// reasoning
	Summary []string `json:"summary,omitzero"`
	// commandExecution
	PluginID         *string         `json:"pluginId,omitzero"`
	ScriptPath       *string         `json:"scriptPath,omitzero"`
	Command          *string         `json:"command,omitzero"`
	Cwd              *string         `json:"cwd,omitzero"`
	ProcessID        *string         `json:"processId,omitzero"`
	Source           *string         `json:"source,omitzero"`
	Status           *string         `json:"status,omitzero"`
	CommandActions   []CommandAction `json:"commandActions,omitzero"`
	AggregatedOutput *string         `json:"aggregatedOutput,omitzero"`
	ExitCode         *float64        `json:"exitCode,omitzero"`
	DurationMs       *float64        `json:"durationMs,omitzero"`
	// fileChange
	Changes []FileUpdateChange `json:"changes,omitzero"`
	// mcpToolCall, dynamicToolCall
	Server            *string        `json:"server,omitzero"`
	EnvironmentID     *string        `json:"environmentId,omitzero"`
	Namespace         *string        `json:"namespace,omitzero"`
	Tool              *string        `json:"tool,omitzero"`
	Arguments         jsontext.Value `json:"arguments,omitzero"`
	AppContext        jsontext.Value `json:"appContext,omitzero"`
	McpAppResourceURI *string        `json:"mcpAppResourceUri,omitzero"`
	McpAppUI          jsontext.Value `json:"mcpAppUi,omitzero"`
	ReadOnlyHint      *bool          `json:"readOnlyHint,omitzero"`
	// Result is an MCP call's result or an image generation's base64 image.
	Result       jsontext.Value    `json:"result,omitzero"`
	Error        *McpToolCallError `json:"error,omitzero"`
	ContentItems jsontext.Value    `json:"contentItems,omitzero"`
	Success      *bool             `json:"success,omitzero"`
	// collabAgentToolCall, subAgentActivity
	SenderThreadID    *string                     `json:"senderThreadId,omitzero"`
	ReceiverThreadIDs []string                    `json:"receiverThreadIds,omitzero"`
	Prompt            *string                     `json:"prompt,omitzero"`
	Model             *string                     `json:"model,omitzero"`
	ReasoningEffort   *string                     `json:"reasoningEffort,omitzero"`
	AgentsStates      map[string]CollabAgentState `json:"agentsStates,omitzero"`
	Kind              *string                     `json:"kind,omitzero"`
	AgentThreadID     *string                     `json:"agentThreadId,omitzero"`
	AgentPath         *string                     `json:"agentPath,omitzero"`
	// webSearch
	Query   *string        `json:"query,omitzero"`
	Action  jsontext.Value `json:"action,omitzero"`
	Results jsontext.Value `json:"results,omitzero"`
	// imageView, imageGeneration
	Path                  *string        `json:"path,omitzero"`
	RevisedPrompt         *string        `json:"revisedPrompt,omitzero"`
	TransparentBackground *bool          `json:"transparentBackground,omitzero"`
	Failure               jsontext.Value `json:"failure,omitzero"`
	SavedPath             *string        `json:"savedPath,omitzero"`
	InputImages           jsontext.Value `json:"inputImages,omitzero"`
	GenerationID          *string        `json:"generationId,omitzero"`
	FileID                *string        `json:"fileId,omitzero"`
	// enteredReviewMode, exitedReviewMode
	Review *string `json:"review,omitzero"`
}

// UserInputs are a userMessage item's inputs.
func (i ThreadItem) UserInputs() []UserInputItem {
	var inputs []UserInputItem
	if i.Type == ItemUserMessage {
		_ = Unmarshal(i.Content, &inputs)
	}
	return inputs
}

// ReasoningContent is a reasoning item's raw reasoning texts.
func (i ThreadItem) ReasoningContent() []string {
	var texts []string
	if i.Type == ItemReasoning {
		_ = Unmarshal(i.Content, &texts)
	}
	return texts
}

// ImageResult is an imageGeneration item's base64 image, when it carries one inline.
func (i ThreadItem) ImageResult() string {
	var image string
	if i.Type == ItemImageGeneration {
		_ = Unmarshal(i.Result, &image)
	}
	return image
}

// McpToolCallResult is an MCP tool call's result.
type McpToolCallResult struct {
	Content           []jsontext.Value `json:"content"`
	StructuredContent jsontext.Value   `json:"structuredContent,omitzero"`
	Meta              jsontext.Value   `json:"_meta,omitzero"`
}

// McpResult is an mcpToolCall item's result, if it has one.
func (i ThreadItem) McpResult() *McpToolCallResult {
	if i.Type != ItemMcpToolCall || len(i.Result) == 0 || i.Result.Kind() == 'n' {
		return nil
	}
	var result McpToolCallResult
	if Unmarshal(i.Result, &result) != nil {
		return nil
	}
	return &result
}

// ErrorNotification reports a turn error, and whether the turn is retrying.
type ErrorNotification struct {
	Error     TurnError `json:"error"`
	WillRetry bool      `json:"willRetry"`
	ThreadID  string    `json:"threadId"`
	TurnID    string    `json:"turnId"`
}

// WarningNotification is a warning from the server.
type WarningNotification struct {
	ThreadID *string `json:"threadId"`
	Message  string  `json:"message"`
}

// ThreadStartedNotification says a thread started (a subagent's, too).
type ThreadStartedNotification struct {
	Thread Thread `json:"thread"`
}

// ThreadStatusChangedNotification says a thread's status changed.
type ThreadStatusChangedNotification struct {
	ThreadID string       `json:"threadId"`
	Status   ThreadStatus `json:"status"`
}

// ThreadNotification names only a thread (thread/closed, thread/goal/cleared).
type ThreadNotification struct {
	ThreadID string `json:"threadId"`
}

// ThreadGoalUpdatedNotification carries a thread's goal after it changed.
type ThreadGoalUpdatedNotification struct {
	ThreadID string     `json:"threadId"`
	TurnID   *string    `json:"turnId"`
	Goal     ThreadGoal `json:"goal"`
}

// TokenUsageBreakdown counts tokens by kind.
type TokenUsageBreakdown struct {
	TotalTokens           float64 `json:"totalTokens"`
	InputTokens           float64 `json:"inputTokens"`
	CachedInputTokens     float64 `json:"cachedInputTokens"`
	CacheWriteInputTokens float64 `json:"cacheWriteInputTokens"`
	OutputTokens          float64 `json:"outputTokens"`
	ReasoningOutputTokens float64 `json:"reasoningOutputTokens"`
}

// ThreadTokenUsage is a thread's usage so far and its last response's.
type ThreadTokenUsage struct {
	Total              TokenUsageBreakdown `json:"total"`
	Last               TokenUsageBreakdown `json:"last"`
	ModelContextWindow *float64            `json:"modelContextWindow"`
}

// ThreadTokenUsageUpdatedNotification carries a thread's usage after a response.
type ThreadTokenUsageUpdatedNotification struct {
	ThreadID   string           `json:"threadId"`
	TurnID     string           `json:"turnId"`
	TokenUsage ThreadTokenUsage `json:"tokenUsage"`
}

// TurnNotification carries a turn (turn/started, turn/completed).
type TurnNotification struct {
	ThreadID string `json:"threadId"`
	Turn     Turn   `json:"turn"`
}

// ContextCompactedNotification says a thread's context was compacted.
type ContextCompactedNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// ItemStartedNotification carries an item as it starts.
type ItemStartedNotification struct {
	Item        ThreadItem `json:"item"`
	ThreadID    string     `json:"threadId"`
	TurnID      string     `json:"turnId"`
	StartedAtMs float64    `json:"startedAtMs"`
}

// ItemCompletedNotification carries an item once it is done.
type ItemCompletedNotification struct {
	Item              ThreadItem `json:"item"`
	ThreadID          string     `json:"threadId"`
	TurnID            string     `json:"turnId"`
	ContentTokenCount *float64   `json:"contentTokenCount,omitzero"`
	CompletedAtMs     float64    `json:"completedAtMs"`
}

// AgentMessageDeltaNotification carries text an agent message grew by.
type AgentMessageDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

// ServerRequestResolvedNotification says a server request no longer needs an answer.
type ServerRequestResolvedNotification struct {
	ThreadID  string    `json:"threadId"`
	RequestID RequestID `json:"requestId"`
}

// AccountLoginCompletedNotification says how a sign-in ended.
type AccountLoginCompletedNotification struct {
	LoginID *string `json:"loginId"`
	Success bool    `json:"success"`
	Error   *string `json:"error"`
}

// CommandExecutionRequestApprovalParams ask whether a command may run.
type CommandExecutionRequestApprovalParams struct {
	Kind                    jsontext.Value  `json:"kind,omitzero"`
	ThreadID                string          `json:"threadId"`
	TurnID                  string          `json:"turnId"`
	ItemID                  string          `json:"itemId"`
	StartedAtMs             float64         `json:"startedAtMs"`
	ApprovalID              *string         `json:"approvalId,omitzero"`
	EnvironmentID           *string         `json:"environmentId"`
	Reason                  *string         `json:"reason,omitzero"`
	NetworkApprovalContext  jsontext.Value  `json:"networkApprovalContext,omitzero"`
	Command                 *string         `json:"command,omitzero"`
	Cwd                     *string         `json:"cwd,omitzero"`
	CommandActions          []CommandAction `json:"commandActions,omitzero"`
	ProposedExecpolicy      jsontext.Value  `json:"proposedExecpolicyAmendment,omitzero"`
	ProposedNetworkPolicies jsontext.Value  `json:"proposedNetworkPolicyAmendments,omitzero"`
}

// Approval decisions that are plain names.
const (
	DecisionAccept           = "accept"
	DecisionAcceptForSession = "acceptForSession"
	DecisionDecline          = "decline"
	DecisionCancel           = "cancel"
)

// ApprovalResponse answers a command or file change approval with a decision name.
type ApprovalResponse struct {
	Decision string `json:"decision"`
}

// FileChangeRequestApprovalParams ask whether a patch may be applied.
type FileChangeRequestApprovalParams struct {
	ThreadID    string  `json:"threadId"`
	TurnID      string  `json:"turnId"`
	ItemID      string  `json:"itemId"`
	StartedAtMs float64 `json:"startedAtMs"`
	Reason      *string `json:"reason,omitzero"`
	GrantRoot   *string `json:"grantRoot,omitzero"`
}

// RequestPermissionProfile is the access a permissions request asks for.
type RequestPermissionProfile struct {
	Network    jsontext.Value `json:"network"`
	FileSystem jsontext.Value `json:"fileSystem"`
}

// PermissionsRequestApprovalParams ask for more access.
type PermissionsRequestApprovalParams struct {
	ThreadID      string                   `json:"threadId"`
	TurnID        string                   `json:"turnId"`
	ItemID        string                   `json:"itemId"`
	EnvironmentID *string                  `json:"environmentId"`
	StartedAtMs   float64                  `json:"startedAtMs"`
	Cwd           string                   `json:"cwd"`
	Reason        *string                  `json:"reason"`
	Permissions   RequestPermissionProfile `json:"permissions"`
}

// GrantedPermissionProfile is the access granted.
type GrantedPermissionProfile struct {
	Network    jsontext.Value `json:"network,omitzero"`
	FileSystem jsontext.Value `json:"fileSystem,omitzero"`
}

// PermissionsRequestApprovalResponse answers a permissions request.
type PermissionsRequestApprovalResponse struct {
	Permissions      GrantedPermissionProfile `json:"permissions"`
	Scope            string                   `json:"scope"`
	StrictAutoReview *bool                    `json:"strictAutoReview,omitzero"`
}

// ToolRequestUserInputOption is one answer offered.
type ToolRequestUserInputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ToolRequestUserInputQuestion is one question asked.
type ToolRequestUserInputQuestion struct {
	ID       string                       `json:"id"`
	Header   string                       `json:"header"`
	Question string                       `json:"question"`
	IsOther  bool                         `json:"isOther"`
	IsSecret bool                         `json:"isSecret"`
	Options  []ToolRequestUserInputOption `json:"options"`
}

// ToolRequestUserInputParams ask the user questions.
type ToolRequestUserInputParams struct {
	ThreadID         string                         `json:"threadId"`
	TurnID           string                         `json:"turnId"`
	ItemID           string                         `json:"itemId"`
	Questions        []ToolRequestUserInputQuestion `json:"questions"`
	IsBlocking       bool                           `json:"isBlocking"`
	AutoResolutionMs *float64                       `json:"autoResolutionMs"`
}

// ToolRequestUserInputAnswer is the answers to one question.
type ToolRequestUserInputAnswer struct {
	Answers []string `json:"answers"`
}

// ToolRequestUserInputResponse answers the questions, by id.
type ToolRequestUserInputResponse struct {
	Answers map[string]ToolRequestUserInputAnswer `json:"answers"`
}

// McpServerElicitationRequestParams ask the user for input on an MCP server's behalf: a form
// ("form", "openai/form", "openaiForm") or a URL to visit ("url").
type McpServerElicitationRequestParams struct {
	ThreadID        string         `json:"threadId"`
	TurnID          *string        `json:"turnId"`
	ServerName      string         `json:"serverName"`
	EnvironmentID   *string        `json:"environmentId"`
	Mode            string         `json:"mode"`
	Meta            jsontext.Value `json:"_meta,omitzero"`
	Message         string         `json:"message"`
	RequestedSchema jsontext.Value `json:"requestedSchema,omitzero"`
	URL             *string        `json:"url,omitzero"`
	ElicitationID   *string        `json:"elicitationId,omitzero"`
}

// McpServerElicitationRequestResponse answers an elicitation.
type McpServerElicitationRequestResponse struct {
	Action  string         `json:"action"`
	Content jsontext.Value `json:"content"`
	Meta    jsontext.Value `json:"_meta"`
}

// DeclineElicitation is the answer that turns an elicitation down.
func DeclineElicitation() McpServerElicitationRequestResponse {
	return McpServerElicitationRequestResponse{Action: "decline", Content: jsontext.Value("null"), Meta: jsontext.Value("null")}
}
