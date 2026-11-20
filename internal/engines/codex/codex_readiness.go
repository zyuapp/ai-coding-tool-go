package codex

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

// Codex speaks the protocol its own version generated, and the app holds one generated copy of it, so
// a command older than that copy is turned away rather than asked to answer messages it lacks.
func ReadCodexReadiness(ctx context.Context, connect CodexConnect) domain.EngineReadiness {
	installed, ok := agent.InstalledEngineOf(ctx, Binary)
	if !ok {
		return domain.EngineReadiness{Access: domain.EngineAccessMissing, Fix: ptr(Binary.Install)}
	}
	if domain.IsOlderThan(installed.Version, protocol.Version) {
		readiness := agent.VersionReadiness(ctx, Binary, installed, protocol.Version)
		readiness.Access = domain.EngineAccessOutdated
		return readiness
	}
	versions := make(chan domain.EngineReadiness, 1)
	go func() { versions <- agent.VersionReadiness(ctx, Binary, installed, protocol.Version) }()
	access := ReadCodexAccess(ctx, connect)
	readiness := <-versions
	readiness.Access = access
	return readiness
}
