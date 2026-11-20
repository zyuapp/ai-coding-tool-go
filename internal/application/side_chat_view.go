package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var sideChatViewNoQueued = []*QueuedMessageView{}

// One thread drawn in a dock tab, projected from the thread and its active run.
func sideChatViewDockConversationView(state *WorkspaceState, thread *domain.Thread) DockConversationView {
	active := RunRecord(state.ActiveRuns[thread.ID])
	view := DockConversationView{
		ID:               thread.ID,
		Title:            thread.Title,
		Thread:           *thread,
		Prompt:           state.Prompts[thread.ID],
		Annotations:      threadsNonNil(AnnotationsFor(state, thread.ID)),
		AnnotationReveal: AnnotationRevealFor(state, thread.ID),
		Pastes:           threadsNonNil(PastesFor(state, thread.ID)),
		Images:           threadsNonNil(ImagesFor(state, thread.ID)),
		Files:            threadsNonNil(FilesFor(state, thread.ID)),
		Running:          active != nil,
		Retrying:         RunRetry(state.ActiveRuns[thread.ID]),
		Status:           RunStatus(state, &thread.ID),
		StreamingTail:    state.StreamingTails[thread.ID],
		QueuedMessages:   QueuedView(state, thread.ID),
		ReadingPoint:     state.ReadingPoints[thread.ID],
	}
	catalog := CatalogOf(state)
	if model, ok := domain.ThreadModel(&catalog, thread); ok {
		view.Model = &model
	}
	if view.QueuedMessages == nil {
		view.QueuedMessages = sideChatViewNoQueued
	}
	if active != nil {
		if len(active.Questions) > 0 {
			view.Question = active.Questions[0]
		}
		view.Compacting = active.Status == ActiveRunRunningStatusCompacting
		if active.Status == ActiveRunRunningStatusAwaitingApproval {
			view.Approval = state.Approvals[active.RunID]
		}
	}
	return view
}

// threadsNonNil is items, or an empty slice for none, so a view encodes `[]`.
func threadsNonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

// One open side chat projected from its owning thread and active run: none when the thread is gone.
func SideChatViewSideChatView(state *WorkspaceState, chat *SideChat) []*SideChatView {
	thread := threadsFind(state.Threads, chat.ID)
	if thread == nil {
		return []*SideChatView{}
	}
	view := sideChatViewDockConversationView(state, thread)
	return []*SideChatView{{
		ID:               view.ID,
		SourceThreadID:   chat.SourceThreadID,
		Error:            chat.Error,
		Title:            view.Title,
		Thread:           view.Thread,
		Model:            view.Model,
		Prompt:           view.Prompt,
		Annotations:      view.Annotations,
		AnnotationReveal: view.AnnotationReveal,
		Pastes:           view.Pastes,
		Images:           view.Images,
		Files:            view.Files,
		Running:          view.Running,
		Compacting:       view.Compacting,
		Retrying:         view.Retrying,
		Status:           view.Status,
		StreamingTail:    view.StreamingTail,
		QueuedMessages:   view.QueuedMessages,
		Approval:         view.Approval,
		Question:         view.Question,
		ReadingPoint:     view.ReadingPoint,
	}}
}

// One thread's tab in its coordinator's dock, with where its work stands: none when the thread is gone.
func SideChatViewThreadTabView(state *WorkspaceState, taskID string, busy, blocked jsonx.Set[string]) []*ThreadTabView {
	thread := threadsFind(state.Threads, taskID)
	if thread == nil {
		return []*ThreadTabView{}
	}
	standing := CoordinatedThreadStatusCoordinatedThreadStatus(thread, busy, blocked)
	view := &ThreadTabView{
		DockConversationView: sideChatViewDockConversationView(state, thread),
		Standing:             standing.Status,
		Summary:              standing.Summary,
	}
	if project := ProjectFor(state, thread); project != nil {
		view.Folder = project.Root
	}
	var workspaceID *string
	if id, ok := ThreadWorkspaceID(state, thread); ok && id != "" {
		workspaceID = &id
		view.WorkspaceID = workspaceID
		view.Environment = state.Environments[id]
	}
	view.PullRequest = PullRequestFor(state.PullRequest, workspaceID, view.Environment)
	for _, automation := range state.Automations {
		if automation.TaskID == thread.ID {
			view.Automation = automation
			break
		}
	}
	return []*ThreadTabView{view}
}
