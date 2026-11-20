package protocol

import (
	"encoding/json/jsontext"
	"fmt"
	"strconv"
)

// Version is the `codex --version` these types follow (src/main/codex/protocol/version.ts).
const Version = "0.171.0"

// RequestID is a JSON-RPC id: a string or a number.
type RequestID struct {
	raw jsontext.Value
}

// NumberID is a numeric request id.
func NumberID(id int64) RequestID { return RequestID{jsontext.Value(strconv.FormatInt(id, 10))} }

// StringID is a string request id.
func StringID(id string) RequestID {
	raw, _ := Marshal(id)
	return RequestID{raw}
}

// String is the id as text: a string id as is, a number in its JSON form.
func (r RequestID) String() string {
	if r.raw.Kind() == '"' {
		var text string
		_ = Unmarshal(r.raw, &text)
		return text
	}
	return string(r.raw)
}

// IsZero reports an id that was never set.
func (r RequestID) IsZero() bool { return len(r.raw) == 0 }

// Raw is the id as it was written.
func (r RequestID) Raw() jsontext.Value { return r.raw }

func (r RequestID) MarshalJSONTo(enc *jsontext.Encoder) error {
	if len(r.raw) == 0 {
		return enc.WriteToken(jsontext.Null)
	}
	return enc.WriteValue(r.raw)
}

func (r *RequestID) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	value, err := dec.ReadValue()
	if err != nil {
		return err
	}
	if kind := value.Kind(); kind != '"' && kind != '0' {
		return fmt.Errorf("request id must be a string or a number, not %s", kind)
	}
	r.raw = append(jsontext.Value(nil), value...)
	return nil
}

// ClientInfo is how a client introduces itself.
type ClientInfo struct {
	Name    string  `json:"name"`
	Title   *string `json:"title"`
	Version string  `json:"version"`
}

// InitializeCapabilities are what the client supports.
type InitializeCapabilities struct {
	RequiredHumanApproval          *bool                     `json:"requiredHumanApproval,omitzero"`
	ExplicitGatewayOauth           *bool                     `json:"explicitGatewayOauth,omitzero"`
	ExperimentalAPI                bool                      `json:"experimentalApi"`
	RequestAttestation             bool                      `json:"requestAttestation"`
	McpServerOpenaiFormElicitation *bool                     `json:"mcpServerOpenaiFormElicitation,omitzero"`
	OptOutNotificationMethods      []string                  `json:"optOutNotificationMethods,omitzero"`
	Extensions                     map[string]jsontext.Value `json:"extensions,omitzero"`
}

// InitializeParams are initialize's params.
type InitializeParams struct {
	ClientInfo   ClientInfo              `json:"clientInfo"`
	Capabilities *InitializeCapabilities `json:"capabilities"`
}

// InitializeResponse is the server's answer to initialize.
type InitializeResponse struct {
	UserAgent      string `json:"userAgent"`
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOs     string `json:"platformOs"`
}

// ApprovalsReviewer is who reviews an approval request.
type ApprovalsReviewer string

const (
	ApprovalsReviewerUser             ApprovalsReviewer = "user"
	ApprovalsReviewerAutoReview       ApprovalsReviewer = "auto_review"
	ApprovalsReviewerGuardianSubagent ApprovalsReviewer = "guardian_subagent"
)

// SandboxMode is a thread's sandbox, as thread/start names it.
type SandboxMode string

const (
	SandboxReadOnly         SandboxMode = "read-only"
	SandboxWorkspaceWrite   SandboxMode = "workspace-write"
	SandboxDangerFullAccess SandboxMode = "danger-full-access"
)

// GranularApproval says which requests reach the user under a granular approval policy.
type GranularApproval struct {
	SandboxApproval    bool `json:"sandbox_approval"`
	Rules              bool `json:"rules"`
	SkillApproval      bool `json:"skill_approval"`
	RequestPermissions bool `json:"request_permissions"`
	McpElicitations    bool `json:"mcp_elicitations"`
}

// AskForApproval is "untrusted", "on-request", "never", or a granular policy.
type AskForApproval struct {
	Mode     string
	Granular *GranularApproval
}

const (
	ApprovalUntrusted = "untrusted"
	ApprovalOnRequest = "on-request"
	ApprovalNever     = "never"
)

// ApprovalMode is a policy named by one of the approval modes.
func ApprovalMode(mode string) *AskForApproval { return &AskForApproval{Mode: mode} }

type granularApproval struct {
	Granular GranularApproval `json:"granular"`
}

func (a AskForApproval) MarshalJSONTo(enc *jsontext.Encoder) error {
	if a.Granular != nil {
		data, err := Marshal(granularApproval{Granular: *a.Granular})
		if err != nil {
			return err
		}
		return enc.WriteValue(data)
	}
	return enc.WriteToken(jsontext.String(a.Mode))
}

func (a *AskForApproval) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	value, err := dec.ReadValue()
	if err != nil {
		return err
	}
	*a = AskForApproval{}
	if value.Kind() == '"' {
		return Unmarshal(value, &a.Mode)
	}
	var granular granularApproval
	if err := Unmarshal(value, &granular); err != nil {
		return err
	}
	a.Granular = &granular.Granular
	return nil
}

// SandboxPolicy is a turn's sandbox, in full.
type SandboxPolicy interface{ sandboxPolicy() }

type dangerFullAccessTag struct{}

func (dangerFullAccessTag) tagValue() string { return "dangerFullAccess" }

type readOnlyTag struct{}

func (readOnlyTag) tagValue() string { return "readOnly" }

type externalSandboxTag struct{}

func (externalSandboxTag) tagValue() string { return "externalSandbox" }

type workspaceWriteTag struct{}

func (workspaceWriteTag) tagValue() string { return "workspaceWrite" }

// DangerFullAccessPolicy runs commands with no sandbox.
type DangerFullAccessPolicy struct {
	Type Tag[dangerFullAccessTag] `json:"type"`
}

// ReadOnlyPolicy lets commands read but not write.
type ReadOnlyPolicy struct {
	Type          Tag[readOnlyTag] `json:"type"`
	NetworkAccess bool             `json:"networkAccess"`
}

// ExternalSandboxPolicy defers to a sandbox outside Codex.
type ExternalSandboxPolicy struct {
	Type          Tag[externalSandboxTag] `json:"type"`
	NetworkAccess string                  `json:"networkAccess"`
}

// WorkspaceWritePolicy lets commands write inside the workspace and the roots named.
type WorkspaceWritePolicy struct {
	Type                Tag[workspaceWriteTag] `json:"type"`
	WritableRoots       []string               `json:"writableRoots"`
	NetworkAccess       bool                   `json:"networkAccess"`
	ExcludeTmpdirEnvVar bool                   `json:"excludeTmpdirEnvVar"`
	ExcludeSlashTmp     bool                   `json:"excludeSlashTmp"`
}

func (DangerFullAccessPolicy) sandboxPolicy() {}
func (ReadOnlyPolicy) sandboxPolicy()         {}
func (ExternalSandboxPolicy) sandboxPolicy()  {}
func (WorkspaceWritePolicy) sandboxPolicy()   {}

// ReasoningSummary is how much reasoning summary a turn produces.
type ReasoningSummary string

// Personality is the model's tone.
type Personality string

// ThreadStartParams are thread/start's params.
type ThreadStartParams struct {
	Model                 *string                   `json:"model,omitzero"`
	ModelProvider         *string                   `json:"modelProvider,omitzero"`
	ServiceTier           *string                   `json:"serviceTier,omitzero"`
	Cwd                   *string                   `json:"cwd,omitzero"`
	ApprovalPolicy        *AskForApproval           `json:"approvalPolicy,omitzero"`
	ApprovalsReviewer     *ApprovalsReviewer        `json:"approvalsReviewer,omitzero"`
	Sandbox               *SandboxMode              `json:"sandbox,omitzero"`
	Config                map[string]jsontext.Value `json:"config,omitzero"`
	ServiceName           *string                   `json:"serviceName,omitzero"`
	BaseInstructions      *string                   `json:"baseInstructions,omitzero"`
	DeveloperInstructions *string                   `json:"developerInstructions,omitzero"`
	Personality           *Personality              `json:"personality,omitzero"`
	Ephemeral             *bool                     `json:"ephemeral,omitzero"`
}

// ThreadResumeParams are thread/resume's params.
type ThreadResumeParams struct {
	ThreadID              string                    `json:"threadId"`
	Model                 *string                   `json:"model,omitzero"`
	ModelProvider         *string                   `json:"modelProvider,omitzero"`
	ServiceTier           *string                   `json:"serviceTier,omitzero"`
	Cwd                   *string                   `json:"cwd,omitzero"`
	ApprovalPolicy        *AskForApproval           `json:"approvalPolicy,omitzero"`
	ApprovalsReviewer     *ApprovalsReviewer        `json:"approvalsReviewer,omitzero"`
	Sandbox               *SandboxMode              `json:"sandbox,omitzero"`
	Config                map[string]jsontext.Value `json:"config,omitzero"`
	BaseInstructions      *string                   `json:"baseInstructions,omitzero"`
	DeveloperInstructions *string                   `json:"developerInstructions,omitzero"`
	Personality           *Personality              `json:"personality,omitzero"`
	ExcludeTurns          *bool                     `json:"excludeTurns,omitzero"`
}

// ThreadForkParams are thread/fork's params.
type ThreadForkParams struct {
	ThreadID              string                    `json:"threadId"`
	LastTurnID            *string                   `json:"lastTurnId,omitzero"`
	Model                 *string                   `json:"model,omitzero"`
	ModelProvider         *string                   `json:"modelProvider,omitzero"`
	ServiceTier           *string                   `json:"serviceTier,omitzero"`
	Cwd                   *string                   `json:"cwd,omitzero"`
	ApprovalPolicy        *AskForApproval           `json:"approvalPolicy,omitzero"`
	ApprovalsReviewer     *ApprovalsReviewer        `json:"approvalsReviewer,omitzero"`
	Sandbox               *SandboxMode              `json:"sandbox,omitzero"`
	Config                map[string]jsontext.Value `json:"config,omitzero"`
	BaseInstructions      *string                   `json:"baseInstructions,omitzero"`
	DeveloperInstructions *string                   `json:"developerInstructions,omitzero"`
	Ephemeral             *bool                     `json:"ephemeral,omitzero"`
	ExcludeTurns          *bool                     `json:"excludeTurns,omitzero"`
}

// ThreadSessionResponse is what thread/start, thread/resume and thread/fork answer with.
type ThreadSessionResponse struct {
	Thread             Thread            `json:"thread"`
	Model              string            `json:"model"`
	ModelProvider      string            `json:"modelProvider"`
	ServiceTier        *string           `json:"serviceTier"`
	DisabledPluginIDs  []string          `json:"disabledPluginIds"`
	Cwd                string            `json:"cwd"`
	InstructionSources []string          `json:"instructionSources"`
	ApprovalPolicy     AskForApproval    `json:"approvalPolicy"`
	ApprovalsReviewer  ApprovalsReviewer `json:"approvalsReviewer"`
	Sandbox            jsontext.Value    `json:"sandbox"`
	ReasoningEffort    *string           `json:"reasoningEffort"`
}

// ThreadStatus is a thread's state: notLoaded, idle, systemError, or active with its flags.
type ThreadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags,omitzero"`
}

// GitInfo is the repository state a thread was recorded in.
type GitInfo struct {
	Sha       *string `json:"sha"`
	Branch    *string `json:"branch"`
	OriginURL *string `json:"originUrl"`
}

// Thread is one conversation.
type Thread struct {
	ID              string         `json:"id"`
	SessionID       string         `json:"sessionId"`
	ForkedFromID    *string        `json:"forkedFromId"`
	ParentThreadID  *string        `json:"parentThreadId"`
	Preview         string         `json:"preview"`
	Ephemeral       bool           `json:"ephemeral"`
	ModelProvider   string         `json:"modelProvider"`
	Model           *string        `json:"model"`
	ReasoningEffort *string        `json:"reasoningEffort"`
	CreatedAt       float64        `json:"createdAt"`
	UpdatedAt       float64        `json:"updatedAt"`
	Status          ThreadStatus   `json:"status"`
	Path            *string        `json:"path"`
	Cwd             string         `json:"cwd"`
	CliVersion      string         `json:"cliVersion"`
	Originator      *string        `json:"originator"`
	Source          jsontext.Value `json:"source,omitzero"`
	ThreadSource    jsontext.Value `json:"threadSource,omitzero"`
	AgentNickname   *string        `json:"agentNickname"`
	AgentRole       *string        `json:"agentRole"`
	GitInfo         *GitInfo       `json:"gitInfo"`
	Name            *string        `json:"name"`
	Turns           []Turn         `json:"turns"`
}

// TurnStatus is how far a turn got.
type TurnStatus string

const (
	TurnCompleted   TurnStatus = "completed"
	TurnInterrupted TurnStatus = "interrupted"
	TurnFailed      TurnStatus = "failed"
	TurnInProgress  TurnStatus = "inProgress"
)

// CodexErrorInfo classifies a turn error: a name ("usageLimitExceeded"), or an object keyed by one
// ({"httpConnectionFailed": {...}}).
type CodexErrorInfo struct {
	raw jsontext.Value
}

// Name is the error's kind, whichever form it came in.
func (c CodexErrorInfo) Name() string {
	switch c.raw.Kind() {
	case '"':
		var name string
		_ = Unmarshal(c.raw, &name)
		return name
	case '{':
		var object map[string]jsontext.Value
		if Unmarshal(c.raw, &object) == nil && len(object) == 1 {
			for key := range object {
				return key
			}
		}
	}
	return ""
}

func (c CodexErrorInfo) MarshalJSONTo(enc *jsontext.Encoder) error {
	if len(c.raw) == 0 {
		return enc.WriteToken(jsontext.Null)
	}
	return enc.WriteValue(c.raw)
}

func (c *CodexErrorInfo) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	value, err := dec.ReadValue()
	if err != nil {
		return err
	}
	c.raw = append(jsontext.Value(nil), value...)
	return nil
}

// TurnError is why a turn failed.
type TurnError struct {
	Message           string          `json:"message"`
	CodexErrorInfo    *CodexErrorInfo `json:"codexErrorInfo"`
	AdditionalDetails *string         `json:"additionalDetails"`
}

// Turn is one exchange of a thread.
type Turn struct {
	ID          string       `json:"id"`
	RootTurnID  *string      `json:"rootTurnId"`
	Items       []ThreadItem `json:"items"`
	Status      TurnStatus   `json:"status"`
	Error       *TurnError   `json:"error"`
	StartedAt   *float64     `json:"startedAt"`
	CompletedAt *float64     `json:"completedAt"`
	DurationMs  *float64     `json:"durationMs"`
}

// ThreadReadParams are thread/read's params.
type ThreadReadParams struct {
	ThreadID     string `json:"threadId"`
	IncludeTurns *bool  `json:"includeTurns,omitzero"`
}

// ThreadResponse is an answer that carries a thread (thread/read, thread/unarchive,
// thread/metadata/update).
type ThreadResponse struct {
	Thread Thread `json:"thread"`
}

// ByteRange is a span of a text input, in bytes.
type ByteRange struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// TextElement marks a span of a text input.
type TextElement struct {
	ByteRange   ByteRange `json:"byteRange"`
	Placeholder *string   `json:"placeholder"`
}

// UserInput is one part of what a user sends.
type UserInput interface{ userInput() }

type textTag struct{}

func (textTag) tagValue() string { return "text" }

type imageTag struct{}

func (imageTag) tagValue() string { return "image" }

type localImageTag struct{}

func (localImageTag) tagValue() string { return "localImage" }

type skillTag struct{}

func (skillTag) tagValue() string { return "skill" }

type mentionTag struct{}

func (mentionTag) tagValue() string { return "mention" }

// TextInput is text the user wrote.
type TextInput struct {
	Type         Tag[textTag]  `json:"type"`
	Text         string        `json:"text"`
	TextElements []TextElement `json:"text_elements"`
}

// Text is a text input with no marked spans.
func Text(text string) TextInput { return TextInput{Text: text, TextElements: []TextElement{}} }

// ImageInput is an image by URL or uploaded file id.
type ImageInput struct {
	Type   Tag[imageTag] `json:"type"`
	Detail *string       `json:"detail,omitzero"`
	URL    string        `json:"url,omitzero"`
	FileID string        `json:"fileId,omitzero"`
}

// LocalImageInput is an image on disk.
type LocalImageInput struct {
	Type   Tag[localImageTag] `json:"type"`
	Detail *string            `json:"detail,omitzero"`
	Path   string             `json:"path"`
}

// SkillInput invokes a skill.
type SkillInput struct {
	Type Tag[skillTag] `json:"type"`
	Name string        `json:"name"`
	Path string        `json:"path"`
}

// MentionInput mentions a file or an app.
type MentionInput struct {
	Type Tag[mentionTag] `json:"type"`
	Name string          `json:"name"`
	Path string          `json:"path"`
}

func (TextInput) userInput()       {}
func (ImageInput) userInput()      {}
func (LocalImageInput) userInput() {}
func (SkillInput) userInput()      {}
func (MentionInput) userInput()    {}

// UserInputItem is a user input as a thread item records it, any kind, flattened.
type UserInputItem struct {
	Type         string        `json:"type"`
	Text         *string       `json:"text,omitzero"`
	TextElements []TextElement `json:"text_elements,omitzero"`
	Detail       *string       `json:"detail,omitzero"`
	URL          *string       `json:"url,omitzero"`
	FileID       *string       `json:"fileId,omitzero"`
	Path         *string       `json:"path,omitzero"`
	Name         *string       `json:"name,omitzero"`
}

// TurnStartParams are turn/start's params.
type TurnStartParams struct {
	ThreadID          string             `json:"threadId"`
	ClientUserMessage *string            `json:"clientUserMessageId,omitzero"`
	Input             []UserInput        `json:"input"`
	Cwd               *string            `json:"cwd,omitzero"`
	ApprovalPolicy    *AskForApproval    `json:"approvalPolicy,omitzero"`
	ApprovalsReviewer *ApprovalsReviewer `json:"approvalsReviewer,omitzero"`
	SandboxPolicy     SandboxPolicy      `json:"sandboxPolicy,omitzero"`
	Model             *string            `json:"model,omitzero"`
	ServiceTier       *string            `json:"serviceTier,omitzero"`
	Effort            *string            `json:"effort,omitzero"`
	Summary           *ReasoningSummary  `json:"summary,omitzero"`
	Personality       *Personality       `json:"personality,omitzero"`
	OutputSchema      jsontext.Value     `json:"outputSchema,omitzero"`
}

// TurnResponse is what turn/start answers with.
type TurnResponse struct {
	Turn Turn `json:"turn"`
}

// TurnSteerParams are turn/steer's params.
type TurnSteerParams struct {
	ThreadID          string      `json:"threadId"`
	ClientUserMessage *string     `json:"clientUserMessageId,omitzero"`
	Input             []UserInput `json:"input"`
	ExpectedTurnID    string      `json:"expectedTurnId"`
}

// TurnSteerResponse is turn/steer's answer.
type TurnSteerResponse struct {
	TurnID string `json:"turnId"`
}

// TurnInterruptParams are turn/interrupt's params.
type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// ThreadIDParams are the params of the requests that name only a thread (thread/goal/get,
// thread/goal/clear, thread/compact/start, thread/unarchive).
type ThreadIDParams struct {
	ThreadID string `json:"threadId"`
}

// ThreadGoal is the objective a thread works toward.
type ThreadGoal struct {
	ThreadID        string   `json:"threadId"`
	Objective       string   `json:"objective"`
	Status          string   `json:"status"`
	TokenBudget     *float64 `json:"tokenBudget"`
	TokensUsed      float64  `json:"tokensUsed"`
	TimeUsedSeconds float64  `json:"timeUsedSeconds"`
	CreatedAt       float64  `json:"createdAt"`
	UpdatedAt       float64  `json:"updatedAt"`
}

// ThreadGoalSetParams are thread/goal/set's params.
type ThreadGoalSetParams struct {
	ThreadID    string   `json:"threadId"`
	Objective   *string  `json:"objective,omitzero"`
	Status      *string  `json:"status,omitzero"`
	TokenBudget *float64 `json:"tokenBudget,omitzero"`
}

// ThreadGoalSetResponse is thread/goal/set's answer.
type ThreadGoalSetResponse struct {
	Goal ThreadGoal `json:"goal"`
}

// ThreadGoalGetResponse is thread/goal/get's answer.
type ThreadGoalGetResponse struct {
	Goal *ThreadGoal `json:"goal"`
}

// ThreadGoalClearResponse is thread/goal/clear's answer.
type ThreadGoalClearResponse struct {
	Cleared bool `json:"cleared"`
}

// ThreadInjectItemsParams are thread/inject_items's params.
type ThreadInjectItemsParams struct {
	ThreadID string `json:"threadId"`
	Items    []any  `json:"items"`
}

// ThreadMetadataGitInfo is the repository state thread/metadata/update records.
type ThreadMetadataGitInfo struct {
	Sha       *string `json:"sha,omitzero"`
	Branch    *string `json:"branch,omitzero"`
	OriginURL *string `json:"originUrl,omitzero"`
}

// ThreadMetadataUpdateParams are thread/metadata/update's params.
type ThreadMetadataUpdateParams struct {
	ThreadID string                 `json:"threadId"`
	GitInfo  *ThreadMetadataGitInfo `json:"gitInfo,omitzero"`
}

// ThreadSetNameParams are thread/name/set's params.
type ThreadSetNameParams struct {
	ThreadID string `json:"threadId"`
	Name     string `json:"name"`
}

// ReviewTarget is what a review looks at.
type ReviewTarget interface{ reviewTarget() }

type uncommittedChangesTag struct{}

func (uncommittedChangesTag) tagValue() string { return "uncommittedChanges" }

type baseBranchTag struct{}

func (baseBranchTag) tagValue() string { return "baseBranch" }

type commitTag struct{}

func (commitTag) tagValue() string { return "commit" }

type customTag struct{}

func (customTag) tagValue() string { return "custom" }

// UncommittedChangesTarget reviews the working tree.
type UncommittedChangesTarget struct {
	Type Tag[uncommittedChangesTag] `json:"type"`
}

// BaseBranchTarget reviews what differs from a branch.
type BaseBranchTarget struct {
	Type   Tag[baseBranchTag] `json:"type"`
	Branch string             `json:"branch"`
}

// CommitTarget reviews one commit.
type CommitTarget struct {
	Type  Tag[commitTag] `json:"type"`
	Sha   string         `json:"sha"`
	Title *string        `json:"title"`
}

// CustomTarget reviews by the instructions given.
type CustomTarget struct {
	Type         Tag[customTag] `json:"type"`
	Instructions string         `json:"instructions"`
}

func (UncommittedChangesTarget) reviewTarget() {}
func (BaseBranchTarget) reviewTarget()         {}
func (CommitTarget) reviewTarget()             {}
func (CustomTarget) reviewTarget()             {}

// ReviewStartParams are review/start's params.
type ReviewStartParams struct {
	ThreadID string       `json:"threadId"`
	Target   ReviewTarget `json:"target"`
	Delivery *string      `json:"delivery,omitzero"`
}

// ReviewStartResponse is review/start's answer.
type ReviewStartResponse struct {
	Turn           Turn   `json:"turn"`
	ReviewThreadID string `json:"reviewThreadId"`
}

// BackgroundTerminalsListParams are thread/backgroundTerminals/list's params.
type BackgroundTerminalsListParams struct {
	ThreadID string   `json:"threadId"`
	Cursor   *string  `json:"cursor,omitzero"`
	Limit    *float64 `json:"limit,omitzero"`
}

// BackgroundTerminal is a shell a thread left running.
type BackgroundTerminal struct {
	ItemID     string   `json:"itemId"`
	ProcessID  string   `json:"processId"`
	Command    string   `json:"command"`
	Cwd        string   `json:"cwd"`
	OsPid      *float64 `json:"osPid"`
	CPUPercent *float64 `json:"cpuPercent"`
	RssKb      *int64   `json:"rssKb"`
}

// BackgroundTerminalsListResponse is one page of a thread's background shells.
type BackgroundTerminalsListResponse struct {
	Data       []BackgroundTerminal `json:"data"`
	NextCursor *string              `json:"nextCursor"`
}

// BackgroundTerminalsTerminateParams are thread/backgroundTerminals/terminate's params.
type BackgroundTerminalsTerminateParams struct {
	ThreadID  string `json:"threadId"`
	ProcessID string `json:"processId"`
}

// BackgroundTerminalsTerminateResponse is thread/backgroundTerminals/terminate's answer.
type BackgroundTerminalsTerminateResponse struct {
	Terminated bool `json:"terminated"`
}

// SkillsListParams are skills/list's params.
type SkillsListParams struct {
	Cwds        []string `json:"cwds,omitzero"`
	ForceReload *bool    `json:"forceReload,omitzero"`
}

// SkillInterface is how a skill presents itself.
type SkillInterface struct {
	DisplayName      *string `json:"displayName,omitzero"`
	ShortDescription *string `json:"shortDescription,omitzero"`
	IconSmall        *string `json:"iconSmall,omitzero"`
	IconLarge        *string `json:"iconLarge,omitzero"`
	IconSmallURL     *string `json:"iconSmallUrl"`
	IconLargeURL     *string `json:"iconLargeUrl"`
	BrandColor       *string `json:"brandColor,omitzero"`
	DefaultPrompt    *string `json:"defaultPrompt,omitzero"`
}

// SkillMetadata is one skill.
type SkillMetadata struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	ShortDescription *string         `json:"shortDescription,omitzero"`
	Interface        *SkillInterface `json:"interface,omitzero"`
	Dependencies     jsontext.Value  `json:"dependencies,omitzero"`
	Path             string          `json:"path"`
	Scope            string          `json:"scope"`
	Enabled          bool            `json:"enabled"`
	PluginID         *string         `json:"pluginId"`
}

// SkillErrorInfo is a skill that could not be read.
type SkillErrorInfo struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// SkillsListEntry is the skills of one folder.
type SkillsListEntry struct {
	Cwd    string           `json:"cwd"`
	Skills []SkillMetadata  `json:"skills"`
	Errors []SkillErrorInfo `json:"errors"`
}

// SkillsListResponse is skills/list's answer.
type SkillsListResponse struct {
	Data []SkillsListEntry `json:"data"`
}

// SkillsExtraRootsSetParams are skills/extraRoots/set's params.
type SkillsExtraRootsSetParams struct {
	ExtraRoots []string `json:"extraRoots"`
}

// GetAccountParams are account/read's params.
type GetAccountParams struct {
	RefreshToken *bool `json:"refreshToken,omitzero"`
}

// Account is the account a server runs under: apiKey, chatgpt, amazonBedrock or gcp.
type Account struct {
	Type                        string  `json:"type"`
	Email                       *string `json:"email,omitzero"`
	PlanType                    *string `json:"planType,omitzero"`
	WorkspaceName               *string `json:"workspaceName,omitzero"`
	UsesCodexManagedCredentials *bool   `json:"usesCodexManagedCredentials,omitzero"`
}

// GetAccountResponse is account/read's answer.
type GetAccountResponse struct {
	Account            *Account `json:"account"`
	UserID             *string  `json:"userId"`
	RequiresOpenaiAuth bool     `json:"requiresOpenaiAuth"`
}

// LoginAccountParams are account/login/start's params.
type LoginAccountParams interface{ loginAccount() }

type apiKeyTag struct{}

func (apiKeyTag) tagValue() string { return "apiKey" }

type chatgptTag struct{}

func (chatgptTag) tagValue() string { return "chatgpt" }

type chatgptAuthTokensTag struct{}

func (chatgptAuthTokensTag) tagValue() string { return "chatgptAuthTokens" }

// APIKeyLogin signs in with an API key.
type APIKeyLogin struct {
	Type   Tag[apiKeyTag] `json:"type"`
	APIKey string         `json:"apiKey"`
}

// ChatGPTLogin signs in through the browser.
type ChatGPTLogin struct {
	Type                      Tag[chatgptTag] `json:"type"`
	CodexStreamlinedLogin     *bool           `json:"codexStreamlinedLogin,omitzero"`
	UseHostedLoginSuccessPage *bool           `json:"useHostedLoginSuccessPage,omitzero"`
	AppBrand                  *string         `json:"appBrand,omitzero"`
}

// ChatGPTAuthTokensLogin signs in with tokens another server holds.
type ChatGPTAuthTokensLogin struct {
	Type             Tag[chatgptAuthTokensTag] `json:"type"`
	AccessToken      string                    `json:"accessToken"`
	ChatgptAccountID string                    `json:"chatgptAccountId"`
	ChatgptPlanType  *string                   `json:"chatgptPlanType"`
}

func (APIKeyLogin) loginAccount()            {}
func (ChatGPTLogin) loginAccount()           {}
func (ChatGPTAuthTokensLogin) loginAccount() {}

// LoginAccountResponse is account/login/start's answer, any kind, flattened.
type LoginAccountResponse struct {
	Type            string  `json:"type"`
	LoginID         *string `json:"loginId,omitzero"`
	AuthURL         *string `json:"authUrl,omitzero"`
	VerificationURL *string `json:"verificationUrl,omitzero"`
	UserCode        *string `json:"userCode,omitzero"`
}

// RateLimitWindow is one rate-limit window.
type RateLimitWindow struct {
	UsedPercent        float64  `json:"usedPercent"`
	WindowDurationMins *float64 `json:"windowDurationMins"`
	ResetsAt           *float64 `json:"resetsAt"`
}

// CreditsSnapshot is the account's credits.
type CreditsSnapshot struct {
	HasCredits bool    `json:"hasCredits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance"`
}

// RateLimitSnapshot is the account's limits at one moment.
type RateLimitSnapshot struct {
	LimitID              *string          `json:"limitId"`
	LimitName            *string          `json:"limitName"`
	NormalModelSlug      *string          `json:"normalModelSlug"`
	Primary              *RateLimitWindow `json:"primary"`
	Secondary            *RateLimitWindow `json:"secondary"`
	Credits              *CreditsSnapshot `json:"credits"`
	IndividualLimit      jsontext.Value   `json:"individualLimit,omitzero"`
	SpendControlReached  *bool            `json:"spendControlReached"`
	PlanType             *string          `json:"planType"`
	RateLimitReachedType *string          `json:"rateLimitReachedType"`
}

// GetAccountRateLimitsResponse is account/rateLimits/read's answer.
type GetAccountRateLimitsResponse struct {
	OrdinaryUsageAllowed  *bool                        `json:"ordinaryUsageAllowed"`
	RateLimits            RateLimitSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID   map[string]RateLimitSnapshot `json:"rateLimitsByLimitId"`
	RateLimitResetCredits jsontext.Value               `json:"rateLimitResetCredits,omitzero"`
	AccountID             *string                      `json:"accountId"`
	RateLimitUpsell       jsontext.Value               `json:"rateLimitUpsell,omitzero"`
}

// GetAuthStatusParams are getAuthStatus's params.
type GetAuthStatusParams struct {
	IncludeToken *bool `json:"includeToken"`
	RefreshToken *bool `json:"refreshToken"`
}

// GetAuthStatusResponse is getAuthStatus's answer.
type GetAuthStatusResponse struct {
	AuthMethod         *string `json:"authMethod"`
	AuthToken          *string `json:"authToken"`
	RequiresOpenaiAuth *bool   `json:"requiresOpenaiAuth"`
}

// ChatgptAuthTokensRefreshParams are the params of the server's account/chatgptAuthTokens/refresh.
type ChatgptAuthTokensRefreshParams struct {
	Reason            string  `json:"reason"`
	PreviousAccountID *string `json:"previousAccountId,omitzero"`
}

// ChatgptAuthTokensRefreshResponse answers account/chatgptAuthTokens/refresh.
type ChatgptAuthTokensRefreshResponse struct {
	AccessToken      string  `json:"accessToken"`
	ChatgptAccountID string  `json:"chatgptAccountId"`
	ChatgptPlanType  *string `json:"chatgptPlanType"`
}
