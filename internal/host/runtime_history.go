package host

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// historyHost is what loading histories takes: TypeScript's HistoryHost, holding the state by
// pointer and the persistence queue shared rather than copied.
type historyHost struct {
	state func() *application.WorkspaceState
	load  func(ctx context.Context, taskID string) ([]*domain.ConversationMessage, error)
	// search names which of these threads have stored message text containing the lowercased search.
	search      func(ctx context.Context, search string, taskIDs []string) ([]string, error)
	dispatch    func(ctx context.Context, input application.WorkspaceInput) error
	persistence *PersistenceQueue
}

// RuntimeHistory loads threads' stored transcripts. Concurrent readers share the disk read, and loaded
// messages become the durable baseline.
type RuntimeHistory struct {
	host       historyHost
	mu         sync.Mutex
	reads      map[string]*historyRead
	generation int
}

// historyRead is one disk read in flight; done closes once err is set.
type historyRead struct {
	done chan struct{}
	err  error
}

func (r *historyRead) wait(ctx context.Context) error {
	select {
	case <-r.done:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func createRuntimeHistory(host historyHost) *RuntimeHistory {
	return &RuntimeHistory{host: host, reads: map[string]*historyRead{}}
}

// Hydrate loads a thread's stored transcript into the workspace, joining a read already in flight.
func (h *RuntimeHistory) Hydrate(ctx context.Context, taskID string) error {
	h.mu.Lock()
	if held := h.reads[taskID]; held != nil {
		h.mu.Unlock()
		return held.wait(ctx)
	}
	if thread := findThread(h.host.state().Threads, taskID); thread == nil || thread.HistorySummary == nil {
		h.mu.Unlock()
		return nil
	}
	generation := h.generation
	read := &historyRead{done: make(chan struct{})}
	h.reads[taskID] = read
	h.mu.Unlock()
	go func() {
		err := func() error {
			messages, err := h.host.load(background, taskID)
			if err != nil {
				return err
			}
			if !h.current(generation) {
				return nil
			}
			AdoptPersistedMessages(h.host.persistence, taskID, messages)
			return h.host.dispatch(background, application.WorkspaceEventStoreThreadLoaded{TaskID: taskID, Messages: nonNil(messages)})
		}()
		if err != nil && !h.current(generation) {
			err = nil
		}
		read.err = err
		h.mu.Lock()
		if h.reads[taskID] == read {
			delete(h.reads, taskID)
		}
		h.mu.Unlock()
		close(read.done)
	}()
	return read.wait(ctx)
}

func (h *RuntimeHistory) current(generation int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.generation == generation
}

// Needed lists the threads whose disk history an input requires. Only operations that read or append
// transcript content require it. Selection does not: it lands at once and the thread on screen loads
// behind its loading state.
func (h *RuntimeHistory) Needed(input application.WorkspaceInput) []string {
	return neededHistories(h.host.state(), input)
}

func neededHistories(state *application.WorkspaceState, input application.WorkspaceInput) []string {
	var ids []string
	seen := map[string]bool{}
	add := func(taskID *string) {
		if taskID != nil && *taskID != "" && !seen[*taskID] {
			seen[*taskID] = true
			ids = append(ids, *taskID)
		}
	}
	claimants := func(worktreeID *string) {
		if worktreeID == nil || *worktreeID == "" {
			return
		}
		for _, thread := range state.Threads {
			if thread.WorktreeID != nil && *thread.WorktreeID == *worktreeID {
				add(&thread.ID)
			}
		}
	}
	orCurrent := func(taskID *string) *string {
		if taskID != nil {
			return taskID
		}
		return state.CurrentID
	}
	worktreeWhere := func(matches func(worktree *domain.Worktree) bool) *string {
		for _, worktree := range state.Worktrees {
			if matches(worktree) {
				return &worktree.ID
			}
		}
		return nil
	}
	switch input := input.(type) {
	case contracts.ViewCommandViewShortcut:
		commands := application.ShortcutCommands(state, input.Action, input.Surface)
		for _, command := range commands {
			if _, starting := command.(contracts.TaskCommandTaskNew); starting {
				return nil
			}
		}
		for _, command := range commands {
			for _, id := range neededHistories(state, command.(application.WorkspaceInput)) {
				add(&id)
			}
		}
	case contracts.TaskCommandTaskSend:
		if input.TaskID != nil {
			add(input.TaskID)
		} else if input.Text == nil {
			add(state.CurrentID)
		}
	case contracts.TaskCommandTaskFork:
		add(orCurrent(input.TaskID))
	case contracts.TaskCommandTaskSetWorktree:
		add(orCurrent(input.TaskID))
	case contracts.TaskCommandTaskMoveWorktree:
		add(orCurrent(input.TaskID))
	case contracts.RunControlCommandRunCompact:
		add(orCurrent(input.TaskID))
	case contracts.ReviewCommandReviewStart:
		add(orCurrent(input.TaskID))
	case contracts.ViewCommandViewMoveWorktree:
		if input.Worktree != nil {
			add(state.CurrentID)
		}
	case contracts.ViewCommandViewForkConfirm:
		if state.ForkName != nil {
			add(&state.ForkName.TaskID)
		}
	case contracts.SideChatCommandSideChatOpen:
		add(state.CurrentID)
	case contracts.ViewCommandViewFindOpen:
		target := input.Target
		if target == nil && state.Find != nil {
			target = state.Find.Target
		}
		if target == nil {
			target = application.FindTargetFor(state, "any")
		}
		if thread, ok := target.(domain.FindTargetThread); ok {
			add(thread.TaskID)
		}
	case contracts.ViewCommandViewFindQuery, contracts.ViewCommandViewFindStep:
		if state.Find != nil {
			if thread, ok := state.Find.Target.(domain.FindTargetThread); ok {
				add(thread.TaskID)
			}
		}
	case application.WorkspaceEventAgentEvents:
		for _, event := range input.Events {
			add(commandTaskID(event))
		}
	case application.WorkspaceEventRunEvent:
		add(commandTaskID(input.Event))
	case application.AutomationEventAutomationFired:
		add(&input.Fire.TaskID)
	case application.WorkspaceEventRunResolved:
		if pending := state.PendingRuns[input.PendingID]; pending != nil {
			add(pending.TaskID)
		}
	case application.WorktreeEventWorktreeCreated:
		add(&input.TaskID)
	case application.WorktreeEventWorktreeReleased:
		if thread := findThread(state.Threads, input.TaskID); thread != nil {
			claimants(thread.WorktreeID)
		}
	case application.WorktreeEventWorktreeDeleted:
		claimants(worktreeWhere(func(worktree *domain.Worktree) bool {
			return worktree.ID == input.WorktreeID || worktree.Root == input.Root
		}))
	case contracts.WorktreeCommandWorktreeDeleteSafe:
		for _, root := range input.Roots {
			claimants(worktreeWhere(func(worktree *domain.Worktree) bool { return worktree.Root == root }))
		}
	case contracts.WorktreeCommandWorktreeDelete:
		taskID := orCurrent(input.TaskID)
		id := input.WorktreeID
		if id == nil {
			if input.Root != nil && *input.Root != "" {
				id = worktreeWhere(func(worktree *domain.Worktree) bool { return worktree.Root == *input.Root })
			} else if taskID != nil {
				if thread := findThread(state.Threads, *taskID); thread != nil {
					id = thread.WorktreeID
				}
			}
		}
		claimants(id)
	}
	return slices.DeleteFunc(ids, func(id string) bool {
		thread := findThread(state.Threads, id)
		return thread == nil || thread.HistorySummary == nil
	})
}

func (h *RuntimeHistory) Scope(input application.WorkspaceInput) application.InputScope {
	return application.InputScopeInputScope(h.host.state(), input)
}

// Invalidate drops every read in flight; what they read is discarded.
func (h *RuntimeHistory) Invalidate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.generation++
	clear(h.reads)
}

// PrepareThreadRequest loads what a request reads. A list search reads unloaded histories on disk, so
// it answers without loading them into the workspace. Nil when the request needed nothing prepared.
func (h *RuntimeHistory) PrepareThreadRequest(ctx context.Context, request contracts.ThreadRequest) (*PreparedThreadRequest, error) {
	remote := func(computer *string) bool {
		return computer != nil && *computer != "this" && *computer != "all"
	}
	switch request := request.(type) {
	case contracts.ThreadRequestRead:
		if remote(request.Computer) {
			return nil, nil
		}
		selected := "all"
		if request.Computer != nil {
			selected = *request.Computer
		}
		match, err := ResolveThreadRead(h.host.state(), request.ThreadID, selected)
		if err != nil {
			return nil, err
		}
		if match.Computer == nil {
			return nil, h.Hydrate(ctx, match.Thread.ID)
		}
		return nil, nil
	case contracts.ThreadRequestWait:
		match, err := ResolveThreadRead(h.host.state(), request.ThreadID, "all")
		if err != nil {
			return nil, err
		}
		if match.Computer == nil {
			return nil, h.Hydrate(ctx, match.Thread.ID)
		}
		return nil, nil
	case contracts.ThreadRequestList:
		if remote(request.Computer) || request.Search == nil || jsTrim(*request.Search) == "" {
			return nil, nil
		}
		state := h.host.state()
		project := request.Project
		if project == nil && request.Computer != nil && *request.Computer == "all" {
			project = new("all")
		}
		scope, err := application.ResolveScope(state, request.TaskID, project)
		if err != nil {
			return nil, nil
		}
		filter := contracts.ThreadFilter{Scope: scope, Archived: request.Archived, Attachments: request.Attachments, IdleForMs: request.IdleForMs}
		search := strings.ToLower(jsTrim(*request.Search))
		unloaded := map[string]bool{}
		for _, thread := range state.Threads {
			if thread.HistorySummary != nil {
				unloaded[thread.ID] = true
			}
		}
		var ids []string
		for _, thread := range application.ThreadSummaries(state, filter, application.Now()) {
			if unloaded[thread.ID] && !strings.Contains(strings.ToLower(thread.Title), search) {
				ids = append(ids, thread.ID)
			}
		}
		stored := jsonx.Set[string]{}
		if len(ids) > 0 {
			found, err := h.host.search(ctx, search, ids)
			if err != nil {
				return nil, err
			}
			stored = jsonx.SetOf(found...)
		}
		return &PreparedThreadRequest{Stored: stored}, nil
	}
	return nil, nil
}
