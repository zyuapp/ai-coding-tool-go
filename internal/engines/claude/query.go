package claude

import (
	"context"
	"encoding/json/jsontext"
	"os/exec"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/claude/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

const claudeEntrypoint = "aicodingtool"

// Query is one Claude session as the engine drives it; *protocol.Query is the real one.
type Query interface {
	// Messages are the session's messages in order, closed when it ends; Err then says why.
	Messages() <-chan protocol.Message
	Err() error
	SendUser(message protocol.UserMessage) error
	Interrupt(ctx context.Context) error
	SetModel(ctx context.Context, model *string) error
	SetPermissionMode(ctx context.Context, mode string) error
	ApplyFlagSettings(ctx context.Context, settings any) error
	StopTask(ctx context.Context, taskID string) error
	SupportedCommands(ctx context.Context) ([]protocol.SlashCommand, error)
	SupportedModels(ctx context.Context) ([]protocol.ModelInfo, error)
	Usage(ctx context.Context) (jsontext.Value, error)
	// EndInput closes the session's input once what was written has been sent.
	EndInput()
	Close()
}

// QueryFactory starts a Claude session; a test hands in a scripted one.
type QueryFactory func(ctx context.Context, options protocol.Options) (Query, error)

// StartQuery starts the installed Claude Code.
func StartQuery(ctx context.Context, options protocol.Options) (Query, error) {
	query, err := protocol.Start(ctx, options)
	if err != nil {
		return nil, err
	}
	return query, nil
}

// claudeExecutable is the Claude Code the user installed, found as their shell would find it. The
// app carries no copy of its own.
func claudeExecutable() (string, bool) {
	return agent.EngineBinaryPath(claudeBinary)
}

// claudeProcess is how every Claude process is started: the command, this app's name on its requests
// in place of any inherited one, and no Artifact skills, which the app cannot show.
func claudeProcess() protocol.Options {
	executable, ok := claudeExecutable()
	if !ok {
		executable = claudeBinary.Command
	}
	return protocol.Options{
		Executable: executable,
		Env:        map[string]string{"CLAUDE_CODE_ENTRYPOINT": claudeEntrypoint, "CLAUDE_CODE_DISABLE_ARTIFACT": "1"},
		Command: func(name string, args ...string) *exec.Cmd {
			return platform.Command(context.Background(), name, args...)
		},
	}
}
