package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Text a person has typed but the workspace has not published yet. The authoritative state arrives a
// revision at a time, so what is drawn is that state with every unacknowledged edit replayed over it.
// An edit is held until the revision that carries it arrives, and is dropped the moment the
// authoritative text catches up.

// optimisticKey is JSON.stringify of a field's name parts.
func optimisticKey(parts ...string) string {
	key, _ := jsonx.Marshal(parts)
	return string(key)
}

// Text edits name the field visible when they were typed, even if selection changes in transit.
func OptimisticEditsOptimisticEdit(state *WorkspaceState, input WorkspaceInput) *OptimisticEdit {
	switch input := input.(type) {
	case contracts.ViewCommandViewSetPrompt:
		taskID := PromptKey(state)
		if input.TaskID != nil {
			taskID = *input.TaskID
		}
		input.TaskID = &taskID
		return &OptimisticEdit{Key: optimisticKey("view.set-prompt", taskID), Input: input}
	case contracts.AnnotationCommandAnnotationNote:
		taskID := PromptKey(state)
		if input.TaskID != nil {
			taskID = *input.TaskID
		}
		input.TaskID = &taskID
		return &OptimisticEdit{Key: optimisticKey("annotation.note", taskID, input.AnnotationID), Input: input}
	case contracts.TaskCommandTaskRename:
		return &OptimisticEdit{Key: optimisticKey("task.rename", input.TaskID), Input: input}
	case contracts.WorktreeCommandWorktreeMenuSearch:
		return &OptimisticEdit{Key: optimisticKey("worktree.menu-search", string(input.List)), Input: input}
	case contracts.ViewCommandViewJumpQuery:
		return &OptimisticEdit{Key: "view.jump-query", Input: input}
	case contracts.ViewCommandViewFindQuery:
		if state.Find == nil {
			return nil
		}
		return &OptimisticEdit{Key: "view.find-query", Input: input, FindTarget: state.Find.Target}
	}
	return nil
}

// Whether the state already reads the way the edit would leave it. Replaying an edit the workspace
// has caught up with makes a copy that draws the same thing, and every copy costs a render.
func alreadyDrawn(state *WorkspaceState, input TextInput) bool {
	switch input := input.(type) {
	case contracts.ViewCommandViewSetPrompt:
		key := PromptKey(state)
		if input.TaskID != nil {
			key = *input.TaskID
		}
		return state.Prompts[key] == input.Prompt
	case contracts.AnnotationCommandAnnotationNote:
		key := PromptKey(state)
		if input.TaskID != nil {
			key = *input.TaskID
		}
		for _, annotation := range AnnotationsFor(state, key) {
			if annotation.ID == input.AnnotationID {
				return annotation.Note == input.Note
			}
		}
		return true
	case contracts.TaskCommandTaskRename:
		thread := threadsFind(state.Threads, input.TaskID)
		return thread != nil && thread.TitleByUser != nil && *thread.TitleByUser && thread.Title == domain.ClampTitle(input.Title)
	case contracts.WorktreeCommandWorktreeMenuSearch:
		query, ok := state.WorktreeMenuSearch[WorktreeMenuList(input.List)]
		return ok && query == input.Query
	case contracts.ViewCommandViewFindQuery:
		return state.Find != nil && state.Find.Query == input.Query && state.Find.Index == 0 && state.FindResults == nil
	case contracts.ViewCommandViewJumpQuery:
		return state.Jump != nil && state.Jump.Query == input.Query && state.Jump.Index == 0
	}
	return false
}

// Replays the pending edits, oldest first, over the authoritative state. An edit whose revision has
// arrived is settled and drops out; a search edit whose find target has moved on is held but not
// drawn, because it no longer describes the field on screen. An edit the state already reads as is
// left unreplayed, so the authoritative state is drawn as it stands.
func ApplyOptimisticEdits(authoritative *WorkspaceState, pending []*OptimisticEdit, revision float64) OptimisticView {
	state := authoritative
	edits := []*OptimisticEdit{}
	for _, edit := range pending {
		// Command replies and state patches can arrive in either order across the process boundary.
		if edit.Revision != nil && *edit.Revision <= revision {
			continue
		}
		edits = append(edits, edit)
		if edit.FindTarget != nil && (state.Find == nil || !domain.SameFindTarget(edit.FindTarget, state.Find.Target)) {
			continue
		}
		if alreadyDrawn(state, edit.Input) {
			continue
		}
		next := Reduce(state, edit.Input.(WorkspaceInput)).State
		state = &next
	}
	return OptimisticView{State: *state, Edits: edits}
}
