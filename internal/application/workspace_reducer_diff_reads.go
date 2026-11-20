package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// What a review opens on. The session panel counts from where HEAD left the origin default branch, so
// a review reached from that row starts on the same comparison and reports the same totals. Without
// an origin to measure from the Branch mode compares the working tree from HEAD.
func InitialRange(state *WorkspaceState, owner string, diff *DiffState) domain.DiffRange {
	if diff.Mode != DiffStateModeBranch || diff.WorkspaceID != nil || diff.Result != nil {
		return diff.Range
	}
	return DefaultBranchRange(state, owner)
}

func DefaultBranchRange(state *WorkspaceState, owner string) domain.DiffRangeBranches {
	if counted, ok := EnvironmentFor(state, SubjectWorkspaceID(state, owner)).(contracts.ChangedFilesResultAvailable); ok && counted.Baseline != nil && *counted.Baseline != "" {
		return domain.DiffRangeBranches{Base: *counted.Baseline, Compare: nil}
	}
	return domain.DefaultBranchRange
}

// The range as a branches comparison, when it is one.
func branchesRange(diffRange domain.DiffRange) (domain.DiffRangeBranches, bool) {
	switch diffRange := diffRange.(type) {
	case domain.DiffRangeBranches:
		return diffRange, true
	case *domain.DiffRangeBranches:
		if diffRange != nil {
			return *diffRange, true
		}
	}
	return domain.DiffRangeBranches{}, false
}

// The workspace a thread works in, or nil for no thread or none (TS: string | undefined).
func threadWorkspaceOf(state *WorkspaceState, thread *domain.Thread) *string {
	if thread == nil {
		return nil
	}
	if workspaceID, ok := ThreadWorkspaceID(state, thread); ok {
		return &workspaceID
	}
	return nil
}

func threadByID(threads []*domain.Thread, id string) *domain.Thread {
	if index := slices.IndexFunc(threads, func(thread *domain.Thread) bool { return thread.ID == id }); index >= 0 {
		return threads[index]
	}
	return nil
}

// Asks for a comparison, and records in the same breath which checkout was asked. The two have to move
// together: a reply is only accepted when it names the checkout and comparison the dock is holding, so
// an effect issued without writing that down is an answer the reducer would throw away.
// `workspaceID` and `patch` may be nil.
func DiffReadFrom(state *WorkspaceState, owner string, workspaceID *string, diffRange domain.DiffRange, patch DiffPatch) Step {
	previous := DiffFor(state, owner)
	sameWorkspace := workspaceID != nil && previous.WorkspaceID != nil && *previous.WorkspaceID == *workspaceID
	mode := DiffStateMode(domain.ModeForRange(diffRange))
	var branchRange *domain.DiffRangeBranches
	if branches, ok := branchesRange(diffRange); ok {
		branchRange = &branches
	} else if sameWorkspace {
		branchRange = previous.BranchRange
	}
	read := func(next *DiffState) {
		if patch != nil {
			patch(next)
		}
		next.Mode, next.BranchRange = mode, branchRange
	}
	if workspaceID == nil || *workspaceID == "" {
		return Step{Edits: []OwnerEdit{EditDiff(DocksOf(state), owner, func(next *DiffState) {
			read(next)
			next.Range, next.WorkspaceID, next.Result, next.Loading = diffRange, nil, nil, false
		})}}
	}
	// The read takes the whitespace setting the review lands with, which is the one it already had.
	landed := *previous
	read(&landed)
	id := *workspaceID
	return Step{
		Edits: []OwnerEdit{EditDiff(DocksOf(state), owner, func(next *DiffState) {
			read(next)
			next.Range, next.WorkspaceID, next.Loading = diffRange, &id, true
		})},
		Effects: []WorkspaceEffect{WorkspaceEffectReadDiff{Owner: owner, WorkspaceID: id, Range: diffRange, IgnoreWhitespace: landed.IgnoreWhitespace}},
	}
}

// DiffReadFrom, on the whole state.
func ReadDiffFrom(state *WorkspaceState, owner string, workspaceID *string, diffRange domain.DiffRange, patch DiffPatch) WorkspaceTransition {
	return Applied(state, SliceTransition(DiffReadFrom(state, owner, workspaceID, diffRange, patch)))
}

// The same, for the checkout the dock's review is about. `patch` may be nil.
func DiffRead(state *WorkspaceState, owner string, diffRange domain.DiffRange, patch DiffPatch) Step {
	var workspaceID *string
	if domain.ModeForRange(diffRange) == "commit" {
		workspaceID = DiffFor(state, owner).WorkspaceID
	}
	if workspaceID == nil {
		workspaceID = SubjectWorkspaceID(state, owner)
	}
	return DiffReadFrom(state, owner, workspaceID, diffRange, patch)
}

// A review read from scratch: nothing listed, folded or ticked.
func clearedReview(next *DiffState) {
	next.Result, next.Collapsed, next.Viewed = nil, []string{}, map[string]string{}
}

// A thread whose checkout changed is reviewing the wrong one until it reads again. Nothing to do for a
// thread with no review open, which is most of them. The read is a follow-up of whatever moved it.
func DiffReread(state *WorkspaceState, taskID string) Step {
	diff, ok := state.Diffs[taskID]
	if !ok || diff == nil {
		return Step{}
	}
	subject := taskID
	if named := DockSubject(state, taskID, DiffPanel); named != nil {
		subject = *named
	}
	workspaceID := threadWorkspaceOf(state, threadByID(state.Threads, subject))
	// TS compares string | null with string | undefined, so a review of no checkout is always read again.
	if diff.WorkspaceID != nil && workspaceID != nil && *diff.WorkspaceID == *workspaceID {
		return Step{}
	}
	read := DiffReadFrom(state, taskID, workspaceID, diff.Range, clearedReview)
	read.Effects = FollowUps(read.Effects)
	return read
}

// Every review of a thread's checkout, as follow-ups: its own dock's, and a coordinator's that has its tab as the subject.
func ReviewsReread(state *WorkspaceState, taskID string, workspaceID string) Step {
	owners := make([]string, 0, len(state.Diffs))
	for owner := range state.Diffs {
		subject := owner
		if named := DockSubject(state, owner, DiffPanel); named != nil {
			subject = *named
		}
		if subject == taskID {
			owners = append(owners, owner)
		}
	}
	// Go keeps no insertion order for the reviews, so they are read in owner order.
	slices.Sort(owners)
	steps := make([]func(*WorkspaceState) Step, len(owners))
	for index, owner := range owners {
		diffRange := state.Diffs[owner].Range
		steps[index] = func(next *WorkspaceState) Step { return DiffReadFrom(next, owner, &workspaceID, diffRange, nil) }
	}
	read := InTurn(state, Step{}, steps)
	read.Effects = FollowUps(read.Effects)
	return read
}

// ReviewsReread, on the whole state.
func RereadReviewsOf(state *WorkspaceState, taskID string, workspaceID string) WorkspaceTransition {
	return Applied(state, SliceTransition(ReviewsReread(state, taskID, workspaceID)))
}
