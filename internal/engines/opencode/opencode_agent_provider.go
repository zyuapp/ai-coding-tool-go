package opencode

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

// Everything a session's server is started with. The model, effort, mode and every tool grant are
// the session's own to change, so only the instructions and the tools offered count.
func sessionKey(input *agent.ProviderRunInput) string {
	return agent.IdentityKey(Brief(input).Identity)
}

type ProviderOptions struct {
	// Starts a session's server; nil starts a real one.
	Connect Connect
	// Serves the app's tools to every session; shared across providers in one process. Required.
	Host agent.ToolHost
	// The sessions this engine's threads keep warm; shared with the other engines of its channel. Nil
	// makes one of the provider's own, idle for Idle (0: agent.IdleSession).
	Pool *agent.SessionPool
	Idle time.Duration
	// The app's bundled plugin folder, whose skills every session loads; "" for none.
	PluginRoot string
	// Between reads of a list the server is still loading; 0 is the client's default.
	LoadingRetry time.Duration
}

type Provider struct {
	*agent.PooledProvider[*Session]
	connect      Connect
	host         agent.ToolHost
	pluginRoot   string
	loadingRetry time.Duration
}

func NewProvider(options ProviderOptions) *Provider {
	pool := options.Pool
	if pool == nil {
		pool = agent.NewSessionPool(nil, options.Idle)
	}
	connect := options.Connect
	if connect == nil {
		connect = StartServer
	}
	host := options.Host
	if host == nil {
		host = noToolHost{}
	}
	return &Provider{
		PooledProvider: agent.NewPooledProvider[*Session](pool),
		connect:        connect, host: host, pluginRoot: options.PluginRoot, loadingRetry: options.LoadingRetry,
	}
}

func (p *Provider) Execute(input *agent.ProviderRunInput) (agent.ProviderResult, error) {
	key := sessionKey(input)
	return p.Run(input, key, agent.SessionOpener{Open: func(hooks agent.SessionHooks) agent.PooledSession {
		return newSession(key, p.connect, p.host, hooks, p.pluginRoot, p.loadingRetry)
	}})
}

// OpenCode's own shells are not listed to the app, so there is nothing of theirs to stop.
func (p *Provider) StopProcess(taskID, processID string) bool {
	return false
}

func (p *Provider) LabelThread(taskID, title string) bool {
	session, ok := p.Session(taskID)
	if ok {
		session.label(title)
	}
	return ok
}

// Files the session away in OpenCode's own history too, or brings it back: on the thread's own
// server while it has one, else on one started for that alone. An archived thread's warm session
// goes with it. Filings of one thread land in the order asked.
func (p *Provider) ArchiveThread(taskID string, continuation *domain.Continuation, archived bool) bool {
	handle, ok := agent.HandleFor(continuation, domain.AgentEngineOpencode)
	if continuation != nil && !ok {
		return false
	}
	session, live := p.Session(taskID)
	sessionID := handle
	if !ok && live {
		sessionID = session.SessionID()
	}
	if sessionID == "" {
		return false
	}
	p.File(taskID, func() error {
		ctx := context.Background()
		filed := false
		if live {
			var err error
			if filed, err = session.file(ctx, sessionID, archived); err != nil {
				return err
			}
		}
		if !filed {
			cwd, err := os.Getwd()
			if err != nil {
				cwd = os.TempDir()
			}
			c, err := p.connect(ctx, ServerOptions{Cwd: cwd})
			if err != nil {
				return err
			}
			_, err = c.Request(ctx, "PATCH", "/api/session/"+sessionID, map[string]any{"archived": archived}, 0)
			c.Close()
			if err != nil {
				return err
			}
		}
		if archived {
			<-p.Pool.Retire(taskID)
		}
		return nil
	})
	return true
}

type noToolHost struct{}

func (noToolHost) Serve(context.Context, []agent.BoundTool) (agent.ServedTools, error) {
	return agent.ServedTools{}, errors.New("The app's tools are not being served.")
}
