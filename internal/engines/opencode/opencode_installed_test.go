package opencode

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

// The OpenCode on this machine, when it is one the app speaks to.
func installedOpenCode(t *testing.T) string {
	t.Helper()
	installed, ok := agent.InstalledEngineOf(context.Background(), Binary)
	if !ok || installed.Version == nil || domain.IsOlderThan(installed.Version, BaselineVersion) {
		t.Skip("no OpenCode this app speaks to is installed")
	}
	return *installed.Version
}

func TestInstalledOpenCodeStartsAnswersAndStops(t *testing.T) {
	version := installedOpenCode(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	server, err := StartServer(ctx, ServerOptions{Cwd: os.TempDir()})
	if err != nil {
		t.Fatalf("OpenCode %s: %v", version, err)
	}
	models, err := requestAs[[]*Model](ctx, server, "GET", "/api/model", nil, 0)
	server.Close()
	if err != nil {
		t.Fatalf("the server did not answer behind the password it made up: %v", err)
	}
	if models == nil {
		t.Log("no models listed")
	}
	select {
	case <-server.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("the server is still up once closed")
	}
	if server.Exit() == nil {
		t.Fatal("no exit status")
	}
}

func TestInstalledOpenCodeServicesSmoke(t *testing.T) {
	if os.Getenv("OPENCODE_SMOKE") == "" {
		t.Skip("set OPENCODE_SMOKE=1 to read the installed OpenCode's catalog, commands and readiness")
	}
	installedOpenCode(t)
	services := NewServices(nil, "")
	defer services.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	readiness, err := services.Readiness(ctx)
	if err != nil || readiness.Access != domain.EngineAccessReady {
		t.Fatalf("readiness %+v %v", readiness, err)
	}
	t.Logf("version %v, %d models, first %s", *readiness.Version, len(readiness.Catalog), readiness.Catalog[0].ID)
	commands, err := services.Commands(ctx, os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d commands", len(commands))
	usage, err := services.PlanUsage(ctx)
	t.Logf("plan usage %+v %v", usage, err)
}

func TestInstalledOpenCodeTurnSmoke(t *testing.T) {
	if os.Getenv("OPENCODE_SMOKE_TURN") == "" {
		t.Skip("set OPENCODE_SMOKE_TURN=1 to run one turn on the installed OpenCode's default model")
	}
	installedOpenCode(t)
	services := NewServices(nil, "")
	catalog, err := services.Catalog(context.Background())
	services.Close()
	if err != nil || len(catalog) == 0 {
		t.Fatalf("catalog %v %v", catalog, err)
	}
	provider := NewProvider(ProviderOptions{Host: fakeHost{}})
	defer provider.CloseAll()
	r := newRun(func(input *agent.ProviderRunInput) {
		input.Model = catalog[0].ID
		input.WorkspaceRoot = t.TempDir()
	})
	result, err := provider.Execute(r.input)
	if err != nil || result.Status != domain.RunStatusSucceeded {
		t.Fatalf("result %+v %v", result, err)
	}
	text := ""
	for _, event := range r.all() {
		if event, ok := event.(agent.EventAssistant); ok {
			text += event.Text
		}
	}
	t.Logf("%s answered %q; events %d", catalog[0].ID, text, len(r.all()))
	if !provider.LabelThread("task-1", "Smoke") {
		t.Fatal("no warm session to label")
	}
}
