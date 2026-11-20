package application

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Side chats, which fork a thread to ask something beside it.

var SideChatInputs = []string{"side-chat.open", "side-chat.close"}

// `input` is one of the SideChatInputs.
func ReduceSideChats(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.SideChatCommandSideChatClose:
		return SliceTransition(sideChatClosed(state, input.ChatID))
	case contracts.SideChatCommandSideChatOpen:
		if state.CurrentID == nil {
			return SliceTransition{}
		}
		source := threadsFind(state.Threads, *state.CurrentID)
		if source == nil {
			return SliceTransition{}
		}
		sequence := state.SideChatSequence + 1
		thread := SideChatThread(source, input.ChatID, fmt.Sprintf("Chat %s", threadsNumber(sequence)), Now())
		docks, effects := FocusDockTabIn(ShowDockTabIn(DocksOf(state), source.ID, input.ChatID), source.ID, input.ChatID)
		chats := append(slices.Clip(state.SideChats), &SideChat{ID: input.ChatID, SourceThreadID: source.ID, Error: nil})
		return SliceTransition{
			Patch: func(next *WorkspaceState) {
				next.SideChats = chats
				next.SideChatSequence = sequence
			},
			Edits:   []OwnerEdit{AddedIn(ThreadsOf(state), thread), docks},
			Effects: effects,
		}
	}
	return SliceTransition{}
}

// threadsNumber is a whole count as JavaScript writes it in a template.
func threadsNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// Closing one ends its run and drops what waits on it.
func sideChatClosed(state *WorkspaceState, chatID string) Step {
	for _, chat := range state.SideChats {
		if chat.ID == chatID {
			return SideChatsClosed(state, []*SideChat{chat})
		}
	}
	return Step{}
}

// Closing a side chat discards the thread itself, so its run, queue, and draft go with it, the run cancelled first.
func SideChatsClosed(state *WorkspaceState, closing []*SideChat) Step {
	closed := jsonx.Set[string]{}
	ids := make([]string, 0, len(closing))
	for _, chat := range closing {
		if !closed.Has(chat.ID) {
			ids = append(ids, chat.ID)
		}
		closed[chat.ID] = struct{}{}
	}
	steps := make([]func(*WorkspaceState) Step, 0, len(closing)+1)
	for _, chat := range closing {
		steps = append(steps, func(next *WorkspaceState) Step {
			step := Step{Edits: []OwnerEdit{ClearedDraftIn(ComposerOf(next), chat.ID)}}
			if active := RunRecord(next.ActiveRuns[chat.ID]); active != nil {
				step.Effects = []WorkspaceEffect{WorkspaceEffectSendRunCommand{Command: contracts.CancelRunCommand{TaskID: chat.ID, RunID: active.RunID}}}
			}
			return step
		})
	}
	steps = append(steps, func(next *WorkspaceState) Step {
		return AndThen(next, Step{
			Edits: []OwnerEdit{RemovedIn(ThreadsOf(next), closed), ChatTabsClosedIn(DocksOf(next), next.SideChats, closed)},
			// Nothing a side chat can reach schedules one today; this keeps that true if the tool table changes.
			Effects: RetireAutomations(next, ids),
		}, func(left *WorkspaceState) Step { return Pruned(left, closed) })
	})
	return InTurn(state, Step{}, steps)
}
