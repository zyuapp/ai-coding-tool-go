package application

import (
	"maps"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// runsChange makes the run state as one input leaves it. Only the functions here make one.
func runsChange(view RunsState) RunsEdit {
	return RunsEdit{View: view}
}

func RunsOf(state *WorkspaceState) RunsEdit {
	return runsChange(RunsState{
		RunFeedsState: RunFeedsState{
			ActiveRuns:          state.ActiveRuns,
			RunStatuses:         state.RunStatuses,
			Approvals:           state.Approvals,
			StreamingTails:      state.StreamingTails,
			BackgroundProcesses: state.BackgroundProcesses,
			Workflows:           state.Workflows,
			Subagents:           state.Subagents,
			Goals:               state.Goals,
		},
		PendingRuns:       state.PendingRuns,
		StartingProposals: state.StartingProposals,
		QuietStops:        state.QuietStops,
		QuietSessions:     state.QuietSessions,
		QueuedMessages:    state.QueuedMessages,
		LastRunIDs:        state.LastRunIDs,
	})
}

// PendingIn is a run on its way, resolving its checkout.
func PendingIn(edit RunsEdit, pending *PendingRun) RunsEdit {
	view := edit.View
	view.PendingRuns = runsWithKey(view.PendingRuns, pending.ID, pending)
	return runsChange(view)
}

// WithoutPendingIn is a run no longer on its way, whether it started or was dropped.
func WithoutPendingIn(edit RunsEdit, pendingID string) RunsEdit {
	view := edit.View
	view.PendingRuns = runsWithoutKey(view.PendingRuns, pendingID)
	return runsChange(view)
}

// QueuedIn is what waits behind a thread's run, in order. An empty queue is dropped.
func QueuedIn(edit RunsEdit, taskID string, messages []*QueuedMessage) RunsEdit {
	view := edit.View
	if len(messages) > 0 {
		view.QueuedMessages = runsWithKey(view.QueuedMessages, taskID, messages)
	} else {
		view.QueuedMessages = runsWithoutKey(view.QueuedMessages, taskID)
	}
	return runsChange(view)
}

// QuestionAnswerIn is one of the questions a thread's live run is waiting on, with what the user has
// written in answer so far. question is matched by identity; a nil answer or submitting is left as it was.
func QuestionAnswerIn(edit RunsEdit, taskID string, question *domain.PendingQuestion, answer *string, submitting *bool) RunsEdit {
	active := edit.View.ActiveRuns[taskID]
	if active == nil {
		return edit
	}
	view := edit.View
	view.ActiveRuns = runsWithKey(view.ActiveRuns, taskID, EditRun(active, func(record *ActiveRunRunning) {
		if record.Questions == nil {
			return
		}
		questions := make([]*domain.PendingQuestion, len(record.Questions))
		for index, pending := range record.Questions {
			if pending != question {
				questions[index] = pending
				continue
			}
			answered := *pending
			if answer != nil {
				answered.Answer = answer
			}
			if submitting != nil {
				answered.Submitting = submitting
			}
			questions[index] = &answered
		}
		record.Questions = questions
	}))
	return runsChange(view)
}

// AttendedIn is a scheduled run a person joined, which is theirs from then on.
func AttendedIn(edit RunsEdit, taskID string) RunsEdit {
	active := edit.View.ActiveRuns[taskID]
	if active == nil || RunRecord(active).Origin == ActiveRunRunningOriginComposer {
		return edit
	}
	view := edit.View
	view.ActiveRuns = runsWithKey(view.ActiveRuns, taskID, EditRun(active, func(record *ActiveRunRunning) { record.Origin = ActiveRunRunningOriginComposer }))
	return runsChange(view)
}

// ProposalStartingIn is a coordinator's suggestion whose thread is on its way.
func ProposalStartingIn(edit RunsEdit, proposalID string) RunsEdit {
	view := edit.View
	view.StartingProposals = append(slices.Clip(view.StartingProposals), proposalID)
	return runsChange(view)
}

// ProposalSettledIn is a suggestion no longer starting, whether it started, failed, or was stopped.
func ProposalSettledIn(edit RunsEdit, proposalID string) RunsEdit {
	if !slices.Contains(edit.View.StartingProposals, proposalID) {
		return edit
	}
	view := edit.View
	view.StartingProposals = runsWithoutString(view.StartingProposals, proposalID)
	return runsChange(view)
}

// RunStatusIn is how a thread reads while no run of its own is live: idle, which is dropped, or stopped.
func RunStatusIn(edit RunsEdit, taskID string, status ThreadRunStatus) RunsEdit {
	view := edit.View
	view.RunStatuses = runStatusesWith(view.RunStatuses, taskID, status)
	return runsChange(view)
}

// RunAnsweredIn marks the Stop all that cancelled this run answered by its end.
func RunAnsweredIn(edit RunsEdit, runID string) RunsEdit {
	view := edit.View
	view.QuietStops = runsWithoutString(view.QuietStops, runID)
	return runsChange(view)
}

// stopAnswerMs is how long after Stop all stopped a thread's session work the agent's own turn is still its answer to that stop.
const stopAnswerMs = 2 * 60_000

// AnswersInTime reports whether a turn the agent opens at `at` could still be its answer to the stop
// that hushed its session at stoppedAt.
func AnswersInTime(stoppedAt float64, at float64) bool {
	return at-stoppedAt <= stopAnswerMs
}

// StopAllIn is what a Stop all at `at` answers for here: the runs it cancelled, and the threads whose session work it stopped.
func StopAllIn(edit RunsEdit, cancelled []string, hushed []string, threads []*domain.Thread, at float64) RunsEdit {
	going := map[string]bool{}
	for _, run := range edit.View.ActiveRuns {
		going[RunRecord(run).RunID] = true
	}
	quietStops := []string{}
	for _, runID := range edit.View.QuietStops {
		if going[runID] {
			quietStops = append(quietStops, runID)
		}
	}
	quietStops = append(quietStops, cancelled...)
	var stopped []string
	for _, taskID := range hushed {
		if !slices.Contains(stopped, taskID) {
			stopped = append(stopped, taskID)
		}
	}
	quietSessions := []*QuietSession{}
	for _, session := range edit.View.QuietSessions {
		if !slices.Contains(stopped, session.TaskID) && AnswersInTime(session.At, at) && slices.ContainsFunc(threads, func(thread *domain.Thread) bool { return thread.ID == session.TaskID }) {
			quietSessions = append(quietSessions, session)
		}
	}
	for _, taskID := range stopped {
		quietSessions = append(quietSessions, &QuietSession{TaskID: taskID, At: at})
	}
	view := edit.View
	view.QuietStops = quietStops
	view.QuietSessions = quietSessions
	return runsChange(view)
}

// StopAskedIn marks a stop asked of what a thread's session runs in the background, a workflow or a
// subagent included. False when there is nothing there still to stop.
func StopAskedIn(edit RunsEdit, taskID string, processID string) (RunsEdit, bool) {
	view := edit.View
	processes := view.BackgroundProcesses[taskID]
	if index := slices.IndexFunc(processes, func(process *domain.BackgroundProcess) bool { return process.ID == processID }); index != -1 {
		if runsIsTrue(processes[index].Stopping) {
			return edit, false
		}
		marked := slices.Clone(processes)
		stopping := *processes[index]
		stopping.Stopping = new(true)
		marked[index] = &stopping
		view.RunFeedsState = feedsWithBackgroundProcesses(view.RunFeedsState, taskID, marked)
		return runsChange(view), true
	}
	workflows := view.Workflows[taskID]
	if index := slices.IndexFunc(workflows, func(workflow *domain.Workflow) bool { return workflow.ID == processID }); index != -1 {
		if runsIsTrue(workflows[index].Stopping) || workflows[index].Status != domain.WorkflowStatusRunning {
			return edit, false
		}
		marked := slices.Clone(workflows)
		stopping := *workflows[index]
		stopping.Stopping = new(true)
		marked[index] = &stopping
		view.RunFeedsState = feedsWithWorkflows(view.RunFeedsState, taskID, marked)
		return runsChange(view), true
	}
	subagents := view.Subagents[taskID]
	index := slices.IndexFunc(subagents, func(subagent *domain.Subagent) bool { return subagent.ID == processID })
	if index == -1 || runsIsTrue(subagents[index].Stopping) || subagents[index].Status != domain.SubagentStatusWorking {
		return edit, false
	}
	marked := slices.Clone(subagents)
	stopping := *subagents[index]
	stopping.Stopping = new(true)
	marked[index] = &stopping
	view.RunFeedsState = feedsWithSubagents(view.RunFeedsState, taskID, marked)
	return runsChange(view), true
}

// NotifiedIn is a scheduled run that answered for itself, having found something: news unless
// raised is false, and the issue it named, if any.
func NotifiedIn(edit RunsEdit, taskID string, raised bool, key *string) RunsEdit {
	active := edit.View.ActiveRuns[taskID]
	if active == nil {
		return edit
	}
	view := edit.View
	view.ActiveRuns = runsWithKey(view.ActiveRuns, taskID, EditRun(active, func(record *ActiveRunRunning) {
		if key != nil && !slices.Contains(record.ReportedIssues, *key) {
			record.ReportedIssues = append(slices.Clip(record.ReportedIssues), *key)
		}
		record.Acknowledged = true
		record.Notified = record.Notified || raised
	}))
	return runsChange(view)
}

// AcknowledgedIn is a scheduled run that answered for itself, having found nothing.
func AcknowledgedIn(edit RunsEdit, taskID string) RunsEdit {
	active := edit.View.ActiveRuns[taskID]
	if active == nil {
		return edit
	}
	view := edit.View
	view.ActiveRuns = runsWithKey(view.ActiveRuns, taskID, EditRun(active, func(record *ActiveRunRunning) { record.Acknowledged = true }))
	return runsChange(view)
}

// RunDismissedIn is a run whose thread was dismissed while it went on.
func RunDismissedIn(edit RunsEdit, taskID string) RunsEdit {
	active := edit.View.ActiveRuns[taskID]
	if active == nil || runsIsTrue(RunRecord(active).Dismissed) {
		return edit
	}
	view := edit.View
	view.ActiveRuns = runsWithKey(view.ActiveRuns, taskID, EditRun(active, func(record *ActiveRunRunning) { record.Dismissed = new(true) }))
	return runsChange(view)
}

// WatchesEndedBeforeIn records, by thread, watches that ended while a tick of each was going: a tick
// that settles unseen puts its thread back to its verdict.
func WatchesEndedBeforeIn(edit RunsEdit, verdicts map[string]*FinishedVerdict) RunsEdit {
	var activeRuns map[string]ActiveRun
	for taskID, active := range edit.View.ActiveRuns {
		verdict := verdicts[taskID]
		if verdict == nil || active == nil {
			continue
		}
		if activeRuns == nil {
			activeRuns = maps.Clone(edit.View.ActiveRuns)
		}
		activeRuns[taskID] = EditRun(active, func(record *ActiveRunRunning) {
			outcome, endedAs := domain.ThreadOutcome(verdict.Outcome.String()), domain.ThreadOutcome(verdict.RunEndedAs.String())
			record.Before.Outcome = &outcome
			record.Before.RunEndedAs = &endedAs
			if verdict.RankedAt != nil {
				record.Before.RankedAt = verdict.RankedAt
			}
			if verdict.OutcomeUnread != nil {
				record.Before.OutcomeUnread = verdict.OutcomeUnread
			}
			if verdict.RunEndedAt != nil {
				record.Before.RunEndedAt = verdict.RunEndedAt
			}
		})
	}
	if activeRuns == nil {
		return edit
	}
	view := edit.View
	view.ActiveRuns = activeRuns
	return runsChange(view)
}

// ThreadEventIn is what a thread's session reports of work that outlives the run that started it, as ApplyThreadEvent keeps it.
func ThreadEventIn(edit RunsEdit, event contracts.ThreadEvent) RunsEdit {
	view := edit.View
	view.RunFeedsState = feedsWithThreadEvent(view.RunFeedsState, event)
	return runsChange(view)
}

// SubagentActivityIn is a subagent's log as its run kept it, ahead of whatever it has reported since.
func SubagentActivityIn(edit RunsEdit, taskID string, subagentID string, activity []*domain.SubagentActivity) RunsEdit {
	stored := map[string]bool{}
	for _, item := range activity {
		stored[item.ID] = true
	}
	held := edit.View.Subagents[taskID]
	next := make([]*domain.Subagent, len(held))
	for index, subagent := range held {
		if subagent.ID != subagentID {
			next[index] = subagent
			continue
		}
		joined := *subagent
		joined.Activity = slices.Clone(activity)
		for _, item := range subagent.Activity {
			if !stored[item.ID] {
				joined.Activity = append(joined.Activity, item)
			}
		}
		if joined.Activity == nil {
			joined.Activity = []*domain.SubagentActivity{}
		}
		next[index] = &joined
	}
	view := edit.View
	view.RunFeedsState = feedsWithSubagents(view.RunFeedsState, taskID, next)
	return runsChange(view)
}

// RunsForgottenIn drops every run, queue, approval and report of threads gone for good.
func RunsForgottenIn(edit RunsEdit, removed jsonx.Set[string]) RunsEdit {
	view := edit.View
	view.PendingRuns = WithoutMatchingValues(view.PendingRuns, func(pending *PendingRun) bool {
		return pending.TaskID != nil && *pending.TaskID != "" && removed.Has(*pending.TaskID)
	})
	view.QueuedMessages = WithoutThreadKeys(view.QueuedMessages, removed)
	view.LastRunIDs = WithoutThreadKeys(view.LastRunIDs, removed)
	view.ActiveRuns = WithoutThreadKeys(view.ActiveRuns, removed)
	view.RunStatuses = WithoutThreadKeys(view.RunStatuses, removed)
	view.Approvals = WithoutMatchingValues(view.Approvals, func(approval *ApprovalView) bool { return removed.Has(approval.TaskID) })
	view.StreamingTails = WithoutThreadKeys(view.StreamingTails, removed)
	view.BackgroundProcesses = WithoutThreadKeys(view.BackgroundProcesses, removed)
	view.Workflows = WithoutThreadKeys(view.Workflows, removed)
	view.Subagents = WithoutThreadKeys(view.Subagents, removed)
	return runsChange(view)
}

// ApprovalAnsweredIn is a thread's live run with its approval answered, running again.
func ApprovalAnsweredIn(edit RunsEdit, taskID string) RunsEdit {
	view := edit.View
	view.RunFeedsState = feedsWithApprovalAnswered(view.RunFeedsState, taskID)
	return runsChange(view)
}

// CancellingIn is the user stopping a thread's run, which the run remembers, so however it ends first it ends as the stop asked.
func CancellingIn(edit RunsEdit, taskID string) RunsEdit {
	active := edit.View.ActiveRuns[taskID]
	if active == nil || runsIsTrue(RunRecord(active).Cancelling) {
		return edit
	}
	view := edit.View
	view.RunFeedsState = feedsWithActiveRun(view.RunFeedsState, taskID, EditRun(active, func(record *ActiveRunRunning) { record.Cancelling = new(true) }))
	return runsChange(view)
}

// runsWithoutString is items without every one equal to value, as a new list.
func runsWithoutString(items []string, value string) []string {
	kept := []string{}
	for _, item := range items {
		if item != value {
			kept = append(kept, item)
		}
	}
	return kept
}
