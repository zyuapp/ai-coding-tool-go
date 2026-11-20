package application

import (
	"reflect"
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// heldProjection is a selector's last projection and the inputs it was made from.
type heldProjection[Value any] struct {
	inputs []any
	value  Value
}

// HeldSelector retains one projection while its inputs stay the same by identity (sameInput). A new
// projection is handed the last (`previous`, with `held` false for none), so it can keep whatever
// parts of it still stand. `equal` may be nil.
type HeldSelector[Value any] struct {
	inputs  func(state *WorkspaceState) []any
	project func(state *WorkspaceState, previous Value, held bool) Value
	equal   func(before, next Value) bool
	held    *heldProjection[Value]
}

func (s *HeldSelector[Value]) Select(state *WorkspaceState) Value {
	nextInputs := s.inputs(state)
	previous := s.held
	if previous != nil && len(nextInputs) == len(previous.inputs) {
		kept := true
		for index, value := range nextInputs {
			if !sameInput(value, previous.inputs[index]) {
				kept = false
				break
			}
		}
		if kept {
			return previous.value
		}
	}
	var last Value
	if previous != nil {
		last = previous.value
	}
	value := s.project(state, last, previous != nil)
	if previous != nil && s.equal != nil && s.equal(previous.value, value) {
		value = previous.value
	}
	s.held = &heldProjection[Value]{inputs: nextInputs, value: value}
	return value
}

func (s *HeldSelector[Value]) Release() {
	s.held = nil
}

func heldSelector[Value any](inputs func(state *WorkspaceState) []any, project func(state *WorkspaceState, previous Value, held bool) Value, equal func(before, next Value) bool) *HeldSelector[Value] {
	return &HeldSelector[Value]{inputs: inputs, project: project, equal: equal}
}

func sameIds(before, next jsonx.Set[string]) bool {
	if len(before) != len(next) {
		return false
	}
	for id := range next {
		if !before.Has(id) {
			return false
		}
	}
	return true
}

func linkOf(computer *PairedComputer) *domain.ComputerLink {
	link := computer.ComputerLink
	return &link
}

func sameLinks(before, next []*domain.ComputerLink) bool {
	if len(before) != len(next) {
		return false
	}
	for index, link := range before {
		other := next[index]
		if link.ID != other.ID || link.Name != other.Name || link.Host != other.Host || link.Status != other.Status || !sameOptionalString(link.Error, other.Error) {
			return false
		}
	}
	return true
}

// sameWorktrees: a paired computer sends its whole state again on every change, so its checkouts are
// compared by what they hold.
func sameWorktrees(before, next map[string]*domain.Worktree) bool {
	if len(before) != len(next) {
		return false
	}
	for id, worktree := range next {
		held := before[id]
		if held == nil || (held != worktree && !SameFields(held, worktree)) {
			return false
		}
	}
	return true
}

// union is `own` when `others` is empty, else a new set of both.
func union(own, others jsonx.Set[string]) jsonx.Set[string] {
	if len(others) == 0 {
		return own
	}
	both := make(jsonx.Set[string], len(own)+len(others))
	for id := range own {
		both[id] = struct{}{}
	}
	for id := range others {
		both[id] = struct{}{}
	}
	return both
}

// WorkspaceCollections is what a ViewCollections hands back: workspace-wide collections that do not
// rebuild when only a composer, streaming tail, or panel changes.
type WorkspaceCollections struct {
	ListedThreads          []*domain.Thread
	VisibleThreads         []*domain.Thread
	ArchivedThreads        []*domain.Thread
	WorktreeThreadIDs      jsonx.Set[string]
	SideChatAttention      jsonx.Set[string]
	UnreadCount            float64
	Lists                  SidebarListing
	StartProjects          []*domain.Project
	ProjectSettings        []*ProjectSettingsRow
	Busy                   jsonx.Set[string]
	Blocked                jsonx.Set[string]
	EveryBusy              jsonx.Set[string]
	EveryRanked            jsonx.Set[string]
	EveryMonitoring        jsonx.Set[string]
	EveryBlocked           jsonx.Set[string]
	EveryStoppable         jsonx.Set[string]
	Remote                 RemoteCollections
	ComputerLinks          []*domain.ComputerLink
	ComputerUsage          []*ComputerUsage
	ComputerUpkeep         []*ComputerUpkeep
	ManagedWorktrees       jsonx.NullSlice[*WorktreeSettingsView]
	WorktreeSettings       WorktreeSettingsPage
	WorktreeGroups         []*WorktreeGroup
	WorktreesByID          map[string]*domain.Worktree
	Schedules              map[string]*domain.AutomationView
	CoordinatorsByComputer map[*string][]*domain.Thread
	Coordination           CoordinationView
	MemberPullRequests     MemberPullRequestsView
	MemberWorktrees        []*MemberWorktree
}

// ViewCollections is one source's collections, each selector holding that source's last projection
// (TS: createViewCollections' callable). Safe for one goroutine at a time; Of and Release lock.
type ViewCollections struct {
	mu       sync.Mutex
	releases []func()
	of       func(state *WorkspaceState) *WorkspaceCollections
}

// Of is the collections for `state`.
func (c *ViewCollections) Of(state *WorkspaceState) *WorkspaceCollections {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.of(state)
}

// Release drops every held projection, so a source no longer shown keeps none of its state alive.
func (c *ViewCollections) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, release := range c.releases {
		release()
	}
}

func createViewCollections() *ViewCollections {
	var releases []func()
	held := func(release func()) { releases = append(releases, release) }
	activity := ThreadActivityThreadActivity
	lists := ThreadActivityThreadLists

	// Each of this computer's activity sets, kept by identity while its members stay the same.
	activitySet := func(pick func(activity *ThreadActivity) jsonx.Set[string]) *HeldSelector[jsonx.Set[string]] {
		return heldSelector(
			func(state *WorkspaceState) []any { return []any{pick(activity(state))} },
			func(state *WorkspaceState, _ jsonx.Set[string], _ bool) jsonx.Set[string] {
				return pick(activity(state))
			},
			sameIds,
		)
	}
	busy := activitySet(func(activity *ThreadActivity) jsonx.Set[string] { return activity.Working })
	blocked := activitySet(func(activity *ThreadActivity) jsonx.Set[string] { return activity.Blocked })
	ranked := activitySet(func(activity *ThreadActivity) jsonx.Set[string] { return activity.Ranked })
	monitoring := activitySet(func(activity *ThreadActivity) jsonx.Set[string] { return activity.Monitoring })
	held(busy.Release)
	held(blocked.Release)
	held(ranked.Release)
	held(monitoring.Release)

	// The threads that can take others under them, for the menu that moves a thread under one, by the
	// computer that holds them: this one's under nil. Only a coordinator on the computer holding a
	// thread can take it, as only that computer moves it.
	coordinatorsByComputer := heldSelector(
		func(state *WorkspaceState) []any { return []any{lists(state).ListedThreads, state.Computers.Paired} },
		func(state *WorkspaceState, _ map[*string][]*domain.Thread, _ bool) map[*string][]*domain.Thread {
			coordinators := func(threads []*domain.Thread) []*domain.Thread {
				leads := []*domain.Thread{}
				for _, thread := range threads {
					if domain.IsCoordinator(thread) {
						leads = append(leads, thread)
					}
				}
				return leads
			}
			byComputer := map[*string][]*domain.Thread{nil: coordinators(lists(state).ListedThreads)}
			for _, computer := range state.Computers.Paired {
				if computer.State != nil {
					byComputer[&computer.ID] = coordinators(lists(computer.State).ListedThreads)
				}
			}
			return byComputer
		},
		nil,
	)
	held(coordinatorsByComputer.Release)

	// The coordinators Stop all has something to stop under, read by every place that offers it.
	stoppable := heldSelector(
		func(state *WorkspaceState) []any {
			return []any{state.Threads, state.Computers.Paired, state.ActiveRuns, state.PendingRuns, state.StartingProposals, state.Workflows, state.Subagents}
		},
		func(state *WorkspaceState, _ jsonx.Set[string], _ bool) jsonx.Set[string] {
			return StoppableCoordinators(state)
		},
		sameIds,
	)
	held(stoppable.Release)

	// What the open thread shows: a coordinator's threads, open decisions and suggestions, or a thread's
	// coordinator and brief. Each of a coordinator's threads keeps its view object while nothing it shows moves.
	coordination := heldSelector(
		func(state *WorkspaceState) []any {
			return []any{state.Threads, deref(state.CurrentID), state.Computers.Paired, busy.Select(state), blocked.Select(state), state.ActiveRuns, state.Approvals, state.Projects, state.Worktrees, state.MemberPullRequests, state.StartingProposals}
		},
		func(state *WorkspaceState, previous CoordinationView, _ bool) CoordinationView {
			approvalOf := func(threadID string) *ApprovalView {
				run := RunRecord(state.ActiveRuns[threadID])
				if run == nil || run.Status != ActiveRunRunningStatusAwaitingApproval {
					return nil
				}
				return state.Approvals[run.RunID]
			}
			return CoordinationViewCoordinationView(state, currentThread(state), busy.Select(state), blocked.Select(state), approvalOf, previous)
		},
		nil,
	)
	held(coordination.Release)

	// The pull requests the open coordinator's threads work on.
	memberPullRequests := heldSelector(
		func(state *WorkspaceState) []any {
			return []any{state.Threads, deref(state.CurrentID), state.Projects, state.Worktrees, state.Computers.Paired, state.Environments, state.MemberPullRequests}
		},
		func(state *WorkspaceState, _ MemberPullRequestsView, _ bool) MemberPullRequestsView {
			return PullRequestViewMemberPullRequestsView(state, currentLead(state))
		},
		nil,
	)
	held(memberPullRequests.Release)

	// The checkouts the open coordinator's threads work in, which its panel offers to clean up.
	memberWorktrees := heldSelector(
		func(state *WorkspaceState) []any {
			return []any{state.Threads, deref(state.CurrentID), state.Worktrees, state.Environments, state.ManagedWorktrees, state.DeletingWorktrees, state.ReleasingWorktrees, busy.Select(state)}
		},
		func(state *WorkspaceState, _ []*MemberWorktree, _ bool) []*MemberWorktree {
			return MemberWorktreesView(state, currentLead(state), busy.Select(state))
		},
		nil,
	)
	held(memberWorktrees.Release)

	// The paired computers' threads, gathered once per change to any of them.
	remote := heldSelector(
		func(state *WorkspaceState) []any { return []any{state.Computers.Paired, state.Computers.Filter} },
		func(state *WorkspaceState, _ *RemoteCollections, _ bool) *RemoteCollections {
			return ComputersRemoteCollections(&state.Computers)
		},
		nil,
	)
	held(remote.Release)

	// The paired computers without their states, which the chrome draws and which move only when a line does.
	links := heldSelector(
		func(state *WorkspaceState) []any { return []any{state.Computers.Paired} },
		func(state *WorkspaceState, _ []*domain.ComputerLink, _ bool) []*domain.ComputerLink {
			links := make([]*domain.ComputerLink, len(state.Computers.Paired))
			for index, computer := range state.Computers.Paired {
				links[index] = linkOf(computer)
			}
			return links
		},
		sameLinks,
	)
	held(links.Release)
	usage := heldSelector(
		func(state *WorkspaceState) []any { return []any{state.Computers.Paired} },
		func(state *WorkspaceState, _ []*ComputerUsage, _ bool) []*ComputerUsage {
			return PlanLimitsComputerUsage(state.Computers.Paired)
		},
		SameComputerUsage,
	)
	held(usage.Release)
	upkeep := heldSelector(
		func(state *WorkspaceState) []any { return []any{state.Computers.Paired, state.AppBuild} },
		func(state *WorkspaceState, _ []*ComputerUpkeep, _ bool) []*ComputerUpkeep {
			return ComputerUpdatesComputerUpkeep(state.Computers.Paired, state.AppBuild)
		},
		SameComputerUpkeep,
	)
	held(upkeep.Release)

	// Every computer's sets together, which is what the rows of a merged list read.
	everySet := func(own func(state *WorkspaceState) jsonx.Set[string], others func(remote *RemoteCollections) jsonx.Set[string]) *HeldSelector[jsonx.Set[string]] {
		selector := heldSelector(
			func(state *WorkspaceState) []any { return []any{own(state), remote.Select(state)} },
			func(state *WorkspaceState, _ jsonx.Set[string], _ bool) jsonx.Set[string] {
				return union(own(state), others(remote.Select(state)))
			},
			sameIds,
		)
		held(selector.Release)
		return selector
	}
	everyBusy := everySet(busy.Select, func(remote *RemoteCollections) jsonx.Set[string] { return remote.Busy })
	// What the activity list ranks as running: every busy thread, and every thread a live watch holds between its ticks.
	everyRanked := everySet(ranked.Select, func(remote *RemoteCollections) jsonx.Set[string] { return remote.Ranked })
	everyMonitoring := everySet(monitoring.Select, func(remote *RemoteCollections) jsonx.Set[string] { return remote.Monitoring })
	everyStoppable := everySet(stoppable.Select, func(remote *RemoteCollections) jsonx.Set[string] { return remote.Stoppable })
	everyBlocked := everySet(blocked.Select, func(remote *RemoteCollections) jsonx.Set[string] { return remote.Blocked })
	everySideChatAttention := everySet(func(state *WorkspaceState) jsonx.Set[string] { return lists(state).SideChatAttention }, func(remote *RemoteCollections) jsonx.Set[string] { return remote.SideChatAttention })
	everyWorktree := everySet(func(state *WorkspaceState) jsonx.Set[string] { return lists(state).WorktreeThreadIDs }, func(remote *RemoteCollections) jsonx.Set[string] { return remote.WorktreeThreadIDs })

	// The sidebar draws every computer's threads together, filed under every computer's folders.
	sidebar := heldSelector(
		func(state *WorkspaceState) []any {
			return []any{lists(state).VisibleThreads, state.Projects, everyRanked.Select(state), everyBlocked.Select(state), remote.Select(state), state.Computers.Filter, deref(state.ActivityProject), state.Sections, state.StartingProposals}
		},
		func(state *WorkspaceState, _ SidebarListing, _ bool) SidebarListing {
			others := remote.Select(state)
			// A filter naming one paired computer leaves this computer's own out.
			own := state.Computers.Filter == "all" || state.Computers.Filter == "this"
			projects := []*domain.Project{}
			if own {
				projects = state.Projects
			}
			if len(others.Projects) > 0 {
				projects = slices.Concat(projects, others.Projects)
			}
			visible := []*domain.Thread{}
			if own {
				visible = lists(state).VisibleThreads
			}
			threads := visible
			if len(others.Threads) > 0 {
				threads = slices.Concat(visible, others.Threads)
			}
			preferences := SidebarPreferences{Sections: state.Sections, ActivityProject: state.ActivityProject, StartingProposals: state.StartingProposals}
			return SidebarLists(preferences, projects, threads, everyRanked.Select(state), everyBlocked.Select(state), others.ProjectHosts)
		},
		nil,
	)
	held(sidebar.Release)

	// Every computer's projects, this one's first, for a draft to start in. The sidebar's filter narrows
	// what is listed, never where a thread may begin, so the draft's own project is always among these.
	// A computer whose line is down takes no thread, so its projects wait with it.
	startProjects := heldSelector(
		func(state *WorkspaceState) []any { return []any{state.Projects, state.Computers.Paired} },
		func(state *WorkspaceState, _ []*domain.Project, _ bool) []*domain.Project {
			own := OrderProjects(state.Projects)
			var others []*domain.Project
			for _, computer := range state.Computers.Paired {
				shown := computer.State
				if shown == nil || computer.Status != domain.ComputerStatusConnected {
					continue
				}
				offered := []*domain.Project{}
				for _, project := range shown.Projects {
					if project.Unlisted == nil || !*project.Unlisted || (shown.DraftProjectID != nil && project.ID == *shown.DraftProjectID) {
						offered = append(offered, project)
					}
				}
				others = append(others, OrderProjects(offered)...)
			}
			if len(others) > 0 {
				return slices.Concat(own, others)
			}
			return own
		},
		nil,
	)
	held(startProjects.Release)

	// The Projects page lists every computer's projects, whatever the sidebar is narrowed to.
	projectSettings := heldSelector(
		func(state *WorkspaceState) []any { return []any{state.Projects, state.Threads, state.Computers.Paired} },
		func(state *WorkspaceState, _ []*ProjectSettingsRow, _ bool) []*ProjectSettingsRow {
			return ProjectSettingsRows(state)
		},
		nil,
	)
	held(projectSettings.Release)

	managed := heldSelector(
		func(state *WorkspaceState) []any {
			return []any{state.ManagedWorktrees, state.Projects, state.Worktrees, state.Threads, state.DeletingWorktrees, state.ReleasingWorktrees, busy.Select(state)}
		},
		func(state *WorkspaceState, _ jsonx.NullSlice[*WorktreeSettingsView], _ bool) jsonx.NullSlice[*WorktreeSettingsView] {
			return WorktreeSettingsViews(state, busy.Select(state))
		},
		nil,
	)
	held(managed.Release)

	settings := heldSelector(
		func(state *WorkspaceState) []any {
			return []any{managed.Select(state), state.Projects, state.WorktreeSettings, state.WorktreeManagementLoading}
		},
		func(state *WorkspaceState, _ WorktreeSettingsPage, _ bool) WorktreeSettingsPage {
			return WorktreeSettingsWorktreeSettingsPage(state, managed.Select(state))
		},
		nil,
	)
	held(settings.Release)

	groups := heldSelector(
		func(state *WorkspaceState) []any {
			return []any{sidebar.Select(state).OrderedThreads, state.Worktrees, remote.Select(state)}
		},
		func(state *WorkspaceState, _ []*WorktreeGroup, _ bool) []*WorktreeGroup {
			byWorktree := map[string][]*domain.Thread{}
			for _, thread := range sidebar.Select(state).OrderedThreads {
				if thread.WorktreeID == nil || *thread.WorktreeID == "" {
					continue
				}
				byWorktree[*thread.WorktreeID] = append(byWorktree[*thread.WorktreeID], thread)
			}
			worktrees := slices.Concat(state.Worktrees, remote.Select(state).Worktrees)
			groups := make([]*WorktreeGroup, len(worktrees))
			for index, worktree := range worktrees {
				threads := byWorktree[worktree.ID]
				if threads == nil {
					threads = []*domain.Thread{}
				}
				groups[index] = &WorktreeGroup{Worktree: *worktree, Threads: threads}
			}
			return groups
		},
		nil,
	)
	held(groups.Release)

	// Every computer's checkouts by id, for the rows that name the one a thread works in. Unlike the
	// groups, it does not move when a thread does, so a row whose thread did not move draws nothing anew.
	worktreesByID := heldSelector(
		func(state *WorkspaceState) []any { return []any{state.Worktrees, remote.Select(state).Worktrees} },
		func(state *WorkspaceState, _ map[string]*domain.Worktree, _ bool) map[string]*domain.Worktree {
			byID := map[string]*domain.Worktree{}
			for _, worktree := range slices.Concat(state.Worktrees, remote.Select(state).Worktrees) {
				byID[worktree.ID] = worktree
			}
			return byID
		},
		sameWorktrees,
	)
	held(worktreesByID.Release)

	schedules := heldSelector(
		func(state *WorkspaceState) []any { return []any{state.Automations} },
		func(state *WorkspaceState, _ map[string]*domain.AutomationView, _ bool) map[string]*domain.AutomationView {
			byTask := map[string]*domain.AutomationView{}
			for _, automation := range state.Automations {
				byTask[automation.TaskID] = automation
			}
			return byTask
		},
		nil,
	)
	held(schedules.Release)

	return &ViewCollections{releases: releases, of: func(state *WorkspaceState) *WorkspaceCollections {
		threadLists := lists(state)
		return &WorkspaceCollections{
			ListedThreads:          threadLists.ListedThreads,
			VisibleThreads:         threadLists.VisibleThreads,
			ArchivedThreads:        threadLists.ArchivedThreads,
			WorktreeThreadIDs:      everyWorktree.Select(state),
			SideChatAttention:      everySideChatAttention.Select(state),
			UnreadCount:            threadLists.UnreadCount + remote.Select(state).UnreadCount,
			Lists:                  sidebar.Select(state),
			StartProjects:          startProjects.Select(state),
			ProjectSettings:        projectSettings.Select(state),
			Busy:                   busy.Select(state),
			Blocked:                blocked.Select(state),
			EveryBusy:              everyBusy.Select(state),
			EveryRanked:            everyRanked.Select(state),
			EveryMonitoring:        everyMonitoring.Select(state),
			EveryBlocked:           everyBlocked.Select(state),
			EveryStoppable:         everyStoppable.Select(state),
			Remote:                 *remote.Select(state),
			ComputerLinks:          links.Select(state),
			ComputerUsage:          usage.Select(state),
			ComputerUpkeep:         upkeep.Select(state),
			ManagedWorktrees:       managed.Select(state),
			WorktreeSettings:       settings.Select(state),
			WorktreeGroups:         groups.Select(state),
			WorktreesByID:          worktreesByID.Select(state),
			Schedules:              schedules.Select(state),
			CoordinatorsByComputer: coordinatorsByComputer.Select(state),
			Coordination:           coordination.Select(state),
			MemberPullRequests:     memberPullRequests.Select(state),
			MemberWorktrees:        memberWorktrees.Select(state),
		}
	}}
}

// WorkspaceViewCollections is this computer's own collections.
var WorkspaceViewCollections = createViewCollections()

// ShownViewCollections is a paired computer's collections while its thread is on screen, held apart
// so the two never evict each other.
var ShownViewCollections = createViewCollections()

func currentThread(state *WorkspaceState) *domain.Thread {
	if state.CurrentID == nil {
		return nil
	}
	return threadsFind(state.Threads, *state.CurrentID)
}

// currentLead is the thread on screen when it coordinates others, else nil.
func currentLead(state *WorkspaceState) *domain.Thread {
	if current := currentThread(state); domain.IsCoordinator(current) {
		return current
	}
	return nil
}

// sameInput is TypeScript's === on a selector's inputs: jsonx.Same, with struct values (objects held
// inline) the same when every field is.
func sameInput(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, right := reflect.ValueOf(a), reflect.ValueOf(b)
	return left.Type() == right.Type() && sameValue(left, right)
}
