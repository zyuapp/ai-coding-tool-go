package codex

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

type AppServerOptions struct {
	Cwd string
	// The environment the server is started from; nil is the app's own, as its children see it.
	Env map[string]string
	// Account operations keep the user's home.
	SharedHome bool
}

func envList(environment map[string]string) []string {
	list := make([]string, 0, len(environment))
	for _, key := range slices.Sorted(maps.Keys(environment)) {
		list = append(list, key+"="+environment[key])
	}
	return list
}

// Starts children the way every child of the app starts.
func makeCommand(name string, args ...string) *exec.Cmd {
	return platform.Command(context.Background(), name, args...)
}

// Account operations keep the user's home; conversation processes use the app's private state.
func CodexAppServer(args []string, options AppServerOptions) (protocol.Command, error) {
	environment := options.Env
	if environment == nil {
		environment = childEnvironment()
	}
	env := environment
	if !options.SharedHome {
		var err error
		if env, err = CodexChildEnvironment(environment); err != nil {
			return protocol.Command{}, err
		}
	}
	var storage []string
	if !options.SharedHome && environment[agent.PrivateCodexHomeEnv] != "" {
		storage = []string{"-c", "sqlite_home=" + Toml(env["CODEX_HOME"]), "-c", "log_dir=" + Toml(env["CODEX_HOME"]+"/log")}
	}
	executable, err := CodexExecutable()
	if err != nil {
		return protocol.Command{}, err
	}
	base := []string{"app-server", "--listen", "stdio://"}
	if len(storage) == 0 {
		return protocol.Command{Executable: executable, Args: append(slices.Clip(base), args...), Cwd: options.Cwd, Env: envList(env), Make: makeCommand}, nil
	}
	source, err := realpath(SharedCodexHome(environment))
	if err != nil {
		return protocol.Command{}, err
	}
	config, err := ReadHomeConfig(source)
	if err != nil {
		return protocol.Command{}, err
	}
	if EncryptedMcpCredentials(config) {
		_, err := os.Stat(filepath.Join(source, "secrets", "mcp_oauth.age"))
		if err == nil {
			return protocol.Command{}, errors.New("Codex's encrypted MCP credential store cannot be shared across homes. Configure mcp_oauth_credentials_store = \"file\" in your Codex config and reconnect those MCP servers before using them in AICodingTool.")
		}
		if !missing(err) {
			return protocol.Command{}, err
		}
	}
	keyring := UsesSharedKeyring(config)
	command := protocol.Command{Executable: executable, Cwd: options.Cwd, Env: envList(env), Make: makeCommand}
	command.Args = append(slices.Clip(base), PrivateHomeConfig(config, source, env["CODEX_HOME"])...)
	command.Args = append(command.Args, args...)
	if keyring {
		command.Args = append(command.Args, "-c", `cli_auth_credentials_store="ephemeral"`)
		authEnv := maps.Clone(environment)
		authEnv["CODEX_HOME"] = source
		authEnv["CODEX_SQLITE_HOME"] = env["CODEX_HOME"]
		authArgs := append(slices.Clip(base), "--disable", "plugins", "--disable", "apps", "--disable", "hooks")
		command.SharedAuth = &protocol.Command{Executable: executable, Cwd: source, Env: envList(authEnv), Args: append(authArgs, storage...), Make: makeCommand}
	}
	command.Args = append(command.Args, storage...)
	return command, nil
}

// Arguments that keep a short-lived server from loading what a lookup does not need.
var bareServer = []string{"--disable", "plugins", "--disable", "apps", "--disable", "hooks"}
