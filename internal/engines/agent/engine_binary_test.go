package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var (
	codexCommand  = EngineCommand{Command: "codex", PackageName: "@openai/codex", IsNative: func(string) bool { return false }}
	claudeCommand = EngineCommand{Command: "claude", PackageName: "@anthropic-ai/claude-code", IsNative: func(path string) bool { return strings.Contains(path, "/.local/share/claude/") }}
)

func onPath(t *testing.T, folder string) {
	t.Helper()
	held := searchPath
	searchPath = func() string { return folder }
	t.Cleanup(func() { searchPath = held })
}

func script(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func realTemp(t *testing.T) string {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestEngineIsTheCommandOnThePathReadForItsVersion(t *testing.T) {
	folder := realTemp(t)
	executable := filepath.Join(folder, "codex")
	script(t, executable, "#!/bin/sh\necho \"codex-cli 0.150.1\"\n")
	onPath(t, folder)
	found, ok := InstalledEngineOf(context.Background(), codexCommand)
	equal(t, ok, true)
	equal(t, found.Path, executable)
	equal(t, *found.Version, "0.150.1")
	equal(t, found.Update.Command, "codex update", "an install the path places nowhere known is left to the engine's own updater")
}

func TestEngineNowhereOnThePathIsAbsent(t *testing.T) {
	onPath(t, t.TempDir())
	_, ok := EngineBinaryPath(claudeCommand)
	equal(t, ok, false)
	_, ok = InstalledEngineOf(context.Background(), claudeCommand)
	equal(t, ok, false)
}

func TestUpgradeFollowsTheLauncherToTheCask(t *testing.T) {
	root := realTemp(t)
	real := filepath.Join(root, "Caskroom", "codex", "0.150.1", "codex")
	script(t, real, "#!/bin/sh\necho 'codex-cli 0.150.1'\n")
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	os.Symlink(real, filepath.Join(bin, "codex"))
	onPath(t, bin)
	found, _ := InstalledEngineOf(context.Background(), codexCommand)
	equal(t, found.Update, &EngineUpdater{Command: "brew upgrade --cask codex", File: "brew", Args: []string{"upgrade", "--cask", "codex"}})
	equal(t, found.Keg, &HomebrewKeg{Kind: "cask", Name: "codex", Prefix: root})
}

func TestGlobalNpmInstallUpgradesThroughItsOwnNpm(t *testing.T) {
	root := realTemp(t)
	real := filepath.Join(root, "node", "lib", "node_modules", "@openai", "codex", "bin", "codex.js")
	script(t, real, "#!/bin/sh\necho 'codex-cli 0.150.1'\n")
	bin := filepath.Join(root, "node", "bin")
	os.MkdirAll(bin, 0o755)
	os.Symlink(real, filepath.Join(bin, "codex"))
	script(t, filepath.Join(bin, "npm"), "#!/bin/sh\n")
	prefix := filepath.Join(root, "node")
	onPath(t, bin)
	found, _ := InstalledEngineOf(context.Background(), codexCommand)
	equal(t, found.Update, &EngineUpdater{
		Command: "npm install -g @openai/codex@latest",
		File:    filepath.Join(bin, "npm"),
		Args:    []string{"install", "-g", "--prefix", prefix, "--allow-scripts=@openai/codex", "@openai/codex@latest"},
	})

	mise := filepath.Join(root, "mise", "installs", "claude", "2.1.0")
	script(t, filepath.Join(mise, "claude"), "#!/bin/sh\necho '2.1.0 (Claude Code)'\n")
	onPath(t, mise)
	managed, _ := InstalledEngineOf(context.Background(), claudeCommand)
	equal(t, *managed.Version, "2.1.0")
	equal(t, managed.Update, (*EngineUpdater)(nil))

	local := filepath.Join(root, ".local", "bin")
	script(t, filepath.Join(local, "claude"), "#!/bin/bash\nmise use -g --quiet \"claude\" || exit 1\nexec mise x \"claude\" -- \"claude\" \"$@\"\n")
	onPath(t, local)
	launched, _ := InstalledEngineOf(context.Background(), claudeCommand)
	equal(t, launched.Update, (*EngineUpdater)(nil), "a mise launcher where the native install would be is still mise's")
}

type readinessStub struct {
	readiness func() domain.EngineReadiness
	update    func()
}

type stubServices struct {
	readinessStub
}

func (s stubServices) Readiness(context.Context) (domain.EngineReadiness, error) {
	if s.readiness == nil {
		return domain.EngineReadiness{Access: domain.EngineAccessReady}, nil
	}
	return s.readiness(), nil
}
func (s stubServices) SuggestTitle(context.Context, string, []string, *domain.AgentModel) (*string, error) {
	return nil, nil
}
func (s stubServices) PlanUsage(context.Context) (domain.PlanUsage, error) { return nil, nil }

type updatingServices struct{ stubServices }

func (s updatingServices) Update(context.Context) error { s.update(); return nil }

func stubs(codex EngineServices) map[domain.AgentEngine]EngineServices {
	services := map[domain.AgentEngine]EngineServices{}
	for _, engine := range domain.AgentEngines {
		services[engine] = stubServices{}
	}
	services[domain.AgentEngineCodex] = codex
	return services
}

func TestAccessKeepsAnAllReadyAnswerAndRereadsOneMissing(t *testing.T) {
	answers := []domain.EngineReadiness{
		{Access: domain.EngineAccessMissing, Fix: ptr("brew install --cask codex")},
		{Access: domain.EngineAccessReady, Version: ptr("0.150.1")},
		{Access: domain.EngineAccessReady, Version: ptr("0.150.1")},
	}
	var mu sync.Mutex
	reads, paths := 0, 0
	host := NewEngineAccessHost(stubs(stubServices{readinessStub{readiness: func() domain.EngineReadiness {
		mu.Lock()
		defer mu.Unlock()
		reads++
		if len(answers) == 0 {
			return domain.EngineReadiness{Access: domain.EngineAccessReady}
		}
		answer := answers[0]
		answers = answers[1:]
		return answer
	}}}), func(context.Context) error { paths++; return nil })
	ctx := context.Background()
	status, _ := host.Read(ctx, false)
	equal(t, status.Codex.Access, domain.EngineAccessMissing)
	equal(t, paths, 0)
	status, _ = host.Read(ctx, false)
	equal(t, status.Codex.Access, domain.EngineAccessReady)
	equal(t, paths, 1)
	host.Read(ctx, false)
	equal(t, reads, 2)
	status, _ = host.Read(ctx, true)
	equal(t, status.Codex.Access, domain.EngineAccessReady)
	equal(t, reads, 3)
}

func TestAccessTwoAsksAtOnceRunTheCommandsOnce(t *testing.T) {
	var mu sync.Mutex
	reads := 0
	host := NewEngineAccessHost(stubs(stubServices{readinessStub{readiness: func() domain.EngineReadiness {
		mu.Lock()
		reads++
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		return domain.EngineReadiness{Access: domain.EngineAccessReady}
	}}}), func(context.Context) error { return nil })
	results := make(chan *domain.EngineStatus, 2)
	for range 2 {
		go func() { status, _ := host.Read(context.Background(), false); results <- status }()
	}
	first, second := <-results, <-results
	equal(t, first, second)
	equal(t, reads, 1)
}

func TestAccessUpdateUpgradesThenRereadsEverything(t *testing.T) {
	var mu sync.Mutex
	version := "0.147.0"
	updates, paths := 0, 0
	services := updatingServices{stubServices{readinessStub{
		readiness: func() domain.EngineReadiness {
			mu.Lock()
			defer mu.Unlock()
			if version == "0.147.0" {
				return domain.EngineReadiness{Access: domain.EngineAccessOutdated, Version: ptr(version), Required: ptr("0.150.1")}
			}
			return domain.EngineReadiness{Access: domain.EngineAccessReady, Version: ptr(version)}
		},
		update: func() {
			mu.Lock()
			updates++
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			version = "0.150.1"
			mu.Unlock()
		},
	}}}
	host := NewEngineAccessHost(stubs(services), func(context.Context) error { paths++; return nil })
	status, _ := host.Read(context.Background(), false)
	equal(t, status.Codex.Access, domain.EngineAccessOutdated)
	results := make(chan *domain.EngineStatus, 2)
	for range 2 {
		go func() { status, _ := host.Update(context.Background(), domain.AgentEngineCodex); results <- status }()
	}
	first, second := <-results, <-results
	equal(t, updates, 1)
	equal(t, first, second)
	equal(t, first.Codex, &domain.EngineReadiness{Access: domain.EngineAccessReady, Version: ptr("0.150.1")})
	equal(t, paths, 1)
}

func TestNewestReleaseIsReadOnceAnHour(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	reply := `{"version":"0.161.0"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.RawPath)
		body := reply
		mu.Unlock()
		w.Write([]byte(body))
	}))
	defer server.Close()
	held := npmRegistry
	npmRegistry = server.URL + "/"
	defer func() { npmRegistry = held }()
	latestReads.Lock()
	latestReads.entries = map[string]*latestRead{}
	latestReads.Unlock()
	installed := &InstalledEngine{Path: "/bin/codex", Version: ptr("0.160.1")}
	ctx := context.Background()
	equal(t, *LatestEngineVersion(ctx, codexCommand, installed), "0.161.0")
	equal(t, *LatestEngineVersion(ctx, codexCommand, installed), "0.161.0")
	equal(t, asked, []string{"/%40openai%2Fcodex/latest"}, "the second ask is answered from the last hour's")
	mu.Lock()
	reply = `{"version":"2.2.0-beta.1"}`
	mu.Unlock()
	equal(t, LatestEngineVersion(ctx, claudeCommand, &InstalledEngine{Path: "/bin/claude", Version: ptr("2.1.0")}), (*string)(nil), "a prerelease is not offered")
}

func (s stubServices) Commands(context.Context, Workspace) ([]*contracts.AvailableCommand, error) {
	return nil, nil
}
