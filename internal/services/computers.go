package services

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/computeruse"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
	"github.com/zyuapp/ai-coding-tool-go/internal/mobile"
)

// WorkspaceHooks hands a paired computer this workspace whole, driven by the window's own inputs and
// read the way the window reads it (computer-bridge.ts).
func (s *Services) WorkspaceHooks(publisher *host.Publisher, name func() string) mobile.WorkspaceHooks {
	return computeruse.CreateComputerBridge(computeruse.BridgeHost{Publisher: publisher, Reads: s.Reads, Name: name})
}
