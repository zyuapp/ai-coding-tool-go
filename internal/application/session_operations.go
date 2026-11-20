package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

// CompactableSession: a session compacts on request when its model offers it, its engine holds it,
// and it has reported its context.
func CompactableSession(state *WorkspaceState, thread *domain.Thread) bool {
	catalog := CatalogOf(state)
	model, ok := domain.ThreadModel(&catalog, thread)
	return thread.ResourceStop == nil && ok && domain.ModelSupportsManualCompaction(&catalog, thread.Engine, model) &&
		thread.Continuation != nil && thread.Continuation.Provider == string(thread.Engine) &&
		thread.ContextUsage != nil
}

// ReviewableSession: a session reviews changes when its engine can, its engine holds it, and the
// thread works in a checkout.
func ReviewableSession(state *WorkspaceState, thread *domain.Thread) bool {
	if thread.ResourceStop != nil || !domain.CapabilitiesFor(string(thread.Engine)).Review || thread.Continuation == nil || thread.Continuation.Provider != string(thread.Engine) {
		return false
	}
	id, ok := ThreadWorkspaceID(state, thread)
	return ok && id != ""
}
