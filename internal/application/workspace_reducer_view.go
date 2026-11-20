package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"slices"
	"strings"
)

// sameFindResults says whether a report says nothing new. A nil held is TypeScript's null.
func sameFindResults(held *domain.FindResults, reported *domain.FindResults) bool {
	return held != nil && held.Matches == reported.Matches && samePointee(held.Index, reported.Index) && samePointee(held.Counting, reported.Counting)
}

var ViewInputs = []string{
	"view.set-menu", "view.go-back", "view.go-forward", "view.set-focused", "view.dock-keys", "view.find-open", "view.find-query", "view.find-step",
	"view.find-close", "find.results", "view.jump-open", "view.jump-query", "view.jump-step", "view.jump-choose", "view.jump-choose-setting",
	"view.jump-close", "view.set-zen", "view.dismiss-computer-use-setup",
}

func ReduceView(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.ViewCommandViewSetMenu:
		return SliceTransition{Edits: []OwnerEdit{MenuIn(DialogsOf(state), input.Menu)}}

	case contracts.ViewCommandViewGoBack, contracts.ViewCommandViewGoForward:
		step := 1
		if _, back := input.(contracts.ViewCommandViewGoBack); back {
			step = -1
		}
		index, ok := ReachableVisit(state, step)
		if !ok {
			return SliceTransition{}
		}
		return SliceTransition(Visited(state, index))

	case contracts.ViewCommandViewSetFocused:
		if !input.Focused {
			return SliceTransition{Edits: []OwnerEdit{FocusedIn(DesktopOf(state), false), CaptureStoppedIn(SettingsOf(state))}, Effects: StopCapture(state)}
		}
		engines, asked := RefreshedEnginesIn(EnginesOf(state))
		read := state.CurrentID
		if SelectedComputer(state) != nil {
			read = nil
		}
		return SliceTransition{
			Edits:   []OwnerEdit{FocusedIn(DesktopOf(state), true), engines, RowReadIn(ThreadsOf(state), read)},
			Effects: slices.Concat(FollowUps(RefreshEnvironment(state)), asked),
		}

	case contracts.ViewCommandViewDockKeys:
		var tab *string
		if input.Tab != nil && *input.Tab != "" && DockHoldsTab(state, *input.Tab) {
			tab = input.Tab
		}
		if samePointee(state.KeyboardTab, tab) {
			return SliceTransition{}
		}
		return SliceTransition{Edits: []OwnerEdit{KeyboardTabIn(DocksOf(state), tab)}}

	case contracts.ViewCommandViewFindOpen:
		target := input.Target
		if target == nil && state.Find != nil {
			target = state.Find.Target
		}
		if target == nil {
			target = FindTargetFor(state, domain.ShortcutSurfaceAny)
		}
		previous := state.Find
		same := previous != nil && domain.SameFindTarget(previous.Target, target)
		find := &FindState{Target: target}
		if previous != nil {
			find.Query = previous.Query
			find.Focus = previous.Focus
		}
		find.Focus++
		if same {
			find.Index = previous.Index
		}
		effects := []WorkspaceEffect{}
		if !same {
			effects = append(effects, StopSearchEffects(previous)...)
		}
		if _, browser := target.(domain.FindTargetBrowser); browser {
			effects = append(effects, WorkspaceEffectFocusWindow{})
		}
		if !same {
			effects = append(effects, SearchEffects(find, false, true)...)
		}
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.Find = find
			if !same {
				next.FindResults = nil
			}
		}, Effects: effects}

	case contracts.ViewCommandViewFindQuery:
		if state.Find == nil {
			return SliceTransition{}
		}
		find := *state.Find
		find.Query = input.Query
		find.Index = 0
		var effects []WorkspaceEffect
		if strings.TrimSpace(find.Query) != "" {
			effects = SearchEffects(&find, false, true)
		} else {
			effects = StopSearchEffects(&find)
		}
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.Find = &find
			next.FindResults = nil
		}, Effects: effects}

	case contracts.ViewCommandViewFindStep:
		find := state.Find
		if find == nil {
			return SliceTransition{}
		}
		if domain.SearchesItself(find.Target) {
			return SliceTransition{Effects: SearchEffects(find, true, input.Delta == 1)}
		}
		matches := 0
		if target, ok := find.Target.(domain.FindTargetThread); ok {
			var messages []*domain.ConversationMessage
			if thread := findThread(state.Threads, target.TaskID); thread != nil {
				messages = thread.Messages
			}
			matches = len(domain.MemoizedFindHits(messages, find.Query))
		} else if state.FindResults != nil {
			matches = int(state.FindResults.Matches)
		}
		stepped := *find
		stepped.Index = float64(domain.StepMatch(int(find.Index), int(input.Delta), matches))
		return SliceTransition{Patch: func(next *WorkspaceState) { next.Find = &stepped }}

	case contracts.ViewCommandViewFindClose:
		effects := StopSearchEffects(state.Find)
		if state.Find != nil {
			if target, ok := state.Find.Target.(domain.FindTargetBrowser); ok {
				effects = append(slices.Clip(effects), WorkspaceEffectFocusBrowser{TabID: target.TabID})
			}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.Find = nil
			next.FindResults = nil
		}, Effects: effects}

	case contracts.FindResultsEvent:
		find := state.Find
		if find == nil || !domain.SameFindTarget(find.Target, input.Target) {
			return SliceTransition{}
		}
		var moved *float64
		if !domain.SearchesItself(find.Target) && input.Results.Index != nil && *input.Results.Index != find.Index {
			moved = input.Results.Index
		}
		if moved == nil && sameFindResults(state.FindResults, &input.Results) {
			return SliceTransition{}
		}
		results := input.Results
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.FindResults = &results
			if moved != nil {
				movedFind := *find
				movedFind.Index = *moved
				next.Find = &movedFind
			}
		}}

	case contracts.ViewCommandViewJumpOpen:
		return SliceTransition{Patch: func(next *WorkspaceState) { next.Jump = &JumpState{Query: "", Index: 0} }, ClosesMenu: true, Effects: TakeKeys}

	case contracts.ViewCommandViewJumpQuery:
		if state.Jump == nil {
			return SliceTransition{}
		}
		query := input.Query
		return SliceTransition{Patch: func(next *WorkspaceState) { next.Jump = &JumpState{Query: query, Index: 0} }}

	case contracts.ViewCommandViewJumpStep:
		jump := WorkspaceJumpJumpView(state, ThreadActivityThreadActivity(state).Working)
		if jump == nil {
			return SliceTransition{}
		}
		stepped := &JumpState{Query: jump.Query, Index: float64(domain.StepMatch(int(jump.Index), int(input.Delta), len(jump.Options)))}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.Jump = stepped }}

	case contracts.ViewCommandViewJumpChoose:
		transition := SliceTransition(Selected(state, input.TaskID))
		transition.Patch = func(next *WorkspaceState) { next.Jump = nil }
		return transition

	case contracts.ViewCommandViewJumpChooseSetting:
		section := string(input.Section)
		transition := SliceTransition(SettingsShown(state, true, &section, input.SettingID))
		transition.Patch = func(next *WorkspaceState) { next.Jump = nil }
		return transition

	case contracts.ViewCommandViewJumpClose:
		return SliceTransition{Patch: func(next *WorkspaceState) { next.Jump = nil }}

	case contracts.ViewCommandViewSetZen:
		if input.Zen && !ZenAllowed(state) || state.Zen == input.Zen {
			return SliceTransition{}
		}
		zen := input.Zen
		return SliceTransition{
			Patch:   func(next *WorkspaceState) { next.Zen = zen },
			Edits:   []OwnerEdit{KeyboardTabIn(DocksOf(state), nil), FocusComposerIn(ComposerOf(state))},
			Effects: TakeKeys,
		}

	case contracts.ViewCommandViewDismissComputerUseSetup:
		return SliceTransition{Edits: []OwnerEdit{SetupShownIn(ComputerUseOf(state), false)}}
	}
	return SliceTransition{}
}

// samePointee is TypeScript's === between two optional values: both absent, or both the same value.
func samePointee[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
