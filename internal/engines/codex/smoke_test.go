package codex

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

// Runs against the Codex installed on this machine. Spends no model tokens: a goal read needs no turn.
func TestSmokeAgainstInstalledCodex(t *testing.T) {
	if os.Getenv("CODEX_SMOKE") == "" {
		t.Skip("set CODEX_SMOKE=1 to run against the installed Codex")
	}
	held := engineBinaryPath
	engineBinaryPath = agent.EngineBinaryPath
	defer func() { engineBinaryPath = held }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	readiness := ReadCodexReadiness(ctx, nil)
	t.Logf("readiness: %s", encodedOf(readiness))
	if readiness.Access != domain.EngineAccessReady {
		t.Fatalf("Codex is not ready: %s", readiness.Access)
	}
	commands, err := DiscoverCodexCommands(ctx, t.TempDir(), nil, 0)
	must(t, err)
	t.Logf("%d skills", len(commands))
	t.Logf("plan usage: %s", encodedOf(ReadCodexPlanUsage(ctx, nil, 0)))

	provider := NewCodexAgentProvider(CodexProviderOptions{})
	defer provider.CloseAll()
	events := &recorder{}
	goals := &goalRecorder{}
	result, err := provider.Execute(input(prompt("/goal"), emitting(events), func(in *agentInput) {
		in.WorkspaceRoot = t.TempDir()
		in.ReportGoal = goals.report
	}))
	must(t, err)
	t.Logf("goal read: %+v, events %v, goals %s", result, events.shapes(), encodedOf(goals.all()))
	if result.Status != domain.RunStatusSucceeded || len(events.ofType("continuation")) != 1 {
		t.Fatalf("result = %+v", result)
	}
}
