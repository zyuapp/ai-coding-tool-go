package main

import (
	"context"
	"embed"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/computers"
	"github.com/zyuapp/ai-coding-tool-go/internal/desktop"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
	"github.com/zyuapp/ai-coding-tool-go/internal/services"
	"github.com/zyuapp/ai-coding-tool-go/internal/store"
	"github.com/zyuapp/ai-coding-tool-go/internal/updates"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed wails.json
var project []byte

// version is the build's, as wails.json names it.
func version() string {
	var config struct {
		Info struct {
			ProductVersion string `json:"productVersion"`
		} `json:"info"`
	}
	_ = json.Unmarshal(project, &config)
	return config.Info.ProductVersion
}

// resources is the installed app's resources folder beside this binary, or "" from source.
func resources() string {
	if !platform.Packaged() {
		return ""
	}
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	binary := filepath.Dir(executable)
	for _, candidate := range []string{filepath.Join(binary, "..", "Resources"), filepath.Join(binary, "resources")} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return filepath.Clean(candidate)
		}
	}
	return ""
}

// profile is ACT_PROFILE_DIR, read in place, or the build's own profile, which a source build's first
// launch fills from the Electron dev profile.
func profile() (platform.Profile, []string, error) {
	home, _ := os.UserHomeDir()
	appData := platform.AppDataDir()
	packaged := platform.Packaged()
	own := platform.AppProfile(appData, home, packaged)
	if dir := os.Getenv("ACT_PROFILE_DIR"); dir != "" {
		own.UserData = dir
		return own, legacyRoots(own), os.MkdirAll(dir, 0o755)
	}
	prepared, report, err := platform.PrepareProfile(appData, home, packaged, version(), store.ProfileDatabase{})
	if err != nil {
		return prepared, nil, err
	}
	if report != nil {
		log.Printf("Copied %d threads and %d projects from %s.", report.Threads, report.Projects, platform.ElectronDevProfileName)
	}
	return prepared, legacyRoots(prepared), nil
}

// legacyRoots are the other roots this profile's checkouts may be under: its own old folder, and the
// roots of the profile it was imported from.
func legacyRoots(profile platform.Profile) []string {
	roots := []string{}
	for _, root := range append([]string{filepath.Join(profile.UserData, "worktrees")}, platform.ImportedWorktreesRoots(profile.UserData)...) {
		if root != profile.WorktreesRoot {
			roots = append(roots, root)
		}
	}
	return roots
}

// windowSize reads ACT_WINDOW_SIZE ("WxH"), which opens the window at that size.
func windowSize() *[2]int {
	width, height, ok := strings.Cut(os.Getenv("ACT_WINDOW_SIZE"), "x")
	if !ok {
		return nil
	}
	w, errW := strconv.Atoi(width)
	h, errH := strconv.Atoi(height)
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return nil
	}
	return &[2]int{w, h}
}

func serve(argv []string) int {
	config := services.Config{Packaged: platform.Packaged(), Resources: resources(), AppPath: services.SourceRoot(), Version: version()}
	return computers.ServeMain(argv, version(), func(ctx context.Context, options computers.ServeOptions) (*computers.Served, error) {
		options.Build = services.ServeBuild(config)
		options.RecordUpdateRelaunch = updates.RecordUpdateRelaunch
		return computers.StartServe(ctx, options)
	})
}

// fixture runs the window over one saved state (scripts/fixture-state.mjs), for work on the UI alone.
func fixture(dist fs.FS, file string) error {
	held, err := desktop.NewFixture(file, os.Getenv("ACT_FIXTURE_ATTACHMENTS"))
	if err != nil {
		return fmt.Errorf("could not load the fixture: %w", err)
	}
	dir, err := os.MkdirTemp("", "aicodingtool-fixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	return desktop.New(desktop.Options{Assets: dist, ProfileDir: dir, AppName: platform.AppName, Size: windowSize(), Workspace: held, Calls: held, Images: held}).Run()
}

func run() error {
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		return err
	}
	if file := os.Getenv("ACT_FIXTURE_STATE"); file != "" {
		return fixture(dist, file)
	}
	profile, legacy, err := profile()
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	lock, forwarded, err := platform.ClaimDesktopLock(profile.UserData, platform.Launch{Dir: cwd, Argv: os.Args})
	if forwarded {
		fmt.Printf("%s is already running. Bringing that window forward instead of starting a second one.\n", profile.Name)
		return nil
	}
	if errors.Is(err, platform.ErrProfileInUse) {
		return fmt.Errorf("%s is open in another process", profile.UserData)
	}
	if err != nil {
		return err
	}
	if serving := platform.ServingProcess(profile.UserData); serving != 0 {
		lock.Close()
		return fmt.Errorf("`aic serve` is running from this data folder (process %d). Stop it before opening the app", serving)
	}
	return desktop.Launch(desktop.LaunchOptions{
		Assets:      dist,
		Profile:     profile,
		LegacyRoots: legacy,
		Size:        windowSize(),
		Version:     version(),
		Packaged:    platform.Packaged(),
		Resources:   resources(),
		AppPath:     services.SourceRoot(),
		Lock:        lock,
	})
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "serve" || os.Args[1] == "pair") {
		os.Exit(serve(os.Args[1:]))
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
