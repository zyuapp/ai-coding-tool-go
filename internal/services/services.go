package services

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/computers"
	"github.com/zyuapp/ai-coding-tool-go/internal/computeruse"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
	"github.com/zyuapp/ai-coding-tool-go/internal/store"
	"github.com/zyuapp/ai-coding-tool-go/internal/tools"
	"github.com/zyuapp/ai-coding-tool-go/internal/updates"
	"github.com/zyuapp/ai-coding-tool-go/internal/updates/appplugin"
	"github.com/zyuapp/ai-coding-tool-go/internal/workspace"
)

// Config is where the services keep what they keep.
type Config struct {
	UserData      string
	WorktreesRoot string
	// Older roots the app made checkouts under, still its own: listed, never created in.
	LegacyRoots []string
	Packaged    bool
	// The installed app's resources folder; "" from source.
	Resources string
	// The source checkout, where a build from source finds its plugin and bundled driver.
	AppPath string
	Version string
	// A server with no screen: computer use is refused rather than set up.
	Headless bool
}

// WorktreesRoots are every root the app keeps checkouts under.
func (c Config) WorktreesRoots() []string {
	return append([]string{c.WorktreesRoot}, c.LegacyRoots...)
}

// Services are everything the runtime's desktop is answered from, opened once per process.
type Services struct {
	Config
	Events      *Events
	Attachments *store.Attachments
	Images      *store.MessageImageStore
	Database    *store.TaskDatabaseService
	Tasks       TaskDatabase
	Workspaces  *Workspaces
	Engines     *engines.Registry
	ToolHost    *tools.McpHTTPHost
	Runs        agent.RunBridge
	Scheduler   *Schedules
	Terminals   *Terminals
	ComputerUse *computeruse.Host
	Cli         *updates.CliInstaller
	Reads       *computers.ComputerReads

	engineAccess *agent.EngineAccessHost
	accessOnce   sync.Once
	searchPath   chan struct{}
	running      atomic.Bool

	mu      sync.Mutex
	threads func(ctx context.Context, query contracts.ComputerThreadQuery) (any, error)
	state   func() *application.WorkspaceState
	links   func() computers.PairedQueries
}

// SourceRoot is the checkout this binary was built from, for a build run from source.
func SourceRoot() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	for dir := filepath.Dir(executable); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "wails.json")); err == nil {
			return dir
		}
	}
	wd, _ := os.Getwd()
	return wd
}

// Open opens every service on the profile. The search path is read from the login shell meanwhile;
// SearchPathReady closes once it is.
func Open(ctx context.Context, config Config) (*Services, error) {
	s := &Services{Config: config, Events: &Events{}, searchPath: make(chan struct{})}
	s.running.Store(true)
	go func() {
		platform.AdoptLoginShellPath(context.Background())
		close(s.searchPath)
	}()
	platform.SetChildEnv(agent.PrivateCodexHomeEnv, filepath.Join(config.UserData, "codex-private"))
	pluginRoot := appplugin.Path(config.Packaged, config.Resources, config.AppPath)
	if _, err := os.Stat(pluginRoot); err != nil {
		pluginRoot = ""
	}
	appplugin.SetRoot(pluginRoot)

	s.Attachments = store.NewAttachments(config.UserData)
	s.Images = store.NewMessageImageStore(filepath.Join(config.UserData, "message-images"), store.MessageThumbnail, workspace.OpenableFile)
	service := workspace.NewWorkspaceService(workspace.WorkspaceServiceOptions{
		RegistryPath:    filepath.Join(config.UserData, "workspaces.v1.json"),
		ProjectlessRoot: filepath.Join(config.UserData, "projectless"),
	})
	git := workspace.NewGit(platform.Command)
	s.Workspaces = &Workspaces{
		Service:   service,
		Git:       git,
		Worktrees: workspace.NewWorktreeService(git, workspace.WorktreeServiceOptions{WorktreesRoot: config.WorktreesRoot, LegacyRoots: config.LegacyRoots, Workspaces: service}),
	}
	database, err := store.OpenTaskDatabaseService(filepath.Join(config.UserData, store.TaskDatabaseFile), store.OpenOptions{
		WorktreesRoots:      config.WorktreesRoots(),
		CutOverCodexThreads: true,
		JSON:                []json.Options{jsonx.Options()},
	})
	if err != nil {
		return nil, err
	}
	s.Database = database
	s.Tasks = newTaskDatabase(database)

	s.Engines = engines.New(pluginRoot)
	s.ToolHost = tools.NewMcpHTTPHost()
	agent.SetToolServers(agent.AppToolServers())
	s.ComputerUse = computeruse.NewHost(computeruse.DriverPath(config.Packaged, config.Resources, config.AppPath))
	s.Runs = agent.StartRunHost(agent.RunHostConfig{
		Publish:    func(event contracts.AgentEvent) { s.Events.RunEvent.Emit(event) },
		Fire:       s.Events.AutomationFire.Emit,
		Ask:        s.Events.ThreadRequest.Emit,
		Running:    s.running.Load,
		Workspaces: func() (agent.WorkspaceResolver, error) { return service, nil },
		Scheduler: func() (agent.AutomationScheduler, error) {
			if s.Scheduler == nil {
				return nil, errors.New("Automation scheduler is not ready.")
			}
			return s.Scheduler.Scheduler, nil
		},
		ComputerUseForRun: func(ctx context.Context) domain.ComputerUseRunConfig {
			if s.Headless {
				return domain.ComputerUseRunConfigUnavailable{Message: "Computer use needs the desktop app."}
			}
			config, err := s.ComputerUse.ForRun(ctx)
			if err != nil {
				return domain.ComputerUseRunConfigUnavailable{Message: contracts.ErrorMessage(err)}
			}
			return config
		},
		Agent:           s.Engines.Agent(agent.WorkerConfig{Tools: s.ToolHost, GeneratedImages: filepath.Join(config.UserData, "generated-images"), PluginRoot: pluginRoot}),
		ResourceJournal: filepath.Join(config.UserData, "session-processes.json"),
	})
	s.Scheduler = newSchedules(s.Events, Automations{Store: database}, s.Runs)
	if err := s.Scheduler.Start(ctx); err != nil {
		log.Printf("Could not start schedules: %v", err)
	}
	s.Terminals = newTerminals(s.Events, platform.ChildEnv)
	s.Cli = updates.NewRuntimeCliInstaller()
	s.Reads = computers.NewComputerReads(computers.ComputerReadHost{
		ComputerQueryHost: computers.ComputerQueryHost{
			Threads:       s.queryThreads,
			Workspaces:    s.Workspaces,
			Terminals:     s.Terminals,
			Attachments:   s.Attachments,
			MessageImages: s.Images,
			Git:           s.Workspaces,
			Commands:      s.commands,
		},
		State: func() *application.WorkspaceState {
			s.mu.Lock()
			state := s.state
			s.mu.Unlock()
			if state == nil {
				return &application.WorkspaceState{}
			}
			return state()
		},
		Links: func() computers.PairedQueries {
			s.mu.Lock()
			links := s.links
			s.mu.Unlock()
			if links == nil {
				return nil
			}
			return links()
		},
	})
	return s, nil
}

// SearchPathReady closes once the login shell's search path is adopted.
func (s *Services) SearchPathReady() <-chan struct{} { return s.searchPath }

// Bind hands the services the runtime's thread reads and state, once it exists.
func (s *Services) Bind(threads func(ctx context.Context, query contracts.ComputerThreadQuery) (any, error), state func() *application.WorkspaceState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads, s.state = threads, state
}

// BindLinks hands the computer reads the lines to the paired computers.
func (s *Services) BindLinks(links func() computers.PairedQueries) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.links = links
}

func (s *Services) queryThreads(ctx context.Context, query contracts.ComputerThreadQuery) (jsontext.Value, error) {
	s.mu.Lock()
	threads := s.threads
	s.mu.Unlock()
	if threads == nil {
		return nil, errors.New("The workspace runtime is not ready.")
	}
	answer, err := threads(ctx, query)
	if err != nil {
		return nil, err
	}
	return jsonx.Marshal(answer)
}

func (s *Services) commands(ctx context.Context, engine domain.AgentEngine, root string, projectless bool) ([]*contracts.AvailableCommand, error) {
	services := s.Engines.Services()[engine]
	if services == nil {
		return nil, errors.New(domain.UnopenableEngine(string(engine)))
	}
	return services.Commands(ctx, agent.Workspace{WorkspaceRoot: root, Projectless: projectless})
}

// EngineAccess is the engines' readiness, sign-ins and updates, made on first ask.
func (s *Services) EngineAccess() *agent.EngineAccessHost {
	s.accessOnce.Do(func() { s.engineAccess = agent.NewEngineAccessHost(s.Engines.Services(), nil) })
	return s.engineAccess
}

// Host is what every host's desktop is built from; OpenURL and ComputerUse are the caller's.
func (s *Services) Host() computers.ServiceDesktopHost {
	return computers.ServiceDesktopHost{
		Events:       s.Events,
		Reads:        s.Reads,
		Runs:         s.Runs,
		Workspaces:   s.Workspaces,
		Git:          s.Workspaces,
		Worktrees:    s.Workspaces,
		TaskDatabase: s.Tasks,
		Engines:      engineServices{s.Engines.Services()},
		EngineAccess: func(context.Context) (computers.EngineAccess, error) { return engineAccess{s.EngineAccess()}, nil },
		Attachments:  s.Attachments,
		Images:       s.Images,
		Cli:          s.Cli.Port(),
		Terminal:     s.Terminals,
		Automation:   s.Scheduler.Desktop(),
		WorktreesRoots: func() []string {
			return s.WorktreesRoots()
		},
		UpdateResumes: func() []string { return updates.TakeUpdateRelaunch(s.UserData, s.Version) },
	}
}

// Running is false once the app has begun to quit, which a run's start is refused over.
func (s *Services) SetRunning(running bool) { s.running.Store(running) }

// Stop puts down what runs on the services' behalf, before the store is closed: schedules, runs and
// the agents, the engines' own servers, shells and computer use.
func (s *Services) Stop(ctx context.Context) {
	s.running.Store(false)
	s.Scheduler.Stop()
	s.Runs.ClearPendingStarts()
	s.Runs.KillAgent()
	if err := s.Engines.CloseServices(ctx); err != nil {
		log.Printf("Could not stop engine services: %v", err)
	}
	s.Terminals.Host.Stop()
	if err := s.ComputerUse.Stop(ctx); err != nil {
		log.Printf("Could not stop computer use: %v", err)
	}
}

// Resume starts again what Stop put down, for a quit that could not finish.
func (s *Services) Resume(ctx context.Context) {
	s.running.Store(true)
	s.ComputerUse.Resume()
	if err := s.Scheduler.Start(ctx); err != nil {
		log.Printf("Could not restart schedules: %v", err)
	}
}

// Close writes what the scheduler holds and closes the store.
func (s *Services) Close() error {
	if err := s.Scheduler.Flush(); err != nil {
		log.Printf("Could not save schedules: %v", err)
	}
	_ = s.ToolHost.Close()
	_, err := s.Database.Close().Wait()
	return err
}

// SweepUserData is housekeeping nothing waits for: retired data goes to the Trash, unused attachments go.
func (s *Services) SweepUserData(ctx context.Context, say func(string)) error {
	if retired, err := platform.RetireLegacyCodexHome(s.UserData, platform.TrashItem); err != nil {
		return err
	} else if retired {
		say("Moved the retired Codex home to the Trash.")
	}
	paths, err := s.Tasks.AttachmentPaths(ctx)
	if err != nil {
		return err
	}
	swept, err := sweepAttachments(s.Attachments.Directory(), paths)
	if err != nil {
		return err
	}
	if swept.Files > 0 {
		say(sweptLine(swept.Files, swept.Bytes))
	}
	return nil
}

func sweepAttachments(directory string, referenced []string) (platform.SweepResult, error) {
	return platform.SweepOrphanAttachments(directory, platform.AttachmentNames(referenced), time.Now(), platform.OrphanAttachmentMinAge)
}
