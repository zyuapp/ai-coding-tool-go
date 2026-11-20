package codex

import (
	"context"
	"errors"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

// Everything a session is built with. A run that disagrees with any of it needs a session of its
// own. App tools and computer use are granted unasked per process, so what decides those grants
// counts too; computer use's only while it is on.
func sessionKey(input *agent.ProviderRunInput) string {
	scope := &agent.ApprovalScope{Policy: input.Policy, Channel: input.Channel}
	_, available := input.ComputerUse.(domain.ComputerUseRunConfigAvailable)
	parts := append([]any(nil), CodexBrief(input).Identity...)
	parts = append(parts, available && agent.GrantsTool(agent.ReachComputerUse, scope), agent.GrantsTool(agent.ReachWorkspace, scope))
	// Review has no turn-level overrides, so its process must agree with the thread settings.
	if _, review := input.Operation.(contracts.RunOperationReview); review {
		parts = append(parts, []any{input.Model, input.Effort, input.Policy})
	} else {
		parts = append(parts, nil)
	}
	return agent.IdentityKey(parts)
}

type CodexProviderOptions struct {
	// Saves generated images; nil saves them under the provider's generated-images folder.
	ImageOutput ImageOutput
	// Starts an app server; nil is ConnectAppServer.
	Connect CodexConnect
	// Reads the checkout a thread runs in; nil reads it with git.
	ReadOrigin ReadOrigin
	// Serves the app's tools to every session; shared across providers in one process.
	Host agent.ToolHost
	// The sessions this engine's threads keep warm; shared with the other engines of its channel.
	// Nil makes a pool of its own with IdleTime.
	Pool     *agent.SessionPool
	IdleTime time.Duration
}

type CodexAgentProvider struct {
	*agent.PooledProvider[*CodexSession]
	connect     CodexConnect
	host        agent.ToolHost
	readOrigin  ReadOrigin
	imageOutput ImageOutput
}

var _ agent.EngineProvider = (*CodexAgentProvider)(nil)
var _ agent.PooledSession = (*CodexSession)(nil)

type noToolHost struct{}

func (noToolHost) Serve(context.Context, []agent.BoundTool) (agent.ServedTools, error) {
	return agent.ServedTools{}, errors.New("The app's tools are not served in this process.")
}

func NewCodexAgentProvider(options CodexProviderOptions) *CodexAgentProvider {
	pool := options.Pool
	if pool == nil {
		pool = agent.NewSessionPool(nil, options.IdleTime)
	}
	provider := &CodexAgentProvider{
		PooledProvider: agent.NewPooledProvider[*CodexSession](pool),
		connect:        options.Connect,
		host:           options.Host,
		readOrigin:     options.ReadOrigin,
		imageOutput:    options.ImageOutput,
	}
	if provider.connect == nil {
		provider.connect = ConnectAppServer
	}
	if provider.host == nil {
		provider.host = noToolHost{}
	}
	if provider.readOrigin == nil {
		provider.readOrigin = ReadGitOrigin
	}
	if provider.imageOutput == nil {
		provider.imageOutput = func(_ context.Context, item protocol.ThreadItem, root string) (string, error) {
			return CodexImageOutput(item, root, "")
		}
	}
	return provider
}

func (provider *CodexAgentProvider) Execute(input *agent.ProviderRunInput) (agent.ProviderResult, error) {
	key := sessionKey(input)
	return provider.Run(input, key, agent.SessionOpener{Open: func(hooks agent.SessionHooks) agent.PooledSession {
		return newCodexSession(key, provider.connect, provider.host, hooks, provider.readOrigin, provider.imageOutput)
	}})
}

// Reaches the thread's own session, so work that outlived the turn that started it can still be stopped.
func (provider *CodexAgentProvider) StopProcess(taskID, processID string) bool {
	session, ok := provider.Session(taskID)
	if ok {
		session.StopProcess(processID)
	}
	return ok
}

// Names the thread in Codex's own history, so it reads there as it reads here.
func (provider *CodexAgentProvider) LabelThread(taskID, title string) bool {
	session, ok := provider.Session(taskID)
	if ok {
		session.Label(title)
	}
	return ok
}

// Files the thread away in Codex's own history too, or brings it back. Archiving waits out the
// thread's session, which holds the thread open until its cancelled turn has answered; a thread
// archived before Codex named it is filed under the name its session ends up with.
func (provider *CodexAgentProvider) ArchiveThread(taskID string, continuation *domain.Continuation, archived bool) bool {
	handle, handled := agent.HandleFor(continuation, domain.AgentEngineCodex)
	if continuation != nil && !handled {
		return false
	}
	session, live := provider.Session(taskID)
	if !handled && !(archived && live) {
		return false
	}
	provider.File(taskID, func() error {
		if archived {
			<-provider.Pool.Retire(taskID)
		}
		threadID := handle
		if !handled && live {
			threadID = session.ThreadID()
		}
		if threadID == "" {
			return nil
		}
		return FileThread(context.Background(), provider.connect, threadID, archived)
	})
	return true
}
