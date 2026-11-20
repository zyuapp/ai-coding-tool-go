package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
	"golang.org/x/sys/unix"
)

const (
	// Long enough for a cold binary to answer, short enough that the model menu is not held open on one.
	versionTimeout = 10 * time.Second
	// Long enough for a package manager to download and replace a release.
	upgradeTimeout = 10 * time.Minute
	// Releases come out daily at most, so an hour old answer about the newest one is fresh enough.
	latestTTL            = time.Hour
	latestTimeout        = 4 * time.Second
	homebrewInfoTimeout  = 10 * time.Second
	maxLauncherBytes     = 16 * 1024
	homebrewInfoMaxBytes = 1024 * 1024
)

var (
	// The search path the user's own shell has.
	searchPath  = platform.SearchPath
	npmRegistry = "https://registry.npmjs.org/"
)

// JavaScript's encodeURIComponent, for the package names npm publishes under.
func encodeURIComponent(text string) string {
	return strings.ReplaceAll(url.QueryEscape(text), "+", "%20")
}

func isExecutable(candidate string) bool {
	return unix.Access(candidate, unix.X_OK) == nil
}

// Where an engine's command sits, or false when the user has not installed it. It finds what they
// would get by typing the name in their own shell.
func EngineBinaryPath(binary EngineCommand) (string, bool) {
	for _, folder := range strings.Split(searchPath(), string(os.PathListSeparator)) {
		if folder == "" {
			continue
		}
		candidate := filepath.Join(folder, binary.Command)
		if isExecutable(candidate) {
			return candidate, true
		}
	}
	return "", false
}

// How an install is brought up to date: what the user would type, and the program and arguments the app runs.
type EngineUpdater struct {
	Command string
	File    string
	Args    []string
}

type HomebrewKeg struct {
	// formula or cask
	Kind   string
	Name   string
	Prefix string
}

// <prefix>/Cellar/<name>/<version>/… or <prefix>/Caskroom/<name>/<version>/…
var homebrewKegPattern = regexp.MustCompile(`(?i)^(.*)/(cellar|caskroom)/([^/]+)/[^/]+/`)

func homebrewKeg(realPath string) *HomebrewKeg {
	match := homebrewKegPattern.FindStringSubmatch(realPath)
	if match == nil {
		return nil
	}
	kind := "cask"
	if strings.ToLower(match[2]) == "cellar" {
		kind = "formula"
	}
	return &HomebrewKeg{Kind: kind, Name: match[3], Prefix: match[1]}
}

var miseToolPattern = regexp.MustCompile(`/mise/installs/([^/]+)/[^/]+$`)

// The npm prefix a global install of the package sits under. A package nested in another one's
// node_modules belongs to a project, and one inside a mise tool other than Node is mise's.
func npmPrefix(realPath, packageName string) (string, bool) {
	index := strings.LastIndex(realPath, "/lib/node_modules/"+packageName+"/")
	if index < 0 || strings.Contains(realPath[:index], "/node_modules/") {
		return "", false
	}
	if match := miseToolPattern.FindStringSubmatch(realPath[:index]); match != nil && match[1] != "node" {
		return "", false
	}
	if index == 0 {
		return "/", true
	}
	return realPath[:index], true
}

func isMise(filePath string) bool {
	return strings.Contains(filePath, "/mise/installs/") || strings.Contains(filePath, "/mise/shims/")
}

var miseLauncherPattern = regexp.MustCompile(`\bmise\s+(?:x|exec)\b`)

// A launcher script that runs the engine through mise (`exec mise x codex -- codex`), which mise keeps.
func isMiseLauncher(filePath string) bool {
	info, err := os.Stat(filePath)
	if err != nil || info.Size() > maxLauncherBytes {
		return false
	}
	script, err := os.ReadFile(filePath)
	if err != nil {
		return false
	}
	return strings.HasPrefix(string(script), "#!") && miseLauncherPattern.Match(script)
}

// The tool beside an install, which belongs to it, or the one on the search path when there is none.
func besideOrOnPath(prefix, tool string) string {
	beside := filepath.Join(prefix, "bin", tool)
	if isExecutable(beside) {
		return beside
	}
	return tool
}

// How this install is brought up to date, or nil when the app cannot tell and leaves it to the user.
// Each package manager is only used on an install the path proves it made.
func updaterFor(binary EngineCommand, binaryPath, realPath string) *EngineUpdater {
	native := &EngineUpdater{Command: binary.Command + " update", File: binaryPath, Args: []string{"update"}}
	// A mise launcher can sit where the native install would, so it is ruled out first.
	if isMiseLauncher(realPath) {
		return nil
	}
	if prefix, ok := npmPrefix(realPath, binary.PackageName); ok {
		// npm 12 skips install scripts unless allowed, and Claude Code's finishes its install.
		return &EngineUpdater{
			Command: "npm install -g " + binary.PackageName + "@latest",
			File:    besideOrOnPath(prefix, "npm"),
			Args:    []string{"install", "-g", "--prefix", prefix, "--allow-scripts=" + binary.PackageName, binary.PackageName + "@latest"},
		}
	}
	if isMise(realPath) || isMise(binaryPath) {
		return nil
	}
	if binary.IsNative != nil && (binary.IsNative(realPath) || binary.IsNative(binaryPath)) {
		return native
	}
	if keg := homebrewKeg(realPath); keg != nil {
		args := []string{"upgrade", keg.Name}
		if keg.Kind == "cask" {
			args = []string{"upgrade", "--cask", keg.Name}
		}
		return &EngineUpdater{Command: strings.Join(append([]string{"brew"}, args...), " "), File: besideOrOnPath(keg.Prefix, "brew"), Args: args}
	}
	if strings.Contains(realPath, "/node_modules/") {
		return nil
	}
	return native
}

type InstalledEngine struct {
	Path string
	// What --version printed, or nil when the command would not answer.
	Version *string
	// How to bring this install up to date, or nil when only the user can.
	Update *EngineUpdater
	// Where its newest release is read from: the Homebrew keg it sits in, or npm when nil.
	Keg *HomebrewKeg
}

// What the app would run for an engine, with the version it reports; false when it is not installed.
func InstalledEngineOf(ctx context.Context, binary EngineCommand) (*InstalledEngine, bool) {
	binaryPath, ok := EngineBinaryPath(binary)
	if !ok {
		return nil, false
	}
	realPath := binaryPath
	if resolved, err := filepath.EvalSymlinks(binaryPath); err == nil {
		if absolute, err := filepath.Abs(resolved); err == nil {
			realPath = absolute
		}
	}
	var version *string
	if stdout, _, err := platform.Run(ctx, binaryPath, []string{"--version"}, platform.RunOptions{Timeout: versionTimeout}); err == nil {
		if read, ok := domain.ReadVersion(string(stdout)); ok {
			version = &read
		}
	}
	return &InstalledEngine{Path: binaryPath, Version: version, Update: updaterFor(binary, binaryPath, realPath), Keg: homebrewKeg(realPath)}, true
}

var stableVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

type latestRead struct {
	expires time.Time
	done    chan struct{}
	version *string
}

var latestReads = struct {
	sync.Mutex
	entries map[string]*latestRead
}{entries: map[string]*latestRead{}}

// Asked once an hour per source, failures included, so a machine offline at launch does not ask on every read.
func cachedLatest(ctx context.Context, key string, read func(ctx context.Context) (*string, error)) *string {
	latestReads.Lock()
	cached := latestReads.entries[key]
	if cached == nil || !cached.expires.After(time.Now()) {
		cached = &latestRead{expires: time.Now().Add(latestTTL), done: make(chan struct{})}
		latestReads.entries[key] = cached
		entry := cached
		go func() {
			if version, err := read(context.WithoutCancel(ctx)); err == nil {
				entry.version = version
			}
			close(entry.done)
		}()
	}
	latestReads.Unlock()
	select {
	case <-cached.done:
		return cached.version
	case <-ctx.Done():
		return nil
	}
}

func npmLatest(ctx context.Context, packageName string) (*string, error) {
	ctx, cancel := context.WithTimeout(ctx, latestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, npmRegistry+encodeURIComponent(packageName)+"/latest", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, nil
	}
	var body struct {
		Version any `json:"version"`
	}
	if err := jsonx.UnmarshalRead(response.Body, &body); err != nil {
		return nil, err
	}
	if version, ok := body.Version.(string); ok && stableVersion.MatchString(version) {
		return &version, nil
	}
	return nil, nil
}

// A cask's version can carry a build after a comma (1.2.3,456).
func homebrewLatest(ctx context.Context, keg HomebrewKeg) (*string, error) {
	stdout, _, err := platform.Run(ctx, besideOrOnPath(keg.Prefix, "brew"), []string{"info", "--json=v2", keg.Name}, platform.RunOptions{Timeout: homebrewInfoTimeout, MaxBuffer: homebrewInfoMaxBytes})
	if err != nil {
		return nil, err
	}
	var info struct {
		Formulae []struct {
			Versions struct {
				Stable *string `json:"stable"`
			} `json:"versions"`
		} `json:"formulae"`
		Casks []struct {
			Version *string `json:"version"`
		} `json:"casks"`
	}
	if err := jsonx.Unmarshal(stdout, &info); err != nil {
		return nil, err
	}
	var version string
	if keg.Kind == "formula" {
		if len(info.Formulae) > 0 && info.Formulae[0].Versions.Stable != nil {
			version = *info.Formulae[0].Versions.Stable
		}
	} else if len(info.Casks) > 0 && info.Casks[0].Version != nil {
		version, _, _ = strings.Cut(*info.Casks[0].Version, ",")
	}
	if version != "" && stableVersion.MatchString(version) {
		return &version, nil
	}
	return nil, nil
}

// The newest release this install could move to, or nil when it cannot be read. Homebrew lags npm by
// hours, so a Homebrew install is compared with what brew upgrade can deliver.
func LatestEngineVersion(ctx context.Context, binary EngineCommand, installed *InstalledEngine) *string {
	if keg := installed.Keg; keg != nil {
		return cachedLatest(ctx, "brew:"+keg.Name, func(ctx context.Context) (*string, error) { return homebrewLatest(ctx, *keg) })
	}
	return cachedLatest(ctx, "npm:"+binary.PackageName, func(ctx context.Context) (*string, error) { return npmLatest(ctx, binary.PackageName) })
}

var (
	errorLine       = regexp.MustCompile(`(?i)error`)
	trailingPeriods = regexp.MustCompile(`[.\s]+$`)
)

// Brings an installed engine up to date the way the user installed it. Fails with what the command
// said, and the command to run by hand; the caller names the engine.
func UpgradeEngine(ctx context.Context, binary EngineCommand) error {
	installed, ok := InstalledEngineOf(ctx, binary)
	if !ok {
		return fmt.Errorf("%s is not installed.", binary.Command)
	}
	if installed.Update == nil {
		return errors.New("The app cannot tell how it was installed. Update it the way you installed it.")
	}
	_, _, err := platform.Run(ctx, installed.Update.File, installed.Update.Args, platform.RunOptions{Timeout: upgradeTimeout})
	if err == nil {
		return nil
	}
	// Package managers sign off with where their log went, so the line naming the error says more than the last one.
	var stderr string
	var runError *platform.RunError
	if errors.As(err, &runError) {
		stderr = string(runError.Stderr)
	}
	var lines []string
	for _, line := range strings.Split(stderr, "\n") {
		if trimmed := jsTrim(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	output := ""
	for _, line := range lines {
		if errorLine.MatchString(line) {
			output = line
			break
		}
	}
	if output == "" && len(lines) > 0 {
		output = lines[len(lines)-1]
	}
	prefix := ""
	if output != "" {
		prefix = trailingPeriods.ReplaceAllString(output, "") + ". "
	}
	return fmt.Errorf("%sRun `%s` in your terminal.", prefix, installed.Update.Command)
}
