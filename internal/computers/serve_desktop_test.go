package computers

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type fakeEngines struct {
	titles [][]string
}

func (f *fakeEngines) PlanUsage(context.Context, domain.AgentEngine) (domain.PlanUsage, error) {
	return nil, errors.New("Not signed in.")
}

func (f *fakeEngines) SuggestTitle(_ context.Context, _ domain.AgentEngine, text string, images []string, _ *domain.AgentModel) (*string, error) {
	f.titles = append(f.titles, append([]string{text}, images...))
	if text == "fails" {
		return nil, errors.New("down")
	}
	return new("A title"), nil
}

func (f *fakeEngines) SubagentMetadata(context.Context, domain.AgentEngine, string, *string) (*domain.SubagentMetadata, error) {
	return nil, nil
}

type fakeAttachments struct{ written []any }

func (f *fakeAttachments) SavedAttachmentPath(file string) (string, bool) {
	if strings.HasPrefix(file, "/kept/") {
		return file, true
	}
	return "", false
}

func (f *fakeAttachments) ReadAttachmentContext(string) jsontext.Value {
	return jsontext.Value(`{"app":"Notes"}`)
}

func (f *fakeAttachments) WriteAttachment(_ string, context any) (string, error) {
	f.written = append(f.written, context)
	return "/kept/new.png", nil
}

type fakeStore struct{ TaskDatabase }

func (fakeStore) LoadSummaries(context.Context) (*contracts.LoadedTaskStore, error) {
	return &contracts.LoadedTaskStore{HiddenTasks: 2}, nil
}

type fakeMobile struct {
	ServeMobile
	mu      sync.Mutex
	noticed []string
}

func (f *fakeMobile) NoticeComputers(notice contracts.ThreadNotice) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noticed = append(f.noticed, "computers:"+notice.TaskID)
}

func (f *fakeMobile) NoticePhones(_ context.Context, notice contracts.ThreadNotice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noticed = append(f.noticed, "phones:"+notice.TaskID)
	return nil
}

func TestServeDesktopAnswersTheServicesAndRefusesWhatNeedsAWindow(t *testing.T) {
	engines := &fakeEngines{}
	attachments := &fakeAttachments{}
	mobile := &fakeMobile{}
	var said []string
	resumes := []string{"t1"}
	desktop := NewServeDesktop(ServeDesktopHost{
		ServiceDesktopHost: ServiceDesktopHost{
			Engines:       engines,
			Attachments:   attachments,
			TaskDatabase:  fakeStore{},
			UpdateResumes: func() []string { held := resumes; resumes = nil; return held },
			EngineAccess: func(context.Context) (EngineAccess, error) {
				return nil, errors.New("no engines")
			},
		},
		Mobile: mobile,
		Say:    func(line string) { said = append(said, line) },
	})
	ctx := context.Background()

	usage, err := desktop.PlanUsage(ctx, domain.AgentEngineClaude)
	if err != nil || !reflect.DeepEqual(usage, domain.ComputerUseRunConfigUnavailable{Message: "Not signed in."}) {
		t.Fatal(usage, err)
	}
	if title, _ := desktop.SuggestTaskTitle(ctx, "  ", []string{"/elsewhere/a.png"}, domain.AgentEngineClaude, nil); title != nil {
		t.Fatal("nothing to name is no title")
	}
	if title, _ := desktop.SuggestTaskTitle(ctx, "", []string{"/kept/a.png", "/elsewhere/b.png"}, domain.AgentEngineClaude, nil); title == nil || *title != "A title" {
		t.Fatal(title)
	}
	if !reflect.DeepEqual(engines.titles, [][]string{{"", "/kept/a.png"}}) {
		t.Fatal(engines.titles)
	}
	if title, err := desktop.SuggestTaskTitle(ctx, "fails", nil, domain.AgentEngineClaude, nil); title != nil || err != nil {
		t.Fatal("a failed suggestion is no title", title, err)
	}
	if _, err := desktop.SaveAttachment(ctx, "AQID", new("/elsewhere/a.png")); err == nil || err.Error() != "That image is not one this app is keeping." {
		t.Fatal(err)
	}
	if _, err := desktop.SaveAttachment(ctx, "AQID", new("/kept/a.png")); err != nil || string(attachments.written[0].(jsontext.Value)) != `{"app":"Notes"}` {
		t.Fatal(err, attachments.written)
	}
	if metadata, err := desktop.LoadSubagentMetadata(ctx, domain.AgentEngineClaude, "s", nil); err != nil || *metadata != (domain.SubagentMetadata{}) {
		t.Fatal(metadata, err)
	}
	loaded, _ := desktop.LoadTaskStore(ctx)
	if !slices.Equal(loaded.ResumeAfterUpdate, []string{"t1"}) || loaded.HiddenTasks != 2 {
		t.Fatal(loaded)
	}
	if again, _ := desktop.LoadTaskStore(ctx); again.ResumeAfterUpdate != nil {
		t.Fatal("the resumes are given once")
	}

	for _, refused := range []struct {
		err  error
		text string
	}{
		{func() error { _, err := desktop.OpenFolder(ctx); return err }(), "Picking a folder needs the desktop app."},
		{desktop.RevealWorktree(ctx, "/x"), "Revealing a checkout needs the desktop app."},
		{desktop.DownloadImage(ctx, "x"), "Saving an image needs the desktop app."},
		{desktop.OpenSourceLicenses(ctx), "The licenses page needs the desktop app."},
		{desktop.OpenFile(ctx, nil, "/x", nil), "Opening a file needs the desktop app."},
		{desktop.OpenFolderInApp(ctx, "app", "/x"), "Opening a folder in an application needs the desktop app."},
		{desktop.PairComputer(ctx, "h", "n", "c"), "This computer cannot pair with others."},
	} {
		if refused.err == nil || refused.err.Error() != refused.text {
			t.Fatalf("%v, want %q", refused.err, refused.text)
		}
	}
	if apps, _ := desktop.ListApps(ctx); apps == nil || len(apps) != 0 {
		t.Fatal(apps)
	}
	if permissions, _ := desktop.ComputerUsePermissions(ctx); *permissions != (domain.ComputerUsePermissions{}) {
		t.Fatal(permissions)
	}
	if err := desktop.host.OpenURL(ctx, "https://claude.ai/login"); err != nil || !slices.Equal(said, []string{"Sign in at https://claude.ai/login"}) {
		t.Fatal(said)
	}
	desktop.AnnounceThread(contracts.ThreadNotice{TaskID: "t"})
	until(t, "the phones told", func() bool {
		mobile.mu.Lock()
		defer mobile.mu.Unlock()
		return slices.Equal(mobile.noticed, []string{"computers:t", "phones:t"})
	})
}

func TestServerMessagesAreReadDefensively(t *testing.T) {
	for _, ignored := range []string{
		`not json`,
		`[]`,
		`{"kind":"ping","at":1}`,
		`{"kind":"ping","at":1.5,"sequence":1}`,
		`{"kind":"workspace","sequence":1,"update":{"revision":1}}`,
		`{"kind":"answer","sequence":1,"requestId":"r","ok":true}`,
		`{"kind":"future","sequence":1}`,
		`{"kind":"capabilities","sequence":1,"capabilities":[1]}`,
	} {
		if message := readServerMessage([]byte(ignored)); message != nil {
			t.Fatalf("%s read as %#v", ignored, message)
		}
	}
	workspace := readServerMessage([]byte(`{"kind":"workspace","sequence":3,"sessionId":"s","update":{"revision":2,"state":{"threads":[]}}}`))
	if update, ok := workspace.(contracts.ComputerServerMessageWorkspace); !ok || string(update.Update.State) != `{"threads":[]}` || *update.SessionID != "s" || update.Sequence != 3 {
		t.Fatalf("%#v", workspace)
	}
	answer := readServerMessage([]byte(`{"kind":"answer","sequence":4,"requestId":"r","ok":false,"message":"Read failed"}`))
	if refused, ok := answer.(contracts.ComputerServerMessageAnswer); !ok || refused.Ok || *refused.Message != "Read failed" {
		t.Fatalf("%#v", answer)
	}
	result := readServerMessage([]byte(`{"kind":"result","sequence":5,"requestId":"r","result":{"ok":true,"revision":7,"taskId":"t"}}`))
	if held, ok := result.(contracts.ComputerServerMessageResult); !ok || !reflect.DeepEqual(CommandResult(held.Result), contracts.WorkspaceCommandResultTrue{TaskID: new("t")}) {
		t.Fatalf("%#v", result)
	}
}

func TestSocketURLIsPlainOnlyOnTheLoopback(t *testing.T) {
	for host, want := range map[string]string{
		"127.0.0.1:4000":   "ws://127.0.0.1:4000/m/workspace",
		"localhost":        "ws://localhost/m/workspace",
		"[::1]:9":          "ws://[::1]:9/m/workspace",
		"mac.tail.ts.net":  "wss://mac.tail.ts.net/m/workspace",
		"127.0.0.1.evil":   "wss://127.0.0.1.evil/m/workspace",
		"10.0.0.2:4000":    "wss://10.0.0.2:4000/m/workspace",
		"localhost:80:443": "wss://localhost:80:443/m/workspace",
	} {
		if got := ComputerSocketURL(host); got != want {
			t.Errorf("%s: %s", host, got)
		}
	}
}

func TestChosenNamesAreTrimmedAndCutToLength(t *testing.T) {
	if chosen("    ") != nil {
		t.Fatal("blank is no name")
	}
	long := strings.Repeat("é", domain.MaxComputerName+5)
	if got := chosen(long); jsLength(*got) != domain.MaxComputerName {
		t.Fatal(jsLength(*got))
	}
}

type fakeRuntime struct {
	mu         sync.Mutex
	dispatched []string
	disposed   bool
}

func (f *fakeRuntime) Start(context.Context) error { return nil }
func (f *fakeRuntime) Dispatch(_ context.Context, input application.WorkspaceInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, inputType(input))
	return nil
}
func (f *fakeRuntime) State() *application.WorkspaceState {
	return application.EmptyWorkspaceState(nil)
}
func (f *fakeRuntime) Dispose() { f.disposed = true }

type fakeBridge struct {
	state   domain.MobileServerState
	onState func(domain.MobileServerState)
	stopped bool
}

func (f *fakeBridge) Start(_ context.Context, _ *int, _ bool, onState func(domain.MobileServerState)) error {
	f.onState = onState
	return nil
}
func (f *fakeBridge) SetMobileEnabled(context.Context, bool) (*domain.MobileServerState, error) {
	f.state.Status = domain.MobileServerStatusListening
	f.state.Port = new(4242.0)
	return &f.state, nil
}
func (f *fakeBridge) MobileState() domain.MobileServerState { return f.state }
func (f *fakeBridge) CreateMobilePairingCode(context.Context) (*domain.MobilePairingOffer, error) {
	return &domain.MobilePairingOffer{Code: "ABCDEFGH", ExpiresAt: application.Now() + 120_000, URL: "aicodingtool://pair"}, nil
}
func (f *fakeBridge) Stop(context.Context) error { f.stopped = true; return nil }

func TestStartServeRunsTheServicesHeadlessAndStopsThem(t *testing.T) {
	userData := filepath.Join(t.TempDir(), "profile")
	runtime := &fakeRuntime{}
	bridge := &fakeBridge{state: domain.MobileServerState{Tailscale: domain.TailscaleState{Status: domain.TailscaleStateStatusMissing}}}
	var mu sync.Mutex
	var said []string
	say := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		said = append(said, line)
	}
	flushes, stops := 0, 0
	socket := ""
	served, err := StartServe(context.Background(), ServeOptions{
		UserData: userData,
		Say:      say,
		Build: func(_ context.Context, environment ServeEnvironment) (*ServeParts, error) {
			if environment.Updates != nil || !slices.Equal(environment.LegacyRoots, []string{filepath.Join(userData, "worktrees")}) {
				t.Fatalf("%#v", environment)
			}
			return &ServeParts{
				Runtime: runtime,
				Flush:   func(context.Context) error { flushes++; return nil },
				Mobile:  bridge,
				ServePairingSocket: func(_ context.Context, path string, _ func(context.Context) (*domain.MobilePairingOffer, error)) (func(context.Context) error, error) {
					socket = path
					return func(context.Context) error { return nil }, nil
				},
				Stop: func(context.Context) { stops++ },
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runtime.dispatched, []string{"view.set-browser-tools", "view.set-computer-use"}) {
		t.Fatal(runtime.dispatched)
	}
	if socket != filepath.Join(userData, "serve.sock") {
		t.Fatal(socket)
	}
	mu.Lock()
	if !slices.Equal(said, []string{"Listening on port 4242. Waiting for Tailscale to serve it.", "Tailscale is not installed on this computer, so nothing can reach the bridge."}) {
		t.Fatal(said)
	}
	mu.Unlock()
	bridge.onState(domain.MobileServerState{Tailscale: domain.TailscaleState{Serving: true, MagicDnsName: new("box.tail.ts.net"), Error: new("Certs off")}})
	bridge.onState(domain.MobileServerState{Tailscale: domain.TailscaleState{Serving: true, MagicDnsName: new("box.tail.ts.net"), Error: new("Certs off")}})
	until(t, "the pairing code under the name", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(said) == 5
	})
	mu.Lock()
	if said[2] != "Serving as https://box.tail.ts.net" || said[3] != "Tailscale: Certs off" || !strings.HasPrefix(said[4], "Pair with code ABCDEFGH (expires in 2 minutes)") {
		t.Fatal(said)
	}
	mu.Unlock()
	if err := served.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if flushes != 2 || stops != 1 || !bridge.stopped || !runtime.disposed {
		t.Fatal(flushes, stops, bridge.stopped, runtime.disposed)
	}
	if _, err := os.Stat(filepath.Join(userData, "serve.lock")); !os.IsNotExist(err) {
		t.Fatal("a stopped server leaves no lock behind", err)
	}
}
