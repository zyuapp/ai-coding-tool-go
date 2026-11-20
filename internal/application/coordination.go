package application

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Which kind of run the agent process starts: "coordinator", "member", or "" for neither. A thread a
// coordinator on another computer started is a member here, where its reports are recorded.
func CoordinationRole(threads []*domain.Thread, thread *domain.Thread) domain.CoordinationRole {
	if domain.IsCoordinator(thread) {
		return domain.CoordinationRoleCoordinator
	}
	if domain.CoordinatorOf(threads, thread) != nil || domain.RemoteLeadOf(thread) != nil {
		return domain.CoordinationRoleMember
	}
	return ""
}

var coordinationNoMembers = []*CoordinationMember{}

// The members of each coordinator, held for one list of threads and of paired computers, so every
// view of one coordinator reads the same members.
type coordinationMembersHeld struct {
	threads []*domain.Thread
	paired  []*PairedComputer
	byLead  map[string][]*CoordinationMember
}

var members struct {
	sync.Mutex
	held *coordinationMembersHeld
}

// Every thread working under a live coordinator: this computer's, in list order, then each paired
// computer's that names it as their coordinator. This is the one set its views, its roster and its
// wake read. The list returned is shared: callers never change it.
func CoordinationMembers(state *WorkspaceState, leadID string) []*CoordinationMember {
	members.Lock()
	held := members.held
	if held == nil || !jsonx.SameSlice(held.threads, state.Threads) || !jsonx.SameSlice(held.paired, state.Computers.Paired) {
		held = &coordinationMembersHeld{threads: state.Threads, paired: state.Computers.Paired, byLead: map[string][]*CoordinationMember{}}
		members.held = held
	}
	found, ok := held.byLead[leadID]
	members.Unlock()
	if ok {
		return found
	}
	found = coordinationNoMembers
	if lead := coordThread(state.Threads, leadID); lead != nil && domain.IsCoordinator(lead) {
		var gathered []*CoordinationMember
		for _, thread := range domain.CoordinatedThreads(state.Threads, leadID) {
			gathered = append(gathered, &CoordinationMember{Thread: *thread})
		}
		for _, computer := range state.Computers.Paired {
			holder := computer.State
			if holder == nil {
				continue
			}
			for _, thread := range RemotelyLedThreads(holder.Threads) {
				if thread.RemoteLead.ThreadID == leadID {
					gathered = append(gathered, &CoordinationMember{Thread: *thread, Computer: computer, Holder: holder})
				}
			}
		}
		if len(gathered) > 0 {
			found = gathered
		}
	}
	members.Lock()
	if members.held == held {
		held.byLead[leadID] = found
	}
	members.Unlock()
	return found
}

// Which threads are working and which wait on an approval, as the computer holding a member last
// said; this computer's own read `working` and `blocked`.
func MemberActivity(member *CoordinationMember, working, blocked jsonx.Set[string]) (jsonx.Set[string], jsonx.Set[string]) {
	if member.Computer != nil {
		activity := ThreadActivityThreadActivity(member.Holder)
		return activity.Working, activity.Blocked
	}
	return working, blocked
}

// Where the threads under a coordinator stand: this computer's as `here` says, each paired
// computer's as that computer last said. Only the paired computers `*on` count; a nil `on` counts every one.
func CoordinationCoordinatorRoster(state *WorkspaceState, lead *domain.Thread, here RosterActivity, on *[]*PairedComputer) *CoordinatorRoster {
	coordinated := coordinationNoMembers
	if domain.IsCoordinator(lead) {
		coordinated = CoordinationMembers(state, lead.ID)
	}
	roster := &CoordinatorRoster{Members: []*RosterMember{}, Threads: []*domain.Thread{}, Filed: []*RosterMember{}}
	held := map[*WorkspaceState]RosterActivity{}
	for _, member := range coordinated {
		if member.Computer != nil && on != nil && !slices.ContainsFunc(*on, func(computer *PairedComputer) bool { return computer.ID == member.Computer.ID }) {
			continue
		}
		there := here
		if member.Computer != nil {
			activity, ok := held[member.Holder]
			if !ok {
				read := ThreadActivityThreadActivity(member.Holder)
				activity = RosterActivity{Working: read.Working, Blocked: read.Blocked, Ranked: read.Ranked}
				held[member.Holder] = activity
			}
			there = activity
		}
		id := member.Thread.ID
		roster.Members = append(roster.Members, &RosterMember{
			Thread: member.Thread, Computer: member.Computer, Holder: member.Holder,
			Working: DoingWork(there.Working, there.Blocked, id), Blocked: there.Blocked.Has(id), Ranked: there.Ranked.Has(id),
		})
	}
	for _, member := range roster.Members {
		roster.Threads = append(roster.Threads, &member.Thread)
		roster.Blocked = roster.Blocked || member.Blocked
		roster.Running = roster.Running || member.Working
		if !member.Blocked {
			roster.Filed = append(roster.Filed, member)
		}
	}
	filed := make([]*domain.Thread, len(roster.Filed))
	for index, member := range roster.Filed {
		filed[index] = &member.Thread
	}
	roster.Waiting = domain.CoordinationWaitingOnUser(lead, roster.Threads, state.StartingProposals)
	roster.Dismissable = CanDismiss(lead, filed, here.Blocked)
	return roster
}

// A thread under a coordinator a paired computer last said was working, which that computer has said
// nothing of since this app started. Computer is nil when that computer is no longer paired.
type UnheardThread struct {
	ThreadID string
	Title    string
	Computer *PairedComputer
}

// The threads under a coordinator that a paired computer last said were working, which that computer
// has said nothing of since this app started: its line, or the list of computers, has yet to come
// in. They hold its news as a working thread does, since silence is not their work ending. Ordered
// by thread id.
func UnheardWork(state *WorkspaceState, lead *domain.Thread) []UnheardThread {
	working := lead.RemoteWorking
	if working == nil {
		return nil
	}
	var unheard []UnheardThread
	for _, threadID := range slices.Sorted(maps.Keys(working)) {
		work := working[threadID]
		var computer *PairedComputer
		for _, item := range state.Computers.Paired {
			if item.ID == work.Computer {
				computer = item
				break
			}
		}
		if computer != nil && computer.State != nil {
			continue
		}
		unheard = append(unheard, UnheardThread{ThreadID: threadID, Title: work.Title, Computer: computer})
	}
	return unheard
}

// What Stop all would stop under a coordinator, read the same way by the stop and by what offers it.
// A message on its way to a thread that exists already, as the user's or a decision's answer, is left
// to arrive: only a run that has started is stopped. Starts are ordered by pending id.
func CoordinationCoordinatorWork(state *WorkspaceState, lead *domain.Thread) *CoordinatorWork {
	coordinated := CoordinationMembers(state, lead.ID)
	work := &CoordinatorWork{Threads: []*domain.Thread{lead}, Starts: []*PendingRun{}, SessionWork: []*SessionWork{}, Elsewhere: []*RemoteWork{}, Landing: []*domain.ThreadProposal{}}
	for _, member := range coordinated {
		if member.Computer == nil {
			work.Threads = append(work.Threads, coordThread(state.Threads, member.Thread.ID))
		}
	}
	ids := jsonx.Set[string]{}
	for _, thread := range work.Threads {
		ids[thread.ID] = struct{}{}
	}
	for _, key := range slices.Sorted(maps.Keys(state.PendingRuns)) {
		pending := state.PendingRuns[key]
		if pending.Origin != PendingRunOriginComposer {
			continue
		}
		var stops bool
		if pending.TaskID != nil {
			taskID := *pending.TaskID
			stops = taskID == lead.ID && pending.Coordination != nil && pending.Coordination.Notes != nil || ids.Has(taskID) && pending.QueuedIDs != nil
		} else {
			stops = pending.Coordination != nil && pending.Coordination.CoordinatorID != nil && *pending.Coordination.CoordinatorID == lead.ID
		}
		if stops {
			work.Starts = append(work.Starts, pending)
		}
	}
	for _, thread := range work.Threads {
		if processIDs := UnstoppedSessionWork(state, thread.ID); len(processIDs) > 0 {
			work.SessionWork = append(work.SessionWork, &SessionWork{TaskID: thread.ID, ProcessIDs: processIDs})
		}
	}
	for _, member := range coordinated {
		if member.Computer == nil || member.Computer.Status != domain.ComputerStatusConnected {
			continue
		}
		thread, computer, holder := member.Thread, member.Computer, member.Holder
		run := RunRecord(holder.ActiveRuns[thread.ID])
		// Only what that computer's build can stop counts, so Stop all is never offered for work it cannot reach.
		processIDs := []string{}
		for _, processID := range UnstoppedSessionWork(holder, thread.ID) {
			command := contracts.RunControlCommandRunStopProcess{TaskID: &thread.ID, ProcessID: processID}
			if contracts.SupportsComputerCommand(computer.Capabilities, command) {
				processIDs = append(processIDs, processID)
			}
		}
		if run != nil || thread.LimitPause != nil || len(processIDs) > 0 {
			remote := &RemoteWork{Thread: thread, Computer: *computer, ProcessIDs: processIDs}
			if run != nil {
				remote.RunID = &run.RunID
			}
			work.Elsewhere = append(work.Elsewhere, remote)
		}
	}
	for _, proposal := range lead.Proposals {
		if proposal.Place.Computer == nil || !slices.Contains(state.StartingProposals, proposal.ID) {
			continue
		}
		if _, caught := CaughtStartComputer(lead, proposal.ID); !caught {
			work.Landing = append(work.Landing, proposal)
		}
	}
	return work
}

// Each live coordinator with a run going, a wait on a usage limit, a workflow or subagent working, or
// a run or a start on its way under it.
func StoppableCoordinators(state *WorkspaceState) jsonx.Set[string] {
	stoppable := jsonx.Set[string]{}
	for _, lead := range state.Threads {
		if !domain.IsCoordinator(lead) {
			continue
		}
		work := CoordinationCoordinatorWork(state, lead)
		going := slices.ContainsFunc(work.Threads, func(thread *domain.Thread) bool {
			return state.ActiveRuns[thread.ID] != nil || thread.LimitPause != nil
		})
		if len(work.Starts) > 0 || len(work.SessionWork) > 0 || len(work.Elsewhere) > 0 || len(work.Landing) > 0 || going {
			stoppable[lead.ID] = struct{}{}
		}
	}
	return stoppable
}

var coordinationNoStarts = []string{}

// Whether a finished thread under a coordinator is waiting behind it. A resolved thread is filed
// away, so whatever a later run left on it is no reason to lead.
func MembersWantAttention(members []*domain.Thread) bool {
	return slices.ContainsFunc(members, func(thread *domain.Thread) bool {
		return thread.ResolvedAt == nil && domain.WantsAttention(thread)
	})
}

// `one` for 1, "<count> <many>" for more, "" for none.
func counted(count float64, one, many string) string {
	switch {
	case count == 1:
		return one
	case count != 0:
		return strconv.FormatFloat(count, 'f', -1, 64) + " " + many
	}
	return ""
}

// What a row says waits on `owedBy` ("you" when ""), or nil when nothing does, worded as its
// Needs-you card asks: a request names the computer it would run on, as the card's "Run on Studio?" does.
func WaitingLine(waiting domain.WaitingOnUser, owedBy string) *string {
	if owedBy == "" {
		owedBy = "you"
	}
	requests := waiting.Requests
	where := "on other computers"
	if len(jsonx.SetOf(requests...)) == 1 {
		where = "on " + requests[0]
	}
	var parts []string
	for _, part := range []string{
		counted(waiting.Decisions, "a decision", "decisions"),
		counted(float64(len(requests)), "a request to run a thread "+where, "requests to run threads "+where),
		counted(waiting.Suggestions, "a suggested thread", "suggested threads"),
		counted(waiting.Blocked, "a blocked thread", "blocked threads"),
		counted(waiting.Failed, "a failed thread", "failed threads"),
	} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	said := parts[0]
	if len(parts) > 1 {
		said = strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
	verb := "are"
	if waiting.Decisions+waiting.Suggestions+float64(len(requests))+waiting.Blocked+waiting.Failed == 1 {
		verb = "is"
	}
	line := strings.ToUpper(said[:1]) + said[1:] + " " + verb + " waiting on " + owedBy
	return &line
}

// A row's second line once nothing under it waits on an approval: what waits on the user, else the
// newest thing a run found that they have not seen. The desktop's row and the phone's say the same.
func RowHeadline(thread *domain.Thread, waiting *string) *string {
	if waiting != nil {
		return waiting
	}
	if finding := domain.NewestUnreadFinding(thread); finding != nil {
		return &finding.Headline
	}
	return nil
}

// Whether Dismiss on a row files anything away: a verdict or a finding on its thread or, on a
// coordinator's, on a thread under it, which it is dismissed with. A thread waiting on an approval,
// the row's own or one under it, has nothing to dismiss until that is answered, and what waits on the
// user is answered, never dismissed.
func CanDismiss(thread *domain.Thread, members []*domain.Thread, blocked jsonx.Set[string]) bool {
	if blocked.Has(thread.ID) {
		return false
	}
	if domain.WantsAttention(thread) {
		return true
	}
	var free []*domain.Thread
	for _, member := range members {
		if !blocked.Has(member.ID) {
			free = append(free, member)
		}
	}
	return MembersWantAttention(free)
}

// What a Dismiss of the rows `ids` files away, by the rule CanDismiss reads: those rows and a
// coordinator's threads with it, none waiting on an approval.
func DismissedWith(threads []*domain.Thread, ids, blocked jsonx.Set[string]) jsonx.Set[string] {
	dismissed := jsonx.Set[string]{}
	for id := range domain.WithCoordinated(threads, maps.Clone(ids)) {
		if !blocked.Has(id) {
			dismissed[id] = struct{}{}
		}
	}
	return dismissed
}

// The rows of a list Dismiss would file anything away from, which are what Dismiss all files away.
func DismissableRows(rows []*domain.Thread, threadsByCoordinator map[string][]*domain.Thread, blocked jsonx.Set[string]) []*domain.Thread {
	dismissable := []*domain.Thread{}
	for _, row := range rows {
		if CanDismiss(row, threadsByCoordinator[row.ID], blocked) {
			dismissable = append(dismissable, row)
		}
	}
	return dismissable
}

// The activity lists with each coordinator standing for its threads: they are drawn under it rather
// than in a list of their own, and it ranks by whichever of them wants the user most. A suggestion
// already `starting` waits on no one. `starting` may be nil.
func CoordinationSections(threads []*domain.Thread, busy, blocked jsonx.Set[string], starting []string) ActivitySections {
	if starting == nil {
		starting = coordinationNoStarts
	}
	memberIDs := domain.CoordinatedThreadIds(threads)
	if len(memberIDs) == 0 && !slices.ContainsFunc(threads, func(thread *domain.Thread) bool { return len(thread.Decisions) > 0 || len(thread.Proposals) > 0 }) {
		return ThreadOrderActivitySections(threads, busy, blocked)
	}
	ranked := maps.Clone(busy)
	if ranked == nil {
		ranked = jsonx.Set[string]{}
	}
	asking := maps.Clone(blocked)
	if asking == nil {
		asking = jsonx.Set[string]{}
	}
	attention := jsonx.Set[string]{}
	for _, lead := range threads {
		if !domain.IsCoordinator(lead) {
			continue
		}
		led := domain.CoordinatedThreads(threads, lead.ID)
		coordination := append([]*domain.Thread{lead}, led...)
		if slices.ContainsFunc(coordination, func(thread *domain.Thread) bool { return blocked.Has(thread.ID) }) || domain.IsWaitingOnUser(domain.CoordinationWaitingOnUser(lead, led, starting)) {
			asking[lead.ID] = struct{}{}
		}
		if slices.ContainsFunc(coordination, func(thread *domain.Thread) bool { return busy.Has(thread.ID) }) {
			ranked[lead.ID] = struct{}{}
		}
		if MembersWantAttention(led) {
			attention[lead.ID] = struct{}{}
		}
	}
	for _, thread := range threads {
		if !memberIDs.Has(thread.ID) && (thread.Role == nil || *thread.Role != domain.ThreadRoleCoordinator) && domain.IsWaitingOnUser(domain.CoordinationWaitingOnUser(thread, []*domain.Thread{}, starting)) {
			asking[thread.ID] = struct{}{}
		}
	}
	listed := []*domain.Thread{}
	for _, thread := range threads {
		if !memberIDs.Has(thread.ID) {
			listed = append(listed, thread)
		}
	}
	sections := ThreadOrderActivitySections(listed, ranked, asking)
	if len(attention) == 0 {
		return sections
	}
	// A coordinator a finished thread is waiting behind leads as that thread would have.
	var promoted, rest []*domain.Thread
	for _, thread := range sections[ActivitySectionsKeyThreads] {
		if attention.Has(thread.ID) {
			promoted = append(promoted, thread)
		} else {
			rest = append(rest, thread)
		}
	}
	if len(promoted) == 0 {
		return sections
	}
	if rest == nil {
		rest = []*domain.Thread{}
	}
	return ActivitySections{
		ActivitySectionsKeyPriority: slices.Concat(sections[ActivitySectionsKeyPriority], promoted),
		ActivitySectionsKeyRunning:  sections[ActivitySectionsKeyRunning],
		ActivitySectionsKeyThreads:  rest,
		ActivitySectionsKeySnoozed:  sections[ActivitySectionsKeySnoozed],
	}
}

// A computer's own threads ranked as its sidebar ranks them: each coordinator here stands for its
// threads on the paired `computers` too, as each of those computers last said they stand. What
// Dismiss all files away reads it over the computers the sidebar shows, and the phone over every one.
// The threads it ranked come with it, for narrowing them to one project.
func OwnCoordinationSections(state *WorkspaceState, own []*domain.Thread, here RosterActivity, computers []*PairedComputer) (ActivitySections, []*domain.Thread) {
	var elsewhere []*RosterMember
	for _, lead := range own {
		if !domain.IsCoordinator(lead) {
			continue
		}
		for _, member := range CoordinationCoordinatorRoster(state, lead, here, &computers).Members {
			if member.Computer != nil {
				elsewhere = append(elsewhere, member)
			}
		}
	}
	if len(elsewhere) == 0 {
		return CoordinationSections(own, here.Ranked, here.Blocked, state.StartingProposals), own
	}
	where := func(held func(member *RosterMember) bool, ids jsonx.Set[string]) jsonx.Set[string] {
		joined := maps.Clone(ids)
		if joined == nil {
			joined = jsonx.Set[string]{}
		}
		for _, member := range elsewhere {
			if held(member) {
				joined[member.Thread.ID] = struct{}{}
			}
		}
		return joined
	}
	threads := slices.Clone(own)
	for _, member := range elsewhere {
		threads = append(threads, &member.Thread)
	}
	ranked := where(func(member *RosterMember) bool { return member.Ranked }, here.Ranked)
	blocked := where(func(member *RosterMember) bool { return member.Blocked }, here.Blocked)
	return CoordinationSections(threads, ranked, blocked, state.StartingProposals), threads
}

// The activity lists narrowed to one project. A coordinator's row stays while any thread under it
// works there, since its row is where those threads are drawn.
func ProjectActivity(sections ActivitySections, threads []*domain.Thread, projectID string) ActivitySections {
	memberIDs := domain.CoordinatedThreadIds(threads)
	leads := jsonx.Set[string]{}
	for _, thread := range threads {
		if memberIDs.Has(thread.ID) && thread.ProjectID != nil && *thread.ProjectID == projectID {
			if leadID, ok := domain.LeadIDOf(thread); ok {
				leads[leadID] = struct{}{}
			}
		}
	}
	keep := func(list []*domain.Thread) []*domain.Thread {
		kept := []*domain.Thread{}
		for _, thread := range list {
			if thread.ProjectID != nil && *thread.ProjectID == projectID || leads.Has(thread.ID) {
				kept = append(kept, thread)
			}
		}
		return kept
	}
	return ActivitySections{
		ActivitySectionsKeyPriority: keep(sections[ActivitySectionsKeyPriority]),
		ActivitySectionsKeyRunning:  keep(sections[ActivitySectionsKeyRunning]),
		ActivitySectionsKeyThreads:  keep(sections[ActivitySectionsKeyThreads]),
		ActivitySectionsKeySnoozed:  keep(sections[ActivitySectionsKeySnoozed]),
	}
}
