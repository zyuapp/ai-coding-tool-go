// Command reduce-lines runs application.Reduce over JSON lines, for comparing it with the TypeScript
// reducer (scripts/reducer-diff). Each line `{"now": ms, "input": {..}}` is reduced and answered with
// one line `{"state", "effects", "result", "ids"}`, or `{"error"}` when the input does not decode or
// the reducer panics. `{"reset": true}` starts again from the empty state with ids counted from zero.
// A line carrying `"state"` reduces that state instead of the running one, with ids counted from zero.
// Ids are `00000000-0000-4000-8000-<12 digits>` counting up, as scripts/reducer-diff/ts-reduce.cjs makes them.
// `{"views": {at, taskId, before, reference}}` answers the projections the host makes of the current state;
// `patches` and `mobilePatch` run from the state and mobile view of the previous views request.
// `{"call": name, "state": {..}, "args": [..]}` answers `{"value"}`: one projection of that state, named
// as the TypeScript function it ports (see calls).
// `{"check": name, "value": v}` answers `{"value": bool}`: the contracts validator the TypeScript one named ports.
// `reduce-lines <requests-fifo> <answers-fifo>` reads and answers through those pipes instead of stdio.
package main

import (
	"bufio"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"reflect"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var (
	fixedNow float64
	ids      int
	// The state and mobile view the last views request saw.
	viewedState  *application.WorkspaceState
	viewedMobile *contracts.MobileView
)

func pin() {
	now := func() float64 { return fixedNow }
	newID := func() string {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", ids)
		ids++
		return id
	}
	domain.Now, domain.NewID = now, newID
	application.Now, application.NewID = now, newID
}

func main() {
	pin()
	reader, writer := os.Stdin, os.Stdout
	if len(os.Args) == 3 {
		var err error
		if reader, err = os.Open(os.Args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if writer, err = os.OpenFile(os.Args[2], os.O_WRONLY, 0); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	in := bufio.NewScanner(reader)
	in.Buffer(make([]byte, 0, 1<<20), 1<<30)
	out := bufio.NewWriter(writer)
	state := application.EmptyWorkspaceState(nil)
	viewedState = state
	for in.Scan() {
		answer := respond(&state, in.Bytes())
		bytes, err := jsonx.Marshal(answer)
		if err != nil {
			bytes, _ = jsonx.Marshal(map[string]string{"error": "encode: " + err.Error()})
		}
		out.Write(bytes)
		out.WriteByte('\n')
		out.Flush()
	}
}

type request struct {
	Reset bool             `json:"reset,omitzero"`
	Views *viewsRequest    `json:"views,omitzero"`
	Call  string           `json:"call,omitzero"`
	Check string           `json:"check,omitzero"`
	Value jsontext.Value   `json:"value,omitzero"`
	Args  []jsontext.Value `json:"args,omitzero"`
	Now   *float64         `json:"now,omitzero"`
	State jsontext.Value   `json:"state,omitzero"`
	Input jsontext.Value   `json:"input,omitzero"`
}

type answer struct {
	State   *application.WorkspaceState `json:"state,omitzero"`
	Effects any                         `json:"effects,omitzero"`
	Result  any                         `json:"result,omitzero"`
	IDs     *int                        `json:"ids,omitzero"`
	Views   *views                      `json:"views,omitzero"`
	Value   any                         `json:"value,omitzero"`
	Error   string                      `json:"error,omitzero"`
}

func respond(state **application.WorkspaceState, line []byte) (reply answer) {
	var asked request
	if err := jsonx.Unmarshal(line, &asked); err != nil {
		return answer{Error: "unreadable line: " + err.Error()}
	}
	if asked.Reset {
		*state = application.EmptyWorkspaceState(nil)
		viewedState, viewedMobile = *state, nil
		ids = 0
		return answer{State: *state}
	}
	if asked.Views != nil {
		defer func() {
			if problem := recover(); problem != nil {
				reply = answer{Error: fmt.Sprintf("views panicked: %v", problem)}
			}
		}()
		made, err := project(*state, asked.Views)
		if err != nil {
			return answer{Error: "views: " + err.Error()}
		}
		return answer{Views: made}
	}
	if asked.Check != "" {
		validator, ok := validators[asked.Check]
		if !ok {
			return answer{Error: "unknown check " + asked.Check}
		}
		var value any
		if err := jsonx.Unmarshal(asked.Value, &value); err != nil {
			return answer{Error: "value: " + err.Error()}
		}
		return answer{Value: jsonValue{validator(value)}}
	}
	if asked.Call != "" {
		defer func() {
			if problem := recover(); problem != nil {
				reply = answer{Error: fmt.Sprintf("%s panicked: %v", asked.Call, problem)}
			}
		}()
		given := &application.WorkspaceState{}
		if err := jsonx.Unmarshal(asked.State, given); err != nil {
			return answer{Error: "state: " + err.Error()}
		}
		if asked.Now != nil {
			fixedNow = *asked.Now
		}
		value, err := call(given, asked.Call, asked.Args)
		if err != nil {
			return answer{Error: asked.Call + ": " + err.Error()}
		}
		return answer{Value: jsonValue{value}}
	}
	if len(asked.State) > 0 {
		// A zero state: decoding merges into maps, and the empty state's are shared.
		given := &application.WorkspaceState{}
		if err := jsonx.Unmarshal(asked.State, given); err != nil {
			return answer{Error: "state: " + err.Error()}
		}
		*state = given
		ids = 0
	}
	fixedNow = 0
	if asked.Now != nil {
		fixedNow = *asked.Now
	}
	var input application.WorkspaceInput
	if err := jsonx.Unmarshal(asked.Input, &input); err != nil {
		return answer{Error: "input: " + err.Error()}
	}
	defer func() {
		if problem := recover(); problem != nil {
			reply = answer{Error: fmt.Sprintf("panicked: %v", problem)}
		}
	}()
	transition := application.Reduce(*state, input)
	next := transition.State
	*state = &next
	count := ids
	return answer{State: *state, Effects: transition.Effects, Result: transition.Result, IDs: &count}
}

type viewsRequest struct {
	At        float64 `json:"at"`
	TaskID    string  `json:"taskId"`
	Before    int     `json:"before"`
	Reference string  `json:"reference"`
}

type threadLists struct {
	Listed            []string          `json:"listed"`
	Visible           []string          `json:"visible"`
	Archived          []string          `json:"archived"`
	WorktreeThreadIDs jsonx.Set[string] `json:"worktreeThreadIds"`
	SideChatAttention jsonx.Set[string] `json:"sideChatAttention"`
	UnreadCount       float64           `json:"unreadCount"`
}

type views struct {
	Mobile      *contracts.MobileView             `json:"mobile"`
	MobilePatch *contracts.MobilePatch            `json:"mobilePatch"`
	Archived    []*contracts.MobileArchivedThread `json:"archived"`
	Read        *contracts.MobileThreadRead       `json:"read"`
	History     *contracts.MobileThreadHistory    `json:"history"`
	Lists       threadLists                       `json:"lists"`
	Badge       float64                           `json:"badge"`
	Summaries   [][]*contracts.ThreadSummary      `json:"summaries"`
	Transcript  *contracts.ThreadTranscript       `json:"transcript"`
	Wait        *contracts.ThreadWaitResult       `json:"wait"`
	Find        *string                           `json:"find"`
	Patches     []*contracts.WorkspacePatch       `json:"patches"`
}

// The filters ts-reduce.cjs asks threadSummaries for, in its order.
var summaryFilters = []string{
	`{"scope":{"kind":"all"}}`,
	`{"scope":{"kind":"all"},"archived":true,"search":%q,"limit":5}`,
	`{"scope":{"kind":"project","projectId":"project-1"},"idleForMs":1000}`,
	`{"scope":{"kind":"projectless"},"attachments":true}`,
}

func project(state *application.WorkspaceState, asked *viewsRequest) (*views, error) {
	fixedNow = asked.At
	ids := func(threads []*domain.Thread) []string {
		out := make([]string, len(threads))
		for i, thread := range threads {
			out[i] = thread.ID
		}
		return out
	}
	lists := application.ThreadActivityThreadLists(state)
	view := application.ProjectMobileView(state, asked.At)
	made := &views{
		Mobile:   view,
		Archived: application.MobileArchivedThreads(state),
		Read:     application.MobileThreadRead(state, asked.TaskID),
		History:  application.MobileThreadHistory(state, asked.TaskID, asked.Before),
		Lists: threadLists{
			Listed: ids(lists.ListedThreads), Visible: ids(lists.VisibleThreads), Archived: ids(lists.ArchivedThreads),
			WorktreeThreadIDs: lists.WorktreeThreadIDs, SideChatAttention: lists.SideChatAttention, UnreadCount: lists.UnreadCount,
		},
		Badge:      lists.UnreadCount + application.RemoteUnreadCount(&state.Computers),
		Transcript: application.ThreadTranscript(state, asked.TaskID, 30, 2_000, nil),
		Wait:       application.ThreadWaitResult(state, asked.TaskID, false),
	}
	if viewedMobile != nil {
		made.MobilePatch = application.DiffMobileView(viewedMobile, view)
	}
	for i, filter := range summaryFilters {
		if i == 1 {
			filter = fmt.Sprintf(filter, asked.Reference)
		}
		var parsed contracts.ThreadFilter
		if err := jsonx.Unmarshal([]byte(filter), &parsed); err != nil {
			return nil, err
		}
		made.Summaries = append(made.Summaries, application.ThreadSummaries(state, parsed, asked.At))
	}
	if found := application.FindThread(state, asked.Reference); found != nil {
		made.Find = &found.ID
	}
	patches, err := application.Patches(viewedState, state)
	if err != nil {
		return nil, err
	}
	made.Patches = patches
	viewedState, viewedMobile = state, view
	return made, nil
}

// jsonValue encodes a nil result as null rather than leaving it out.
type jsonValue struct{ value any }

func (v jsonValue) MarshalJSONTo(enc *jsontext.Encoder) error {
	if v.value == nil || reflect.ValueOf(v.value).Kind() == reflect.Pointer && reflect.ValueOf(v.value).IsNil() {
		return enc.WriteToken(jsontext.Null)
	}
	return json.MarshalEncode(enc, v.value, jsonx.Options())
}

func arg[T any](args []jsontext.Value, index int) (T, error) {
	var value T
	if index >= len(args) || len(args[index]) == 0 || string(args[index]) == "null" {
		return value, nil
	}
	return value, jsonx.Unmarshal(args[index], &value)
}

// call runs the projection the TypeScript function `name` makes of state with args.
func call(state *application.WorkspaceState, name string, args []jsontext.Value) (any, error) {
	text := func(index int) string { value, _ := arg[string](args, index); return value }
	number := func(index int) float64 { value, _ := arg[float64](args, index); return value }
	ids := func(threads []*domain.Thread) []string {
		out := make([]string, len(threads))
		for i, thread := range threads {
			out[i] = thread.ID
		}
		return out
	}
	switch name {
	case "projectMobileView":
		return application.ProjectMobileView(state, number(0)), nil
	case "mobileArchivedThreads":
		return application.MobileArchivedThreads(state), nil
	case "mobileThreadRead":
		return application.MobileThreadRead(state, text(0)), nil
	case "mobileThreadHistory":
		return application.MobileThreadHistory(state, text(0), int(number(1))), nil
	case "threadLists":
		lists := application.ThreadActivityThreadLists(state)
		return threadLists{
			Listed: ids(lists.ListedThreads), Visible: ids(lists.VisibleThreads), Archived: ids(lists.ArchivedThreads),
			WorktreeThreadIDs: lists.WorktreeThreadIDs, SideChatAttention: lists.SideChatAttention, UnreadCount: lists.UnreadCount,
		}, nil
	case "remoteUnreadCount":
		return application.RemoteUnreadCount(&state.Computers), nil
	case "threadSummaries":
		filter, err := arg[contracts.ThreadFilter](args, 0)
		if err != nil {
			return nil, err
		}
		return application.ThreadSummaries(state, filter, number(1)), nil
	case "threadTranscript":
		limit, textLimit := 30, 2_000
		if len(args) > 1 && string(args[1]) != "null" {
			limit = int(number(1))
		}
		if len(args) > 2 && string(args[2]) != "null" {
			textLimit = int(number(2))
		}
		var end *int
		if len(args) > 3 && string(args[3]) != "null" {
			at := int(number(3))
			end = &at
		}
		return application.ThreadTranscript(state, text(0), limit, textLimit, end), nil
	case "threadWaitResult":
		timedOut, _ := arg[bool](args, 1)
		return application.ThreadWaitResult(state, text(0), timedOut), nil
	case "findThread":
		if found := application.FindThread(state, text(0)); found != nil {
			return found.ID, nil
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown call")
}

// validators by the name of the TypeScript function each ports.
var validators = map[string]func(any) bool{
	"isMobileClientMessage":      contracts.IsMobileClientMessage,
	"isMobileQuery":              contracts.IsMobileQuery,
	"isMobileRequest":            contracts.IsMobileRequest,
	"isMobileResponse":           contracts.IsMobileResponse,
	"isComputerClientRequest":    contracts.IsComputerClientRequest,
	"isComputerQuery":            contracts.IsComputerQuery,
	"isComputerServerMessage":    contracts.IsComputerServerMessage,
	"isThreadRequest":            contracts.IsThreadRequest,
	"isThreadResponse":           contracts.IsThreadResponse,
	"isRunCommand":               contracts.IsRunCommand,
	"isInternalRunCommand":       contracts.IsInternalRunCommand,
	"isAutomationRequest":        contracts.IsAutomationRequest,
	"isAutomationResponse":       contracts.IsAutomationResponse,
	"isAutomationAck":            contracts.IsAutomationAck,
	"isRunEvent":                 contracts.IsRunEvent,
	"isBackgroundEvent":          contracts.IsBackgroundEvent,
	"isSubagentEvent":            contracts.IsSubagentEvent,
	"isGoalEvent":                contracts.IsGoalEvent,
	"isWorkflowEvent":            contracts.IsWorkflowEvent,
	"isBrowserRead":              contracts.IsBrowserRead,
	"isSimulatorRead":            contracts.IsSimulatorRead,
	"isTerminalRead":             contracts.IsTerminalRead,
	"isFindingReport":            contracts.IsFindingReport,
	"isSessionResourceMessage":   contracts.IsSessionResourceMessage,
	"isTerminateSessionCommand":  contracts.IsTerminateSessionCommand,
	"isResourceStoppedEvent":     contracts.IsResourceStoppedEvent,
	"isThreadSummary":            contracts.IsThreadSummary,
	"isThreadTranscript":         contracts.IsThreadTranscript,
	"isGlassRegions":             contracts.IsGlassRegions,
	"isWindowTheme":              contracts.IsWindowTheme,
	"isAgentSettingsReloadEvent": contracts.IsAgentSettingsReloadEvent,
	"isTerminalOutputRead":       contracts.IsTerminalOutputRead,
	"isBrowserAction":            contracts.IsBrowserAction,
	"isBrowserBounds":            contracts.IsBrowserBounds,
	"isFindTarget":               contracts.IsFindTarget,
	"isWorkspaceViewInput":       contracts.IsWorkspaceViewInput,
	"isExternalCommand":          contracts.IsExternalCommand,
	"isPhoneCommand":             contracts.IsPhoneCommand,
}
