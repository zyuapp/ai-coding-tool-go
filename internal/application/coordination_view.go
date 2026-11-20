package application

import (
	"encoding/json/jsontext"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// What a coordinator and every thread under it spent, resolved ones too, or nil before any of them
// has spent anything. Threads archived out from under it no longer count.
func CoordinationViewCoordinationUsage(members []*CoordinationMember, lead *domain.Thread) *CoordinationUsage {
	var byModel []*CoordinationUsageByModel
	keyed := map[string]*CoordinationUsageByModel{}
	spent := func(thread *domain.Thread) float64 {
		tokens := 0.0
		for _, recorded := range slices.Sorted(maps.Keys(thread.Usage)) {
			count := thread.Usage[recorded]
			model, _ := domain.CurrentModel(recorded).(string)
			key := string(thread.Engine) + "\x00" + model
			if entry := keyed[key]; entry != nil {
				entry.Tokens += count
			} else {
				entry = &CoordinationUsageByModel{Engine: thread.Engine, Model: model, Tokens: count}
				keyed[key] = entry
				byModel = append(byModel, entry)
			}
			tokens += count
		}
		return tokens
	}
	coordinator := spent(lead)
	byThread := []*CoordinationUsageByThread{}
	for _, member := range members {
		if tokens := spent(&member.Thread); tokens != 0 {
			byThread = append(byThread, &CoordinationUsageByThread{Thread: member.Thread, Computer: member.Computer, Tokens: tokens})
		}
	}
	total := coordinator
	for _, entry := range byThread {
		total += entry.Tokens
	}
	if total == 0 {
		return nil
	}
	spentModels := []*CoordinationUsageByModel{}
	for _, entry := range byModel {
		if entry.Tokens != 0 {
			spentModels = append(spentModels, entry)
		}
	}
	return &CoordinationUsage{
		Total:       total,
		Coordinator: coordinator,
		ByModel:     mostFirst(spentModels, func(entry *CoordinationUsageByModel) float64 { return entry.Tokens }),
		ByThread:    mostFirst(byThread, func(entry *CoordinationUsageByThread) float64 { return entry.Tokens }),
	}
}

// `entries` sorted most tokens first, in place and stable.
func mostFirst[Entry any](entries []Entry, tokens func(Entry) float64) []Entry {
	slices.SortStableFunc(entries, func(left, right Entry) int { return coordCompare(tokens(right), tokens(left)) })
	return entries
}

// The usage held before while every count, and every thread's name, is the same, so its section draws
// nothing anew. The threads it holds may be older ones, which is fine: it reads only their id and title.
func sameUsage(before, next *CoordinationUsage) *CoordinationUsage {
	if before == nil || next == nil {
		return next
	}
	same := before.Total == next.Total && before.Coordinator == next.Coordinator &&
		slices.EqualFunc(before.ByModel, next.ByModel, func(entry, other *CoordinationUsageByModel) bool {
			return entry.Engine == other.Engine && entry.Model == other.Model && entry.Tokens == other.Tokens
		}) &&
		slices.EqualFunc(before.ByThread, next.ByThread, func(entry, other *CoordinationUsageByThread) bool {
			return entry.Thread.ID == other.Thread.ID && entry.Thread.Title == other.Thread.Title && entry.Computer == other.Computer && entry.Tokens == other.Tokens
		})
	if same {
		return before
	}
	return next
}

var noCoordination CoordinationView = CoordinationViewFalse{
	Members:     []*CoordinatedThreadView{},
	Starts:      []*ThreadStart{},
	Approvals:   []*CoordinationApprovalView{},
	Decisions:   []*CoordinationDecisionView{},
	Proposals:   []*ProposalView{},
	ThreadModel: jsontext.Value("null"),
	Memory:      jsontext.Value("null"),
}

var noNotes = []*domain.MemoryNote{}

var noProposals = []*ProposalView{}

// The parts of a view last made that a new one may hand back.
type coordinationHeld struct {
	members   []*CoordinatedThreadView
	starts    []*ThreadStart
	proposals []*ProposalView
	usage     *CoordinationUsage
	waiting   *domain.WaitingOnUser
}

func coordinationHeldOf(previous CoordinationView) coordinationHeld {
	switch view := previous.(type) {
	case CoordinationViewTrue:
		return coordinationHeld{view.Members, view.Starts, view.Proposals, view.Usage, view.Waiting}
	case CoordinationViewFalse:
		return coordinationHeld{view.Members, view.Starts, view.Proposals, view.Usage, view.Waiting}
	}
	return coordinationHeld{}
}

// What the open thread shows. `approvalOf` may be nil (no approvals); `previous` is the view last
// made, or nil, whose objects are handed back where nothing they show changed. A thread on a paired
// computer stands, and waits on approvals, as that computer last said. `thread` may be nil.
func CoordinationViewCoordinationView(state *WorkspaceState, thread *domain.Thread, busy, blocked jsonx.Set[string], approvalOf func(threadID string) *ApprovalView, previous CoordinationView) CoordinationView {
	if approvalOf == nil {
		approvalOf = func(string) *ApprovalView { return nil }
	}
	if thread == nil {
		return noCoordination
	}
	if domain.IsCoordinator(thread) {
		before := coordinationHeldOf(previous)
		held := map[string]*CoordinatedThreadView{}
		for _, view := range before.members {
			held[view.Thread.ID] = view
		}
		coordinated := CoordinationMembers(state, thread.ID)
		views := make([]*CoordinatedThreadView, len(coordinated))
		memberThreads := make([]*domain.Thread, len(coordinated))
		for index, member := range coordinated {
			views[index] = memberView(state, member, busy, blocked, held[member.Thread.ID])
			memberThreads[index] = &member.Thread
		}
		views = sameItems(before.members, views)
		starts := []*ThreadStart{}
		for _, view := range views {
			if view.StartedAt != nil {
				starts = append(starts, &ThreadStart{ID: view.Thread.ID, At: *view.StartedAt})
			}
		}
		slices.SortStableFunc(starts, func(left, right *ThreadStart) int { return coordCompare(left.At, right.At) })
		approvals := []*CoordinationApprovalView{}
		if own := approvalOf(thread.ID); own != nil {
			approvals = append(approvals, &CoordinationApprovalView{Thread: *thread, Approval: *own})
		}
		for index, view := range views {
			if view.Status != CoordinatedThreadStatusApproval {
				continue
			}
			if approval := memberApproval(coordinated[index], approvalOf); approval != nil {
				approvals = append(approvals, &CoordinationApprovalView{Thread: view.Thread, Approval: *approval})
			}
		}
		decisions := []*CoordinationDecisionView{}
		for _, decision := range domain.CoordinationDecisions(thread, memberThreads) {
			decisions = append(decisions, &CoordinationDecisionView{Thread: *decision.Thread, Decision: *decision.Decision})
		}
		memory := CoordinatorMemoryView{Notes: noNotes}
		if thread.Memory != nil {
			memory.Purpose = thread.Memory.Purpose
			if thread.Memory.Notes != nil {
				memory.Notes = thread.Memory.Notes
			}
		}
		catalog := CatalogOf(state)
		waiting := sameWaiting(before.waiting, domain.CoordinationWaitingOnUser(thread, memberThreads, state.StartingProposals))
		return CoordinationViewTrue{
			Members:     views,
			Starts:      sameStarts(before.starts, starts),
			Approvals:   approvals,
			Decisions:   decisions,
			Proposals:   proposalViews(before.proposals, thread.Proposals, state.StartingProposals, state.Computers.Paired),
			Usage:       sameUsage(before.usage, CoordinationViewCoordinationUsage(coordinated, thread)),
			Brief:       thread.Brief,
			Waiting:     waiting,
			ThreadModel: domain.ThreadModelOf(&catalog, thread),
			Memory:      memory,
		}
	}
	lead := domain.CoordinatorOf(state.Threads, thread)
	var remote *domain.HeldLead
	if lead == nil {
		remote = domain.RemoteLeadOf(thread)
	}
	// A thread that left its coordinator with a decision still open is where that decision is answered now.
	decisions := []*CoordinationDecisionView{}
	if lead == nil && remote == nil {
		for _, decision := range domain.OpenDecisions(thread) {
			decisions = append(decisions, &CoordinationDecisionView{Thread: *thread, Decision: *decision})
		}
	}
	if lead == nil && remote == nil && thread.Brief == nil && len(decisions) == 0 {
		return noCoordination
	}
	view := noCoordination.(CoordinationViewFalse)
	view.Lead = lead
	view.Decisions = decisions
	view.Brief = thread.Brief
	if remote != nil {
		held := slices.ContainsFunc(state.Computers.Paired, func(computer *PairedComputer) bool {
			return computer.State != nil && slices.ContainsFunc(computer.State.Threads, func(item *domain.Thread) bool { return item.ID == remote.ThreadID })
		})
		view.RemoteLead = &CoordinationViewTrueRemoteLead{Title: remote.Title, Computer: remote.Computer}
		if held {
			view.RemoteLead.ThreadID = &remote.ThreadID
		}
	}
	if lead != nil || remote != nil {
		view.Asking = WaitingLine(domain.CoordinationWaitingOnUser(thread, []*domain.Thread{}, state.StartingProposals), "")
	}
	return view
}

// The count last made while it has not moved, so the badge reading it draws nothing anew.
func sameWaiting(before *domain.WaitingOnUser, next domain.WaitingOnUser) *domain.WaitingOnUser {
	if before != nil && before.Decisions == next.Decisions && before.Suggestions == next.Suggestions && before.Blocked == next.Blocked && before.Failed == next.Failed && strings.Join(before.Requests, "\n") == strings.Join(next.Requests, "\n") {
		return before
	}
	return &next
}

// The approval a thread under a coordinator waits on, read on the computer that holds it.
func memberApproval(member *CoordinationMember, approvalOf func(threadID string) *ApprovalView) *ApprovalView {
	if member.Computer == nil {
		return approvalOf(member.Thread.ID)
	}
	run := RunRecord(member.Holder.ActiveRuns[member.Thread.ID])
	if run != nil && run.Status == ActiveRunRunningStatusAwaitingApproval {
		return member.Holder.Approvals[run.RunID]
	}
	return nil
}

// A thread's view, or the one held for it when nothing it shows has changed.
func memberView(state *WorkspaceState, member *CoordinationMember, busy, blocked jsonx.Set[string], held *CoordinatedThreadView) *CoordinatedThreadView {
	thread := &member.Thread
	working, waiting := MemberActivity(member, busy, blocked)
	standing := CoordinatedThreadStatusCoordinatedThreadStatus(thread, working, waiting)
	// What GitHub said of its checkout is known where the checkout is.
	checkoutState := state
	if member.Computer != nil {
		checkoutState = member.Holder
	}
	steps := NextSteps(thread, standing.Status, CheckoutAnswer(checkoutState, thread))
	var host *ThreadHost
	if member.Computer != nil {
		host = ThreadHostOf(&member.Computer.ComputerLink)
	}
	// The pull request and the start are read off the thread, so the same thread has the same ones.
	if held != nil && coordShallowSame(reflect.ValueOf(held.Thread), reflect.ValueOf(*thread)) && held.Status == standing.Status &&
		coordSameText(held.Summary, standing.Summary) && slices.Equal(held.Steps, steps) && sameHost(held.Host, host) {
		return held
	}
	var startedAt *float64
	if thread.Brief != nil {
		startedAt = new(domain.ThreadCreatedAt(thread))
	}
	return &CoordinatedThreadView{
		CoordinatedThreadStanding: standing,
		PullRequest:               CoordinatedThreadStatusOwnPullRequest(thread),
		Steps:                     steps,
		StartedAt:                 startedAt,
		Host:                      host,
		LastHeard:                 standing.Status == CoordinatedThreadStatusWorking && host != nil && host.Offline,
	}
}

func coordSameText(before, next *string) bool {
	return before == next || before != nil && next != nil && *before == *next
}

func sameHost(before, next *ThreadHost) bool {
	return before == next || before != nil && next != nil && before.ID == next.ID && before.Name == next.Name && before.Offline == next.Offline && coordTrue(before.Reconnecting) == coordTrue(next.Reconnecting) && (before.Reconnecting == nil) == (next.Reconnecting == nil)
}

// coordShallowSame is TypeScript's === on two copies of one record: every field the same value, slices,
// maps and pointers by identity, structs held inline field by field. Copies of one object always are.
func coordShallowSame(a, b reflect.Value) bool {
	if a.Kind() == reflect.Struct {
		for index := range a.NumField() {
			if !coordShallowSame(a.Field(index), b.Field(index)) {
				return false
			}
		}
		return true
	}
	if !a.CanInterface() {
		return false
	}
	if a.Kind() == reflect.Interface && !a.IsNil() && !b.IsNil() && a.Elem().Type() == b.Elem().Type() && a.Elem().Kind() == reflect.Struct {
		return coordShallowSame(a.Elem(), b.Elem())
	}
	return jsonx.Same(a.Interface(), b.Interface())
}

// Each suggestion's view, the one held before while neither it, whether it is starting, nor for one
// elsewhere what that computer can take changed. `before` is nil when there was none.
func proposalViews(before []*ProposalView, proposals []*domain.ThreadProposal, starting []string, paired []*PairedComputer) []*ProposalView {
	if len(proposals) == 0 {
		return noProposals
	}
	views := make([]*ProposalView, len(proposals))
	for index, proposal := range proposals {
		isStarting := slices.Contains(starting, proposal.ID)
		elsewhere := coordinationViewElsewhereView(proposal, paired)
		var view *ProposalView
		for _, held := range before {
			if coordShallowSame(reflect.ValueOf(held.Proposal), reflect.ValueOf(*proposal)) {
				view = held
				break
			}
		}
		if view != nil && view.Starting == isStarting && sameElsewhere(view.Elsewhere, elsewhere) {
			views[index] = view
		} else {
			views[index] = &ProposalView{Proposal: *proposal, Starting: isStarting, Elsewhere: elsewhere}
		}
	}
	return sameItems(before, views)
}

// Where a thread asked for on another computer would run, as that computer now stands: the folder
// asked for while it is still there, else the first one there is. Nil for one here.
func coordinationViewElsewhereView(proposal *domain.ThreadProposal, paired []*PairedComputer) *ElsewhereView {
	place := proposal.Place.Computer
	if place == nil {
		return nil
	}
	var computer *PairedComputer
	for _, item := range paired {
		if item.ID == place.ID {
			computer = item
			break
		}
	}
	asked := ""
	if proposal.Place.Project != nil {
		asked = *proposal.Place.Project
	}
	projects := []*domain.Project{}
	if computer != nil && computer.State != nil {
		for _, project := range computer.State.Projects {
			if !coordTrue(project.Unlisted) || proposal.Place.Project != nil && project.ID == asked {
				projects = append(projects, project)
			}
		}
	}
	var project *string
	for _, item := range projects {
		if proposal.Place.Project != nil && item.ID == asked {
			project = &item.ID
			break
		}
	}
	if project == nil && len(projects) > 0 {
		project = &projects[0].ID
	}
	refusal := LedStartRefusal(computer)
	if refusal == nil && project == nil && computer != nil {
		refusal = new("That folder is no longer on " + computer.Name + ".")
	}
	link := &domain.ComputerLink{ID: place.ID, Name: place.Name, Status: domain.ComputerStatusOffline}
	if computer != nil {
		link = &computer.ComputerLink
	}
	return &ElsewhereView{Computer: *ThreadHostOf(link), Projects: projects, Project: project, Refusal: refusal}
}

func sameElsewhere(before, next *ElsewhereView) bool {
	if before == nil || next == nil {
		return before == next
	}
	return sameHost(&before.Computer, &next.Computer) && coordSameText(before.Project, next.Project) && coordSameText(before.Refusal, next.Refusal) &&
		slices.Equal(before.Projects, next.Projects)
}

// The list held before when it holds the same items in the same order, else the new one. A nil
// `before` is no list.
func sameItems[Item comparable](before, next []Item) []Item {
	if before != nil && slices.Equal(before, next) {
		return before
	}
	return next
}

func sameStarts(before, next []*ThreadStart) []*ThreadStart {
	if before != nil && slices.EqualFunc(before, next, func(start, other *ThreadStart) bool { return start.ID == other.ID && start.At == other.At }) {
		return before
	}
	return next
}

// The members filed under each Overview group, every group present.
func OverviewGroups(members []*CoordinatedThreadView) map[domain.OverviewGroup][]*CoordinatedThreadView {
	groups := map[domain.OverviewGroup][]*CoordinatedThreadView{
		domain.OverviewGroupNeeds:    {},
		domain.OverviewGroupReview:   {},
		domain.OverviewGroupWorking:  {},
		domain.OverviewGroupDone:     {},
		domain.OverviewGroupResolved: {},
	}
	for _, member := range members {
		group := GroupOf[member.Status]
		groups[group] = append(groups[group], member)
	}
	return groups
}
