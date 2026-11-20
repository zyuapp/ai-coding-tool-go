package agent

import (
	"context"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

type statusFlight struct {
	done   chan struct{}
	status *domain.EngineStatus
	err    error
}

func (flight *statusFlight) wait(ctx context.Context) (*domain.EngineStatus, error) {
	select {
	case <-flight.done:
		return flight.status, flight.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Which engines can take a run. An answer where every engine is fine is kept, since asking runs the
// engine commands; one with anything wrong is read again on every ask, because the user is the one who
// fixes it and the app has to notice when they have.
type EngineAccessHost struct {
	engines         map[domain.AgentEngine]EngineServices
	searchPathAgain func(ctx context.Context) error

	mu        sync.Mutex
	status    *domain.EngineStatus
	reading   *statusFlight
	signingIn *statusFlight
	updating  map[domain.AgentEngine]*statusFlight
}

// searchPathAgain reads the user's shell again, so a command installed after the app started is on
// the search path; nil means platform.AdoptLoginShellPath.
func NewEngineAccessHost(engines map[domain.AgentEngine]EngineServices, searchPathAgain func(ctx context.Context) error) *EngineAccessHost {
	if searchPathAgain == nil {
		searchPathAgain = func(ctx context.Context) error {
			platform.AdoptLoginShellPath(ctx)
			return nil
		}
	}
	return &EngineAccessHost{engines: engines, searchPathAgain: searchPathAgain, updating: map[domain.AgentEngine]*statusFlight{}}
}

func (host *EngineAccessHost) Read(ctx context.Context, refresh bool) (*domain.EngineStatus, error) {
	host.mu.Lock()
	// An engine the user had to fix is read from scratch, since what fixed it can sit anywhere.
	again := refresh || (host.status != nil && domain.EngineNeedsAttention(host.status))
	if host.status != nil && !again {
		status := host.status
		host.mu.Unlock()
		return status, nil
	}
	// One read at a time: a second ask while the commands are answering joins the first.
	flight := host.reading
	if flight == nil {
		flight = &statusFlight{done: make(chan struct{})}
		host.reading = flight
		go func() {
			flight.status, flight.err = host.readAll(context.WithoutCancel(ctx), again)
			host.mu.Lock()
			host.reading = nil
			host.mu.Unlock()
			close(flight.done)
		}()
	}
	host.mu.Unlock()
	return flight.wait(ctx)
}

// One sign-in at a time: a second ask while the browser is out joins the first.
func (host *EngineAccessHost) SignIn(ctx context.Context, engine domain.AgentEngine, openURL OpenURL) (*domain.EngineStatus, error) {
	signIn, ok := host.engines[engine].(SignInService)
	if !ok {
		return host.Read(ctx, false)
	}
	host.mu.Lock()
	flight := host.signingIn
	if flight == nil {
		flight = &statusFlight{done: make(chan struct{})}
		host.signingIn = flight
		detached := context.WithoutCancel(ctx)
		go func() {
			flight.status, flight.err = host.signedIn(detached, engine, signIn, openURL)
			host.mu.Lock()
			host.signingIn = nil
			host.mu.Unlock()
			close(flight.done)
		}()
	}
	host.mu.Unlock()
	return flight.wait(ctx)
}

func (host *EngineAccessHost) signedIn(ctx context.Context, engine domain.AgentEngine, signIn SignInService, openURL OpenURL) (*domain.EngineStatus, error) {
	access, err := signIn.SignIn(ctx, openURL)
	if err != nil {
		return nil, err
	}
	// Only the access changed; the version and the models found earlier still hold.
	current, err := host.Read(ctx, false)
	if err != nil {
		return nil, err
	}
	status := *current
	readiness := domain.EngineReadiness{}
	if previous := current.Get(engine); previous != nil {
		readiness = *previous
	}
	readiness.Access = access
	setReadiness(&status, engine, &readiness)
	host.mu.Lock()
	host.status = &status
	host.mu.Unlock()
	return &status, nil
}

// One update per engine at a time; the status is read from scratch after it, since the version moved.
func (host *EngineAccessHost) Update(ctx context.Context, engine domain.AgentEngine) (*domain.EngineStatus, error) {
	update, ok := host.engines[engine].(UpdateService)
	if !ok {
		return host.Read(ctx, false)
	}
	host.mu.Lock()
	flight := host.updating[engine]
	if flight == nil {
		flight = &statusFlight{done: make(chan struct{})}
		host.updating[engine] = flight
		detached := context.WithoutCancel(ctx)
		go func() {
			if flight.err = update.Update(detached); flight.err == nil {
				flight.status, flight.err = host.readAll(detached, true)
			}
			host.mu.Lock()
			delete(host.updating, engine)
			host.mu.Unlock()
			close(flight.done)
		}()
	}
	host.mu.Unlock()
	return flight.wait(ctx)
}

func (host *EngineAccessHost) readAll(ctx context.Context, refresh bool) (*domain.EngineStatus, error) {
	// A fresh install can sit in a folder the app never had, so the path is read before the commands.
	if refresh {
		_ = host.searchPathAgain(ctx)
	}
	status := &domain.EngineStatus{}
	var (
		wait  sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for _, engine := range domain.AgentEngines {
		services := host.engines[engine]
		if services == nil {
			continue
		}
		wait.Go(func() {
			readiness, err := services.Readiness(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = err
				}
				return
			}
			setReadiness(status, engine, &readiness)
		})
	}
	wait.Wait()
	if first != nil {
		return nil, first
	}
	host.mu.Lock()
	host.status = status
	host.mu.Unlock()
	return status, nil
}

func setReadiness(status *domain.EngineStatus, engine domain.AgentEngine, readiness *domain.EngineReadiness) {
	switch engine {
	case domain.AgentEngineClaude:
		status.Claude = readiness
	case domain.AgentEngineCodex:
		status.Codex = readiness
	case domain.AgentEngineOpencode:
		status.Opencode = readiness
	}
}
