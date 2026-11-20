package claude

import (
	"context"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

// readClaudeReadiness says whether Claude Code can take a run. Claude Code names the models it can
// run, so a command behind the app loses the models it never heard of rather than being turned away.
// The hint carries the upgrade command so the loss is not silent.
func readClaudeReadiness(ctx context.Context, open QueryFactory) domain.EngineReadiness {
	installed, ok := agent.InstalledEngineOf(ctx, claudeBinary)
	if !ok {
		fix := claudeBinary.Install
		return domain.EngineReadiness{Access: domain.EngineAccessMissing, Fix: &fix}
	}
	var (
		wait    sync.WaitGroup
		models  []domain.AgentModel
		version domain.EngineReadiness
	)
	wait.Go(func() { models = discoverClaudeModels(ctx, open) })
	wait.Go(func() { version = agent.VersionReadiness(ctx, claudeBinary, installed, BaselineVersion) })
	wait.Wait()
	version.Access = domain.EngineAccessReady
	version.Models = models
	return version
}
