package application

import (
	"fmt"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const (
	reopenProjectError           = "Reopen this project folder before running a task."
	sameProjectError             = "Choose the same project folder to continue this task."
	MissingProjectError          = "This task's project is unavailable. Reopen the project folder before running it."
	RunningProjectError          = "Stop the running tasks before removing this project."
	ProjectWorktreesError        = "Delete this project's worktrees before removing the project."
	busyAutomationError          = "This task is already running. The automation will run on its next tick."
	WorktreeProjectError         = "Open this thread in a project folder before giving it a worktree."
	WorktreeMissingError         = "That worktree is not one this app is keeping."
	WorktreeElsewhereError       = "That worktree is a checkout of another project."
	TerminalFolderError          = "Open a project folder before starting a terminal."
	WorktreeRunningError         = "Stop this thread's run before changing where it works."
	WorktreeCreatingError        = "This thread's worktree is still being created."
	WorktreeReleasingError       = "This thread's worktree is still being removed."
	ArchivedSendError            = "This thread is archived, so only the user can pick it back up, by writing to it or restoring it from Archived threads in Settings."
	WorktreeCoordinatorError     = "A coordinator stays in its project's checkout, since it changes nothing itself. Start a thread with start_thread and worktree: true to do the work in a worktree."
	CoordinatorRoleError         = "A coordinator keeps its role until the user changes it from the thread menu."
	WorktreeCoordinatorRoleError = "A coordinator stays in its project's checkout, since it changes nothing itself, and this thread works in a worktree. Start a coordinator with start_thread and role \"coordinator\" instead."
)

// CoordinatorAskRefusal is what no thread asks of a coordinator, here or on a paired computer. ask is
// a contracts.TaskCommandTaskMoveWorktree (only Destination is read) or a contracts.TaskCommandTaskSetRole
// (only Role is read); nil is no refusal.
func CoordinatorAskRefusal(subject *domain.Thread, ask contracts.AppCommand, creatingWorktree bool) *string {
	refusal := func(message string) *string { return &message }
	switch ask := ask.(type) {
	case contracts.TaskCommandTaskMoveWorktree:
		if _, local := ask.Destination.(domain.WorktreeDestinationLocal); !local && domain.IsCoordinator(subject) {
			return refusal(WorktreeCoordinatorError)
		}
		return nil
	case contracts.TaskCommandTaskSetRole:
		if ask.Role == nil || *ask.Role != domain.ThreadRoleCoordinator {
			if subject.Role != nil && *subject.Role == domain.ThreadRoleCoordinator {
				return refusal(CoordinatorRoleError)
			}
			return nil
		}
		if subject.WorktreeID != nil || creatingWorktree {
			return refusal(WorktreeCoordinatorRoleError)
		}
	}
	return nil
}

// What a thread's own report, decision or memory change is told when nothing was recorded.
const (
	NoCoordinator       = "This thread works under no coordinator, so there is no one to report to and nothing was recorded. Say it in your reply instead."
	OutsideCoordination = "Only a coordinator and the threads working under it can raise decisions, so nothing was recorded. Ask the user in your reply instead."
	NoMemory            = "Only a coordinator keeps a memory."
)

var DecisionsFull = fmt.Sprintf("This thread already has %d decisions waiting on the user, so this one was not raised. Wait for their answers, or ask in your reply instead.", domain.MaxOpenDecisions)

const (
	CheckoutRunningError = "Stop the threads running in this project before starting one on another branch."
	SwitchRunningError   = "Stop the threads running in this checkout before switching it to another branch."
	SwitchProjectError   = "Open this thread in a project folder before switching branches."
	FileFolderError      = "Open this thread in a project folder before opening a file from it."
	AppFolderError       = "Open this thread in a project folder before opening it in another application."

	BrowserURLError = "That is not a page the browser can open."
	BrowserTabError = "The browser has no page open to act on."
)

// WorkspaceErrors is the refusals the renderer knows by name.
var WorkspaceErrors = map[string]string{
	"reopenProject":           reopenProjectError,
	"sameProject":             sameProjectError,
	"busyAutomation":          busyAutomationError,
	"projectWorktrees":        ProjectWorktreesError,
	"worktreeProject":         WorktreeProjectError,
	"worktreeMissing":         WorktreeMissingError,
	"worktreeElsewhere":       WorktreeElsewhereError,
	"terminalFolder":          TerminalFolderError,
	"worktreeRunning":         WorktreeRunningError,
	"worktreeCreating":        WorktreeCreatingError,
	"worktreeReleasing":       WorktreeReleasingError,
	"worktreeCoordinator":     WorktreeCoordinatorError,
	"worktreeCoordinatorRole": WorktreeCoordinatorRoleError,
	"coordinatorRole":         CoordinatorRoleError,
	"checkoutRunning":         CheckoutRunningError,
	"switchRunning":           SwitchRunningError,
	"switchProject":           SwitchProjectError,
	"fileFolder":              FileFolderError,
	"appFolder":               AppFolderError,
}
