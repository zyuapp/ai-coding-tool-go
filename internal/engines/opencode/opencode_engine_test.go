package opencode

import (
	"context"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

// A session no engine here opens, standing in for another engine's in the shared pool.
type foreignSession struct {
	hooks agent.SessionHooks
	live  bool
}

func (s *foreignSession) Key() string            { return "foreign" }
func (s *foreignSession) Live() bool             { return s.live }
func (s *foreignSession) Busy() bool             { return false }
func (s *foreignSession) Answering() bool        { return false }
func (s *foreignSession) Continues(*string) bool { return true }
func (s *foreignSession) Close() <-chan struct{} {
	s.live = false
	go s.hooks.Ended()
	return agent.Settled()
}
func (s *foreignSession) Run(*agent.ProviderRunInput) (agent.ProviderResult, error) {
	return agent.ProviderResult{Status: domain.RunStatusSucceeded}, nil
}

func TestEngineDescribesItsCommand(t *testing.T) {
	binary := NewEngine("").Binary()
	for _, field := range []string{binary.Command, binary.PackageName, binary.Install} {
		if strings.TrimSpace(field) == "" {
			t.Fatal("empty binary field")
		}
	}
	if binary.IsNative("/nowhere/bin/unrelated") || !binary.IsNative("/Users/me/.opencode/bin/opencode") {
		t.Fatal("IsNative")
	}
}

func TestProviderLeavesAloneWhatItDoesNotOwnAndClosesSafelyWhileIdle(t *testing.T) {
	pool := agent.NewSessionPool(nil, 0)
	built, err := NewEngine("").CreateProvider(context.Background(), agent.ProviderContext{Host: fakeHost{}, Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(func(input *agent.ProviderRunInput) { input.TaskID = "theirs" })
	if _, err := pool.Execute(r.input, "foreign", agent.SessionOpener{Open: func(hooks agent.SessionHooks) agent.PooledSession {
		return &foreignSession{hooks: hooks, live: true}
	}}); err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"theirs", "nobody"} {
		if built.StopProcess(taskID, "process-1") || built.LabelThread(taskID, "Title") || built.ArchiveThread(taskID, nil, true) {
			t.Fatalf("%s was touched", taskID)
		}
	}
	for _, owner := range []string{"claude", "codex", "gemini"} {
		if built.ArchiveThread("theirs", &domain.Continuation{Provider: owner, Value: "handle"}, true) {
			t.Fatalf("a continuation %s made is not this engine's to file", owner)
		}
	}
	if pool.LiveSession("theirs") == nil {
		t.Fatal("another engine's session is left running")
	}
	role := domain.CoordinationRoleCoordinator
	built.RetireOutOfRole("nobody", &role)
	built.CloseAll()
	built.CloseAll()
	if pool.LiveSession("theirs") != nil {
		t.Fatal("closing closes the pool the engines share")
	}
}
