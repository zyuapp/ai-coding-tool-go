package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"maps"
	"slices"
)

// StateFromData is the workspace a store answer opens on, with its threads restored and any turn the
// last session cut off ended, except those resuming carries on. nil storageError, engineStatus and
// resuming are TypeScript's defaults.
func StateFromData(data *domain.ThreadStoreData, storageError *string, engineStatus *domain.EngineStatus, resuming jsonx.Set[string]) *WorkspaceState {
	projects := data.Projects
	if data.LastFolder != nil && *data.LastFolder != "" && !slices.ContainsFunc(data.Projects, func(project *domain.Project) bool { return project.Root == *data.LastFolder }) {
		projects = slices.Concat(data.Projects, []*domain.Project{{ID: domain.LegacyProjectID(*data.LastFolder), Root: *data.LastFolder}})
	}
	if projects == nil {
		projects = []*domain.Project{}
	}
	at := Now()
	threads, subagents, cutOff := RestoredThreads(domain.RetainedThreads(data.Tasks, at))
	var firstThread, draftThread *domain.Thread
	for _, thread := range threads {
		if domain.CoordinatorOf(threads, thread) != nil {
			continue
		}
		if firstThread == nil {
			firstThread = thread
		}
		if draftThread == nil && (thread.Role == nil || *thread.Role != domain.ThreadRoleReviewer) {
			draftThread = thread
		}
		if firstThread != nil && draftThread != nil {
			break
		}
	}
	var firstProject *string
	if firstThread != nil {
		firstProject = firstThread.ProjectID
	} else {
		for _, project := range projects {
			if data.LastFolder != nil && project.Root == *data.LastFolder {
				id := project.ID
				firstProject = &id
				break
			}
		}
	}
	state := EmptyWorkspaceState(storageError)
	state.EngineStatus = engineStatus
	state.Threads = BackfillSortIndex(threads)
	state.Subagents = subagents
	state.Projects = BackfillProjectSortIndex(projects)
	state.Worktrees = data.Worktrees
	if state.Worktrees == nil {
		state.Worktrees = []*domain.Worktree{}
	}
	state.LastFolder = data.LastFolder
	state.DraftProjectID = firstProject
	change := AgentDraftChange{}
	engine, model, effort, policy, fastMode := domain.DefaultEngine, domain.AgentModel(""), domain.DefaultEffort, domain.ExecutionPolicyConfirm, false
	if firstThread != nil {
		id := firstThread.ID
		state.CurrentID = &id
		state.History = []string{id}
		state.HistoryIndex = 0
		engine = firstThread.Engine
		if firstThread.Model != nil {
			model = *firstThread.Model
		}
		if firstThread.Effort != nil {
			effort = *firstThread.Effort
		}
		if firstThread.FastMode != nil {
			fastMode = *firstThread.FastMode
		}
	} else {
		state.CurrentID = nil
		state.History = []string{}
		state.HistoryIndex = -1
	}
	if draftThread != nil {
		policy = draftThread.ExecutionPolicy
	}
	change.Engine, change.Model, change.Effort, change.Policy, change.FastMode = &engine, &model, &effort, &policy, &fastMode
	restored := WithDraft(state, change)
	ended := []*domain.Thread{}
	for _, thread := range cutOff {
		if !resuming.Has(thread.ID) {
			ended = append(ended, thread)
		}
	}
	return WithCutOffTurnsEnded(restored, ended, at)
}

// WithStoreData is the store answering the load a session opened with: threads and projects arrive
// into the session rather than replacing it.
func WithStoreData(state *WorkspaceState, data *domain.ThreadStoreData, resuming jsonx.Set[string]) *WorkspaceState {
	landing := StateFromData(data, nil, state.EngineStatus, resuming)
	held := SideChatIds(state)
	if held == nil {
		held = jsonx.Set[string]{}
	}
	for taskID := range state.ActiveRuns {
		held[taskID] = struct{}{}
	}
	for _, pending := range state.PendingRuns {
		if pending.TaskID != nil && *pending.TaskID != "" {
			held[*pending.TaskID] = struct{}{}
		}
	}
	for _, taskID := range state.CreatingWorktrees {
		held[taskID] = struct{}{}
	}
	stored := make(map[string]bool, len(landing.Threads))
	for _, thread := range landing.Threads {
		stored[thread.ID] = true
	}
	threads := slices.Clone(landing.Threads)
	for _, thread := range state.Threads {
		if held.Has(thread.ID) && !stored[thread.ID] {
			threads = append(threads, thread)
		}
	}
	if threads == nil {
		threads = []*domain.Thread{}
	}
	landingWorktreeIDs := map[string]bool{}
	for _, worktree := range landing.Worktrees {
		landingWorktreeIDs[worktree.ID] = true
	}
	claimedWorktreeIDs := map[string]bool{}
	for _, thread := range threads {
		if thread.WorktreeID != nil && *thread.WorktreeID != "" {
			claimedWorktreeIDs[*thread.WorktreeID] = true
		}
	}
	worktrees := slices.Clone(landing.Worktrees)
	for _, worktree := range state.Worktrees {
		if !landingWorktreeIDs[worktree.ID] && claimedWorktreeIDs[worktree.ID] {
			worktrees = append(worktrees, worktree)
		}
	}
	if worktrees == nil {
		worktrees = []*domain.Worktree{}
	}
	subagents := maps.Clone(landing.Subagents)
	if subagents == nil {
		subagents = map[string][]*domain.Subagent{}
	}
	maps.Copy(subagents, state.Subagents)
	arrived := *state
	arrived.Threads = threads
	arrived.Subagents = subagents
	arrived.Worktrees = worktrees
	arrived.Projects = landing.Projects
	arrived.LastFolder = landing.LastFolder
	arrived.StorageError = landing.StorageError
	arrived.Writable = landing.Writable
	if sessionStarted(&arrived) {
		return &arrived
	}
	arrived.CurrentID = landing.CurrentID
	arrived.History = landing.History
	arrived.HistoryIndex = landing.HistoryIndex
	arrived.DraftProjectID = landing.DraftProjectID
	draft := DraftOf(landing)
	return WithDraft(&arrived, AgentDraftChange{Engine: &draft.Engine, Model: &draft.Model, Effort: &draft.Effort, Policy: &draft.Policy, FastMode: &draft.FastMode})
}

// sessionStarted says whether the session has gone anywhere of its own: a thread, a project, a draft, or a run.
func sessionStarted(state *WorkspaceState) bool {
	return findThread(state.Threads, state.CurrentID) != nil ||
		state.DraftProjectID != nil ||
		len(state.Prompts) > 0 ||
		len(state.Annotations) > 0 ||
		len(state.Pastes) > 0 ||
		len(state.Images) > 0 ||
		len(state.Files) > 0 ||
		len(state.PendingRuns) > 0
}

// StoreLoadInputs is the store and the preferences arriving: composition, filling in every owner's state at once.
var StoreLoadInputs = []string{"store.loaded", "preferences.loaded"}

func ReduceStoreLoad(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition {
	switch input := input.(type) {
	case WorkspaceEventStoreLoaded:
		resumingIDs := []string{}
		resuming := jsonx.Set[string]{}
		for _, thread := range input.Data.Tasks {
			if thread.RunOpen != nil && *thread.RunOpen && slices.Contains(input.Resuming, thread.ID) && !resuming.Has(thread.ID) {
				resuming[thread.ID] = struct{}{}
				resumingIDs = append(resumingIDs, thread.ID)
			}
		}
		arrived := WithStoreData(state, &input.Data, resuming)
		kept := make(map[string]bool, len(arrived.Threads))
		for _, thread := range arrived.Threads {
			kept[thread.ID] = true
		}
		gone := []*domain.Thread{}
		for _, stored := range input.Data.Tasks {
			if kept[stored.ID] {
				continue
			}
			thread := &domain.Thread{}
			thread.ID = stored.ID
			if stored.Role != nil {
				role := domain.ThreadRole(*stored.Role)
				thread.Role = &role
			}
			thread.RemoteHeard = stored.RemoteHeard
			gone = append(gone, thread)
		}
		marked := *arrived
		marked.HiddenThreads = 0
		if input.HiddenTasks != nil {
			marked.HiddenThreads = *input.HiddenTasks
		}
		marked.Restored = true
		return ResumedAfterUpdate(DiscardedLeads(&marked, gone), resumingIDs)

	case WorkspaceEventPreferencesLoaded:
		preferences := input.Preferences
		docks := maps.Clone(state.Docks)
		if docks == nil {
			docks = map[string]*ThreadDock{}
		}
		for _, owner := range slices.Sorted(maps.Keys(preferences.BrowserTabs)) {
			browserTabs := []*domain.BrowserTab{}
			for _, url := range preferences.BrowserTabs[owner] {
				if loadable, ok := domain.BrowserURL(url); ok {
					browserTabs = append(browserTabs, &domain.BrowserTab{ID: NewID(), URL: loadable})
				}
			}
			if len(browserTabs) > 0 {
				dock := *DockFor(state, owner)
				dock.BrowserTabs = browserTabs
				dock.BrowserTabID = &browserTabs[0].ID
				docks[owner] = &dock
			}
		}
		next := *state
		ViewPreferenceState(&next, &preferences)
		next.BrowserImport.Last = preferences.BrowserImport
		next.Docks = docks
		next.Computers.DiscardedLeads = preferences.DiscardedLeads
		if next.Computers.DiscardedLeads == nil {
			next.Computers.DiscardedLeads = []string{}
		}
		next.PlanUsage.Dismissed = preferences.DismissedUsageWarnings
		if next.PlanUsage.Dismissed == nil {
			next.PlanUsage.Dismissed = []string{}
		}
		return Settled(&next, nil, nil)
	}
	return Settled(state, nil, nil)
}
