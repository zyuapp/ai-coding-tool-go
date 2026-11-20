package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The menus and confirmations as one input leaves them. Only the functions here make one.
type dialogsChange = DialogsEdit

func DialogsOf(state *WorkspaceState) DialogsEdit {
	return DialogsEdit{View: DialogsState{ForkName: state.ForkName, ArchivePrompt: state.ArchivePrompt, OpenMenu: state.OpenMenu, ReviewPicker: state.ReviewPicker}}
}

// MenuIn is the menu open over the window, or none.
func MenuIn(edit DialogsEdit, menu *string) DialogsEdit {
	edit.View.OpenMenu = menu
	return edit
}

// ForkNameIn is the fork naming dialog, open on a fork or closed.
func ForkNameIn(edit DialogsEdit, forkName *ForkName) DialogsEdit {
	edit.View.ForkName = forkName
	return edit
}

// ArchivePromptIn is the archive confirmation, open on a thread or closed.
func ArchivePromptIn(edit DialogsEdit, taskID *string) DialogsEdit {
	edit.View.ArchivePrompt = taskID
	return edit
}

// ReviewPickerIn is the review picker, open on a thread at a step or closed.
func ReviewPickerIn(edit DialogsEdit, picker *ReviewPicker) DialogsEdit {
	edit.View.ReviewPicker = picker
	return edit
}

func WorkspaceDialogsProjectEditorView(state *WorkspaceState) *ProjectEditorView {
	edit := state.ProjectEdit
	if edit == nil {
		return nil
	}
	index := slices.IndexFunc(state.Projects, func(item *domain.Project) bool { return item.ID == edit.ProjectID })
	if index < 0 {
		return nil
	}
	project := state.Projects[index]
	checkouts := 0
	for _, worktree := range state.Worktrees {
		if worktree.ProjectID == project.ID {
			checkouts++
		}
	}
	return &ProjectEditorView{Project: *project, Checkouts: float64(checkouts), Saving: edit.Saving, Error: edit.Error}
}

func WorkspaceDialogsWorktreeMoveView(state *WorkspaceState) *WorktreeMoveView {
	move := state.WorktreeMove
	if move == nil {
		return nil
	}
	index := slices.IndexFunc(state.Threads, func(item *domain.Thread) bool { return item.ID == move.TaskID })
	if index < 0 {
		return nil
	}
	thread := state.Threads[index]
	changes := 0
	if workspaceID, ok := ThreadWorkspaceID(state, thread); ok && workspaceID != "" {
		if environment, ok := state.Environments[workspaceID].(contracts.ChangedFilesResultAvailable); ok {
			changes = len(environment.Files)
		}
	}
	others := 0
	if worktree := WorktreeFor(state, thread); worktree != nil {
		others = max(len(WorktreeClaimants(state, worktree.ID))-1, 0)
	}
	return &WorktreeMoveView{Worktree: move.Worktree, Changes: float64(changes), Others: float64(others)}
}

// WorkspaceDialogsArchivePromptView is nil once the thread is gone or already archived, so the
// question never outlives what it asks about. TS defaults `taskID` to state.archivePrompt.
func WorkspaceDialogsArchivePromptView(state *WorkspaceState, taskID *string) *ArchivePromptView {
	if taskID == nil {
		taskID = state.ArchivePrompt
	}
	if taskID == nil {
		return nil
	}
	holder := state
	if computer := ComputerOfThread(state, *taskID); computer != nil && computer.State != nil {
		holder = computer.State
	}
	index := slices.IndexFunc(holder.Threads, func(item *domain.Thread) bool { return item.ID == *taskID })
	if index < 0 || holder.Threads[index].ArchivedAt != nil {
		return nil
	}
	_, running := holder.ActiveRuns[*taskID]
	return &ArchivePromptView{TaskID: *taskID, Title: holder.Threads[index].Title, Running: running}
}

func SettledArchivePrompt(state *WorkspaceState) *WorkspaceState {
	if state.ArchivePrompt == nil || WorkspaceDialogsArchivePromptView(state, nil) != nil {
		return state
	}
	next := *state
	next.ArchivePrompt = nil
	return &next
}

// WorkspaceDialogs is what workspaceDialogsView returns.
type WorkspaceDialogs struct {
	ProjectAdd    *ProjectAddState
	ProjectEditor *ProjectEditorView
	WorktreeMove  *WorktreeMoveView
	ForkName      *ForkName
	ArchivePrompt *ArchivePromptView
}

// WorkspaceDialogsView: dialogs always describe the window that opened them, including over a remote thread.
func WorkspaceDialogsView(state *WorkspaceState) WorkspaceDialogs {
	return WorkspaceDialogs{
		ProjectAdd:    state.ProjectAdd,
		ProjectEditor: WorkspaceDialogsProjectEditorView(state),
		WorktreeMove:  WorkspaceDialogsWorktreeMoveView(state),
		ForkName:      state.ForkName,
		ArchivePrompt: WorkspaceDialogsArchivePromptView(state, nil),
	}
}
