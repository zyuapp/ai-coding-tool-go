package claude

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

// Runs against the installed Claude Code when CLAUDE_SMOKE is set; CLAUDE_SMOKE_TURN adds one Haiku turn.
func TestSmokeInstalledClaude(t *testing.T) {
	if os.Getenv("CLAUDE_SMOKE") == "" {
		t.Skip("set CLAUDE_SMOKE=1 to run against the installed Claude Code")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	engine := NewEngine("", nil)
	readiness, _ := engine.Readiness(ctx)
	t.Logf("readiness: %+v models=%v", readiness, readiness.Models)
	commands, err := engine.Commands(ctx, agent.Workspace{WorkspaceRoot: t.TempDir()})
	t.Logf("commands: %d err=%v", len(commands), err)
	usage, _ := engine.PlanUsage(ctx)
	t.Logf("plan usage: %#v", usage)
	if os.Getenv("CLAUDE_SMOKE_TURN") == "" {
		return
	}
	r := &recorder{}
	provider := NewProvider(nil, nil, "")
	defer provider.CloseAll()
	result, err := provider.Execute(runInput(r, func(input *agent.ProviderRunInput) {
		input.Model = "haiku"
		input.WorkspaceRoot = t.TempDir()
		input.Prompt = "Reply with the single word: pomegranate"
	}))
	t.Logf("result: %+v err=%v", result, err)
	for _, event := range r.all() {
		t.Logf("event: %#v", event)
	}
	if result.Status != domain.RunStatusSucceeded {
		t.Fatal("the turn did not succeed")
	}
	session, ok := provider.Session("task-1")
	if !ok || !session.Live() {
		t.Fatal("the session stays warm after its turn")
	}
}
