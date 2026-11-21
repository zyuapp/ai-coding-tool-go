package application

import (
	"encoding/json/jsontext"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// RunRecord is run's fields as one struct, whatever its phase: a retrying run reads Status
// "retrying", its notice left out (see RunRetry). Nil for no run. The struct is a copy; the slices
// and maps in it are shared with run.
func RunRecord(run ActiveRun) *ActiveRunRunning {
	switch run := run.(type) {
	case ActiveRunRunning:
		return &run
	case ActiveRunRetrying:
		return &ActiveRunRunning{
			Origin:         ActiveRunRunningOrigin(run.Origin),
			Quiet:          run.Quiet,
			Operation:      (*ActiveRunRunningOperation)(run.Operation),
			Idle:           run.Idle,
			UsersOwn:       run.UsersOwn,
			Status:         ActiveRunRunningStatus("retrying"),
			TaskID:         run.TaskID,
			RunID:          run.RunID,
			Sequence:       run.Sequence,
			Questions:      run.Questions,
			Notified:       run.Notified,
			Acknowledged:   run.Acknowledged,
			ReportedIssues: run.ReportedIssues,
			MessagesBefore: run.MessagesBefore,
			Before:         run.Before,
			AnswerParts:    run.AnswerParts,
			Warming:        run.Warming,
			AnswersStop:    run.AnswersStop,
			AgentInitiated: run.AgentInitiated,
			Cancelling:     run.Cancelling,
			Dismissed:      run.Dismissed,
		}
	}
	return nil
}

// RunRetry is the notice a retrying run retries under, or nil for any other run.
func RunRetry(run ActiveRun) *domain.RetryNotice {
	if retrying, ok := run.(ActiveRunRetrying); ok {
		return &retrying.Retry
	}
	return nil
}

// RunPhaseOf is the phase run is in, or nil for no run.
func RunPhaseOf(run ActiveRun) RunPhase {
	switch run := run.(type) {
	case ActiveRunRunning:
		return RunPhaseRunning{Status: RunPhaseRunningStatus(run.Status)}
	case ActiveRunRetrying:
		return RunPhaseRetrying{Retry: run.Retry}
	}
	return nil
}

// RecordedRun is record as an ActiveRun in phase. The phase's status replaces record's.
func RecordedRun(record ActiveRunRunning, phase RunPhase) ActiveRun {
	if retrying, ok := phase.(RunPhaseRetrying); ok {
		return ActiveRunRetrying{
			Origin:         ActiveRunRetryingOrigin(record.Origin),
			Quiet:          record.Quiet,
			Operation:      (*ActiveRunRetryingOperation)(record.Operation),
			Idle:           record.Idle,
			UsersOwn:       record.UsersOwn,
			Retry:          retrying.Retry,
			TaskID:         record.TaskID,
			RunID:          record.RunID,
			Sequence:       record.Sequence,
			Questions:      record.Questions,
			Notified:       record.Notified,
			Acknowledged:   record.Acknowledged,
			ReportedIssues: record.ReportedIssues,
			MessagesBefore: record.MessagesBefore,
			Before:         record.Before,
			AnswerParts:    record.AnswerParts,
			Warming:        record.Warming,
			AnswersStop:    record.AnswersStop,
			AgentInitiated: record.AgentInitiated,
			Cancelling:     record.Cancelling,
			Dismissed:      record.Dismissed,
		}
	}
	if running, ok := phase.(RunPhaseRunning); ok {
		record.Status = ActiveRunRunningStatus(running.Status)
	}
	return record
}

// EditRun is run with edit applied to a copy of its fields, in the phase run is in. Nil for no run.
func EditRun(run ActiveRun, edit func(record *ActiveRunRunning)) ActiveRun {
	record := RunRecord(run)
	if record == nil {
		return nil
	}
	edit(record)
	return RecordedRun(*record, RunPhaseOf(run))
}

func ThreadRunStateThreadMark(thread *domain.Thread) ThreadMark {
	if thread == nil {
		return ThreadMark{}
	}
	mark := ThreadMark{
		UpdatedAt:  thread.UpdatedAt,
		RankedAt:   new(thread.RankedAt),
		RunEndedAt: thread.RunEndedAt,
		RunEndedAs: thread.RunEndedAs,
		Outcome:    thread.Outcome,
		Report:     thread.Report,
	}
	if thread.OutcomeUnread != nil && *thread.OutcomeUnread {
		mark.OutcomeUnread = new(true)
	}
	return mark
}

// WithdrawRun puts a thread back where a tick found it: the messages that tick wrote are withdrawn,
// the moments it moved are rolled back, and the verdict and report beginning the run superseded are
// returned. Only a report the tick made of where its work ended up stands.
func WithdrawRun(thread *domain.Thread, from float64, before ThreadMark) *domain.Thread {
	next := *thread
	next.RunEndedAt, next.RunEndedAs, next.Outcome, next.OutcomeUnread = nil, nil, nil, nil
	next.Messages = domain.WithdrawMessages(thread.Messages, int(from))
	next.UpdatedAt = before.UpdatedAt
	if before.RankedAt != nil {
		next.RankedAt = *before.RankedAt
	}
	next.RunEndedAt = before.RunEndedAt
	next.RunEndedAs = before.RunEndedAs
	next.Outcome = before.Outcome
	if before.OutcomeUnread != nil && *before.OutcomeUnread {
		next.OutcomeUnread = new(true)
	}
	if before.Report != nil && (thread.Report == nil || thread.Report.State == "working") {
		next.Report = before.Report
	}
	return &next
}

var ThreadRunStateAttendedRun = RunProvenance{Origin: RunProvenanceOriginComposer, Quiet: false}

func threadRunStateNow() float64 {
	return Now()
}

// OpenedTurn marks a run open on its thread until it ends, and ranks the thread by its start.
// Compacting is not a turn, and a quiet scheduled run only speaks through what it finds, so neither is.
func OpenedTurn(thread *domain.Thread, provenance RunProvenance) *domain.Thread {
	if provenance.Operation != nil && *provenance.Operation == RunProvenanceOperationCompact || provenance.Origin == RunProvenanceOriginAutomation && provenance.Quiet {
		return thread
	}
	next := *thread
	next.RunOpen = new(true)
	next.RankedAt = threadRunStateNow()
	return &next
}

func ThreadRunStateFinishedVerdict(at float64, onScreen bool) FinishedVerdict {
	verdict := FinishedVerdict{RunEndedAt: &at, RankedAt: &at}
	if !onScreen {
		unread := true
		verdict.OutcomeUnread = &unread
	}
	return verdict
}

// AutomationRunPrompt frames a scheduled run: nobody is present to answer a question or end the
// loop. An empty surfaceWhen or endsWhen is absent.
func AutomationRunPrompt(prompt string, runNumber float64, surfaceWhen string, endsWhen string) string {
	stop := "If the automation's stop condition is now met, call the aicodingtool-automation stop tool to end it."
	if endsWhen != "" {
		stop = "This automation ends when: " + endsWhen + " Once that holds, call the aicodingtool-automation stop tool to end it."
	}
	framing := "This is automated run #" + strconv.FormatFloat(runNumber, 'f', -1, 64) + " of this task's automation, started by AICodingTool's scheduler with no user watching. " + stop
	quiet := ""
	if surfaceWhen != "" {
		quiet = "\n\n" + quietFraming(surfaceWhen)
	}
	return prompt + "\n\n---\n" + framing + quiet
}

// quietFraming tells a quiet run it has to earn its silence: saying nothing at all is what a broken run does too.
func quietFraming(surfaceWhen string) string {
	return "This run is quiet: it settles without reaching the user unless you say otherwise. Surface it when: " + surfaceWhen + " If that is what you found, call the aicodingtool-automation notify tool with a headline before you finish. If it is not, call nothing_to_report with what you checked. Call neither and the run surfaces as an ordinary one, which is what a run that could not do its job should do."
}

// runsWithKey is record with key set to value, as a new map.
func runsWithKey[V any](record map[string]V, key string, value V) map[string]V {
	next := make(map[string]V, len(record)+1)
	maps.Copy(next, record)
	next[key] = value
	return next
}

// runsWithoutKey is record without key, as a new map even when key is not there.
func runsWithoutKey[V any](record map[string]V, key string) map[string]V {
	next := maps.Clone(record)
	if next == nil {
		next = map[string]V{}
	}
	delete(next, key)
	return next
}

// feedsOf is the run feeds state holds.
func feedsOf(state *WorkspaceState) RunFeedsState {
	return RunFeedsState{
		ActiveRuns:          state.ActiveRuns,
		RunStatuses:         state.RunStatuses,
		Approvals:           state.Approvals,
		StreamingTails:      state.StreamingTails,
		BackgroundProcesses: state.BackgroundProcesses,
		Workflows:           state.Workflows,
		Subagents:           state.Subagents,
		Goals:               state.Goals,
	}
}

func sameFeeds(a, b RunFeedsState) bool {
	return jsonx.SameMap(a.ActiveRuns, b.ActiveRuns) && jsonx.SameMap(a.RunStatuses, b.RunStatuses) &&
		jsonx.SameMap(a.Approvals, b.Approvals) && jsonx.SameMap(a.StreamingTails, b.StreamingTails) &&
		jsonx.SameMap(a.BackgroundProcesses, b.BackgroundProcesses) && jsonx.SameMap(a.Workflows, b.Workflows) &&
		jsonx.SameMap(a.Subagents, b.Subagents) && jsonx.SameMap(a.Goals, b.Goals)
}

// withFeeds is state holding feeds; state itself when they are its own.
func withFeeds(state *WorkspaceState, feeds RunFeedsState) *WorkspaceState {
	if sameFeeds(feedsOf(state), feeds) {
		return state
	}
	next := *state
	writeFeeds(&next, feeds)
	return &next
}

func writeFeeds(state *WorkspaceState, feeds RunFeedsState) {
	state.ActiveRuns = feeds.ActiveRuns
	state.RunStatuses = feeds.RunStatuses
	state.Approvals = feeds.Approvals
	state.StreamingTails = feeds.StreamingTails
	state.BackgroundProcesses = feeds.BackgroundProcesses
	state.Workflows = feeds.Workflows
	state.Subagents = feeds.Subagents
	state.Goals = feeds.Goals
}

// transitionOf is what a run report may change of state.
func transitionOf(state *WorkspaceState) RunTransitionState {
	return RunTransitionState{ThreadsState: ThreadsState{Threads: state.Threads}, RunFeedsState: feedsOf(state)}
}

// withTransition is state with what a run report changed; state itself when nothing did.
func withTransition(state *WorkspaceState, transition RunTransitionState) *WorkspaceState {
	if jsonx.SameSlice(state.Threads, transition.Threads) && sameFeeds(feedsOf(state), transition.RunFeedsState) {
		return state
	}
	next := *state
	next.Threads = transition.Threads
	writeFeeds(&next, transition.RunFeedsState)
	return &next
}

// WithActiveRun sets threadID's run, or drops it for a nil run.
func WithActiveRun(state *WorkspaceState, threadID string, run ActiveRun) *WorkspaceState {
	return withFeeds(state, feedsWithActiveRun(feedsOf(state), threadID, run))
}

func feedsWithActiveRun(feeds RunFeedsState, threadID string, run ActiveRun) RunFeedsState {
	if run != nil {
		feeds.ActiveRuns = runsWithKey(feeds.ActiveRuns, threadID, run)
	} else {
		feeds.ActiveRuns = runsWithoutKey(feeds.ActiveRuns, threadID)
	}
	return feeds
}

// WithRunPhase moves a run to another phase: what the phase it left carried goes with it.
func WithRunPhase(state *WorkspaceState, threadID string, phase RunPhase) *WorkspaceState {
	return withFeeds(state, feedsWithRunPhase(feedsOf(state), threadID, phase))
}

func feedsWithRunPhase(feeds RunFeedsState, threadID string, phase RunPhase) RunFeedsState {
	run := feeds.ActiveRuns[threadID]
	if run == nil {
		return feeds
	}
	return feedsWithActiveRun(feeds, threadID, RecordedRun(*RunRecord(run), phase))
}

// WithApprovalAnswered drops the run's approval, and the run is running again.
func WithApprovalAnswered(state *WorkspaceState, threadID string) *WorkspaceState {
	return withFeeds(state, feedsWithApprovalAnswered(feedsOf(state), threadID))
}

func feedsWithApprovalAnswered(feeds RunFeedsState, threadID string) RunFeedsState {
	run := feeds.ActiveRuns[threadID]
	if run == nil {
		return feeds
	}
	feeds.Approvals = runsWithoutKey(feeds.Approvals, RunRecord(run).RunID)
	return feedsWithRunPhase(feeds, threadID, RunPhaseRunning{Status: RunPhaseRunningStatusRunning})
}

func WithRunStatus(state *WorkspaceState, threadID string, status ThreadRunStatus) *WorkspaceState {
	next := *state
	next.RunStatuses = runStatusesWith(state.RunStatuses, threadID, status)
	return &next
}

func runStatusesWith(statuses map[string]ThreadRunStatus, threadID string, status ThreadRunStatus) map[string]ThreadRunStatus {
	if status != ThreadRunStatusIdle {
		return runsWithKey(statuses, threadID, status)
	}
	return runsWithoutKey(statuses, threadID)
}

// WithStreamingTail sets threadID's tail, or drops it for nil.
func WithStreamingTail(state *WorkspaceState, threadID string, tail *StreamingTail) *WorkspaceState {
	return withFeeds(state, feedsWithStreamingTail(feedsOf(state), threadID, tail))
}

func feedsWithStreamingTail(feeds RunFeedsState, threadID string, tail *StreamingTail) RunFeedsState {
	if tail != nil {
		feeds.StreamingTails = runsWithKey(feeds.StreamingTails, threadID, tail)
		return feeds
	}
	if _, ok := feeds.StreamingTails[threadID]; !ok {
		return feeds
	}
	feeds.StreamingTails = runsWithoutKey(feeds.StreamingTails, threadID)
	return feeds
}

func WithBackgroundProcesses(state *WorkspaceState, threadID string, processes []*domain.BackgroundProcess) *WorkspaceState {
	return withFeeds(state, feedsWithBackgroundProcesses(feedsOf(state), threadID, processes))
}

func feedsWithBackgroundProcesses(feeds RunFeedsState, threadID string, processes []*domain.BackgroundProcess) RunFeedsState {
	feeds.BackgroundProcesses = runsListsWith(feeds.BackgroundProcesses, threadID, processes)
	return feeds
}

// runsListsWith is record with threadID's list set, dropped when empty; record itself when an empty list drops nothing.
func runsListsWith[V any](record map[string][]V, threadID string, list []V) map[string][]V {
	if len(list) > 0 {
		return runsWithKey(record, threadID, list)
	}
	if _, ok := record[threadID]; !ok {
		return record
	}
	return runsWithoutKey(record, threadID)
}

func WithWorkflows(state *WorkspaceState, threadID string, workflows []*domain.Workflow) *WorkspaceState {
	return withFeeds(state, feedsWithWorkflows(feedsOf(state), threadID, workflows))
}

func feedsWithWorkflows(feeds RunFeedsState, threadID string, workflows []*domain.Workflow) RunFeedsState {
	feeds.Workflows = runsListsWith(feeds.Workflows, threadID, workflows)
	return feeds
}

// updateWorkflow replaces the named workflow with what update returns, or appends it; update gets nil for a new one.
func updateWorkflow(feeds RunFeedsState, threadID string, id string, update func(existing *domain.Workflow) *domain.Workflow) RunFeedsState {
	workflows := feeds.Workflows[threadID]
	index := slices.IndexFunc(workflows, func(workflow *domain.Workflow) bool { return workflow.ID == id })
	if index == -1 {
		return feedsWithWorkflows(feeds, threadID, append(slices.Clip(workflows), update(nil)))
	}
	next := make([]*domain.Workflow, len(workflows))
	for position, workflow := range workflows {
		if workflow.ID == id {
			next[position] = update(workflow)
		} else {
			next[position] = workflow
		}
	}
	return feedsWithWorkflows(feeds, threadID, next)
}

// UpdateThread replaces threadID's thread with what update returns.
func UpdateThread(state *WorkspaceState, threadID string, update func(thread *domain.Thread) *domain.Thread) *WorkspaceState {
	next := *state
	next.Threads = threadsUpdated(state.Threads, threadID, update)
	return &next
}

// threadsUpdated is threads with threadID's replaced by what update returns, as a new list.
func threadsUpdated(threads []*domain.Thread, threadID string, update func(thread *domain.Thread) *domain.Thread) []*domain.Thread {
	next := make([]*domain.Thread, len(threads))
	for index, thread := range threads {
		if thread.ID == threadID {
			next[index] = update(thread)
		} else {
			next[index] = thread
		}
	}
	return next
}

func transitionUpdateThread(transition RunTransitionState, threadID string, update func(thread *domain.Thread) *domain.Thread) RunTransitionState {
	transition.Threads = threadsUpdated(transition.Threads, threadID, update)
	return transition
}

func WithSubagents(state *WorkspaceState, threadID string, subagents []*domain.Subagent) *WorkspaceState {
	return withFeeds(state, feedsWithSubagents(feedsOf(state), threadID, subagents))
}

func feedsWithSubagents(feeds RunFeedsState, threadID string, subagents []*domain.Subagent) RunFeedsState {
	feeds.Subagents = runsListsWith(feeds.Subagents, threadID, subagents)
	return feeds
}

// updateSubagent replaces or appends one subagent; update gets nil for a new one, and returning its argument changes nothing.
func updateSubagent(feeds RunFeedsState, threadID string, subagentID string, update func(subagent *domain.Subagent) *domain.Subagent) RunFeedsState {
	held := feeds.Subagents[threadID]
	index := slices.IndexFunc(held, func(subagent *domain.Subagent) bool { return subagent.ID == subagentID })
	if index == -1 {
		return feedsWithSubagents(feeds, threadID, append(slices.Clip(held), update(nil)))
	}
	replaced := update(held[index])
	if replaced == held[index] {
		return feeds
	}
	next := slices.Clone(held)
	next[index] = replaced
	return feedsWithSubagents(feeds, threadID, next)
}

func newSubagent(id string) *domain.Subagent {
	return &domain.Subagent{ID: id, Description: "Subagent", Status: domain.SubagentStatusWorking, StartedAt: threadRunStateNow(), Activity: []*domain.SubagentActivity{}}
}

// subagentMetadata is the metadata details carries, without the fields it leaves out.
func subagentMetadata(next *domain.Subagent, model, effort, prompt *string) {
	if model != nil {
		next.Model = model
	}
	if effort != nil {
		next.Effort = effort
	}
	if prompt != nil {
		next.Prompt = prompt
	}
}

func runsTruthy(text *string) bool { return text != nil && *text != "" }

// applySubagentReport applies a subagent change, whether a run carries it or a session reports it later.
func applySubagentReport(feeds RunFeedsState, threadID string, event contracts.SubagentEvent) RunFeedsState {
	switch event := event.(type) {
	case contracts.SubagentEventSubagentMetadata:
		return updateSubagent(feeds, threadID, event.ID, func(existing *domain.Subagent) *domain.Subagent {
			var next domain.Subagent
			if existing != nil {
				next = *existing
			} else {
				next = *newSubagent(event.ID)
			}
			subagentMetadata(&next, event.Model, event.Effort, event.Prompt)
			if existing != nil && existing.Prompt != nil {
				next.Prompt = existing.Prompt
			}
			return &next
		})
	case contracts.SubagentEventSubagentStarted:
		return updateSubagent(feeds, threadID, event.ID, func(existing *domain.Subagent) *domain.Subagent {
			var next domain.Subagent
			if existing != nil {
				next = *existing
				next.FinishedAt, next.LastToolName, next.Stopping = nil, nil, nil
			}
			subagentMetadata(&next, event.Model, event.Effort, event.Prompt)
			if existing != nil && existing.Prompt != nil {
				next.Prompt = existing.Prompt
			}
			next.ID = event.ID
			next.Description = event.Description
			if runsTruthy(event.AgentType) {
				next.AgentType = event.AgentType
			}
			if event.SessionScoped != nil && *event.SessionScoped {
				next.SessionScoped = new(true)
			}
			next.Status = domain.SubagentStatusWorking
			if existing != nil {
				next.StartedAt = existing.StartedAt
				next.Activity = existing.Activity
			} else {
				next.StartedAt = threadRunStateNow()
				next.Activity = []*domain.SubagentActivity{}
			}
			return &next
		})
	case contracts.SubagentEventSubagentStatus:
		return updateSubagent(feeds, threadID, event.ID, func(existing *domain.Subagent) *domain.Subagent {
			base := existing
			if base == nil {
				base = newSubagent(event.ID)
			}
			next := *base
			next.FinishedAt = nil
			if event.Status == domain.SubagentLiveStatusWorking {
				next.LastToolName = nil
				next.Status = domain.SubagentStatusWorking
			} else {
				next.Stopping = nil
				next.Status = domain.SubagentStatusIdle
			}
			if runsTruthy(event.Summary) {
				next.Summary = event.Summary
			}
			return &next
		})
	case contracts.SubagentEventSubagentProgress:
		return updateSubagent(feeds, threadID, event.ID, func(existing *domain.Subagent) *domain.Subagent {
			var next domain.Subagent
			if existing != nil {
				next = *existing
			} else {
				next = domain.Subagent{ID: event.ID, Status: domain.SubagentStatusWorking, StartedAt: threadRunStateNow(), Activity: []*domain.SubagentActivity{}}
			}
			next.ID = event.ID
			next.Description = event.Description
			if runsTruthy(event.AgentType) {
				next.AgentType = event.AgentType
			}
			if runsTruthy(event.LastToolName) {
				next.LastToolName = event.LastToolName
			}
			if runsTruthy(event.Summary) {
				next.Summary = event.Summary
			}
			held := 0.0
			if existing != nil && existing.TotalTokens != nil {
				held = *existing.TotalTokens
			}
			next.TotalTokens = new(runsJSMax(held, event.TotalTokens))
			return &next
		})
	case contracts.SubagentEventSubagentActivity:
		return updateSubagent(feeds, threadID, event.ID, func(existing *domain.Subagent) *domain.Subagent {
			base := existing
			if base == nil {
				base = newSubagent(event.ID)
			}
			if slices.ContainsFunc(base.Activity, func(item *domain.SubagentActivity) bool { return item.ID == event.ActivityID }) {
				return base
			}
			item := &domain.SubagentActivity{ID: event.ActivityID, Kind: domain.SubagentActivityKind(event.Kind), Text: event.Text, At: threadRunStateNow()}
			if runsTruthy(event.Title) {
				item.Title = event.Title
			}
			next := *base
			next.Activity = append(slices.Clip(base.Activity), item)
			return &next
		})
	case contracts.SubagentEventSubagentFinished:
		return updateSubagent(feeds, threadID, event.ID, func(existing *domain.Subagent) *domain.Subagent {
			var next domain.Subagent
			if existing != nil {
				next = *existing
				next.Stopping = nil
			} else {
				next = domain.Subagent{ID: event.ID, Description: "Subagent", StartedAt: threadRunStateNow(), Activity: []*domain.SubagentActivity{}}
			}
			next.Status = domain.SubagentStatus(event.Status)
			switch {
			case event.Summary != "":
				next.Summary = new(event.Summary)
			case existing != nil:
				next.Summary = existing.Summary
			default:
				next.Summary = nil
			}
			next.FinishedAt = new(threadRunStateNow())
			return &next
		})
	}
	return feeds
}

// runsJSMax is Math.max of two numbers, NaN when either is.
func runsJSMax(a, b float64) float64 {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.NaN()
	}
	return math.Max(a, b)
}

// ApplyThreadEvent applies what the agent process reports about work that outlives the run that started it.
func ApplyThreadEvent(state *WorkspaceState, event contracts.ThreadEvent) *WorkspaceState {
	return withFeeds(state, feedsWithThreadEvent(feedsOf(state), event))
}

func feedsWithThreadEvent(feeds RunFeedsState, event contracts.ThreadEvent) RunFeedsState {
	switch event := event.(type) {
	case contracts.ResourceStoppedEvent:
		return feeds
	case contracts.SubagentEvent:
		return applySubagentReport(feeds, subagentEventTaskID(event), event)
	case contracts.BackgroundEvent:
		return applyBackgroundEvent(feeds, event)
	case contracts.GoalEvent:
		goals := maps.Clone(feeds.Goals)
		if goals == nil {
			goals = map[string]*domain.ActiveGoal{}
		}
		if event.Goal != nil {
			goals[event.TaskID] = event.Goal
		} else {
			delete(goals, event.TaskID)
		}
		feeds.Goals = goals
		return feeds
	case contracts.WorkflowEvent:
		return applyWorkflowEvent(feeds, event)
	}
	return feeds
}

func subagentEventTaskID(event contracts.SubagentEvent) string {
	switch event := event.(type) {
	case contracts.SubagentEventSubagentStarted:
		return event.TaskID
	case contracts.SubagentEventSubagentMetadata:
		return event.TaskID
	case contracts.SubagentEventSubagentStatus:
		return event.TaskID
	case contracts.SubagentEventSubagentProgress:
		return event.TaskID
	case contracts.SubagentEventSubagentActivity:
		return event.TaskID
	case contracts.SubagentEventSubagentFinished:
		return event.TaskID
	}
	return ""
}

// ThreadEventTaskID is the thread a thread event reports to.
func ThreadEventTaskID(event contracts.ThreadEvent) string {
	switch event := event.(type) {
	case contracts.ResourceStoppedEvent:
		return event.TaskID
	case contracts.SubagentEvent:
		return subagentEventTaskID(event)
	case contracts.BackgroundEvent:
		return event.TaskID
	case contracts.GoalEvent:
		return event.TaskID
	case contracts.WorkflowEventWorkflowStarted:
		return event.TaskID
	case contracts.WorkflowEventWorkflowProgress:
		return event.TaskID
	case contracts.WorkflowEventWorkflowFinished:
		return event.TaskID
	}
	return ""
}

// applyBackgroundEvent keeps what the thread's session has running in the background. A stop
// already asked for stays marked until the process is gone.
func applyBackgroundEvent(feeds RunFeedsState, event contracts.BackgroundEvent) RunFeedsState {
	stopping := map[string]bool{}
	for _, process := range feeds.BackgroundProcesses[event.TaskID] {
		if process.Stopping != nil && *process.Stopping {
			stopping[process.ID] = true
		}
	}
	processes := make([]*domain.BackgroundProcess, len(event.Processes))
	for index, process := range event.Processes {
		if stopping[process.ID] {
			marked := *process
			marked.Stopping = new(true)
			processes[index] = &marked
		} else {
			processes[index] = process
		}
	}
	return feedsWithBackgroundProcesses(feeds, event.TaskID, processes)
}

// applyWorkflowEvent keeps what a workflow reports by its thread: the run that started it may be long over.
func applyWorkflowEvent(feeds RunFeedsState, event contracts.WorkflowEvent) RunFeedsState {
	switch event := event.(type) {
	case contracts.WorkflowEventWorkflowStarted:
		return updateWorkflow(feeds, event.TaskID, event.ID, func(existing *domain.Workflow) *domain.Workflow {
			var next domain.Workflow
			if existing != nil {
				next = *existing
			} else {
				next = domain.Workflow{Phases: []*domain.WorkflowPhase{}, Agents: []*domain.WorkflowAgent{}, StartedAt: threadRunStateNow()}
			}
			next.ID = event.ID
			next.Name = event.Name
			next.Description = event.Description
			next.Status = domain.WorkflowStatusRunning
			return &next
		})
	case contracts.WorkflowEventWorkflowProgress:
		return updateWorkflow(feeds, event.TaskID, event.ID, func(existing *domain.Workflow) *domain.Workflow {
			var next domain.Workflow
			if existing != nil {
				next = *existing
			} else {
				next = domain.Workflow{ID: event.ID, Name: event.ID, Description: "Dynamic workflow", Status: domain.WorkflowStatusRunning, StartedAt: threadRunStateNow()}
			}
			next.Phases = event.Phases
			next.Agents = event.Agents
			next.TotalTokens = event.TotalTokens
			next.TotalToolCalls = event.TotalToolCalls
			return &next
		})
	case contracts.WorkflowEventWorkflowFinished:
		return updateWorkflow(feeds, event.TaskID, event.ID, func(existing *domain.Workflow) *domain.Workflow {
			var next domain.Workflow
			if existing != nil {
				next = *existing
			} else {
				next = domain.Workflow{ID: event.ID, Name: event.ID, Description: "Dynamic workflow", Phases: []*domain.WorkflowPhase{}, Agents: []*domain.WorkflowAgent{}, StartedAt: threadRunStateNow()}
			}
			next.Status = domain.WorkflowStatus(event.Status)
			next.FinishedAt = new(threadRunStateNow())
			next.Stopping = new(false)
			if event.Summary != "" {
				next.Summary = new(event.Summary)
			}
			return &next
		})
	}
	return feeds
}

// WithTurnEnded ends a thread's turn, however it ends: it reads as stopped or idle, its run is
// closed, and it keeps how and when it ended.
func WithTurnEnded(state *WorkspaceState, taskID string, status domain.RunStatus) *WorkspaceState {
	return withTransition(state, transitionWithTurnEnded(transitionOf(state), taskID, status))
}

func transitionWithTurnEnded(transition RunTransitionState, taskID string, status domain.RunStatus) RunTransitionState {
	transition.RunStatuses = runStatusesWith(transition.RunStatuses, taskID, TurnEndStatus(status))
	return transitionUpdateThread(transition, taskID, func(thread *domain.Thread) *domain.Thread { return EndedTurn(thread, status) })
}

// TurnEndStatus is how a thread reads once its turn ended with status.
func TurnEndStatus(status domain.RunStatus) ThreadRunStatus {
	if status == "cancelled" {
		return ThreadRunStatusStopped
	}
	return ThreadRunStatusIdle
}

// EndedTurn is the thread a turn ending with status leaves: no run open, and how and when it ended.
func EndedTurn(thread *domain.Thread, status domain.RunStatus) *domain.Thread {
	next := *thread
	next.RunOpen = nil
	ended, endedOK := domain.RunOutcome(status)
	if count := len(thread.Messages); count > 0 {
		last := thread.Messages[count-1]
		if last.Kind == domain.ConversationMessageKindAssistant && !runsIsTrue(last.Artifact) && !runsIsTrue(last.Answer) {
			answered := *last
			answered.Answer = new(true)
			next.Messages = domain.ReplaceLastMessage(thread.Messages, &answered)
		}
	}
	at := threadRunStateNow()
	next.RunEndedAt = new(at)
	next.RankedAt = at
	if endedOK {
		next.RunEndedAs = new(ended)
	}
	return &next
}

func runsIsTrue(flag *bool) bool { return flag != nil && *flag }

// RestoredTestimony puts back how the last run ended, its verdict and its report, which compaction does not replace.
func RestoredTestimony(thread *domain.Thread, before ThreadMark) *domain.Thread {
	next := *thread
	if before.RankedAt != nil {
		next.RankedAt = *before.RankedAt
	}
	next.RunEndedAs = before.RunEndedAs
	next.Outcome = before.Outcome
	next.OutcomeUnread = nil
	if runsIsTrue(before.OutcomeUnread) {
		next.OutcomeUnread = new(true)
	}
	next.RunEndedAt = before.RunEndedAt
	if before.Report != nil {
		next.Report = before.Report
	}
	return &next
}

// applyRunFinished is a run reaching its end: the run record goes, and what it left behind is settled on its thread.
func applyRunFinished(transition RunTransitionState, event contracts.RunEventRunStatus) RunTransitionState {
	transition.RunFeedsState = feedsWithActiveRun(feedsWithStreamingTail(transition.RunFeedsState, event.TaskID, nil), event.TaskID, nil)
	next := transitionWithTurnEnded(transition, event.TaskID, event.Status)
	next.Approvals = runsWithoutKey(next.Approvals, event.RunID)
	subagents := next.Subagents[event.TaskID]
	cutOff := func(subagent *domain.Subagent) bool {
		return subagent.Status == domain.SubagentStatusWorking && !runsIsTrue(subagent.SessionScoped)
	}
	if slices.ContainsFunc(subagents, cutOff) {
		status := domain.SubagentStatus("stopped")
		switch event.Status {
		case domain.RunStatusSucceeded:
			status = "completed"
		case domain.RunStatusFailed:
			status = "failed"
		}
		ended := make([]*domain.Subagent, len(subagents))
		for index, subagent := range subagents {
			if cutOff(subagent) {
				finished := *subagent
				finished.Status = status
				finished.FinishedAt = new(threadRunStateNow())
				ended[index] = &finished
			} else {
				ended[index] = subagent
			}
		}
		next.RunFeedsState = feedsWithSubagents(next.RunFeedsState, event.TaskID, ended)
	}
	workflows := next.Workflows[event.TaskID]
	running := func(workflow *domain.Workflow) bool { return workflow.Status == domain.WorkflowStatusRunning }
	if event.Status != domain.RunStatusSucceeded && slices.ContainsFunc(workflows, running) {
		stopped := make([]*domain.Workflow, len(workflows))
		for index, workflow := range workflows {
			if running(workflow) {
				ended := *workflow
				ended.Status = domain.WorkflowStatusStopped
				ended.FinishedAt = new(threadRunStateNow())
				ended.Stopping = new(false)
				stopped[index] = &ended
			} else {
				stopped[index] = workflow
			}
		}
		next.RunFeedsState = feedsWithWorkflows(next.RunFeedsState, event.TaskID, stopped)
	}
	if event.Status == domain.RunStatusFailed && runsTruthy(event.Message) {
		next = transitionUpdateThread(next, event.TaskID, func(thread *domain.Thread) *domain.Thread {
			failed := *thread
			failed.Messages = domain.AppendMessages(thread.Messages, []*domain.ConversationMessage{domain.CreateFailureMessage(*event.Message)})
			failed.UpdatedAt = threadRunStateNow()
			return &failed
		})
	}
	return next
}

// answerPart is the message id a block of a streamed answer belongs to, and how many parts it is in.
// Once another message lands after the answer, its next block starts a new part with its own id.
func answerPart(run ActiveRun, last *domain.ConversationMessage, messageID string) (id string, parts float64) {
	held, ok := RunRecord(run).AnswerParts[messageID]
	if !ok {
		return messageID, 0
	}
	id = messageID
	if held != 0 {
		id = runsPartID(messageID, held)
	}
	if last != nil && last.ID == id {
		return id, held
	}
	return runsPartID(messageID, held+1), held + 1
}

func runsPartID(messageID string, parts float64) string {
	return messageID + ":" + strconv.FormatFloat(parts, 'f', -1, 64)
}

// AnswerPartID is the message the latest block of a streamed answer landed on.
func AnswerPartID(run ActiveRun, messageID string) string {
	record := RunRecord(run)
	if record == nil {
		return messageID
	}
	if parts := record.AnswerParts[messageID]; parts != 0 {
		return runsPartID(messageID, parts)
	}
	return messageID
}

// RunEventBaseOf is the run, thread and sequence a run event carries.
func RunEventBaseOf(event contracts.RunEvent) contracts.RunEventBase {
	switch event := event.(type) {
	case contracts.RunEventQuestionRequested:
		return event.RunEventBase
	case contracts.RunEventQuestionAnswered:
		return event.RunEventBase
	case contracts.RunEventQuestionClosed:
		return event.RunEventBase
	case contracts.RunEventRunStarted:
		return event.RunEventBase
	case contracts.RunEventRunStatus:
		return event.RunEventBase
	case contracts.RunEventAssistantDelta:
		return event.RunEventBase
	case contracts.RunEventAssistantTail:
		return event.RunEventBase
	case contracts.RunEventContextUsage:
		return event.RunEventBase
	case contracts.RunEventContextSpent:
		return event.RunEventBase
	case contracts.RunEventContextCompactionStatus:
		return event.RunEventBase
	case contracts.RunEventRunRetrying:
		return event.RunEventBase
	case contracts.RunEventContextCompacted:
		return event.RunEventBase
	case contracts.RunEventToolIntent:
		return event.RunEventBase
	case contracts.RunEventComputerUseSetupRequired:
		return event.RunEventBase
	case contracts.RunEventApprovalRequested:
		return event.RunEventBase
	case contracts.RunEventContinuationUpdated:
		return event.RunEventBase
	case contracts.RunEventContinuationLost:
		return event.RunEventBase
	case contracts.RunEventQueuedDelivered:
		return event.RunEventBase
	case contracts.RunEventQueuedSteerFailed:
		return event.RunEventBase
	}
	return contracts.RunEventBase{}
}

// throughRetry is a run that hears anything from its engine other than another retry notice through the retry.
func throughRetry(transition RunTransitionState, run ActiveRun, event contracts.RunEvent) RunTransitionState {
	if _, retrying := run.(ActiveRunRetrying); !retrying {
		return transition
	}
	switch event.(type) {
	case contracts.RunEventRunRetrying, contracts.RunEventQueuedDelivered, contracts.RunEventQueuedSteerFailed, contracts.RunEventContextSpent:
		return transition
	}
	transition.RunFeedsState = feedsWithRunPhase(transition.RunFeedsState, RunRecord(run).TaskID, RunPhaseRunning{Status: RunPhaseRunningStatusRunning})
	return transition
}

func ApplyRunEvent(state *WorkspaceState, event contracts.RunEvent) *WorkspaceState {
	return withTransition(state, applyRunEvent(transitionOf(state), event))
}

func runsLastMessage(threads []*domain.Thread, taskID string) *domain.ConversationMessage {
	for _, thread := range threads {
		if thread.ID == taskID {
			if count := len(thread.Messages); count > 0 {
				return thread.Messages[count-1]
			}
			return nil
		}
	}
	return nil
}

func runThreadOf(threads []*domain.Thread, taskID string) *domain.Thread {
	for _, thread := range threads {
		if thread.ID == taskID {
			return thread
		}
	}
	return nil
}

func runsWithMessages(thread *domain.Thread, messages []*domain.ConversationMessage) *domain.Thread {
	next := *thread
	next.Messages = messages
	next.UpdatedAt = threadRunStateNow()
	return &next
}

// compactedText is the line a compaction leaves in its thread. idle is one the app started while nobody used the thread.
func compactedText(event contracts.RunEventContextCompacted, idle bool) string {
	before := runsLocaleNumber(event.PreTokens)
	if idle {
		if event.PostTokens == nil {
			return "Compacted while idle at " + before + " tokens."
		}
		return "Compacted while idle: " + before + " → " + runsLocaleNumber(*event.PostTokens) + " tokens."
	}
	if event.PostTokens == nil {
		return "Context " + string(event.Trigger) + "-compacted at " + before + " tokens."
	}
	return "Context " + string(event.Trigger) + "-compacted: " + before + " → " + runsLocaleNumber(*event.PostTokens) + " tokens."
}

func applyRunEvent(state RunTransitionState, event contracts.RunEvent) RunTransitionState {
	base := RunEventBaseOf(event)
	taskID := base.TaskID
	found := state.ActiveRuns[taskID]
	if found == nil {
		return state
	}
	if record := RunRecord(found); base.RunID != record.RunID || base.Sequence <= record.Sequence {
		return state
	}
	through := throughRetry(state, found, event)
	withSequence := through
	withSequence.RunFeedsState = feedsWithActiveRun(through.RunFeedsState, taskID, EditRun(through.ActiveRuns[taskID], func(record *ActiveRunRunning) { record.Sequence = base.Sequence }))
	active := withSequence.ActiveRuns[taskID]
	record := RunRecord(active)
	feeds := withSequence.RunFeedsState

	switch event := event.(type) {
	case contracts.RunEventRunRetrying:
		withSequence.RunFeedsState = feedsWithRunPhase(feeds, taskID, RunPhaseRetrying{Retry: event.RetryNotice})
		return withSequence

	case contracts.RunEventRunStarted:
		var carried []*domain.Workflow
		for _, workflow := range state.Workflows[taskID] {
			if workflow.Status == domain.WorkflowStatusRunning {
				carried = append(carried, workflow)
			}
		}
		withSequence.RunFeedsState = feedsWithWorkflows(feedsWithStreamingTail(feeds, taskID, nil), taskID, carried)
		return withSequence

	case contracts.RunEventRunStatus:
		switch event.Status {
		case domain.RunStatusRunning:
			withSequence.RunFeedsState = feedsWithApprovalAnswered(feeds, taskID)
			return withSequence
		case domain.RunStatusAwaitingApproval:
			withSequence.RunFeedsState = feedsWithRunPhase(feeds, taskID, RunPhaseRunning{Status: RunPhaseRunningStatusAwaitingApproval})
			return withSequence
		}
		return applyRunFinished(withSequence, event)

	case contracts.RunEventQuestionRequested:
		questions := make([]*domain.PendingQuestion, len(event.Request.Questions))
		messages := make([]*domain.ConversationMessage, len(event.Request.Questions))
		for index, question := range event.Request.Questions {
			questions[index] = &domain.PendingQuestion{
				AgentQuestion:   *question,
				QuestionAddress: domain.QuestionAddress{RunID: event.RunID, RequestID: event.RequestID, QuestionID: question.ID},
				Blocking:        event.Request.Blocking,
			}
			text := question.Question
			if len(question.Options) > 0 {
				options := make([]string, len(question.Options))
				for position, option := range question.Options {
					if option.Description != "" {
						options[position] = "- " + option.Label + ": " + option.Description
					} else {
						options[position] = "- " + option.Label
					}
				}
				text += "\n\n" + strings.Join(options, "\n")
			}
			messages[index] = domain.CreateConversationMessage(domain.ConversationMessageKindAssistant, text, nil, nil, nil, nil, nil)
		}
		asked := append(slices.Clone(record.Questions), questions...)
		if asked == nil {
			asked = []*domain.PendingQuestion{}
		}
		withSequence.RunFeedsState = feedsWithActiveRun(feeds, taskID, EditRun(active, func(next *ActiveRunRunning) { next.Questions = asked }))
		return transitionUpdateThread(withSequence, taskID, func(thread *domain.Thread) *domain.Thread {
			return runsWithMessages(thread, slices.Concat(thread.Messages, messages))
		})

	case contracts.RunEventQuestionAnswered, contracts.RunEventQuestionClosed:
		requestID, questionID, answered := "", "", false
		if answer, ok := event.(contracts.RunEventQuestionAnswered); ok {
			requestID, questionID, answered = answer.RequestID, answer.QuestionID, true
		} else {
			requestID = event.(contracts.RunEventQuestionClosed).RequestID
		}
		questions := []*domain.PendingQuestion{}
		for _, question := range record.Questions {
			if question.RequestID != requestID || answered && question.QuestionID != questionID {
				questions = append(questions, question)
			}
		}
		withSequence.RunFeedsState = feedsWithActiveRun(feeds, taskID, EditRun(active, func(next *ActiveRunRunning) { next.Questions = questions }))
		if !answered {
			return withSequence
		}
		text := event.(contracts.RunEventQuestionAnswered).Text
		return transitionUpdateThread(withSequence, taskID, func(thread *domain.Thread) *domain.Thread {
			return runsWithMessages(thread, slices.Concat(thread.Messages, []*domain.ConversationMessage{domain.CreateConversationMessage(domain.ConversationMessageKindUser, text, nil, nil, nil, nil, nil)}))
		})

	case contracts.RunEventAssistantTail:
		if event.Text == "" {
			withSequence.RunFeedsState = feedsWithStreamingTail(feeds, taskID, nil)
			return withSequence
		}
		id, _ := answerPart(active, runsLastMessage(withSequence.Threads, taskID), event.MessageID)
		withSequence.RunFeedsState = feedsWithStreamingTail(feeds, taskID, &StreamingTail{MessageID: id, Text: event.Text})
		return withSequence

	case contracts.RunEventAssistantDelta:
		last := runsLastMessage(withSequence.Threads, taskID)
		artifact := runsIsTrue(event.Artifact)
		runsPartID, parts := event.MessageID, 0.0
		if !artifact {
			runsPartID, parts = answerPart(active, last, event.MessageID)
		}
		tracked := withSequence
		if held, ok := record.AnswerParts[event.MessageID]; !artifact && !(ok && held == parts) {
			answerParts := runsWithKey(record.AnswerParts, event.MessageID, parts)
			tracked.RunFeedsState = feedsWithActiveRun(feeds, taskID, EditRun(active, func(next *ActiveRunRunning) {
				next.Sequence = base.Sequence
				next.AnswerParts = answerParts
			}))
		}
		if !artifact {
			tracked.RunFeedsState = feedsWithStreamingTail(tracked.RunFeedsState, taskID, nil)
		}
		return transitionUpdateThread(tracked, taskID, func(thread *domain.Thread) *domain.Thread {
			var messages []*domain.ConversationMessage
			if last != nil && last.Kind == domain.ConversationMessageKindAssistant && last.ID == runsPartID {
				joined := *last
				separator := "\n"
				if runsIsTrue(event.Append) {
					separator = ""
				}
				joined.Text = last.Text + separator + event.Text
				messages = domain.ReplaceLastMessage(thread.Messages, &joined)
			} else {
				message := &domain.ConversationMessage{ID: runsPartID, Kind: domain.ConversationMessageKindAssistant, Text: event.Text, At: threadRunStateNow()}
				if artifact {
					message.Artifact = new(true)
				}
				if artifact && last != nil && last.Kind == domain.ConversationMessageKindAssistant && !runsIsTrue(last.Artifact) {
					messages = slices.Concat(thread.Messages[:len(thread.Messages)-1], []*domain.ConversationMessage{message, last})
				} else {
					messages = domain.AppendMessages(thread.Messages, []*domain.ConversationMessage{message})
				}
			}
			return runsWithMessages(thread, messages)
		})

	case contracts.RunEventContextUsage:
		settledRun := withSequence
		if runsIsTrue(record.Warming) {
			settledRun.RunFeedsState = feedsWithActiveRun(feeds, taskID, EditRun(active, func(next *ActiveRunRunning) { next.Warming = nil }))
		}
		var current *domain.ContextUsage
		if thread := runThreadOf(withSequence.Threads, taskID); thread != nil {
			current = thread.ContextUsage
		}
		if current != nil && current.Tokens == event.Tokens && current.Limit == event.Limit && current.Model == event.Model {
			return settledRun
		}
		return transitionUpdateThread(settledRun, taskID, func(thread *domain.Thread) *domain.Thread {
			next := *thread
			next.ContextUsage = &domain.ContextUsage{Tokens: event.Tokens, Limit: event.Limit, Model: event.Model}
			return &next
		})

	case contracts.RunEventContextSpent:
		return transitionUpdateThread(withSequence, taskID, func(thread *domain.Thread) *domain.Thread {
			model, ok := thread.Model, thread.Model != nil
			if !ok {
				fallback, has := domain.DefaultModelFor(domain.EngineID(thread.Engine))
				if !has {
					return thread
				}
				model = &fallback
			}
			key := string(*model)
			next := *thread
			next.Usage = runsWithKey(thread.Usage, key, math.Min(runsMaxSafeInteger, thread.Usage[key]+event.Tokens))
			return &next
		})

	case contracts.RunEventContextCompactionStatus:
		status := RunPhaseRunningStatusRunning
		if event.Compacting {
			status = RunPhaseRunningStatusCompacting
		}
		withSequence.RunFeedsState = feedsWithRunPhase(feeds, taskID, RunPhaseRunning{Status: status})
		if !runsTruthy(event.Error) {
			return withSequence
		}
		return transitionUpdateThread(withSequence, taskID, func(thread *domain.Thread) *domain.Thread {
			return runsWithMessages(thread, domain.AppendMessages(thread.Messages, []*domain.ConversationMessage{domain.CreateFailureMessage(*event.Error)}))
		})

	case contracts.RunEventContextCompacted:
		withSequence.RunFeedsState = feedsWithRunPhase(feeds, taskID, RunPhaseRunning{Status: RunPhaseRunningStatusRunning})
		text := compactedText(event, runsIsTrue(record.Idle))
		return transitionUpdateThread(withSequence, taskID, func(thread *domain.Thread) *domain.Thread {
			next := *domain.WithMemoryUnheard(thread)
			next.Messages = domain.AppendMessages(thread.Messages, []*domain.ConversationMessage{domain.CreateConversationMessage(domain.ConversationMessageKindSystem, text, nil, nil, nil, nil, nil)})
			if thread.ContextUsage != nil && event.PostTokens != nil {
				usage := *thread.ContextUsage
				usage.Tokens = *event.PostTokens
				next.ContextUsage = &usage
			}
			next.UpdatedAt = threadRunStateNow()
			return &next
		})

	case contracts.RunEventToolIntent:
		detail := runsPrettyJSON(event.Intent.Input)
		return transitionUpdateThread(withSequence, taskID, func(thread *domain.Thread) *domain.Thread {
			return runsWithMessages(thread, domain.AppendMessages(thread.Messages, []*domain.ConversationMessage{domain.CreateConversationMessage(domain.ConversationMessageKindTool, event.Intent.Name, detail, nil, nil, nil, nil)}))
		})

	case contracts.RunEventApprovalRequested:
		approval := &ApprovalView{
			ApprovalID:  event.ApprovalID,
			TaskID:      event.TaskID,
			RunID:       event.RunID,
			Title:       event.Title,
			Description: event.Description,
			ToolName:    event.Intent.Name,
			Input:       approvalInput(event.Intent.Input),
		}
		withSequence.Approvals = runsWithKey(feeds.Approvals, event.RunID, approval)
		return withSequence

	case contracts.RunEventContinuationUpdated:
		return transitionUpdateThread(withSequence, taskID, func(thread *domain.Thread) *domain.Thread {
			next := *thread
			next.InheritedContinuation = nil
			next.Continuation = new(event.Continuation)
			next.ContinuationStatus = domain.ContinuationStatusAvailable
			next.UpdatedAt = threadRunStateNow()
			return &next
		})

	case contracts.RunEventContinuationLost:
		return transitionUpdateThread(withSequence, taskID, func(thread *domain.Thread) *domain.Thread {
			next := *thread
			next.Continuation = nil
			next.ContinuationStatus = domain.ContinuationStatusInvalid
			next.UpdatedAt = threadRunStateNow()
			return &next
		})
	}
	return withSequence
}

// runsMaxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER.
const runsMaxSafeInteger = 1<<53 - 1

// approvalInput is a tool's input as an approval shows it: an object as it is, anything else under `value`.
func approvalInput(input jsontext.Value) map[string]jsontext.Value {
	if len(input) == 0 {
		return map[string]jsontext.Value{}
	}
	if input.Kind() == '{' {
		var fields map[string]jsontext.Value
		if err := jsonx.Unmarshal(input, &fields); err == nil {
			return fields
		}
	}
	return map[string]jsontext.Value{"value": input}
}

// runsPrettyJSON is JSON.stringify(value, null, 2); nil for an absent value.
func runsPrettyJSON(value jsontext.Value) *string {
	if len(value) == 0 {
		return nil
	}
	formatted := value.Clone()
	if err := formatted.Indent(jsontext.WithIndentPrefix(""), jsontext.WithIndent("  "), jsontext.CanonicalizeRawFloats(true), jsontext.CanonicalizeRawInts(true)); err != nil {
		return new(string(value))
	}
	return new(string(formatted))
}

// runsLocaleNumber is Number.prototype.toLocaleString("en-US"): thousands grouped, at most three decimals.
func runsLocaleNumber(value float64) string {
	if math.IsNaN(value) {
		return "NaN"
	}
	if math.IsInf(value, 0) {
		if value < 0 {
			return "-∞"
		}
		return "∞"
	}
	text := strconv.FormatFloat(math.Abs(value), 'f', 3, 64)
	whole, fraction, _ := strings.Cut(text, ".")
	fraction = strings.TrimRight(fraction, "0")
	var grouped strings.Builder
	if value < 0 && (whole != "0" || fraction != "") {
		grouped.WriteByte('-')
	}
	for index, digit := range whole {
		if index > 0 && (len(whole)-index)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	if fraction != "" {
		grouped.WriteString("." + fraction)
	}
	return grouped.String()
}
