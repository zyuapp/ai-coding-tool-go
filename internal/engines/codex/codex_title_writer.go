package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// Runs the Codex binary once with args, feeding input on stdin, and returns when it has exited.
type CodexExec func(ctx context.Context, args []string, input, cwd string) error

const titleModel = "gpt-6-luna"

var execTimeout = 60 * time.Second

// One-shot, read-only, ephemeral, and blind to the user's own Codex config, so a title touches nothing.
func CodexTitleArgs(schemaFile, outputFile string, images []string) []string {
	args := []string{
		"exec", "--ephemeral", "--ignore-user-config", "--skip-git-repo-check",
		"-s", "read-only",
		"-m", titleModel,
		"-c", `model_reasoning_effort="low"`,
		"--output-schema", schemaFile,
		"--output-last-message", outputFile,
	}
	for _, file := range images {
		args = append(args, "-i", file)
	}
	return append(args, "-")
}

func execCodex(ctx context.Context, args []string, input, cwd string) error {
	// An ephemeral title can use native authentication in its original home. Its databases and logs
	// still go to private storage, prepared as for any launch so they never land where it must move
	// from; --ignore-user-config keeps unrelated user behavior out of titles.
	environment := childEnvironment()
	privateHome, err := PrivateCodexHome(environment)
	if err != nil {
		return err
	}
	env := make(map[string]string, len(environment)+1)
	for key, value := range environment {
		env[key] = value
	}
	env["CODEX_HOME"] = SharedCodexHome(environment)
	config, err := ReadHomeConfig(env["CODEX_HOME"])
	if err != nil {
		return err
	}
	var overrides []string
	if store, ok := config["cli_auth_credentials_store"].(string); ok {
		overrides = append(overrides, "-c", "cli_auth_credentials_store="+Toml(store))
	}
	if secretAuth, ok := table(config["features"])["secret_auth_storage"].(bool); ok {
		overrides = append(overrides, "-c", "features.secret_auth_storage="+Toml(secretAuth))
	}
	if privateHome != "" {
		overrides = append(overrides, "-c", "sqlite_home="+Toml(privateHome), "-c", "log_dir="+Toml(filepath.Join(privateHome, "log")))
	}
	executable, err := CodexExecutable()
	if err != nil {
		return err
	}
	full := append(append(append([]string(nil), args[:len(args)-1]...), overrides...), args[len(args)-1])
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	command := platform.Command(ctx, executable, full...)
	command.Dir = cwd
	command.Env = envList(env)
	command.Stdin = strings.NewReader(input)
	if err := command.Start(); err != nil {
		return err
	}
	_ = command.Wait()
	return nil
}

// Names a thread from its first message and the screenshots sent with it, on Codex. The answer is
// read back from the file `codex exec` writes its last message to, shaped by a schema that allows
// nothing but a title. Nil exec runs the installed Codex.
func SuggestCodexTitle(ctx context.Context, text string, attachments []string, exec CodexExec) (*string, error) {
	if exec == nil {
		exec = execCodex
	}
	images := agent.ReadableImages(attachments)
	if jsTrim(text) == "" && len(images) == 0 {
		return nil, nil
	}
	directory, err := os.MkdirTemp("", "aicodingtool-title-")
	if err != nil {
		return nil, nil
	}
	defer os.RemoveAll(directory)
	schemaFile := filepath.Join(directory, "title.schema.json")
	outputFile := filepath.Join(directory, "title.json")
	if err := os.WriteFile(schemaFile, agent.TitleSchema, 0o666); err != nil {
		return nil, nil
	}
	if err := exec(ctx, CodexTitleArgs(schemaFile, outputFile, images), agent.TitleInstructions+"\n\n"+agent.TitleQuestion(text), directory); err != nil {
		return nil, nil
	}
	answer, err := os.ReadFile(outputFile)
	if err != nil {
		return nil, nil
	}
	title, ok := agent.TitleOf(answer)
	if !ok {
		return nil, nil
	}
	return &title, nil
}
