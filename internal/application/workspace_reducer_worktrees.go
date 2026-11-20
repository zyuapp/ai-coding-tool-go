package application

import (
	"slices"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var WorktreeInputs = []string{
	"worktree.open-thread", "view.move-worktree", "task.set-worktree", "task.move-worktree", "task.set-branch", "task.checkout-branch",
	"worktree.created", "worktree.released", "worktree.deleted", "checkout.finished",
}

// The checkouts Settings lists and manages, which change only the worktrees' own state.
var WorktreeManagementInputs = []string{
	"worktree.filter-project", "worktree.confirm-delete", "worktree.confirm-delete-safe", "worktree.delete-safe", "worktree.set-missing-open", "worktree.set-threads-open",
	"worktree.refresh", "worktree.reveal", "worktree.delete", "worktree.failed", "worktrees.loaded", "worktrees.failed", "worktree.release-failed",
}

// The first seven characters of a commit, as `slice(0, 7)` takes them.
func shortHash(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// The snapshot's short commit, else the first seven characters of its commit.
func snapshotShort(snapshot contracts.WorktreeSnapshotResult) string {
	if snapshot.ShortCommit != nil {
		return *snapshot.ShortCommit
	}
	return shortHash(*snapshot.Commit)
}

func hasText(text *string) bool {
	return text != nil && *text != ""
}

// The note a thread hears when its checkout goes, pointing at the snapshot ref when there is one.
func checkoutGoneNote(text string, ref *string) func() *domain.ConversationMessage {
	return func() *domain.ConversationMessage {
		var detail *string
		if hasText(ref) {
			detail = new("Recover it with git show " + *ref)
		}
		return domain.CreateConversationMessage(domain.ConversationMessageKindSystem, text, detail, nil, nil, nil, nil)
	}
}

// Where threads work. `input` is one of the WorktreeInputs.
func ReduceWorktrees(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.TaskCommandTaskMoveWorktree:
		return SliceTransition(WorktreeMoved(state, input))

	case contracts.WorktreeCommandWorktreeOpenThread:
		return SliceTransition(OpenWorktreeThread(state, input))

	// Asks before moving, but only where the answer could cost something. A thread with no checkout
	// yet, and a clean one walking back to the project, have nothing to lose, so they just go.
	case contracts.ViewCommandViewMoveWorktree:
		if input.Worktree == nil {
			return SliceTransition{Patch: func(next *WorkspaceState) { next.WorktreeMove = nil }}
		}
		wanted := *input.Worktree
		var thread *domain.Thread
		if taskID := TargetID(state, nil); taskID != nil && *taskID != "" {
			thread = threadByID(state.Threads, *taskID)
		}
		move := contracts.TaskCommandTaskSetWorktree{Worktree: wanted}
		if thread == nil {
			return ReduceWorktrees(state, move)
		}
		if wanted == hasText(thread.WorktreeID) {
			return SliceTransition{}
		}
		holding := 0
		if workspaceID := threadWorkspaceOf(state, thread); workspaceID != nil && *workspaceID != "" {
			if environment, ok := state.Environments[*workspaceID].(contracts.ChangedFilesResultAvailable); ok {
				holding = len(environment.Files)
			}
		}
		if !wanted && holding == 0 {
			return ReduceWorktrees(state, move)
		}
		pending := &WorktreesStateWorktreeMove{TaskID: thread.ID, Worktree: wanted}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.WorktreeMove = pending }}

	case contracts.TaskCommandTaskSetWorktree:
		return SliceTransition(WorktreeSet(state, input))

	// Only a thread yet to be created can be told where to start; an existing one already is.
	case contracts.TaskCommandTaskSetBranch:
		var branch *DraftBranch
		if input.Branch != nil {
			branch = &DraftBranch{Name: *input.Branch, Create: input.Create != nil && *input.Create}
		}
		return SliceTransition{ClearsError: true, Edits: []OwnerEdit{DraftBranchIn(NavigationOf(state), branch)}}

	case contracts.TaskCommandTaskCheckoutBranch, WorktreeEventCheckoutFinished:
		return reduceCheckout(state, input)

	case WorktreeEventWorktreeCreated:
		settling := CreatedIn(WorktreesOf(state), input.TaskID).View
		cleared := !jsonx.Same(settling.CreatingWorktrees, state.CreatingWorktrees)
		creating := settling.CreatingWorktrees
		clear := func(next *WorkspaceState) {
			if cleared {
				next.CreatingWorktrees = creating
			}
		}
		unchanged := SliceTransition{}
		if cleared {
			unchanged.Patch = clear
		}
		move := input.Move != nil && *input.Move
		thread := threadByID(state.Threads, input.TaskID)
		// A checkout that outlives the request stays on disk for manual management.
		if thread == nil || !hasText(thread.ProjectID) || (hasText(thread.WorktreeID) && !move) {
			return unchanged
		}
		created := input.Worktree
		projectID := *thread.ProjectID
		if input.ProjectID != nil {
			projectID = *input.ProjectID
		}
		worktree := &domain.Worktree{
			ID: created.ID, Name: created.Name, ProjectID: projectID, Root: created.Root, WorkspaceID: created.WorkspaceID,
			BaseCommit: created.BaseCommit, CreatedAt: created.CreatedAt, LastUsedAt: created.LastUsedAt,
		}
		worktrees := append(slices.Clip(state.Worktrees), worktree)
		if move {
			if !slices.Contains(state.CreatingWorktrees, thread.ID) {
				return unchanged
			}
			patch := func(next *WorkspaceState) {
				clear(next)
				next.Worktrees, next.ManagedWorktrees, next.WorktreeManagementLoading = worktrees, nil, true
			}
			listing := WorkspaceEffectListWorktrees{}
			if *thread.ProjectID != worktree.ProjectID {
				return SliceTransition{Patch: patch, Effects: []WorkspaceEffect{listing}}
			}
			// What the thread said it goes on with, then what was queued behind the run that asked to move, starts here.
			return withPatch(patch, InTurn(Afterward(state, SliceTransition{Patch: patch}), Step{}, []func(*WorkspaceState) Step{
				func(next *WorkspaceState) Step { return Relocated(next, thread, worktree, true) },
				func(next *WorkspaceState) Step {
					queued := FollowUpQueued(next, thread.ID, input.Then)
					queued.Effects = []WorkspaceEffect{listing}
					return queued
				},
				func(next *WorkspaceState) Step { return Drained(next, thread.ID, QueueIntentDeliver) },
			}))
		}
		note := domain.CreateConversationMessage(domain.ConversationMessageKindSystem, "Moved into a worktree at "+worktree.Root, new("Detached at "+shortHash(worktree.BaseCommit)), nil, nil, nil, nil)
		patch := func(next *WorkspaceState) {
			clear(next)
			next.Worktrees = worktrees
		}
		entering := Step{Edits: []OwnerEdit{EnteredCheckoutIn(ThreadsOf(state), input.TaskID, worktree.ID, note, Now())}}
		return withPatch(patch, AndThen(Afterward(state, SliceTransition{Patch: patch}), entering, func(next *WorkspaceState) Step {
			return DiffReread(next, input.TaskID)
		}))

	case WorktreeEventWorktreeReleased:
		worktree := WorktreeFor(state, threadByID(state.Threads, input.TaskID))
		if worktree == nil {
			return SliceTransition{}
		}
		snapshot := input.Snapshot
		text := "Returned to the project checkout. The worktree had nothing uncommitted, and was removed."
		if hasText(snapshot.Commit) {
			text = "Returned to the project checkout. Uncommitted work was committed as " + snapshotShort(snapshot) + ", and the worktree was removed."
		}
		dropping := WorktreeDropped(state, ThreadsOf(state), worktree.ID, checkoutGoneNote(text, snapshot.Ref))
		return SliceTransition(AndThen(state, dropping, func(next *WorkspaceState) Step { return DiffReread(next, input.TaskID) }))

	case WorktreeEventWorktreeDeleted:
		var worktree *domain.Worktree
		if index := slices.IndexFunc(state.Worktrees, func(item *domain.Worktree) bool { return item.ID == input.WorktreeID || item.Root == input.Root }); index >= 0 {
			worktree = state.Worktrees[index]
		}
		snapshot := input.Snapshot
		missingOnly := input.MissingOnly != nil && *input.MissingOnly
		text := "Worktree deleted. Back on the project checkout."
		if missingOnly {
			text = "Missing worktree folder forgotten. Back on the project checkout."
		} else if hasText(snapshot.Commit) {
			text = "Worktree deleted. Loose work was committed as " + snapshotShort(snapshot) + " first."
		}
		recovery := ""
		if !missingOnly && hasText(snapshot.Ref) {
			recovery = "git show " + *snapshot.Ref
		} else if !missingOnly && hasText(snapshot.Commit) {
			commit := *snapshot.Commit
			if snapshot.ShortCommit != nil {
				commit = *snapshot.ShortCommit
			}
			recovery = "git show " + commit
		}
		notice := "Deleted " + input.Root + "."
		if missingOnly {
			notice = "Forgot " + input.Root + ". Thread history kept."
		} else if hasText(snapshot.Ref) {
			notice += " Recover it with " + recovery + "."
		} else if hasText(snapshot.Commit) {
			notice += " Recover loose work with " + recovery + "."
		}
		bulk := state.WorktreeSettings.Bulk
		inBulk := bulk != nil && slices.Contains(bulk.Pending, input.Root)
		var recover *string
		if recovery != "" {
			folder, ok := lastSegment(input.Root)
			if !ok {
				folder = "undefined"
			}
			recover = new("Recover " + folder + " with " + recovery + ".")
		}
		settings := settleBulk(state, input.Root, true, recover)
		if inBulk {
			notice = bulkNotice(bulk, recover)
		}
		var managed jsonx.NullSlice[*domain.ManagedWorktree]
		if state.ManagedWorktrees != nil {
			managed = slices.DeleteFunc(slices.Clone(state.ManagedWorktrees), func(item *domain.ManagedWorktree) bool { return item.Root == input.Root })
		}
		deleting := WithoutWorktreeRoot(state, input.Root)
		managementError := state.WorktreeManagementError
		if !inBulk {
			managementError = nil
		}
		patch := func(next *WorkspaceState) {
			next.WorktreeSettings, next.ManagedWorktrees, next.DeletingWorktrees = settings, managed, deleting
			next.WorktreeManagementError, next.WorktreeManagementNotice = managementError, &notice
		}
		after := Afterward(state, SliceTransition{Patch: patch})
		// Successful deletion files away every linked thread, keeping its conversation history.
		var dropping Step
		if worktree != nil {
			claimants := jsonx.Set[string]{}
			for _, thread := range WorktreeClaimants(after, worktree.ID) {
				claimants[thread.ID] = struct{}{}
			}
			dropping = WorktreeDropped(after, DismissedIn(ThreadsOf(after), claimants), worktree.ID, checkoutGoneNote(text, snapshot.Ref))
		}
		transition := withPatch(patch, dropping)
		transition.Effects = FollowUps(RefreshEnvironment(Afterward(after, SliceTransition(dropping))))
		return transition
	}
	return SliceTransition{}
}

// Moves the thread there and then, so it is never left saying it will move later. Turning it off
// takes this thread's claim off the checkout; the last claim to go takes the directory with it,
// and what it still holds is committed first. A move that goes ahead answers the confirmation, so the
// question never outlives its answer.
func WorktreeSet(state *WorkspaceState, input contracts.TaskCommandTaskSetWorktree) Step {
	if state.WorktreeMove != nil {
		return AndThen(state, Step{Edits: []OwnerEdit{MoveDismissedIn(WorktreesOf(state))}}, func(next *WorkspaceState) Step { return WorktreeSet(next, input) })
	}
	var thread *domain.Thread
	if taskID := TargetID(state, input.TaskID); taskID != nil && *taskID != "" {
		thread = threadByID(state.Threads, *taskID)
	}
	// With no thread yet the answer is a draft: the checkout is made when the first message goes.
	if thread == nil {
		if input.TaskID == nil {
			return Step{Edits: []OwnerEdit{DraftCheckoutIn(NavigationOf(state), input.Worktree)}}
		}
		return Step{}
	}
	if slices.Contains(state.CreatingWorktrees, thread.ID) {
		return Step{Refusal: new(WorktreeCreatingError)}
	}
	if _, leaving := LeavingThreadIds(state)[thread.ID]; leaving {
		return Step{Refusal: new(WorktreeReleasingError)}
	}
	if IsWorking(state, thread.ID) {
		return Step{Refusal: new(WorktreeRunningError)}
	}
	if input.Worktree {
		if hasText(thread.WorktreeID) {
			return Step{}
		}
		project := ProjectFor(state, thread)
		if project == nil || !hasText(project.WorkspaceID) {
			return Step{Refusal: new(WorktreeProjectError)}
		}
		step := CreatingWorktree(state, thread.ID)
		step.Effects = []WorkspaceEffect{WorkspaceEffectCreateWorktree{TaskID: thread.ID, ProjectRoot: project.Root}}
		return step
	}
	leaving := WorktreeFor(state, thread)
	if leaving == nil {
		return Step{}
	}
	releasing := ReleaseWorktrees(state, []*domain.Thread{thread})
	// A checkout other threads are still in stays where it is; only this thread walks out of it.
	if len(releasing) == 0 {
		note := domain.CreateConversationMessage(domain.ConversationMessageKindSystem, "Returned to the project checkout. The worktree is still there, and other threads are still working in it.", &leaving.Root, nil, nil, nil, nil)
		return Step{ClearsError: true, Edits: []OwnerEdit{LeftCheckoutIn(ThreadsOf(state), thread.ID, note, Now())}}
	}
	taskIDs := make([]string, len(releasing))
	effects := make([]WorkspaceEffect, len(releasing))
	for index, effect := range releasing {
		taskIDs[index], effects[index] = effect.TaskID, effect
	}
	return Step{ClearsError: true, Edits: []OwnerEdit{ReleasingIn(WorktreesOf(state), taskIDs)}, Effects: effects}
}

func managementFailed(message string) SliceTransition {
	return SliceTransition{
		Patch:  func(next *WorkspaceState) { next.WorktreeManagementError = &message },
		Result: contracts.WorkspaceCommandResultFalse{Message: message},
	}
}

// Settings' checkouts. `input` is one of the WorktreeManagementInputs.
func ReduceWorktreeManagement(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.WorktreeCommandWorktreeFilterProject, contracts.WorktreeCommandWorktreeConfirmDelete, contracts.WorktreeCommandWorktreeConfirmDeleteSafe,
		contracts.WorktreeCommandWorktreeSetMissingOpen, contracts.WorktreeCommandWorktreeSetThreadsOpen:
		return ReduceWorktreeSettings(state, input)

	case contracts.WorktreeCommandWorktreeRefresh:
		if state.WorktreeManagementLoading {
			return SliceTransition{}
		}
		settings := state.WorktreeSettings
		settings.Confirming, settings.ConfirmingSafe = nil, false
		return SliceTransition{
			Patch: func(next *WorkspaceState) {
				next.WorktreeManagementLoading, next.WorktreeSettings = true, settings
				next.WorktreeManagementError, next.WorktreeManagementNotice = nil, nil
			},
			Effects: []WorkspaceEffect{WorkspaceEffectListWorktrees{}},
		}

	case contracts.WorktreeCommandWorktreeReveal:
		// The Settings list loads on demand, so a checkout a thread records is revealable before it has.
		root, found := "", false
		if index := slices.IndexFunc(state.ManagedWorktrees, func(item *domain.ManagedWorktree) bool { return item.Root == input.Root }); index >= 0 {
			root, found = state.ManagedWorktrees[index].Root, true
		} else if index := slices.IndexFunc(state.Worktrees, func(item *domain.Worktree) bool { return item.Root == input.Root }); index >= 0 {
			root, found = state.Worktrees[index].Root, true
		}
		if !found {
			return managementFailed(WorktreeMissingError)
		}
		return SliceTransition{
			Patch:   func(next *WorkspaceState) { next.WorktreeManagementError = nil },
			Effects: []WorkspaceEffect{WorkspaceEffectRevealWorktree{Root: root}},
		}

	case contracts.WorktreeCommandWorktreeDelete:
		return deleteWorktree(state, input)

	case contracts.WorktreeCommandWorktreeDeleteSafe:
		return deleteSafe(state, input.Roots)

	case WorktreeEventWorktreeFailed:
		// A thread that never moved runs what it had queued where it already is.
		creating := WithoutCreatingWorktree(state, input.TaskID).CreatingWorktrees
		patch := func(next *WorkspaceState) { next.CreatingWorktrees = creating }
		transition := withPatch(patch, Drained(Afterward(state, SliceTransition{Patch: patch}), input.TaskID, QueueIntentDeliver))
		transition.Refusal = &input.Message
		return transition

	// The checkout is still there and the thread is still in it, so only the wait and the error change.
	case WorktreeEventWorktreeReleaseFailed:
		releasing := WithoutReleasingWorktree(state, []string{input.TaskID}).ReleasingWorktrees
		return SliceTransition{Patch: func(next *WorkspaceState) { next.ReleasingWorktrees = releasing }, Refusal: &input.Message}

	case WorktreeEventWorktreesLoaded:
		worktrees := jsonx.NullSlice[*domain.ManagedWorktree](input.Worktrees)
		if worktrees == nil {
			worktrees = jsonx.NullSlice[*domain.ManagedWorktree]{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.ManagedWorktrees, next.WorktreeManagementLoading, next.WorktreeManagementError = worktrees, false, nil
		}}

	// A failed delete leaves the checkout on the list, so only its wait and the error change.
	case WorktreeEventWorktreesFailed:
		deleting, settings, loading := state.DeletingWorktrees, state.WorktreeSettings, false
		if hasText(input.Root) {
			deleting = WithoutWorktreeRoot(state, *input.Root)
			settings = settleBulk(state, *input.Root, false, nil)
			loading = state.WorktreeManagementLoading
		}
		return SliceTransition{
			Patch: func(next *WorkspaceState) {
				next.DeletingWorktrees, next.WorktreeSettings, next.WorktreeManagementLoading = deleting, settings, loading
				next.WorktreeManagementError = &input.Message
			},
			Result: contracts.WorkspaceCommandResultFalse{Message: input.Message},
		}
	}
	return SliceTransition{}
}

// Manual deletion snapshots loose work first and refuses to move the ground under a run.
func deleteWorktree(state *WorkspaceState, input contracts.WorktreeCommandWorktreeDelete) SliceTransition {
	var thread *domain.Thread
	if taskID := TargetID(state, input.TaskID); taskID != nil && *taskID != "" {
		thread = threadByID(state.Threads, *taskID)
	}
	var id, root string
	found := false
	var recorded *domain.Worktree
	switch {
	case hasText(input.WorktreeID):
		recorded = WorktreeByID(state, input.WorktreeID)
	case hasText(input.Root):
		if index := slices.IndexFunc(state.Worktrees, func(item *domain.Worktree) bool { return item.Root == *input.Root }); index >= 0 {
			recorded = state.Worktrees[index]
		}
	default:
		recorded = WorktreeFor(state, thread)
	}
	if recorded != nil {
		id, root, found = recorded.ID, recorded.Root, true
	} else if hasText(input.Root) {
		if index := slices.IndexFunc(state.ManagedWorktrees, func(item *domain.ManagedWorktree) bool { return item.Root == *input.Root }); index >= 0 {
			id, root, found = state.ManagedWorktrees[index].ID, state.ManagedWorktrees[index].Root, true
		}
	}
	if !found {
		return managementFailed(WorktreeMissingError)
	}
	if slices.Contains(state.DeletingWorktrees, root) {
		return SliceTransition{}
	}
	var claimants []*domain.Thread
	for _, claimant := range state.Threads {
		if claimant.WorktreeID != nil && *claimant.WorktreeID == id {
			claimants = append(claimants, claimant)
		}
	}
	// A checkout a thread is already walking out of is on its way; asking again would remove it twice.
	if slices.ContainsFunc(claimants, func(claimant *domain.Thread) bool { return slices.Contains(state.ReleasingWorktrees, claimant.ID) }) {
		return managementFailed(WorktreeReleasingError)
	}
	if slices.ContainsFunc(claimants, func(claimant *domain.Thread) bool { return IsWorking(state, claimant.ID) }) {
		return SliceTransition{Patch: func(next *WorkspaceState) { next.WorktreeManagementError = new(WorktreeRunningError) }, Refusal: new(WorktreeRunningError)}
	}
	title, ok := lastSegment(root)
	if !ok {
		title = id
	}
	effect := WorkspaceEffectDeleteWorktree{WorktreeID: id, Root: root, Title: title}
	if input.MissingOnly != nil && *input.MissingOnly {
		effect.MissingOnly = new(true)
	}
	settings := state.WorktreeSettings
	settings.Confirming = nil
	deleting := append(slices.Clip(state.DeletingWorktrees), root)
	return SliceTransition{
		Patch: func(next *WorkspaceState) {
			next.WorktreeSettings, next.DeletingWorktrees = settings, deleting
			next.WorktreeManagementError, next.WorktreeManagementNotice = nil, nil
		},
		ClearsError: true,
		Effects:     []WorkspaceEffect{effect},
	}
}

// Each checkout is judged again here, so one that changed since the confirmation stays. A bulk still running takes the new roots in.
func deleteSafe(state *WorkspaceState, requested []string) SliceTransition {
	views := WorktreeSettingsViews(state, ThreadActivityThreadActivity(state).Working)
	safe := jsonx.Set[string]{}
	for _, view := range views {
		if view.Safe {
			safe[view.Root] = struct{}{}
		}
	}
	copied := *state
	copied.WorktreeSettings.ConfirmingSafe = false
	next := &copied
	effects := []WorkspaceEffect{}
	pending := []string{}
	seen := jsonx.Set[string]{}
	for _, root := range requested {
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		if _, ok := safe[root]; !ok {
			continue
		}
		deleted := Applied(next, deleteWorktree(next, contracts.WorktreeCommandWorktreeDelete{Root: &root}))
		if !slices.Contains(deleted.State.DeletingWorktrees, root) {
			continue
		}
		next = &deleted.State
		effects = append(effects, deleted.Effects...)
		pending = append(pending, root)
	}
	var bulk *WorktreeSettingsStateBulk
	if running := state.WorktreeSettings.Bulk; running != nil {
		bulk = &WorktreeSettingsStateBulk{Pending: slices.Concat(running.Pending, pending), Deleted: running.Deleted, Recover: running.Recover}
	} else if len(pending) > 0 {
		bulk = &WorktreeSettingsStateBulk{Pending: pending, Deleted: 0, Recover: []string{}}
	}
	settings := next.WorktreeSettings
	settings.Bulk = bulk
	deleting, managementError, notice := next.DeletingWorktrees, next.WorktreeManagementError, next.WorktreeManagementNotice
	return SliceTransition{
		Patch: func(next *WorkspaceState) {
			next.WorktreeSettings, next.DeletingWorktrees = settings, deleting
			next.WorktreeManagementError, next.WorktreeManagementNotice = managementError, notice
		},
		ClearsError: len(pending) > 0,
		Effects:     effects,
	}
}

// Takes a finished root off the bulk delete, counting it when `deleted` and keeping its recovery
// pointer when there is one (`recovery` may be nil).
func settleBulk(state *WorkspaceState, root string, deleted bool, recovery *string) WorktreeSettingsState {
	bulk := state.WorktreeSettings.Bulk
	if bulk == nil || !slices.Contains(bulk.Pending, root) {
		return state.WorktreeSettings
	}
	pending := slices.DeleteFunc(slices.Clone(bulk.Pending), func(item string) bool { return item == root })
	next := &WorktreeSettingsStateBulk{Pending: pending, Deleted: bulk.Deleted, Recover: bulk.Recover}
	if deleted {
		next.Deleted++
		if hasText(recovery) {
			next.Recover = append(slices.Clip(bulk.Recover), *recovery)
		}
	}
	settings := state.WorktreeSettings
	settings.Bulk = nil
	if len(pending) > 0 {
		settings.Bulk = next
	}
	return settings
}

// `recovery` may be nil.
func bulkNotice(bulk *WorktreeSettingsStateBulk, recovery *string) string {
	count := bulk.Deleted + 1
	noun := "worktrees"
	if count == 1 {
		noun = "worktree"
	}
	parts := append([]string{"Deleted " + strconv.FormatFloat(count, 'f', -1, 64) + " " + noun + "."}, bulk.Recover...)
	if hasText(recovery) {
		parts = append(parts, *recovery)
	}
	return strings.Join(parts, " ")
}

// `input` is a task.checkout-branch command or a checkout.finished event.
func reduceCheckout(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	// A failed checkout may still have made the branch, so Git is read either way, as a follow-up.
	if finished, ok := input.(WorktreeEventCheckoutFinished); ok {
		reads := FollowUps(RefreshEnvironment(state))
		if hasText(finished.Message) {
			return SliceTransition{Refusal: finished.Message, Effects: reads}
		}
		return SliceTransition{Effects: reads}
	}
	checkout, ok := input.(contracts.TaskCommandTaskCheckoutBranch)
	if !ok {
		return SliceTransition{}
	}
	// Moves the checkout itself, which everything working in it sees, so nothing may be running
	// there. A thread that does not exist yet has no checkout to move: it only records where to start.
	var thread *domain.Thread
	if taskID := TargetID(state, checkout.TaskID); taskID != nil && *taskID != "" {
		thread = threadByID(state.Threads, *taskID)
	}
	create := checkout.Create != nil && *checkout.Create
	var createFlag *bool
	if create {
		createFlag = new(true)
	}
	if thread == nil {
		return ReduceWorktrees(state, contracts.TaskCommandTaskSetBranch{Branch: &checkout.Branch, Create: createFlag})
	}
	workspaceID := threadWorkspaceOf(state, thread)
	if !hasText(workspaceID) {
		return SliceTransition{Refusal: new(SwitchProjectError)}
	}
	if slices.Contains(state.CreatingWorktrees, thread.ID) {
		return SliceTransition{Refusal: new(WorktreeCreatingError)}
	}
	if _, leaving := LeavingThreadIds(state)[thread.ID]; leaving {
		return SliceTransition{Refusal: new(WorktreeReleasingError)}
	}
	if RunsInWorkspace(state, workspaceID) || IsWorking(state, thread.ID) {
		return SliceTransition{Refusal: new(SwitchRunningError)}
	}
	return SliceTransition{
		ClearsError: true,
		Effects:     []WorkspaceEffect{WorkspaceEffectCheckoutBranch{WorkspaceID: *workspaceID, Branch: checkout.Branch, Create: createFlag}},
	}
}
