package computers

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
)

// ServiceEvents are the subscriptions every host offers, whether or not a window is drawn.
type ServiceEvents interface {
	// A folder the `aic` terminal command named, already registered as a workspace.
	OnOpenProject(listener func(workspace domain.WorkspaceRecord)) func()
	OnAgentEvent(listener func(event contracts.AgentEvent)) func()
	OnThreadRequest(listener func(request contracts.ThreadRequest)) func()
}

// RunBridge carries commands to the agents' runs and answers their thread requests.
type RunBridge interface {
	Submit(command contracts.RunCommand)
	AnswerThread(response contracts.ThreadResponse)
}

// WorkspaceServices registers and resolves the folders the app works in.
type WorkspaceServices interface {
	WorkspaceResolver
	RegisterProject(ctx context.Context, root string) (*domain.WorkspaceRecord, error)
	Projectless(ctx context.Context) (*domain.WorkspaceRecord, error)
	// ProjectFolder is the folder root names, refusing one inside the app's own checkouts or anything
	// that is not a folder to work in.
	ProjectFolder(ctx context.Context, root string, worktreesRoots []string) (string, error)
}

// GitServices are what Git and GitHub say about a checkout, beyond the reads another computer may ask for.
type GitServices interface {
	ChangedFiles(ctx context.Context, workspaceID string) (contracts.ChangedFilesResult, error)
	DiffSummary(ctx context.Context, workspaceID string, diffRange domain.DiffRange, ignoreWhitespace bool) (contracts.DiffSummaryResult, error)
	PullRequestFor(ctx context.Context, root string) (domain.PullRequestAnswer, error)
	CheckoutBranch(ctx context.Context, root, branch string) error
	CreateBranch(ctx context.Context, root, branch string) error
}

// WorktreeServices makes, lists and releases the threads' own checkouts.
type WorktreeServices interface {
	Create(ctx context.Context, request contracts.CreateWorktreeRequest) (*contracts.CreatedWorktree, error)
	List(ctx context.Context) ([]*domain.ManagedWorktree, error)
	Release(ctx context.Context, request contracts.ReleaseWorktreeRequest) (*contracts.WorktreeSnapshotResult, error)
}

// TaskDatabase is the thread store.
type TaskDatabase interface {
	LoadSummaries(ctx context.Context) (*contracts.LoadedTaskStore, error)
	LoadThreadMessages(ctx context.Context, taskID string) ([]*domain.ConversationMessage, error)
	SearchThreadMessages(ctx context.Context, search string, taskIDs []string) ([]string, error)
	Persist(ctx context.Context, delta contracts.TaskStoreDelta) error
	SubagentActivity(ctx context.Context, taskID, subagentID string) ([]*domain.SubagentActivity, error)
}

// EngineServices answer for each engine outside a run.
type EngineServices interface {
	PlanUsage(ctx context.Context, engine domain.AgentEngine) (domain.PlanUsage, error)
	SuggestTitle(ctx context.Context, engine domain.AgentEngine, text string, images []string, model *domain.AgentModel) (*string, error)
	// Nil when the engine keeps nothing about its subagents.
	SubagentMetadata(ctx context.Context, engine domain.AgentEngine, subagentID string, sessionID *string) (*domain.SubagentMetadata, error)
}

// EngineAccess says which engines can take a run, signs in and updates them.
type EngineAccess interface {
	Read(ctx context.Context, refresh bool) (*domain.EngineStatus, error)
	SignIn(ctx context.Context, engine domain.AgentEngine, openURL func(ctx context.Context, url string) error) (*domain.EngineStatus, error)
	Update(ctx context.Context, engine domain.AgentEngine) (*domain.EngineStatus, error)
}

// AttachmentStore keeps the screenshots a composer sends. *store.Attachments is one.
type AttachmentStore interface {
	SavedAttachmentPath(file string) (string, bool)
	// Null when the image has no context kept beside it.
	ReadAttachmentContext(file string) jsontext.Value
	WriteAttachment(data string, context any) (string, error)
}

// ImagePreserver keeps the images a reply links to. *store.MessageImageStore is one.
type ImagePreserver interface {
	PreserveMessageImages(files []string, root, messageID string) error
}

// ComputerUseHost reads and asks for the permissions computer use needs.
type ComputerUseHost interface {
	Permissions(ctx context.Context) (*domain.ComputerUsePermissions, error)
	Enable(ctx context.Context, permission domain.ComputerUsePermission) (*domain.ComputerUsePermissions, error)
}

// ServiceDesktopHost is what the services every host has are built from, whether or not a window
// is drawn over them.
type ServiceDesktopHost struct {
	Events       ServiceEvents
	Reads        *ComputerReads
	Runs         RunBridge
	Workspaces   WorkspaceServices
	Git          GitServices
	Worktrees    WorktreeServices
	TaskDatabase TaskDatabase
	Engines      EngineServices
	EngineAccess func(ctx context.Context) (EngineAccess, error)
	Attachments  AttachmentStore
	Images       ImagePreserver
	Cli          contracts.CliPortRuntime
	Terminal     contracts.TerminalPortRuntime
	Automation   AutomationDesktop
	// The roots the app keeps checkouts under, which a folder the user names may not be inside.
	WorktreesRoots func() []string
	// Where an engine's sign-in page goes: the browser beside a window, the terminal behind a server.
	OpenURL func(ctx context.Context, url string) error
	// The threads the update this launch installed cut off mid-turn, given once.
	UpdateResumes func() []string
	ComputerUse   ComputerUseHost
}

// ServiceDesktop is what every host answers the runtime with, in the process that owns the
// services: folders, checkouts, what Git and GitHub say about them, files on disk, the engines,
// storage, terminals and automations.
type ServiceDesktop struct {
	contracts.TerminalPortRuntime
	AutomationDesktop
	host ServiceDesktopHost
}

// AutomationDesktop is the scheduler's half of the desktop, as the runtime calls it.
type AutomationDesktop interface {
	ListAutomations(ctx context.Context) ([]*domain.AutomationView, error)
	SaveAutomation(ctx context.Context, draft domain.AutomationDraft) (*domain.AutomationView, error)
	UpdateAutomation(ctx context.Context, taskID string, patch domain.AutomationPatch) (*domain.AutomationView, error)
	DeleteAutomation(ctx context.Context, taskID string) (bool, error)
	RunAutomationNow(ctx context.Context, taskID string) (host.RuntimeDesktopRunAutomationNowResult, error)
	OnAutomationsChanged(listener func(automations []*domain.AutomationView)) func()
	OnAutomationFire(listener func(fire contracts.AutomationFire)) func()
	AcknowledgeAutomation(ack contracts.AutomationAck)
}

func NewServiceDesktop(services ServiceDesktopHost) *ServiceDesktop {
	return &ServiceDesktop{TerminalPortRuntime: services.Terminal, AutomationDesktop: services.Automation, host: services}
}

func (d *ServiceDesktop) resolved(ctx context.Context, workspaceID string) (string, error) {
	resolution, err := d.host.Workspaces.Resolve(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	switch resolution := resolution.(type) {
	case domain.WorkspaceResolutionAvailable:
		return resolution.Workspace.Root, nil
	case domain.WorkspaceResolutionUnavailable:
		return "", errors.New("Workspace is unavailable (" + string(resolution.Reason) + ").")
	}
	return "", errors.New("Workspace is unavailable.")
}

func (d *ServiceDesktop) Directories(ctx context.Context, prefix string, computerID *string) ([]string, error) {
	return d.host.Reads.Directories(ctx, prefix, computerID)
}

func (d *ServiceDesktop) RegisterProject(ctx context.Context, root string) (*domain.WorkspaceRecord, error) {
	folder, err := d.host.Workspaces.ProjectFolder(ctx, root, d.host.WorktreesRoots())
	if err != nil {
		return nil, err
	}
	return d.host.Workspaces.RegisterProject(ctx, folder)
}

func (d *ServiceDesktop) OnOpenProject(listener func(workspace domain.WorkspaceRecord)) func() {
	return d.host.Events.OnOpenProject(listener)
}

func (d *ServiceDesktop) ProjectlessWorkspace(ctx context.Context) (*domain.WorkspaceRecord, error) {
	return d.host.Workspaces.Projectless(ctx)
}

func (d *ServiceDesktop) Commands(ctx context.Context, workspaceID contracts.WorkspaceID, engine domain.AgentEngine) (contracts.CommandDiscoveryResult, error) {
	return d.host.Reads.Commands(ctx, workspaceID, engine)
}

func (d *ServiceDesktop) ChangedFiles(ctx context.Context, workspaceID contracts.WorkspaceID) (contracts.ChangedFilesResult, error) {
	result, err := d.host.Git.ChangedFiles(ctx, workspaceID)
	if err != nil {
		return contracts.BranchesResultError{Message: contracts.ErrorMessage(err)}, nil
	}
	return result, nil
}

func (d *ServiceDesktop) DiffSummary(ctx context.Context, workspaceID contracts.WorkspaceID, diffRange domain.DiffRange, ignoreWhitespace *bool) (contracts.DiffSummaryResult, error) {
	result, err := d.host.Git.DiffSummary(ctx, workspaceID, diffRange, ignoreWhitespace != nil && *ignoreWhitespace)
	if err != nil {
		return contracts.BranchesResultError{Message: contracts.ErrorMessage(err)}, nil
	}
	return result, nil
}

func (d *ServiceDesktop) DiffPatch(ctx context.Context, workspaceID contracts.WorkspaceID, diffRange domain.DiffRange, path string, previousPath *string, ignoreWhitespace *bool) (contracts.DiffPatchResult, error) {
	return d.host.Reads.DiffPatch(ctx, workspaceID, diffRange, path, previousPath, ignoreWhitespace != nil && *ignoreWhitespace)
}

func (d *ServiceDesktop) PullRequest(ctx context.Context, workspaceID contracts.WorkspaceID) (domain.PullRequestAnswer, error) {
	resolution, err := d.host.Workspaces.Resolve(ctx, workspaceID)
	if err != nil {
		return domain.PullRequestAnswerNone{}, nil
	}
	available, ok := resolution.(domain.WorkspaceResolutionAvailable)
	if !ok {
		return domain.PullRequestAnswerNone{}, nil
	}
	answer, err := d.host.Git.PullRequestFor(ctx, available.Workspace.Root)
	if err != nil || answer == nil {
		return domain.PullRequestAnswerNone{}, nil
	}
	return answer, nil
}

func (d *ServiceDesktop) CheckoutBranch(ctx context.Context, workspaceID contracts.WorkspaceID, branch string) error {
	root, err := d.resolved(ctx, workspaceID)
	if err != nil {
		return err
	}
	return d.host.Git.CheckoutBranch(ctx, root, branch)
}

func (d *ServiceDesktop) CreateBranch(ctx context.Context, workspaceID contracts.WorkspaceID, branch string) error {
	root, err := d.resolved(ctx, workspaceID)
	if err != nil {
		return err
	}
	return d.host.Git.CreateBranch(ctx, root, branch)
}

func (d *ServiceDesktop) CreateWorktree(ctx context.Context, request contracts.CreateWorktreeRequest) (*contracts.CreatedWorktree, error) {
	return d.host.Worktrees.Create(ctx, request)
}

func (d *ServiceDesktop) ListManagedWorktrees(ctx context.Context) ([]*domain.ManagedWorktree, error) {
	return d.host.Worktrees.List(ctx)
}

func (d *ServiceDesktop) ReleaseWorktree(ctx context.Context, request contracts.ReleaseWorktreeRequest) (*contracts.WorktreeSnapshotResult, error) {
	return d.host.Worktrees.Release(ctx, request)
}

func (d *ServiceDesktop) CliStatus(ctx context.Context) (*domain.CliStatus, error) {
	return d.host.Cli.CliStatus(ctx)
}

func (d *ServiceDesktop) InstallCli(ctx context.Context) (*domain.CliStatus, error) {
	return d.host.Cli.InstallCli(ctx)
}

func (d *ServiceDesktop) UninstallCli(ctx context.Context) (*domain.CliStatus, error) {
	return d.host.Cli.UninstallCli(ctx)
}

func (d *ServiceDesktop) ComputerUsePermissions(ctx context.Context) (*domain.ComputerUsePermissions, error) {
	return d.host.ComputerUse.Permissions(ctx)
}

func (d *ServiceDesktop) EnableComputerUse(ctx context.Context, permission domain.ComputerUsePermission) (*domain.ComputerUsePermissions, error) {
	return d.host.ComputerUse.Enable(ctx, permission)
}

// PlanUsage never fails: an engine that cannot answer says why instead.
func (d *ServiceDesktop) PlanUsage(ctx context.Context, engine domain.AgentEngine) (domain.PlanUsage, error) {
	usage, err := d.host.Engines.PlanUsage(ctx, engine)
	if err != nil {
		return domain.ComputerUseRunConfigUnavailable{Message: contracts.ErrorMessage(err)}, nil
	}
	return usage, nil
}

func (d *ServiceDesktop) Send(command contracts.RunCommand) { d.host.Runs.Submit(command) }

func (d *ServiceDesktop) OnAgentEvent(listener func(event contracts.AgentEvent)) func() {
	return d.host.Events.OnAgentEvent(listener)
}

func (d *ServiceDesktop) SaveAttachment(_ context.Context, data string, original *string) (string, error) {
	var context any
	if original != nil {
		if _, ok := d.host.Attachments.SavedAttachmentPath(*original); !ok {
			return "", errors.New("That image is not one this app is keeping.")
		}
		if held := d.host.Attachments.ReadAttachmentContext(*original); len(held) > 0 && string(held) != "null" {
			context = held
		}
	}
	return d.host.Attachments.WriteAttachment(data, context)
}

func (d *ServiceDesktop) PreserveMessageImages(_ context.Context, files []string, root, messageID string) error {
	return d.host.Images.PreserveMessageImages(files, root, messageID)
}

func (d *ServiceDesktop) ReadMessageImage(ctx context.Context, file, root, messageID string, preview bool, taskID string) (*contracts.FilePortRuntimeReadMessageImageResult, error) {
	image, err := d.host.Reads.MessageImage(ctx, file, root, messageID, preview, &taskID)
	if err != nil {
		return nil, err
	}
	return &contracts.FilePortRuntimeReadMessageImageResult{Data: base64.StdEncoding.EncodeToString(image.Bytes), ContentType: image.ContentType}, nil
}

// SuggestTaskTitle answers nil whenever no name came back, failures included.
func (d *ServiceDesktop) SuggestTaskTitle(ctx context.Context, text string, attachments []string, engine domain.AgentEngine, model *domain.AgentModel) (*string, error) {
	images := []string{}
	for _, item := range attachments {
		if file, ok := d.host.Attachments.SavedAttachmentPath(item); ok {
			images = append(images, file)
		}
	}
	if jsTrim(text) == "" && len(images) == 0 {
		return nil, nil
	}
	title, err := d.host.Engines.SuggestTitle(ctx, engine, text, images, model)
	if err != nil {
		return nil, nil
	}
	return title, nil
}

func (d *ServiceDesktop) EngineStatus(ctx context.Context, refresh *bool) (*domain.EngineStatus, error) {
	access, err := d.host.EngineAccess(ctx)
	if err != nil {
		return nil, err
	}
	return access.Read(ctx, refresh != nil && *refresh)
}

func (d *ServiceDesktop) SignInEngine(ctx context.Context, engine domain.AgentEngine) (*domain.EngineStatus, error) {
	access, err := d.host.EngineAccess(ctx)
	if err != nil {
		return nil, err
	}
	return access.SignIn(ctx, engine, d.host.OpenURL)
}

func (d *ServiceDesktop) UpdateEngine(ctx context.Context, engine domain.AgentEngine) (*domain.EngineStatus, error) {
	access, err := d.host.EngineAccess(ctx)
	if err != nil {
		return nil, err
	}
	return access.Update(ctx, engine)
}

func (d *ServiceDesktop) LoadTaskStore(ctx context.Context) (*contracts.LoadedTaskStore, error) {
	data, err := d.host.TaskDatabase.LoadSummaries(ctx)
	if err != nil || data == nil {
		return data, err
	}
	resuming := d.host.UpdateResumes()
	if len(resuming) == 0 {
		return data, nil
	}
	resumed := *data
	resumed.ResumeAfterUpdate = resuming
	return &resumed, nil
}

func (d *ServiceDesktop) LoadThreadMessages(ctx context.Context, taskID string) ([]*domain.ConversationMessage, error) {
	return d.host.TaskDatabase.LoadThreadMessages(ctx, taskID)
}

func (d *ServiceDesktop) SearchThreadMessages(ctx context.Context, search string, taskIDs []string) ([]string, error) {
	return d.host.TaskDatabase.SearchThreadMessages(ctx, search, taskIDs)
}

func (d *ServiceDesktop) PersistTaskStore(ctx context.Context, delta contracts.TaskStoreDelta) error {
	return d.host.TaskDatabase.Persist(ctx, delta)
}

func (d *ServiceDesktop) LoadSubagentActivity(ctx context.Context, taskID, subagentID string) ([]*domain.SubagentActivity, error) {
	return d.host.TaskDatabase.SubagentActivity(ctx, taskID, subagentID)
}

func (d *ServiceDesktop) LoadSubagentMetadata(ctx context.Context, engine domain.AgentEngine, subagentID string, sessionID *string) (*domain.SubagentMetadata, error) {
	metadata, err := d.host.Engines.SubagentMetadata(ctx, engine, subagentID, sessionID)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		return &domain.SubagentMetadata{}, nil
	}
	return metadata, nil
}

func (d *ServiceDesktop) OnThreadRequest(listener func(request contracts.ThreadRequest)) func() {
	return d.host.Events.OnThreadRequest(listener)
}

func (d *ServiceDesktop) AnswerThreadRequest(response contracts.ThreadResponse) {
	d.host.Runs.AnswerThread(response)
}
