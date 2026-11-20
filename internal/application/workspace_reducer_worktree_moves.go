package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// A location change preserves the transcript and forks its continuation on the next run. The menu
// closes when the thread moved is the one on screen and the user asked for the move. A nil
// `destination` is the project's own checkout.
func Relocated(state *WorkspaceState, thread *domain.Thread, destination *domain.Worktree, asked bool) Step {
	label := "Local"
	var root, worktreeID *string
	if destination != nil {
		label = domain.WorktreeName(destination)
		root, worktreeID = &destination.Root, &destination.ID
	} else if project := ProjectFor(state, thread); project != nil {
		root = &project.Root
	}
	note := domain.CreateConversationMessage(domain.ConversationMessageKindSystem, "Moved to "+label+". Existing file changes stay in the previous checkout.", root, nil, nil, nil, nil)
	moving := Step{Edits: []OwnerEdit{RelocatedIn(ThreadsOf(state), thread.ID, worktreeID, note, Now)}}
	if asked && state.CurrentID != nil && *state.CurrentID == thread.ID {
		moving.ClosesMenu = true
	}
	return AndThen(state, moving, func(next *WorkspaceState) Step {
		reread := DiffReread(next, thread.ID)
		workspaceID := threadWorkspaceOf(next, threadByID(next.Threads, thread.ID))
		if workspaceID == nil || *workspaceID == "" {
			return reread
		}
		reread.Effects = append(slices.Clip(reread.Effects), WorkspaceEffectRefreshEnvironment{WorkspaceID: *workspaceID, TaskID: &thread.ID, FollowUp: new(true)})
		return reread
	})
}

// The checkout a move lands in: nil for the project's own, `here` when the thread is already there,
// or a non-empty `refusal` saying why it cannot go. `destination` is never a WorktreeDestinationNew.
func arrival(state *WorkspaceState, thread *domain.Thread, projectID string, destination domain.WorktreeDestination) (target *domain.Worktree, here bool, refusal string) {
	chosen, ok := destination.(domain.WorktreeDestinationWorktree)
	if !ok {
		return nil, thread.WorktreeID == nil || *thread.WorktreeID == "", ""
	}
	worktree := WorktreeByID(state, &chosen.ID)
	if worktree == nil {
		return nil, false, WorktreeMissingError
	}
	if worktree.ProjectID != projectID {
		return nil, false, "Choose a worktree in this thread's project."
	}
	if thread.WorktreeID != nil && worktree.ID == *thread.WorktreeID {
		return nil, true, ""
	}
	if slices.Contains(state.DeletingWorktrees, worktree.Root) || slices.ContainsFunc(state.Threads, func(item *domain.Thread) bool {
		return item.WorktreeID != nil && *item.WorktreeID == worktree.ID && slices.Contains(state.ReleasingWorktrees, item.ID)
	}) {
		return nil, false, WorktreeReleasingError
	}
	if state.ManagedWorktrees != nil {
		index := slices.IndexFunc(state.ManagedWorktrees, func(item *domain.ManagedWorktree) bool { return item.Root == worktree.Root })
		if index < 0 || state.ManagedWorktrees[index].Repository == nil || *state.ManagedWorktrees[index].Repository == "" {
			return nil, false, WorktreeMissingError
		}
	}
	return worktree, false, ""
}

// A checkout made from where the thread stands; the thread moves in once it lands, as `worktree.created` says.
// `then` may be nil.
func toNewWorktree(state *WorkspaceState, thread *domain.Thread, projectID string, then *string) Step {
	step := CreatingWorktree(state, thread.ID)
	root, _ := ThreadWorkspaceRoot(state, thread)
	effect := WorkspaceEffectCreateWorktree{TaskID: thread.ID, ProjectRoot: root, Move: new(true), Name: new(thread.Title), ProjectID: &projectID}
	if then != nil && *then != "" {
		effect.Then = then
	}
	step.Effects = []WorkspaceEffect{effect}
	return step
}

// What a thread that moved itself said it goes on with, first in line in the checkout it moved to.
// `then` may be nil.
func FollowUpQueued(state *WorkspaceState, taskID string, then *string) Step {
	thread := threadByID(state.Threads, taskID)
	if then == nil || *then == "" || thread == nil {
		return Step{}
	}
	origin, detail := domain.OriginFields(domain.MessageOriginThread{ThreadID: &thread.ID, Title: thread.Title})
	followUp := &QueuedMessage{HeldMessage: domain.HeldMessage{
		ID:          NewID(),
		Text:        *then,
		Prompt:      "You moved to this checkout, as you asked. Go on with what you said you would do next:\n\n" + *then,
		Attachments: []string{},
		Origin:      origin,
		Detail:      detail,
	}}
	return Step{Edits: []OwnerEdit{QueuedIn(RunsOf(state), taskID, slices.Concat([]*QueuedMessage{followUp}, QueuedFor(state, taskID)))}}
}

// A move its thread asked to make once its run ends, taken back.
func moveAfterRunDropped(state *WorkspaceState, taskID string) Step {
	if state.MovesAfterRun[taskID] == nil {
		return Step{}
	}
	return Step{Edits: []OwnerEdit{MoveAfterRunIn(WorktreesOf(state), taskID, nil)}}
}

func WorktreeMoved(state *WorkspaceState, input contracts.TaskCommandTaskMoveWorktree) Step {
	var thread *domain.Thread
	if taskID := TargetID(state, input.TaskID); taskID != nil {
		thread = threadByID(state.Threads, *taskID)
	}
	project := ProjectFor(state, thread)
	if thread == nil || project == nil || project.WorkspaceID == nil || *project.WorkspaceID == "" {
		return Step{Refusal: new(WorktreeProjectError)}
	}
	if slices.Contains(state.CreatingWorktrees, thread.ID) {
		return Step{Refusal: new(WorktreeCreatingError)}
	}
	if _, leaving := LeavingThreadIds(state)[thread.ID]; leaving {
		return Step{Refusal: new(WorktreeReleasingError)}
	}
	destination := input.Destination
	afterRun := input.AfterRun != nil && *input.AfterRun
	// A thread asked: this one moving itself, or one on another computer that the link names.
	if afterRun || input.Sender != nil {
		if refusal := CoordinatorAskRefusal(thread, input, false); refusal != nil && *refusal != "" {
			return Step{Refusal: refusal}
		}
	}
	_, creating := destination.(domain.WorktreeDestinationNew)
	var target *domain.Worktree
	if !creating {
		found, here, refusal := arrival(state, thread, project.ID, destination)
		if refusal != "" {
			return Step{Refusal: &refusal}
		}
		// Asking for where the thread already is takes back a move it was waiting to make.
		if here {
			return moveAfterRunDropped(state, thread.ID)
		}
		target = found
	}
	then := input.Then
	if then != nil && *then == "" {
		then = nil
	}
	if IsWorking(state, thread.ID) {
		// Only the run that asked can end before the move; anything else keeping the thread busy refuses it.
		if !afterRun || state.ActiveRuns[thread.ID] == nil {
			return Step{Refusal: new(WorktreeRunningError)}
		}
		return Step{ClearsError: true, Edits: []OwnerEdit{MoveAfterRunIn(WorktreesOf(state), thread.ID, &domain.MoveAfterRun{Destination: destination, Then: then})}}
	}
	staying := moveAfterRunDropped(state, thread.ID)
	if creating {
		return AndThen(state, staying, func(next *WorkspaceState) Step { return toNewWorktree(next, thread, project.ID, input.Then) })
	}
	staying.ClearsError = true
	moving := AndThen(state, staying, func(next *WorkspaceState) Step { return Relocated(next, thread, target, true) })
	if then == nil {
		return moving
	}
	return InTurn(state, moving, []func(*WorkspaceState) Step{
		func(next *WorkspaceState) Step { return FollowUpQueued(next, thread.ID, then) },
		func(next *WorkspaceState) Step { return Drained(next, thread.ID, QueueIntentDeliver) },
	})
}

// A thread that moved itself goes once the run that asked has ended, ahead of anything queued behind
// that run. Whatever changed meanwhile is checked again, and a move that can no longer happen says so,
// dropping what the thread said it would go on with there. A run the user stopped still moves, and
// goes no further. Nothing on screen asked for this move, so whatever the user has open stays open.
func MoveAfterRunSettled(state *WorkspaceState, taskID string, status domain.RunStatus) Step {
	move := state.MovesAfterRun[taskID]
	if move == nil {
		return Step{}
	}
	destination := move.Destination
	then := move.Then
	if status == domain.RunStatusCancelled {
		then = nil
	}
	dropping := moveAfterRunDropped(state, taskID)
	rest := Afterward(state, SliceTransition(dropping))
	thread := threadByID(rest.Threads, taskID)
	project := ProjectFor(rest, thread)
	if thread == nil || thread.ArchivedAt != nil {
		return dropping
	}
	_, creating := destination.(domain.WorktreeDestinationNew)
	var target *domain.Worktree
	here := false
	refusal := ""
	if project == nil || project.WorkspaceID == nil || *project.WorkspaceID == "" {
		refusal = WorktreeProjectError
	} else if asked := CoordinatorAskRefusal(thread, contracts.TaskCommandTaskMoveWorktree{Destination: destination}, false); asked != nil && *asked != "" {
		refusal = *asked
	} else if _, leaving := LeavingThreadIds(rest)[taskID]; leaving {
		refusal = WorktreeReleasingError
	} else if !creating {
		target, here, refusal = arrival(rest, thread, project.ID, destination)
	}
	if refusal == "" && here {
		return AndThen(state, dropping, func(next *WorkspaceState) Step { return FollowUpQueued(next, taskID, then) })
	}
	if refusal != "" {
		note := domain.CreateFailureMessage("Did not move. " + refusal)
		return AndThen(state, dropping, func(next *WorkspaceState) Step {
			return Step{Edits: []OwnerEdit{SaidIn(ThreadsOf(next), taskID, note, Now())}}
		})
	}
	if creating {
		return AndThen(state, dropping, func(next *WorkspaceState) Step { return toNewWorktree(next, thread, project.ID, then) })
	}
	return InTurn(state, dropping, []func(*WorkspaceState) Step{
		func(next *WorkspaceState) Step { return Relocated(next, thread, target, false) },
		func(next *WorkspaceState) Step { return FollowUpQueued(next, taskID, then) },
	})
}
