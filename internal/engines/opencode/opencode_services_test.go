package opencode

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type scriptedServices struct {
	services *Services
	mu       sync.Mutex
	started  []ServerOptions
	servers  []*fakeServer
}

// Services whose servers answer each path from the first prefix it starts with, an error as a failure.
func newScriptedServices(answers [][2]any) *scriptedServices {
	scripted := &scriptedServices{}
	scripted.services = NewServices(func(ctx context.Context, options ServerOptions) (Client, error) {
		server := newFakeServer(options)
		server.answer = func(method, path string) (any, error, bool) {
			for _, entry := range answers {
				if strings.HasPrefix(path, entry[0].(string)) {
					if err, ok := entry[1].(error); ok {
						return nil, err, true
					}
					return entry[1], nil, true
				}
			}
			return nil, nil, true
		}
		scripted.mu.Lock()
		scripted.started = append(scripted.started, options)
		scripted.servers = append(scripted.servers, server)
		scripted.mu.Unlock()
		return server, nil
	}, "")
	scripted.services.loadingRetry = time.Millisecond
	return scripted
}

func (s *scriptedServices) startedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.started)
}

func (s *scriptedServices) closedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	closed := 0
	for _, server := range s.servers {
		if server.isClosed() {
			closed++
		}
	}
	return closed
}

func TestServicesOneServerAnswersEveryQuestionUntilClosed(t *testing.T) {
	scripted := newScriptedServices([][2]any{
		{"/api/model/default", map[string]string{"id": "glm-5.3", "providerID": "opencode-go"}},
		{"/api/model", []any{map[string]string{"id": "z-ai/glm-5.3-flash", "providerID": "openrouter"}, map[string]string{"id": "glm-5.3", "providerID": "opencode-go"}}},
		{"/api/provider", []any{map[string]string{"id": "openrouter", "name": "OpenRouter"}, map[string]string{"id": "opencode-go", "name": "OpenCode Go"}}},
	})
	ctx := context.Background()
	catalog, err := scripted.services.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got [][2]string
	for _, spec := range catalog {
		got = append(got, [2]string{spec.ID, *spec.Provider})
	}
	sameJSON(t, got, `[["opencode-go/glm-5.3","OpenCode Go"],["openrouter/z-ai/glm-5.3-flash","OpenRouter"]]`)
	if _, err := scripted.services.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	if scripted.startedCount() != 1 {
		t.Fatalf("started %d", scripted.startedCount())
	}
	scripted.services.Close()
	if scripted.closedCount() != 1 {
		t.Fatalf("closed %d", scripted.closedCount())
	}
}

func TestServicesCommandsAreOpenCodesOwnAndItsSkillsEachNamedOnce(t *testing.T) {
	scripted := newScriptedServices([][2]any{
		{"/api/command", []any{map[string]string{"name": "init", "description": "guided AGENTS.md setup"}, map[string]string{"name": "review"}}},
		{"/api/skill", []any{map[string]string{"id": "aicodingtool:review-thread", "description": "Review loop"}, map[string]string{"id": "review", "description": "dup"}}},
	})
	defer scripted.services.Close()
	commands, err := scripted.services.Commands(context.Background(), "/repo with space")
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, commands, `[
		{"name":"init","description":"guided AGENTS.md setup","argumentHint":""},
		{"name":"review","description":"dup","argumentHint":""},
		{"name":"review-thread","description":"Review loop","argumentHint":""}
	]`)
	for _, c := range scripted.servers[0].allCalls() {
		if !strings.HasSuffix(c.path, "location[directory]=%2Frepo%20with%20space") {
			t.Fatalf("every list is read in the workspace: %s", c.path)
		}
	}
}

func TestServicesTitleIsWrittenOnTheThreadsOwnModel(t *testing.T) {
	scripted := newScriptedServices([][2]any{{"/api/experimental/generate/text", map[string]string{"text": `"Fix flaky login test."`}}})
	defer scripted.services.Close()
	ctx := context.Background()
	model := domain.AgentModel("openrouter/z-ai/glm-5.3-flash")
	if title := scripted.services.Title(ctx, "The login test fails half the time", &model); title == nil || *title != "Fix flaky login test" {
		t.Fatalf("title %v", title)
	}
	asked := scripted.servers[0].allCalls()
	sameJSON(t, bodyOf(t, asked[0])["model"], `{"providerID":"openrouter","id":"z-ai/glm-5.3-flash"}`)
	opus := domain.AgentModel("opus")
	if scripted.services.Title(ctx, "hello", &opus) != nil || scripted.services.Title(ctx, "hello", nil) != nil {
		t.Fatal("a title for a model OpenCode cannot name")
	}
	if got := len(scripted.servers[0].allCalls()); got != 1 {
		t.Fatalf("asked %d", got)
	}
	failing := newScriptedServices([][2]any{{"/api/experimental/generate/text", errors.New("timeout")}})
	defer failing.services.Close()
	if failing.services.Title(ctx, "hello", &model) != nil {
		t.Fatal("a title that cannot be written leaves the typed one")
	}
}

func TestServicesPlanUsageDrawsOpenCodeGoAsWindows(t *testing.T) {
	at := float64(time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC).UnixMilli())
	meter := func(percent float64) map[string]any {
		return map[string]any{"percent": percent, "limited": false, "resetsAt": at}
	}
	scripted := newScriptedServices([][2]any{{"/api/experimental/usage/limits", map[string]any{"go": map[string]any{"rolling": meter(12), "weekly": meter(40), "monthly": meter(9)}, "budgets": []any{}}}})
	defer scripted.services.Close()
	usage, err := scripted.services.PlanUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	iso := "2026-11-05T18:00:00.000Z"
	sameJSON(t, usage, `{"status":"available","subscription":"Go","windows":[
		{"id":"rolling","label":"Rolling","utilization":12,"resetsAt":"`+iso+`","period":"session"},
		{"id":"weekly","label":"Weekly","utilization":40,"resetsAt":"`+iso+`","period":"week"},
		{"id":"monthly","label":"Monthly","utilization":9,"resetsAt":"`+iso+`"}
	]}`)
	none := newScriptedServices([][2]any{{"/api/experimental/usage/limits", nil}})
	defer none.services.Close()
	usage, err = none.services.PlanUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, usage, `{"status":"not-applicable"}`)
}

func TestServicesLetAFailingServerGo(t *testing.T) {
	scripted := newScriptedServices([][2]any{{"/api/model", errors.New("server gone")}})
	defer scripted.services.Close()
	for range 2 {
		if _, err := scripted.services.Catalog(context.Background()); err == nil || !regexp.MustCompile(`server gone`).MatchString(err.Error()) {
			t.Fatalf("error %v", err)
		}
	}
	if scripted.startedCount() != 2 {
		t.Fatalf("started %d", scripted.startedCount())
	}
}

func TestServicesStopOnceIdle(t *testing.T) {
	scripted := newScriptedServices([][2]any{{"/api/model", []any{}}})
	scripted.services.idle = 10 * time.Millisecond
	if _, err := scripted.services.Catalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool { return scripted.closedCount() == 1 }, "the idle server stopping")
	if _, err := scripted.services.Catalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scripted.startedCount() != 2 {
		t.Fatalf("started %d", scripted.startedCount())
	}
	scripted.services.Close()
}
