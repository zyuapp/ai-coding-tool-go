package services

import (
	"context"
	"errors"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/computeruse"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
	"github.com/zyuapp/ai-coding-tool-go/internal/mobile"
)

// WorkspaceHooks hands a paired computer this workspace whole, driven by the window's own inputs and
// read the way the window reads it (computer-bridge.ts).
func (s *Services) WorkspaceHooks(publisher *host.Publisher, name func() string) mobile.WorkspaceHooks {
	return workspaceHooks{computeruse.CreateComputerBridge(computeruse.BridgeHost{Publisher: publisher, Reads: s.Reads, Name: name})}
}

type workspaceHooks struct{ hooks computeruse.WorkspaceHooks }

func (h workspaceHooks) Name() string                          { return h.hooks.Name() }
func (h workspaceHooks) Snapshot() application.WorkspaceUpdate { return h.hooks.Snapshot() }

func (h workspaceHooks) Subscribe(listener func(application.WorkspaceUpdate)) func() {
	return h.hooks.Subscribe(listener)
}

func (h workspaceHooks) Input(ctx context.Context, inputs []application.WorkspaceInput) (contracts.ComputerServerMessageResultResult, error) {
	if result, ok := h.hooks.Input(ctx, inputs).(contracts.ComputerServerMessageResultResult); ok {
		return result, nil
	}
	return nil, errors.New("The workspace runtime gave no answer.")
}

func (h workspaceHooks) Query(ctx context.Context, query contracts.ComputerQuery) (any, error) {
	return h.hooks.Query(ctx, query)
}
