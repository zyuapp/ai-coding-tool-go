package application

import "github.com/zyuapp/ai-coding-tool-go/internal/jsonx"

// A permanently deleted thread leaves no session-only record holding its data or making it reachable:
// each owner forgets it in its own state.
func Pruned(state *WorkspaceState, removed jsonx.Set[string]) Step {
	if len(removed) == 0 {
		return Step{}
	}
	sideChats := SideChatsForgottenIn(SideChatsOf(state), removed)
	return Step{Edits: []OwnerEdit{
		CheckoutClaimsForgottenIn(WorktreesOf(state), removed),
		VisitsForgottenIn(NavigationOf(state), removed),
		DraftsForgottenIn(ComposerOf(state), removed),
		DocksForgottenIn(DocksOf(state), removed, sideChats.View.SideChats),
		sideChats,
		RunsForgottenIn(RunsOf(state), removed),
		AutomationsForgottenIn(AutomationsOf(state), removed),
		ApprovalForgottenIn(BrowserOf(state), removed),
	}}
}
