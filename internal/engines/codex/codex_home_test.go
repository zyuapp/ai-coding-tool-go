package codex

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

func tempRoot(t *testing.T, prefix string) string {
	t.Helper()
	root, err := realpath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, prefix)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func readText(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	must(t, err)
	return string(data)
}

func writeText(t *testing.T, file, text string) {
	t.Helper()
	must(t, os.WriteFile(file, []byte(text), 0o644))
}

func inode(t *testing.T, file string) uint64 {
	t.Helper()
	info, err := os.Stat(file)
	must(t, err)
	return uint64(info.Sys().(*syscall.Stat_t).Ino)
}

// Paths under foreign stand for a second filesystem: hard links across it fail as the kernel's do.
func foreignFilesystem(t *testing.T, foreign string) {
	t.Helper()
	held := hardLink
	inside := func(file string) bool {
		absolute, _ := filepath.Abs(file)
		return strings.HasPrefix(absolute, foreign)
	}
	hardLink = func(existing, created string) error {
		if inside(existing) != inside(created) {
			return &os.LinkError{Op: "link", Old: existing, New: created, Err: syscall.EXDEV}
		}
		return held(existing, created)
	}
	t.Cleanup(func() { hardLink = held })
}

func TestSharedInputsFollowEditsAndLateSignInWhileEachLaunchRepairsLinks(t *testing.T) {
	root := tempRoot(t, "codex-home")
	source := filepath.Join(root, "shared")
	privateHome := filepath.Join(root, "private")
	must(t, os.Mkdir(source, 0o700))
	writeText(t, filepath.Join(source, "config.toml"), `model = "one"`)
	_, err := PreparePrivateCodexHome(privateHome, source)
	must(t, err)
	writeText(t, filepath.Join(source, "config.toml"), `model = "two"`)
	writeText(t, filepath.Join(source, "auth.json"), "synthetic credentials")
	if got := readText(t, filepath.Join(privateHome, "config.toml")); got != `model = "two"` {
		t.Fatalf("config = %q", got)
	}
	if got := readText(t, filepath.Join(privateHome, "auth.json")); got != "synthetic credentials" {
		t.Fatalf("auth = %q", got)
	}
	must(t, os.Remove(filepath.Join(privateHome, "auth.json")))
	writeText(t, filepath.Join(source, "review.config.toml"), `model = "review"`)
	var waiting sync.WaitGroup
	for range 2 {
		waiting.Go(func() {
			if _, err := PreparePrivateCodexHome(privateHome, source); err != nil {
				t.Error(err)
			}
		})
	}
	waiting.Wait()
	if target, _ := os.Readlink(filepath.Join(privateHome, "auth.json")); target != filepath.Join(source, "auth.json") {
		t.Fatalf("auth link = %q", target)
	}
	if got := readText(t, filepath.Join(privateHome, "review.config.toml")); got != `model = "review"` {
		t.Fatalf("profile = %q", got)
	}
	environment := map[string]string{"PATH": os.Getenv("PATH"), "CODEX_HOME": source, "CODEX_SQLITE_HOME": source, agent.PrivateCodexHomeEnv: privateHome}
	child, err := CodexChildEnvironment(environment)
	must(t, err)
	if child["CODEX_HOME"] != privateHome || child["CODEX_SQLITE_HOME"] != privateHome || environment["CODEX_HOME"] != source {
		t.Fatalf("child = %v, environment = %v", child, environment)
	}
	command, err := CodexAppServer([]string{"-c", `sqlite_home="` + source + `"`}, AppServerOptions{Env: environment})
	must(t, err)
	assertJSON(t, command.Args[len(command.Args)-4:], []string{"-c", `sqlite_home="` + privateHome + `"`, "-c", `log_dir="` + privateHome + `/log"`})
	account, err := CodexAppServer(nil, AppServerOptions{Env: environment, SharedHome: true})
	must(t, err)
	assertJSON(t, account.Args, []string{"app-server", "--listen", "stdio://"})
	assertJSON(t, account.Env, envList(environment))
}

func TestMcpCredentialsSupportNoFollowWritesCommonLocksAndReplacementAfterLogout(t *testing.T) {
	root := tempRoot(t, "codex-mcp-home")
	source := filepath.Join(root, "shared")
	privateHome := filepath.Join(root, "private")
	must(t, os.Mkdir(source, 0o700))
	must(t, os.Mkdir(privateHome, 0o700))
	must(t, os.Symlink(filepath.Join(source, ".credentials.json"), filepath.Join(privateHome, ".credentials.json")))
	must(t, os.MkdirAll(filepath.Join(privateHome, "plugins", "cache"), 0o700))
	writeText(t, filepath.Join(privateHome, "plugins", "cache", "old-install"), "retained")
	_, err := PreparePrivateCodexHome(privateHome, source)
	must(t, err)
	sharedCredentials := filepath.Join(source, ".credentials.json")
	privateCredentials := filepath.Join(privateHome, ".credentials.json")
	file, err := os.OpenFile(privateCredentials, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0)
	must(t, err)
	_, err = file.WriteString(`{"fixture":"refreshed"}`)
	must(t, err)
	must(t, file.Close())
	if got := readText(t, sharedCredentials); got != `{"fixture":"refreshed"}` {
		t.Fatalf("shared = %q", got)
	}
	if locks, _ := realpath(filepath.Join(privateHome, "mcp-oauth-locks")); locks != filepath.Join(source, "mcp-oauth-locks") {
		t.Fatalf("locks = %q", locks)
	}
	must(t, os.Remove(sharedCredentials))
	_, err = PreparePrivateCodexHome(privateHome, source)
	must(t, err)
	if got := readText(t, privateCredentials); got != "{}" {
		t.Fatalf("private = %q", got)
	}
	if inode(t, privateCredentials) != inode(t, sharedCredentials) {
		t.Fatal("the credentials are one file")
	}
	if cache, _ := realpath(filepath.Join(privateHome, "plugins", "cache")); cache != filepath.Join(source, "plugins", "cache") {
		t.Fatalf("cache = %q", cache)
	}
}

func TestAPrivateHomeOnAnotherFilesystemMovesBesideTheSharedHome(t *testing.T) {
	root := tempRoot(t, "codex-cross-device-home")
	source := filepath.Join(root, "shared")
	privateHome := filepath.Join(root, "app-data", "codex-private")
	foreignFilesystem(t, filepath.Join(root, "app-data"))
	must(t, os.Mkdir(source, 0o700))
	must(t, os.MkdirAll(filepath.Join(privateHome, "plugins"), 0o700))
	writeText(t, filepath.Join(source, "config.toml"), `model = "shared"`)
	// Launches that failed before relocation existed left only links behind.
	must(t, os.Symlink(filepath.Join(source, "auth.json"), filepath.Join(privateHome, "auth.json")))
	must(t, os.Symlink(filepath.Join(source, "plugins", "cache"), filepath.Join(privateHome, "plugins", "cache")))
	destination, err := PreparePrivateCodexHome(privateHome, source)
	must(t, err)
	if filepath.Dir(filepath.Dir(destination)) != source {
		t.Fatalf("destination = %s", destination)
	}
	if resolved, _ := realpath(privateHome); resolved != destination {
		t.Fatalf("private home resolves to %s", resolved)
	}
	if got := readText(t, filepath.Join(privateHome, "config.toml")); got != `model = "shared"` {
		t.Fatalf("config = %q", got)
	}
	privateCredentials := filepath.Join(destination, ".credentials.json")
	if info, err := os.Lstat(privateCredentials); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("credentials = %v %v", info, err)
	}
	child, err := CodexChildEnvironment(map[string]string{"CODEX_HOME": source, agent.PrivateCodexHomeEnv: privateHome})
	must(t, err)
	if child["CODEX_HOME"] != destination {
		t.Fatalf("CODEX_HOME = %s", child["CODEX_HOME"])
	}
	must(t, os.RemoveAll(destination))
	again, err := PreparePrivateCodexHome(privateHome, source)
	must(t, err)
	if again != destination || inode(t, privateCredentials) != inode(t, filepath.Join(source, ".credentials.json")) {
		t.Fatal("a removed destination is recreated with the shared credentials")
	}
}

func TestATitleRunOnAnotherFilesystemStoresItsDatabasesInTheMovedHome(t *testing.T) {
	root := tempRoot(t, "codex-cross-device-title")
	source := filepath.Join(root, "shared")
	privateHome := filepath.Join(root, "app-data", "codex-private")
	bin := filepath.Join(root, "bin")
	foreignFilesystem(t, filepath.Join(root, "app-data"))
	must(t, os.Mkdir(source, 0o700))
	must(t, os.MkdirAll(filepath.Join(privateHome, "log"), 0o700))
	must(t, os.Mkdir(bin, 0o700))
	// A title that ran while launches failed wrote straight into the unprepared home.
	for _, name := range []string{"state_5.sqlite", "state_5.sqlite-wal", "state_5.sqlite-shm", "logs_2.sqlite", "log/codex-tui.log"} {
		writeText(t, filepath.Join(privateHome, name), "title")
	}
	must(t, os.Symlink(filepath.Join(source, "auth.json"), filepath.Join(privateHome, "auth.json")))
	// Stands in for `codex exec`: records its database in sqlite_home and answers with a title.
	writeText(t, filepath.Join(bin, "codex"), `#!/bin/sh
for arg; do
  case "$arg" in sqlite_home=*) home=$(printf %s "$arg" | sed 's/^sqlite_home="//; s/"$//') ;; esac
  [ "$previous" = --output-last-message ] && output="$arg"
  previous="$arg"
done
cat > /dev/null
printf title > "$home/state_5.sqlite"
printf '{"title":"Sidebar overlap"}' > "$output"
`)
	must(t, os.Chmod(filepath.Join(bin, "codex"), 0o755))
	held := engineBinaryPath
	engineBinaryPath = func(agent.EngineCommand) (string, bool) { return filepath.Join(bin, "codex"), true }
	t.Cleanup(func() { engineBinaryPath = held })
	t.Setenv("CODEX_HOME", source)
	t.Setenv(agent.PrivateCodexHomeEnv, privateHome)
	title, err := SuggestCodexTitle(context.Background(), "Fix the sidebar overlap", nil, nil)
	must(t, err)
	if title == nil || *title != "Sidebar overlap" {
		t.Fatalf("title = %v", title)
	}
	destination, _ := realpath(privateHome)
	if filepath.Dir(filepath.Dir(destination)) != source {
		t.Fatalf("destination = %s", destination)
	}
	if got := readText(t, filepath.Join(destination, "state_5.sqlite")); got != "title" {
		t.Fatalf("database = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(destination, "log")); err == nil {
		t.Fatal("leftovers from the unprepared home are discarded")
	}
	child, err := CodexChildEnvironment(childEnvironment())
	must(t, err)
	if child["CODEX_HOME"] != destination {
		t.Fatal("Codex still starts")
	}
	if inode(t, filepath.Join(destination, ".credentials.json")) != inode(t, filepath.Join(source, ".credentials.json")) {
		t.Fatal("the credentials are one file")
	}
}

func TestAPrivateHomeWithConversationStateOnAnotherFilesystemIsNeverMoved(t *testing.T) {
	root := tempRoot(t, "codex-cross-device-state")
	source := filepath.Join(root, "shared")
	privateHome := filepath.Join(root, "app-data", "codex-private")
	foreignFilesystem(t, filepath.Join(root, "app-data"))
	must(t, os.Mkdir(source, 0o700))
	must(t, os.MkdirAll(filepath.Join(privateHome, "sessions"), 0o700))
	writeText(t, filepath.Join(privateHome, "sessions", "rollout.jsonl"), "conversation")
	if _, err := PreparePrivateCodexHome(privateHome, source); err == nil || !strings.Contains(err.Error(), "same filesystem") {
		t.Fatalf("err = %v", err)
	}
	if got := readText(t, filepath.Join(privateHome, "sessions", "rollout.jsonl")); got != "conversation" {
		t.Fatal("the conversation is kept")
	}
	if info, _ := os.Lstat(privateHome); info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the home stays where it is")
	}
}

func TestAHomeThatCannotShareSignInsNamesTheFoldersOnDifferentFilesystems(t *testing.T) {
	root := tempRoot(t, "codex-cross-device-names")
	source := filepath.Join(root, "shared")
	privateHome := filepath.Join(root, "app-data", "codex-private")
	foreign := filepath.Join(root, "app-data")
	foreignFilesystem(t, foreign)
	must(t, os.Mkdir(source, 0o700))
	must(t, os.MkdirAll(privateHome, 0o700))
	destination, err := PreparePrivateCodexHome(privateHome, source)
	must(t, err)
	// CODEX_HOME later names a folder on the app data filesystem; the moved home holds the threads.
	moved := filepath.Join(foreign, "codex")
	must(t, os.Mkdir(moved, 0o700))
	_, err = PreparePrivateCodexHome(privateHome, moved)
	want := "Codex MCP sign-ins can only be shared when " + destination + " is on the same filesystem as " + filepath.Join(moved, ".credentials.json") + "."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %s", err, want)
	}
	if resolved, _ := realpath(privateHome); resolved != destination {
		t.Fatal("the home stays where it was moved")
	}
	// Shared credentials that link to another filesystem cannot be reached from any moved home.
	otherHome := filepath.Join(root, "other-private")
	credentials := filepath.Join(foreign, "credentials.json")
	writeText(t, credentials, "{}")
	must(t, os.Remove(filepath.Join(source, ".credentials.json")))
	must(t, os.Symlink(credentials, filepath.Join(source, ".credentials.json")))
	must(t, os.Mkdir(otherHome, 0o700))
	if _, err := PreparePrivateCodexHome(otherHome, source); err == nil || !strings.HasSuffix(err.Error(), "is on the same filesystem as "+credentials+".") {
		t.Fatalf("err = %v", err)
	}
	if info, _ := os.Lstat(otherHome); info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the home is not moved when the move cannot help")
	}
}

func TestFileReferencesRemainRelativeToTheDeclaringHomeAndHookStateFollowsItsSource(t *testing.T) {
	args := PrivateHomeConfig(map[string]any{
		"model_instructions_file": "../instructions.md",
		"agents":                  map[string]any{"reviewer": map[string]any{"config_file": "custom/reviewer.toml"}},
		"hooks": map[string]any{"state": map[string]any{
			"/shared/config.toml:session_start:0:0":                    map[string]any{"trusted_hash": "sha256:original", "enabled": false},
			"/shared/.aicodingtool/home/config.toml:session_start:0:0": map[string]any{"trusted_hash": "sha256:private", "enabled": true},
		}},
	}, "/shared", "/shared/.aicodingtool/home")
	assertJSON(t, args, []string{"-c", `model_instructions_file="/instructions.md"`, "-c", `agents={ "reviewer" = { "config_file" = "/shared/custom/reviewer.toml" } }`,
		"-c", `hooks.state={ "/shared/.aicodingtool/home/config.toml:session_start:0:0" = { "trusted_hash" = "sha256:original", "enabled" = false } }`})
}

func TestKeychainConfigurationsKeepNativeLookupInTheSourceHomeAndPrivateAuthInMemory(t *testing.T) {
	root := tempRoot(t, "codex-keyring-home")
	source := filepath.Join(root, "shared")
	privateHome := filepath.Join(root, "private")
	must(t, os.Mkdir(source, 0o700))
	writeText(t, filepath.Join(source, "config.toml"), `cli_auth_credentials_store = "keyring"`)
	command, err := CodexAppServer(nil, AppServerOptions{Env: map[string]string{"PATH": os.Getenv("PATH"), "CODEX_HOME": source, agent.PrivateCodexHomeEnv: privateHome}})
	must(t, err)
	envOf := func(list []string, key string) string {
		for _, entry := range list {
			if value, ok := strings.CutPrefix(entry, key+"="); ok {
				return value
			}
		}
		return ""
	}
	if envOf(command.Env, "CODEX_HOME") != privateHome {
		t.Fatalf("CODEX_HOME = %s", envOf(command.Env, "CODEX_HOME"))
	}
	if !strings.Contains(strings.Join(command.Args, "\n"), `cli_auth_credentials_store="ephemeral"`) {
		t.Fatal("the private home keeps auth in memory")
	}
	if command.SharedAuth == nil || envOf(command.SharedAuth.Env, "CODEX_HOME") != source || envOf(command.SharedAuth.Env, "CODEX_SQLITE_HOME") != privateHome {
		t.Fatalf("shared auth = %+v", command.SharedAuth)
	}
}

func TestPrivateHomeConflictsFailWithoutOverwritingCredentials(t *testing.T) {
	root := tempRoot(t, "codex-home-conflict")
	source := filepath.Join(root, "shared")
	privateHome := filepath.Join(root, "private")
	must(t, os.Mkdir(source, 0o700))
	must(t, os.Mkdir(privateHome, 0o700))
	writeText(t, filepath.Join(privateHome, "auth.json"), "keep credentials")
	if _, err := PreparePrivateCodexHome(privateHome, source); err == nil || !regexp.MustCompile(`not a link`).MatchString(err.Error()) {
		t.Fatalf("err = %v", err)
	}
	if readText(t, filepath.Join(privateHome, "auth.json")) != "keep credentials" {
		t.Fatal("the credentials are kept")
	}
	if _, err := PreparePrivateCodexHome(source, source); err == nil || !strings.Contains(err.Error(), "must be separate") {
		t.Fatalf("err = %v", err)
	}
	alias := filepath.Join(root, "alias")
	must(t, os.Symlink(source, alias))
	if _, err := PreparePrivateCodexHome(alias, source); err == nil || !strings.Contains(err.Error(), "must be separate") {
		t.Fatalf("err = %v", err)
	}
}

func TestEncryptedMcpCredentialsCannotBeShared(t *testing.T) {
	root := tempRoot(t, "codex-encrypted")
	source := filepath.Join(root, "shared")
	must(t, os.MkdirAll(filepath.Join(source, "secrets"), 0o700))
	writeText(t, filepath.Join(source, "config.toml"), "[features]\nsecret_auth_storage = true\n")
	writeText(t, filepath.Join(source, "secrets", "mcp_oauth.age"), "sealed")
	_, err := CodexAppServer(nil, AppServerOptions{Env: map[string]string{"CODEX_HOME": source, agent.PrivateCodexHomeEnv: filepath.Join(root, "private")}})
	if err == nil || !strings.HasPrefix(err.Error(), "Codex's encrypted MCP credential store cannot be shared across homes.") {
		t.Fatalf("err = %v", err)
	}
}
