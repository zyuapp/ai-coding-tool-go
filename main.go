package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/desktop"
)

//go:embed all:frontend/dist
var assets embed.FS

// channel is "release" in the installed app, which takes the Electron app's name and profile.
var channel = "dev"

// profileDir is ACT_PROFILE_DIR, or the build's own profile under the user config directory.
func profileDir() (string, error) {
	if dir := os.Getenv("ACT_PROFILE_DIR"); dir != "" {
		return dir, nil
	}
	name := "AI Coding Tool Go Dev"
	if channel == "release" {
		name = "AI Coding Tool"
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, name), nil
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

func run() error {
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		return err
	}
	profile, err := profileDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(profile, 0o755); err != nil {
		return err
	}
	options := desktop.Options{Assets: dist, ProfileDir: profile, AppName: "AI Coding Tool", Size: windowSize()}
	// The fixture stands in for the workspace runtime: a saved state (scripts/fixture-state.mjs).
	if file := os.Getenv("ACT_FIXTURE_STATE"); file != "" {
		fixture, err := desktop.NewFixture(file, os.Getenv("ACT_FIXTURE_ATTACHMENTS"))
		if err != nil {
			return fmt.Errorf("could not load the fixture: %w", err)
		}
		options.Workspace, options.Calls, options.Images = fixture, fixture, fixture
	}
	return desktop.New(options).Run()
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
