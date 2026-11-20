package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const automationRequestTimeout = 10 * time.Second

// Turns the agent side's one-way post into request/response bridges scoped per thread.
type AutomationChannel struct {
	post     func(request contracts.AutomationRequest) error
	timeout  time.Duration
	requests *requestChannel
}

// timeout <= 0 means 10s.
func NewAutomationChannel(post func(request contracts.AutomationRequest) error, timeout time.Duration) *AutomationChannel {
	if timeout <= 0 {
		timeout = automationRequestTimeout
	}
	return &AutomationChannel{post: post, timeout: timeout, requests: newRequestChannel(func(op string, ms int64) string {
		return fmt.Sprintf("AICodingTool did not answer the automation \"%s\" request within %dms.", op, ms)
	})}
}

type automationBridge struct {
	channel      *AutomationChannel
	taskID       string
	currentRunID func() (string, error)
}

func (channel *AutomationChannel) BridgeFor(taskID string, currentRunID func() (string, error)) AutomationBridge {
	return &automationBridge{channel: channel, taskID: taskID, currentRunID: currentRunID}
}

func (bridge *automationBridge) send(ctx context.Context, op string, build func(requestID string) contracts.AutomationRequest) ([]byte, error) {
	return bridge.channel.requests.request(ctx, op, bridge.channel.timeout, func(requestID string) error {
		return bridge.channel.post(build(requestID))
	})
}

func (bridge *automationBridge) Read(ctx context.Context) (*domain.AutomationView, error) {
	return decodeResult[*domain.AutomationView](bridge.send(ctx, "read", func(requestID string) contracts.AutomationRequest {
		return contracts.AutomationRequestRead{RequestID: requestID, TaskID: bridge.taskID}
	}))
}

func (bridge *automationBridge) List(ctx context.Context) ([]*domain.AutomationView, error) {
	return decodeResult[[]*domain.AutomationView](bridge.send(ctx, "list", func(requestID string) contracts.AutomationRequest {
		return contracts.AutomationRequestList{RequestID: requestID, TaskID: bridge.taskID}
	}))
}

func (bridge *automationBridge) Save(ctx context.Context, draft contracts.AutomationRequestSaveDraft) (*domain.AutomationView, error) {
	runID, err := bridge.currentRunID()
	if err != nil {
		return nil, err
	}
	return decodeResult[*domain.AutomationView](bridge.send(ctx, "save", func(requestID string) contracts.AutomationRequest {
		return contracts.AutomationRequestSave{RequestID: requestID, TaskID: bridge.taskID, RunID: runID, Draft: draft}
	}))
}

func (bridge *automationBridge) Update(ctx context.Context, patch domain.AutomationPatch) (*domain.AutomationView, error) {
	runID, err := bridge.currentRunID()
	if err != nil {
		return nil, err
	}
	return decodeResult[*domain.AutomationView](bridge.send(ctx, "update", func(requestID string) contracts.AutomationRequest {
		return contracts.AutomationRequestUpdate{RequestID: requestID, TaskID: bridge.taskID, RunID: runID, Patch: patch}
	}))
}

func (bridge *automationBridge) Remove(ctx context.Context) (bool, error) {
	return decodeResult[bool](bridge.send(ctx, "delete", func(requestID string) contracts.AutomationRequest {
		return contracts.AutomationRequestDelete{RequestID: requestID, TaskID: bridge.taskID}
	}))
}

func (channel *AutomationChannel) Settle(response contracts.AutomationResponse) bool {
	switch response := response.(type) {
	case contracts.AutomationResponseTrue:
		return channel.requests.settle(response.RequestID, true, response.Result, "")
	case contracts.AutomationResponseFalse:
		return channel.requests.settle(response.RequestID, false, nil, response.Message)
	}
	return false
}
