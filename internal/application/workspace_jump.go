package application

import (
	"math"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The jump panel's list: every thread it can offer, every settings page and control, and the ones a
// name matches. Thread names only, never message text, so a keystroke stays cheap however long the
// list grows.

// The options built for the last threads and projects read, by identity (TS: a WeakMap keyed by the threads).
var jumpCache struct {
	sync.Mutex
	threads  []*domain.Thread
	projects []*domain.Project
	options  []*domain.ThreadJumpOption
}

// Every thread the panel can offer, newest first. Rebuilt only when the threads or the folders change.
func threadJumpOptions(state *WorkspaceState) []*domain.ThreadJumpOption {
	jumpCache.Lock()
	defer jumpCache.Unlock()
	if jumpCache.options != nil && jsonx.SameSlice(jumpCache.threads, state.Threads) && jsonx.SameSlice(jumpCache.projects, state.Projects) {
		return jumpCache.options
	}
	forked := SideChatIds(state)
	names := make(map[string]string, len(state.Projects))
	for _, project := range state.Projects {
		names[project.ID] = domain.ProjectName(project)
	}
	var offered []*domain.Thread
	for _, thread := range state.Threads {
		if thread.ArchivedAt == nil && !forked.Has(thread.ID) {
			offered = append(offered, thread)
		}
	}
	recent := RecentFirst(offered)
	options := make([]*domain.ThreadJumpOption, len(recent))
	for index, thread := range recent {
		option := &domain.ThreadJumpOption{ID: thread.ID, Title: thread.Title, Engine: thread.Engine, PullRequest: thread.PullRequest, RankedAt: thread.RankedAt}
		if thread.ProjectID != nil && *thread.ProjectID != "" {
			if name, ok := names[*thread.ProjectID]; ok {
				option.Project = &name
			}
		}
		options[index] = option
	}
	jumpCache.threads, jumpCache.projects, jumpCache.options = state.Threads, state.Projects, options
	return options
}

// The jump panel as it is drawn: the threads its name matches, and which row is picked. Nil while it is closed.
func WorkspaceJumpJumpView(state *WorkspaceState, busy jsonx.Set[string]) *JumpView {
	jump := state.Jump
	if jump == nil {
		return nil
	}
	named := domain.PullRequestPullRequestQuery(jump.Query)
	options := []JumpRow{}
	for _, option := range domain.RankThreadJumps(threadJumpOptions(state), jump.Query, domain.ThreadJumpRows) {
		row := ThreadJumpRow{ThreadJumpOption: *option, Running: busy.Has(option.ID)}
		if named != nil && option.PullRequest != nil && domain.LinksPullRequest(option.PullRequest, named) {
			number := option.PullRequest.Number
			row.NamedPullRequest = &number
		}
		options = append(options, row)
	}
	for _, option := range domain.RankSettingsJumps(jump.Query, domain.SettingsJumpRows) {
		options = append(options, SettingJumpRow{SettingsJumpOption: *option})
	}
	index := 0.0
	if len(options) > 0 {
		index = math.Min(jump.Index, float64(len(options)-1))
	}
	return &JumpView{Query: jump.Query, Index: index, Options: options}
}
