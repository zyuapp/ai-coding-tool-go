package application

import (
	"crypto/rand"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// Now is the reducer's clock, Date.now() in milliseconds. Tests pin it.
var Now = func() float64 { return float64(time.Now().UnixMilli()) }

// NewID is the reducer's crypto.randomUUID. Tests pin it.
var NewID = func() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func SharedNow() float64 { return Now() }

func SameStrings(left, right []string) bool { return slices.Equal(left, right) }

func Settled(state *WorkspaceState, effects []WorkspaceEffect, result contracts.WorkspaceCommandResult) WorkspaceTransition {
	if effects == nil {
		effects = []WorkspaceEffect{}
	}
	return WorkspaceTransition{State: *state, Effects: effects, Result: result}
}

// FollowUps marks effects as follow-ups: work that keeps a view current, which the input does not wait for.
func FollowUps(effects []WorkspaceEffect) []WorkspaceEffect {
	marked := make([]WorkspaceEffect, len(effects))
	for index, effect := range effects {
		marked[index] = withFollowUp(effect)
	}
	return marked
}

// withFollowUp is effect with its FollowUp field set, which every WorkspaceEffect member carries.
func withFollowUp(effect WorkspaceEffect) WorkspaceEffect {
	value := reflect.ValueOf(effect)
	field := value.FieldByName("FollowUp")
	if !field.IsValid() {
		panic("application: effect " + value.Type().String() + " has no FollowUp field")
	}
	if !field.IsNil() && field.Elem().Bool() {
		return effect
	}
	copied := reflect.New(value.Type()).Elem()
	copied.Set(value)
	followUp := true
	copied.FieldByName("FollowUp").Set(reflect.ValueOf(&followUp))
	return copied.Interface().(WorkspaceEffect)
}

// Rejected is a refusal that belongs to this transition even when the same message is already on screen.
func Rejected(state *WorkspaceState, message string, effects []WorkspaceEffect) WorkspaceTransition {
	next := *state
	next.ActionError = &message
	if effects == nil {
		effects = []WorkspaceEffect{}
	}
	return WorkspaceTransition{State: next, Effects: effects, Result: contracts.WorkspaceCommandResultFalse{Message: message}}
}

// Declined is a thread's own request turned down: nothing changes, the user is shown nothing, and the reason answers the thread.
func Declined(state *WorkspaceState, reason string) WorkspaceTransition {
	return Settled(state, nil, contracts.WorkspaceCommandResultTrue{Answer: contracts.CoordinationAnswerFalse{Reason: reason}})
}

// Recorded is a thread's own request carried out, answered with what was recorded.
func Recorded(transition WorkspaceTransition, noteID *string) WorkspaceTransition {
	answer := contracts.CoordinationAnswerTrue{}
	if noteID != nil && *noteID != "" {
		answer.NoteID = noteID
	}
	transition.Result = contracts.WorkspaceCommandResultTrue{Answer: answer}
	return transition
}

// TargetID is the named thread, which has to exist; an unnamed command falls back to the one the user is looking at.
func TargetID(state *WorkspaceState, taskID *string) *string {
	if taskID == nil {
		return state.CurrentID
	}
	for _, thread := range state.Threads {
		if thread.ID == *taskID {
			return taskID
		}
	}
	return nil
}

// ReduceErrorDismissed is the error the window shows, put away by the user.
func ReduceErrorDismissed(state *WorkspaceState) SliceTransition {
	return SliceTransition{Patch: func(next *WorkspaceState) {
		next.ActionError = nil
		next.ActionErrorPage = nil
	}}
}
