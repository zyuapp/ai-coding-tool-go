package opencode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/opencode/client"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The release whose server API the app speaks. An older OpenCode, including any 1.x, is turned away.
const BaselineVersion = "2.2.5"

// A title is worth a short wait, not a stuck request.
const titleTimeout = 20 * time.Second

// How long the server the app reads models and commands from stays up after its last question.
const servicesIdle = 60 * time.Second

// The plugin name both engines qualify the app's skills with.
const appPluginName = "aicodingtool"

// The plugin's skills folder, or "" when the app has no plugin.
func skillsRoot(pluginRoot string) string {
	if pluginRoot == "" {
		return ""
	}
	return filepath.Join(pluginRoot, "skills")
}

// `aicodingtool:review-thread` and `review-thread` are the same app skill.
func unqualifiedSkillName(name string) string {
	return strings.TrimPrefix(name, appPluginName+":")
}

type serverStart struct {
	done   chan struct{}
	client Client
	err    error
}

// One server the app asks things of outside a run, started on the first question and stopped once
// nobody has asked anything for a while. It carries the app's skills, so the commands it lists are
// the ones a run can take.
type Services struct {
	connect    Connect
	pluginRoot string
	idle       time.Duration
	// Between reads of a list the server is still loading; 0 is the client's default.
	loadingRetry time.Duration

	mu       sync.Mutex
	starting *serverStart
	timer    *time.Timer
	inFlight int
}

// connect nil starts real servers; pluginRoot "" offers no app skills.
func NewServices(connect Connect, pluginRoot string) *Services {
	if connect == nil {
		connect = StartServer
	}
	return &Services{connect: connect, pluginRoot: pluginRoot, idle: servicesIdle}
}

func (s *Services) start() *serverStart {
	starting := &serverStart{done: make(chan struct{})}
	options := ServerOptions{Cwd: homeDir()}
	if skills := skillsRoot(s.pluginRoot); skills != "" {
		options.Config = map[string]any{"skills": []string{skills}}
	}
	go func() {
		starting.client, starting.err = s.connect(context.Background(), options)
		close(starting.done)
		if starting.err != nil {
			return
		}
		<-starting.client.Exited()
		s.mu.Lock()
		if s.starting == starting {
			s.starting = nil
		}
		s.mu.Unlock()
	}()
	return starting
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return home
}

func use[T any](ctx context.Context, s *Services, ask func(c Client) (T, error)) (T, error) {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.inFlight++
	if s.starting == nil {
		s.starting = s.start()
	}
	starting := s.starting
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		if s.inFlight == 0 {
			s.timer = time.AfterFunc(s.idle, s.Close)
		}
		s.mu.Unlock()
	}()

	var result T
	var err error
	select {
	case <-starting.done:
		err = starting.err
	case <-ctx.Done():
		return result, ctx.Err()
	}
	if err == nil {
		result, err = ask(starting.client)
	}
	if err != nil {
		s.mu.Lock()
		if s.starting == starting {
			s.starting = nil
		}
		s.mu.Unlock()
		if starting.client != nil {
			go starting.client.Close()
		}
	}
	return result, err
}

// Stops the server, when one is up or starting.
func (s *Services) Close() {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	starting := s.starting
	s.starting = nil
	s.mu.Unlock()
	if starting == nil {
		return
	}
	<-starting.done
	if starting.client != nil {
		starting.client.Close()
	}
}

// The models the user's connected providers offer, the server's default first.
func (s *Services) Catalog(ctx context.Context) ([]*domain.ModelSpec, error) {
	return use(ctx, s, func(c Client) ([]*domain.ModelSpec, error) {
		var (
			wait       sync.WaitGroup
			models     []*Model
			modelsErr  error
			providers  []*ModelProvider
			defaultOne *Model
		)
		wait.Go(func() { models, modelsErr = requestAs[[]*Model](ctx, c, "GET", "/api/model", nil, 0) })
		wait.Go(func() { providers, _ = requestAs[[]*ModelProvider](ctx, c, "GET", "/api/provider", nil, 0) })
		wait.Go(func() { defaultOne, _ = requestAs[*Model](ctx, c, "GET", "/api/model/default", nil, 0) })
		wait.Wait()
		if modelsErr != nil {
			return nil, modelsErr
		}
		return Catalog(models, providers, defaultOne), nil
	})
}

type listedCommand struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
}

type listedSkill struct {
	ID          string  `json:"id"`
	Description *string `json:"description,omitzero"`
}

func orEmpty(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}

// OpenCode's commands and the skills it can load in one workspace, the app's own among them.
func (s *Services) Commands(ctx context.Context, workspaceRoot string) ([]*contracts.AvailableCommand, error) {
	return use(ctx, s, func(c Client) ([]*contracts.AvailableCommand, error) {
		location := locationQuery(workspaceRoot)
		var (
			wait     sync.WaitGroup
			commands []listedCommand
			skills   []listedSkill
		)
		wait.Go(func() {
			commands, _ = client.ReadLoaded[listedCommand](ctx, c, "/api/command?"+location, s.loadingRetry)
		})
		wait.Go(func() { skills, _ = client.ReadLoaded[listedSkill](ctx, c, "/api/skill?"+location, s.loadingRetry) })
		wait.Wait()
		listed := []*contracts.AvailableCommand{}
		for _, command := range commands {
			listed = append(listed, &contracts.AvailableCommand{Name: command.Name, Description: orEmpty(command.Description)})
		}
		for _, skill := range skills {
			listed = append(listed, &contracts.AvailableCommand{Name: unqualifiedSkillName(skill.ID), Description: orEmpty(skill.Description)})
		}
		// Each name once, where it first appears, as the last listing of it says.
		at := map[string]int{}
		unique := []*contracts.AvailableCommand{}
		for _, command := range listed {
			if index, ok := at[command.Name]; ok {
				unique[index] = command
				continue
			}
			at[command.Name] = len(unique)
			unique = append(unique, command)
		}
		return unique, nil
	})
}

// Names a thread from its first message, on the model the thread runs on; nil when it cannot.
func (s *Services) Title(ctx context.Context, text string, model *domain.AgentModel) *string {
	var ref *ModelRef
	if model != nil {
		ref = ModelRefOf(*model, "")
	}
	if jsTrim(text) == "" || ref == nil {
		return nil
	}
	title, err := use(ctx, s, func(c Client) (*string, error) {
		type answer struct {
			Text *string `json:"text,omitzero"`
		}
		got, err := requestAs[*answer](ctx, c, "POST", "/api/experimental/generate/text", map[string]any{
			"prompt": agent.TitleInstructions + "\n\n" + agent.TitleQuestion(text) + "\n\nAnswer with the title alone.",
			"model":  ref,
		}, titleTimeout)
		if err != nil {
			return nil, err
		}
		written := ""
		if got != nil && got.Text != nil {
			written = *got.Text
		}
		encoded, err := jsonx.Marshal(map[string]string{"title": written})
		if err != nil {
			return nil, err
		}
		if title, ok := agent.TitleOf(encoded); ok {
			return &title, nil
		}
		return nil, nil
	})
	if err != nil {
		return nil
	}
	return title
}

type meter struct {
	Percent  float64 `json:"percent"`
	ResetsAt float64 `json:"resetsAt"`
}

type goLimits struct {
	Go *struct {
		Rolling *meter `json:"rolling"`
		Weekly  *meter `json:"weekly"`
		Monthly *meter `json:"monthly"`
	} `json:"go,omitzero"`
}

func isoTime(ms float64) string {
	return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02T15:04:05.000Z")
}

// OpenCode Go's allowance, when the account has one; any other provider bills by use.
func (s *Services) PlanUsage(ctx context.Context) (domain.PlanUsage, error) {
	limits, err := use(ctx, s, func(c Client) (*goLimits, error) {
		return requestAs[*goLimits](ctx, c, "GET", "/api/experimental/usage/limits", nil, 0)
	})
	if err != nil {
		return nil, err
	}
	if limits == nil || limits.Go == nil {
		return domain.PlanUsageNotApplicable{}, nil
	}
	plan := limits.Go
	if plan.Rolling == nil || plan.Weekly == nil || plan.Monthly == nil {
		return nil, errors.New("OpenCode answered usage limits without every window.")
	}
	window := func(id, label string, at *meter, period *domain.UsageWindowPeriod) *domain.UsageWindow {
		return &domain.UsageWindow{ID: id, Label: label, Utilization: new(at.Percent), ResetsAt: new(isoTime(at.ResetsAt)), Period: period}
	}
	return domain.PlanUsageAvailable{
		Subscription: new("Go"),
		Windows: []*domain.UsageWindow{
			window("rolling", "Rolling", plan.Rolling, new(domain.UsageWindowPeriodSession)),
			window("weekly", "Weekly", plan.Weekly, new(domain.UsageWindowPeriodWeek)),
			window("monthly", "Monthly", plan.Monthly, nil),
		},
	}, nil
}

// OpenCode serves a version of its API; the app speaks 2.x, so a 1.x install is too old however it
// was installed. A ready install names the models its connected providers offer.
func (s *Services) Readiness(ctx context.Context) (domain.EngineReadiness, error) {
	installed, ok := agent.InstalledEngineOf(ctx, Binary)
	if !ok {
		return domain.EngineReadiness{Access: domain.EngineAccessMissing, Fix: new(Binary.Install)}, nil
	}
	readiness := agent.VersionReadiness(ctx, Binary, installed, BaselineVersion)
	if readiness.Required != nil {
		readiness.Access = domain.EngineAccessOutdated
		return readiness, nil
	}
	catalog, err := s.Catalog(ctx)
	if err != nil {
		readiness.Access = domain.EngineAccessUnavailable
		return readiness, nil
	}
	readiness.Access = domain.EngineAccessReady
	if len(catalog) == 0 {
		readiness.Fix = new("opencode auth login")
	}
	readiness.Catalog = catalog
	return readiness, nil
}
