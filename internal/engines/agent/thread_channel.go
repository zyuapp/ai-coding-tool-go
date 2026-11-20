package agent

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"strings"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const (
	threadRequestTimeout = 10 * time.Second
	// Deleting a worktree commits its loose work and removes the folder, which a large checkout drags out.
	worktreeDeleteTimeout = 10 * time.Minute
	// Typing a long string or reading a busy screen takes the device a while.
	simulatorCommandTimeout = 120 * time.Second
	// A wait is answered by the window when the thread settles, so it outlasts an ordinary request.
	waitSlack = 10 * time.Second
)

// Turns the agent side's one-way post into request/response bridges scoped per thread.
type ThreadChannel struct {
	post          func(request contracts.ThreadRequest) error
	timeout       time.Duration
	remoteTimeout time.Duration
	requests      *requestChannel
}

// Zero durations mean the defaults (10s, and contracts.RemoteThreadReadTimeoutMs for remote reads).
func NewThreadChannel(post func(request contracts.ThreadRequest) error, timeout, remoteTimeout time.Duration) *ThreadChannel {
	if timeout <= 0 {
		timeout = threadRequestTimeout
	}
	if remoteTimeout <= 0 {
		remoteTimeout = contracts.RemoteThreadReadTimeoutMs * time.Millisecond
	}
	return &ThreadChannel{post: post, timeout: timeout, remoteTimeout: remoteTimeout, requests: newRequestChannel(func(op string, ms int64) string {
		return fmt.Sprintf("AICodingTool did not answer the thread \"%s\" request within %dms.", op, ms)
	})}
}

func (channel *ThreadChannel) request(ctx context.Context, op string, timeout time.Duration, build func(requestID string) contracts.ThreadRequest) (jsontext.Value, error) {
	return channel.requests.request(ctx, op, timeout, func(requestID string) error {
		return channel.post(build(requestID))
	})
}

func (channel *ThreadChannel) Settle(response contracts.ThreadResponse) bool {
	switch response := response.(type) {
	case contracts.ThreadResponseTrue:
		return channel.requests.settle(response.RequestID, true, response.Result, "")
	case contracts.ThreadResponseFalse:
		return channel.requests.settle(response.RequestID, false, nil, response.Message)
	}
	return false
}

func milliseconds(ms float64) time.Duration { return time.Duration(ms * float64(time.Millisecond)) }

type threadBridge struct {
	channel *ThreadChannel
	taskID  string
}

func (channel *ThreadChannel) BridgeFor(taskID string) ThreadBridge {
	return &threadBridge{channel: channel, taskID: taskID}
}

func (bridge *threadBridge) List(ctx context.Context, query contracts.ThreadListQuery) ([]*contracts.ThreadSummary, error) {
	timeout := bridge.channel.timeout
	if query.Computer != nil && *query.Computer != "" && *query.Computer != "this" {
		timeout = bridge.channel.remoteTimeout
	}
	return decodeResult[[]*contracts.ThreadSummary](bridge.channel.request(ctx, "list", timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestList{RequestID: requestID, TaskID: bridge.taskID, Computer: query.Computer, Project: query.Project, Archived: query.Archived, IdleForMs: query.IdleForMs, Search: query.Search, Attachments: query.Attachments, Limit: query.Limit}
	}))
}

func (bridge *threadBridge) Read(ctx context.Context, threadID string, limit *float64, computer *string) (contracts.ThreadTranscript, error) {
	timeout := bridge.channel.remoteTimeout
	if computer != nil && *computer == "this" {
		timeout = bridge.channel.timeout
	}
	return decodeResult[contracts.ThreadTranscript](bridge.channel.request(ctx, "read", timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestRead{RequestID: requestID, TaskID: bridge.taskID, ThreadID: threadID, Limit: limit, Computer: computer}
	}))
}

func (bridge *threadBridge) Wait(ctx context.Context, threadID string, timeoutMs float64) (contracts.ThreadWaitResult, error) {
	return decodeResult[contracts.ThreadWaitResult](bridge.channel.request(ctx, "wait", milliseconds(timeoutMs)+waitSlack, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestWait{RequestID: requestID, TaskID: bridge.taskID, ThreadID: threadID, TimeoutMs: timeoutMs}
	}))
}

func (bridge *threadBridge) Command(ctx context.Context, command contracts.ExternalCommand, computer *string) (contracts.ThreadCommandResult, error) {
	kind := CommandType(command)
	timeout := bridge.channel.remoteTimeout
	if kind == "worktree.delete" {
		timeout = worktreeDeleteTimeout
	} else if strings.HasPrefix(kind, "browser.") {
		timeout = bridge.channel.timeout
	}
	return decodeResult[contracts.ThreadCommandResult](bridge.channel.request(ctx, "command", timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestCommand{RequestID: requestID, TaskID: bridge.taskID, Command: command, Computer: computer}
	}))
}

// The discriminator of a command, read the way it travels.
func CommandType(command any) string {
	encoded, err := jsonx.Marshal(command)
	if err != nil {
		return ""
	}
	var header struct {
		Type string `json:"type"`
	}
	_ = jsonx.Unmarshal(encoded, &header)
	return header.Type
}

// A write stamped with the thread it acts for, as the external command it travels as.
func stampedCommand(write any, taskID string) (contracts.ExternalCommand, error) {
	encoded, err := jsonx.Marshal(write)
	if err != nil {
		return nil, err
	}
	var fields map[string]jsontext.Value
	if err := jsonx.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	fields["taskId"], _ = jsonx.Marshal(taskID)
	if encoded, err = jsonx.Marshal(fields); err != nil {
		return nil, err
	}
	var command contracts.ExternalCommand
	if err := jsonx.Unmarshal(encoded, &command); err != nil {
		return nil, err
	}
	return command, nil
}

type findingBridge struct {
	channel *ThreadChannel
	taskID  string
}

// Scoped the same way: a run reports what it found on the thread it is running as.
func (channel *ThreadChannel) FindingsFor(taskID string) FindingBridge {
	return &findingBridge{channel: channel, taskID: taskID}
}

func (bridge *findingBridge) Notify(ctx context.Context, report contracts.FindingReport) (contracts.FindingResult, error) {
	return decodeResult[contracts.FindingResult](bridge.channel.request(ctx, "notify", bridge.channel.timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestNotify{RequestID: requestID, TaskID: bridge.taskID, Report: report}
	}))
}

func (bridge *findingBridge) NothingToReport(ctx context.Context, checked string) (contracts.FindingResult, error) {
	return decodeResult[contracts.FindingResult](bridge.channel.request(ctx, "nothing-to-report", bridge.channel.timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestNothingToReport{RequestID: requestID, TaskID: bridge.taskID, Checked: checked}
	}))
}

type coordinationBridge struct {
	channel *ThreadChannel
	taskID  string
}

// Scoped the same way: a thread only ever speaks for itself to its coordinator and the user.
func (channel *ThreadChannel) CoordinationFor(taskID string) CoordinationBridge {
	return &coordinationBridge{channel: channel, taskID: taskID}
}

func (bridge *coordinationBridge) Report(ctx context.Context, state domain.CoordinationState, summary string) (contracts.FindingResult, error) {
	return decodeResult[contracts.FindingResult](bridge.channel.request(ctx, "report", bridge.channel.timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestReport{RequestID: requestID, TaskID: bridge.taskID, State: state, Summary: summary}
	}))
}

func (bridge *coordinationBridge) Decide(ctx context.Context, request domain.DecisionRequest) (contracts.FindingResult, error) {
	return decodeResult[contracts.FindingResult](bridge.channel.request(ctx, "decision", bridge.channel.timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestDecision{RequestID: requestID, TaskID: bridge.taskID, Request: request}
	}))
}

func (bridge *coordinationBridge) Remember(ctx context.Context, change contracts.ThreadRequestMemoryChange) (MemoryNote, error) {
	return decodeResult[MemoryNote](bridge.channel.request(ctx, "memory", bridge.channel.timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestMemory{RequestID: requestID, TaskID: bridge.taskID, Change: change}
	}))
}

func (bridge *coordinationBridge) Propose(ctx context.Context, proposals []*domain.ProposalRequest) (MemoryNote, error) {
	return decodeResult[MemoryNote](bridge.channel.request(ctx, "propose", bridge.channel.timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestPropose{RequestID: requestID, TaskID: bridge.taskID, Proposals: proposals}
	}))
}

type browserBridge struct {
	channel *ThreadChannel
	taskID  string
}

// The thread is stamped on here, so no browser tool can drive the panel as anything but itself.
func (channel *ThreadChannel) BrowserFor(taskID string) BrowserBridge {
	return &browserBridge{channel: channel, taskID: taskID}
}

func (bridge *browserBridge) Command(ctx context.Context, write contracts.BrowserWrite) error {
	command, err := stampedCommand(write, bridge.taskID)
	if err != nil {
		return err
	}
	_, err = bridge.channel.request(ctx, "command", bridge.channel.timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestCommand{RequestID: requestID, TaskID: bridge.taskID, Command: command}
	})
	return err
}

func (bridge *browserBridge) Read(ctx context.Context, read contracts.BrowserRead) (contracts.BrowserReadResult, error) {
	timeout := bridge.channel.timeout
	switch read := read.(type) {
	case contracts.BrowserReadSnapshot:
		timeout = milliseconds(read.TimeoutMs) + waitSlack
	case contracts.BrowserReadScreenshot:
		timeout = milliseconds(read.TimeoutMs) + waitSlack
	case contracts.BrowserReadWait:
		timeout = milliseconds(read.TimeoutMs) + waitSlack
	}
	return decodeResult[contracts.BrowserReadResult](bridge.channel.request(ctx, "browser", timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestBrowser{RequestID: requestID, TaskID: bridge.taskID, Read: read}
	}))
}

type simulatorBridge struct {
	channel *ThreadChannel
	taskID  string
}

// The thread is stamped on here, so a run only ever drives the device its own dock shows.
func (channel *ThreadChannel) SimulatorFor(taskID string) SimulatorBridge {
	return &simulatorBridge{channel: channel, taskID: taskID}
}

func (bridge *simulatorBridge) Command(ctx context.Context, write contracts.SimulatorWrite) error {
	command, err := stampedCommand(write, bridge.taskID)
	if err != nil {
		return err
	}
	_, err = bridge.channel.request(ctx, "command", simulatorCommandTimeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestCommand{RequestID: requestID, TaskID: bridge.taskID, Command: command}
	})
	return err
}

func (bridge *simulatorBridge) Read(ctx context.Context, read contracts.SimulatorRead) (contracts.SimulatorReadResult, error) {
	timeout := simulatorCommandTimeout
	if status, ok := read.(contracts.SimulatorReadStatus); ok {
		timeout = milliseconds(status.TimeoutMs) + waitSlack
	}
	return decodeResult[contracts.SimulatorReadResult](bridge.channel.request(ctx, "simulator", timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestSimulator{RequestID: requestID, TaskID: bridge.taskID, Read: read}
	}))
}

type terminalBridge struct {
	channel *ThreadChannel
	taskID  string
}

// The thread is stamped on here, so an unnamed read resolves to that thread's own terminal.
func (channel *ThreadChannel) TerminalFor(taskID string) TerminalBridge {
	return &terminalBridge{channel: channel, taskID: taskID}
}

func (bridge *terminalBridge) Read(ctx context.Context, read contracts.TerminalRead) (contracts.TerminalReadResult, error) {
	return decodeResult[contracts.TerminalReadResult](bridge.channel.request(ctx, "terminal", bridge.channel.timeout, func(requestID string) contracts.ThreadRequest {
		return contracts.ThreadRequestTerminal{RequestID: requestID, TaskID: bridge.taskID, Read: read}
	}))
}
