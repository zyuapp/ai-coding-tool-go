package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// What a new thread takes from the thread it was made from: where it runs, and how it runs. Written into `thread`.
func inherited(source, thread *domain.Thread) {
	if source.ProjectID != nil && *source.ProjectID != "" {
		thread.ProjectID = source.ProjectID
	}
	thread.ExecutionPolicy = source.ExecutionPolicy
	thread.Engine = source.Engine
	if source.Model != nil && *source.Model != "" {
		thread.Model = source.Model
	}
	if source.Effort != nil && *source.Effort != "" {
		thread.Effort = source.Effort
	}
	if domain.CapabilitiesFor(string(source.Engine)).FastMode {
		fast := source.FastMode != nil && *source.FastMode
		thread.FastMode = &fast
	}
}

// A side chat: a thread of its own, starting empty, which forks the source's session on its first run.
func SideChatThread(source *domain.Thread, id, title string, at float64) *domain.Thread {
	thread := &domain.Thread{}
	thread.ID = id
	thread.Title = title
	inherited(source, thread)
	thread.Messages = []*domain.ConversationMessage{}
	thread.ContinuationStatus = domain.ContinuationStatusNone
	thread.LastChangeSnapshot = domain.ChangeSnapshot{Files: []string{}, CapturedAt: at}
	thread.CreatedAt = &at
	thread.RankedAt = at
	thread.UpdatedAt = at
	return thread
}

// Copies a thread, and puts the copy straight under it in the list. The copy carries the
// conversation, the session it was left in, and how the thread was set to run. What the thread has
// been through stays its own: no verdict, no findings, no schedule, and no checkout crosses over.
// Answers the new list and the copy.
func ForkedThreads(threads []*domain.Thread, source *domain.Thread, id string, at float64, title *string) ([]*domain.Thread, *domain.Thread) {
	fork := &domain.Thread{}
	fork.ID = id
	if title != nil && *title != "" {
		fork.Title = domain.ClampTitle(*title)
	}
	if fork.Title == "" {
		taken := make([]string, len(threads))
		for index, thread := range threads {
			taken[index] = thread.Title
		}
		fork.Title = domain.ForkTitle(source.Title, taken)
	}
	byUser := true
	fork.TitleByUser = &byUser
	inherited(source, fork)
	fork.Messages = slices.Clone(source.Messages)
	if fork.Messages == nil {
		fork.Messages = []*domain.ConversationMessage{}
	}
	if source.Continuation != nil {
		inheritedSession := true
		fork.Continuation = source.Continuation
		fork.InheritedContinuation = &inheritedSession
	}
	fork.ContinuationStatus = source.ContinuationStatus
	fork.LastChangeSnapshot = domain.ChangeSnapshot{Files: []string{}, CapturedAt: at}
	sortIndex := NextSortIndex(threads)
	fork.SortIndex = &sortIndex
	fork.CreatedAt = &at
	fork.RankedAt = at
	fork.UpdatedAt = at
	// The slot after the source's row, counted the way a drop counts: threads under a coordinator draw
	// beneath it, so they take no slot, and a copy of one goes under its coordinator's row.
	members := domain.CoordinatedThreadIds(threads)
	anchorID, anchored := source.ID, true
	if _, member := members[source.ID]; member {
		anchorID, anchored = domain.LeadIDOf(source)
	}
	var group []*domain.Thread
	for _, thread := range threads {
		if _, member := members[thread.ID]; thread.ArchivedAt == nil && !member && threadsSameString(thread.ProjectID, source.ProjectID) {
			group = append(group, thread)
		}
	}
	under := 0
	if anchored {
		under = slices.IndexFunc(OrderThreads(group), func(thread *domain.Thread) bool { return thread.ID == anchorID }) + 1
	}
	var projectID *string
	if source.ProjectID != nil {
		projectID = source.ProjectID
	}
	moved := MoveThread(append(slices.Clip(threads), fork), fork.ID, domain.ThreadDropTarget{ProjectID: projectID, Index: float64(under)})
	return moved, fork
}
