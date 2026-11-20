package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func InitialRunsState() RunsState {
	return RunsState{
		RunFeedsState: RunFeedsState{
			ActiveRuns:          map[string]ActiveRun{},
			RunStatuses:         map[string]ThreadRunStatus{},
			Approvals:           map[string]*ApprovalView{},
			StreamingTails:      map[string]*StreamingTail{},
			BackgroundProcesses: map[string][]*domain.BackgroundProcess{},
			Workflows:           map[string][]*domain.Workflow{},
			Subagents:           map[string][]*domain.Subagent{},
			Goals:               map[string]*domain.ActiveGoal{},
		},
		PendingRuns:       map[string]*PendingRun{},
		StartingProposals: []string{},
		QuietStops:        []string{},
		QuietSessions:     []*QuietSession{},
		QueuedMessages:    map[string][]*QueuedMessage{},
		LastRunIDs:        map[string]string{},
	}
}

// WorkflowByID finds a workflow by id, wherever it is kept: a dock follows one workflow, not one thread's list.
func WorkflowByID(state *WorkspaceState, id *string) *domain.Workflow {
	if id == nil || *id == "" {
		return nil
	}
	for _, workflows := range state.Workflows {
		for _, workflow := range workflows {
			if workflow.ID == *id {
				return workflow
			}
		}
	}
	return nil
}

const QueuedNotYours = "Only a message you wrote can be edited."
const QueuedSent = "That message has already been sent."

// EditableQueued is the queued message the user may take back into the composer to rewrite, or the
// refusal saying why not: a steered one is already on its way to the agent, and only the user's own
// messages are theirs to rewrite. Exactly one of the two is set.
func EditableQueued(queued []*QueuedMessage, messageID string) (message *QueuedMessage, refusal string) {
	index := slices.IndexFunc(queued, func(item *QueuedMessage) bool { return item.ID == messageID })
	if index == -1 || queued[index].Steering != nil && *queued[index].Steering {
		return nil, QueuedSent
	}
	found := queued[index]
	if domain.IsUsersOwn(found.Origin, found.Detail, found.Answers) {
		return found, ""
	}
	return nil, QueuedNotYours
}

// DrainingQueued reports whether a queued message is already on its way into a run that is starting.
func DrainingQueued(state *WorkspaceState, taskID string, messageID string) bool {
	for _, pending := range state.PendingRuns {
		if pending.TaskID != nil && *pending.TaskID == taskID && slices.Contains(pending.QueuedIDs, messageID) {
			return true
		}
	}
	return false
}

// QueuedView is a thread's queue as shown, nil when it has none: the stored messages themselves
// unless a starting run is taking from it, which sets `starting` on those it takes.
func QueuedView(state *WorkspaceState, taskID string) []*QueuedMessageView {
	queued, ok := state.QueuedMessages[taskID]
	if !ok {
		return nil
	}
	views := make([]*QueuedMessageView, len(queued))
	for index, message := range queued {
		views[index] = &QueuedMessageView{QueuedMessage: *message}
		if DrainingQueued(state, taskID, message.ID) {
			views[index].Starting = new(true)
		}
	}
	return views
}
