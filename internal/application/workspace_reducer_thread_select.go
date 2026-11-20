package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Where the window is: the thread on screen and how the user got there, and the draft of the next
// thread when none is. Only the functions here change them, so putting the user on a thread is one
// thing however a command gets there.

// Where the window is as one input leaves it. Only the functions here make one.
type navigationChange = NavigationEdit

func NavigationOf(state *WorkspaceState) NavigationEdit {
	return NavigationEdit{View: OwnFields[NavigationView](state)}
}

// The window on a thread, the draft's project and the folder following it there. A nil folder keeps the last one.
func OnThreadIn(edit NavigationEdit, taskID string, projectID, folder *string) NavigationEdit {
	view := edit.View
	view.CurrentID = &taskID
	view.DraftProjectID = projectID
	if folder != nil {
		view.LastFolder = folder
	}
	return NavigationEdit{View: view}
}

// Threads gone for good, gone from history, the cursor staying on the visit it was on or the one before it.
func VisitsForgottenIn(edit NavigationEdit, removed jsonx.Set[string]) NavigationEdit {
	view := edit.View
	history := WithoutMatching(view.History, func(threadID string) bool { return removed.Has(threadID) })
	historyIndex := -1
	for index := 0; float64(index) <= view.HistoryIndex && index < len(view.History); index++ {
		if !removed.Has(view.History[index]) {
			historyIndex++
		}
	}
	view.History = history
	view.HistoryIndex = float64(min(historyIndex, len(history)-1))
	return NavigationEdit{View: view}
}

// The place in history back or forward lands on.
func HistoryAtIn(edit NavigationEdit, index float64) NavigationEdit {
	view := edit.View
	view.HistoryIndex = index
	return NavigationEdit{View: view}
}

// No thread on screen, and a fresh draft in `projectID`, in the checkout it names if any, with nothing
// else of the last draft's placement. A nil folder keeps the last one.
func FreshDraftIn(edit NavigationEdit, projectID, worktreeID, folder *string) NavigationEdit {
	view := edit.View
	view.CurrentID = nil
	view.DraftProjectID = projectID
	view.DraftBranch = nil
	view.DraftWorktree = false
	view.DraftWorktreeID = worktreeID
	view.DraftRole = nil
	if folder != nil {
		view.LastFolder = folder
	}
	return NavigationEdit{View: view}
}

// A folder just opened: the window leaves any thread for a draft there.
func ProjectOpenedIn(edit NavigationEdit, projectID, root string) NavigationEdit {
	view := edit.View
	view.CurrentID = nil
	view.DraftProjectID = &projectID
	view.LastFolder = &root
	return NavigationEdit{View: view}
}

// The last folder opened, without the window moving.
func FolderOpenedIn(edit NavigationEdit, root string) NavigationEdit {
	if edit.View.LastFolder != nil && *edit.View.LastFolder == root {
		return edit
	}
	view := edit.View
	view.LastFolder = &root
	return NavigationEdit{View: view}
}

// A project that is gone: off screen if its thread was, out of the draft, and no longer the last folder.
func ProjectLeftIn(edit NavigationEdit, projectID string, root *string, onScreen bool) NavigationEdit {
	view := edit.View
	if onScreen {
		view.CurrentID = nil
	}
	if view.DraftProjectID != nil && *view.DraftProjectID == projectID {
		view.DraftProjectID = nil
	}
	if root != nil && view.LastFolder != nil && *root == *view.LastFolder {
		view.LastFolder = nil
	}
	return NavigationEdit{View: view}
}

// A project that moved folders takes the last folder with it when it was that one.
func FolderMovedIn(edit NavigationEdit, root string) NavigationEdit {
	view := edit.View
	view.LastFolder = &root
	return NavigationEdit{View: view}
}

// The window off a thread that is no longer listed in `threads`.
func OnListedThreadIn(edit NavigationEdit, threads []*domain.Thread) NavigationEdit {
	current := edit.View.CurrentID
	if current == nil || threadsFind(threads, *current) != nil {
		return edit
	}
	view := edit.View
	view.CurrentID = nil
	return NavigationEdit{View: view}
}

// The window off a thread that was just filed away.
func OffThreadIn(edit NavigationEdit, taskID string) NavigationEdit {
	if edit.View.CurrentID == nil || *edit.View.CurrentID != taskID {
		return edit
	}
	view := edit.View
	view.CurrentID = nil
	return NavigationEdit{View: view}
}

// Whether the next new thread gets a checkout of its own, which drops any the user had picked.
func DraftCheckoutIn(edit NavigationEdit, worktree bool) NavigationEdit {
	view := edit.View
	view.DraftWorktree = worktree
	view.DraftWorktreeID = nil
	return NavigationEdit{View: view}
}

// The branch the next new thread starts from, or none.
func DraftBranchIn(edit NavigationEdit, branch *DraftBranch) NavigationEdit {
	view := edit.View
	view.DraftBranch = branch
	return NavigationEdit{View: view}
}

// A checkout that is gone, which a draft pointed at it no longer starts in.
func DraftCheckoutGoneIn(edit NavigationEdit, worktreeID string) NavigationEdit {
	if edit.View.DraftWorktreeID == nil || *edit.View.DraftWorktreeID != worktreeID {
		return edit
	}
	view := edit.View
	view.DraftWorktreeID = nil
	return NavigationEdit{View: view}
}

// The part the next new thread plays.
func DraftRoleIn(edit NavigationEdit, role *domain.ThreadRole) NavigationEdit {
	view := edit.View
	view.DraftRole = role
	return NavigationEdit{View: view}
}

// The agent choices the next new thread starts with, already held to its engine.
func DraftChoiceIn(edit NavigationEdit, draft domain.AgentDraft) NavigationEdit {
	view := edit.View
	view.DraftEngine, view.DraftModel, view.DraftEffort, view.DraftPolicy, view.DraftFastMode = draft.Engine, draft.Model, draft.Effort, draft.Policy, draft.FastMode
	return NavigationEdit{View: view}
}

// Puts the user on a thread: what it holds becomes read, and the app follows it to its folder.
func Landed(state *WorkspaceState, taskID string) Step {
	thread := threadsFind(state.Threads, taskID)
	var projectID, folder *string
	if thread != nil && thread.ProjectID != nil {
		projectID = thread.ProjectID
	}
	if project := ProjectFor(state, thread); project != nil {
		root := project.Root
		folder = &root
	}
	return Step{Edits: []OwnerEdit{OnThreadIn(NavigationOf(state), taskID, projectID, folder), RowReadIn(ThreadsOf(state), &taskID)}, ClearsError: true}
}

// Back or forward to the thread at `index` in history, landing on it as any visit does.
func Visited(state *WorkspaceState, index int) Step {
	moved := Step{Edits: []OwnerEdit{HistoryAtIn(NavigationOf(state), float64(index))}}
	if index < 0 || index >= len(state.History) {
		return moved
	}
	taskID := state.History[index]
	return AndThen(state, moved, func(next *WorkspaceState) Step { return Landed(next, taskID) })
}

// A side chat, or a thread under a coordinator, opens as a tab in the dock of the thread it belongs to.
func Selected(state *WorkspaceState, chosen string) Step {
	var chat *SideChat
	for _, item := range state.SideChats {
		if item.ID == chosen {
			chat = item
			break
		}
	}
	var lead *domain.Thread
	if chat == nil {
		lead = domain.CoordinatorOf(state.Threads, threadsFind(state.Threads, chosen))
	}
	taskID := chosen
	switch {
	case chat != nil:
		taskID = chat.SourceThreadID
	case lead != nil:
		taskID = lead.ID
	}
	landing := Landed(state, taskID)
	if taskID == chosen {
		return landing
	}
	return AndThen(state, landing, func(onThread *WorkspaceState) Step {
		tabs := DockFor(onThread, taskID).ThreadTabs
		opened := DocksOf(onThread)
		if lead != nil && !slices.Contains(tabs, chosen) {
			opened = EditDock(opened, taskID, func(dock *ThreadDock) { dock.ThreadTabs = append(slices.Clip(tabs), chosen) })
		}
		focused, effects := FocusDockTabIn(ShowDockTabIn(opened, taskID, chosen), taskID, chosen)
		if lead != nil {
			effects = slices.Concat(effects, FollowUps(RefreshThreadEnvironment(onThread, chosen)))
		}
		return Step{Edits: []OwnerEdit{ReadIn(ThreadsOf(onThread), chosen), focused}, Effects: effects}
	})
}
