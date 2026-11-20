package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The EnginePort runtime the host calls: what the window asks of engines outside a run.
type EnginePort struct {
	Services map[domain.AgentEngine]EngineServices
	OpenURL  OpenURL
	// The saved attachment a renderer path names, or false for one the app is not keeping.
	SavedAttachmentPath func(path string) (string, bool)

	accessOnce sync.Once
	access     *EngineAccessHost
}

// The access host, made on first ask, since an engine it asks is a process of its own.
func (port *EnginePort) Access() *EngineAccessHost {
	port.accessOnce.Do(func() { port.access = NewEngineAccessHost(port.Services, nil) })
	return port.access
}

func (port *EnginePort) services(engine domain.AgentEngine) (EngineServices, error) {
	if services := port.Services[engine]; services != nil {
		return services, nil
	}
	return nil, errors.New(domain.UnopenableEngine(string(engine)))
}

// Never fails: an engine that cannot answer says why instead.
func (port *EnginePort) PlanUsage(ctx context.Context, engine domain.AgentEngine) (domain.PlanUsage, error) {
	usage, err := func() (domain.PlanUsage, error) {
		services, err := port.services(engine)
		if err != nil {
			return nil, err
		}
		return services.PlanUsage(ctx)
	}()
	if err != nil {
		return domain.ComputerUseRunConfigUnavailable{Message: err.Error()}, nil
	}
	return usage, nil
}

func (port *EnginePort) SuggestTaskTitle(ctx context.Context, text string, attachments []string, engine domain.AgentEngine, model *domain.AgentModel) (*string, error) {
	images := []string{}
	for _, item := range attachments {
		if port.SavedAttachmentPath == nil {
			break
		}
		if file, ok := port.SavedAttachmentPath(item); ok {
			images = append(images, file)
		}
	}
	if jsTrim(text) == "" && len(images) == 0 {
		return nil, nil
	}
	services, err := port.services(engine)
	if err != nil {
		return nil, nil
	}
	title, err := services.SuggestTitle(ctx, text, images, model)
	if err != nil {
		return nil, nil
	}
	return title, nil
}

func (port *EnginePort) EngineStatus(ctx context.Context, refresh *bool) (*domain.EngineStatus, error) {
	return port.Access().Read(ctx, refresh != nil && *refresh)
}

func (port *EnginePort) SignInEngine(ctx context.Context, engine domain.AgentEngine) (*domain.EngineStatus, error) {
	return port.Access().SignIn(ctx, engine, port.OpenURL)
}

func (port *EnginePort) UpdateEngine(ctx context.Context, engine domain.AgentEngine) (*domain.EngineStatus, error) {
	return port.Access().Update(ctx, engine)
}

func (port *EnginePort) LoadSubagentMetadata(ctx context.Context, engine domain.AgentEngine, subagentID string, sessionID *string) (*domain.SubagentMetadata, error) {
	services, err := port.services(engine)
	if err != nil {
		return nil, err
	}
	reader, ok := services.(SubagentMetadataService)
	if !ok {
		return &domain.SubagentMetadata{}, nil
	}
	metadata, err := reader.SubagentMetadata(ctx, subagentID, sessionID)
	if err != nil {
		return nil, err
	}
	return &metadata, nil
}

// Stops what the engines started to answer outside a run, such as OpenCode's services server.
func CloseEngineServices(ctx context.Context, services map[domain.AgentEngine]EngineServices) error {
	var (
		wait sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, engine := range domain.AgentEngines {
		closer, ok := services[engine].(CloseService)
		if !ok {
			continue
		}
		wait.Go(func() {
			if err := closer.Close(ctx); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wait.Wait()
	return errors.Join(errs...)
}

var _ contracts.EnginePortRuntime = (*EnginePort)(nil)
