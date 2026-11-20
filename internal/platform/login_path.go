package platform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// loginShellTimeout leaves room for a shell with heavy start-up files without holding the window back on one.
const loginShellTimeout = 5 * time.Second

// loginShellMaxOutput is `execFile`'s default output ceiling.
const loginShellMaxOutput = 1024 * 1024

// environmentMark is printed either side of the environment, so whatever a start-up file prints of its own is skipped.
const environmentMark = "__aic_environment__"

var loginShellArguments = []string{"-ilc", "printf '" + environmentMark + "'; /usr/bin/env; printf '" + environmentMark + "'"}

// toolFolders are where a Mac keeps tools that launchd's own search path never lists, in the order a shell looks.
func toolFolders(home string) []string {
	return []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", filepath.Join(home, ".local", "bin")}
}

func installedToolFolders() string {
	home, _ := os.UserHomeDir()
	var present []string
	for _, folder := range toolFolders(home) {
		if _, err := os.Stat(folder); err == nil {
			present = append(present, folder)
		}
	}
	return strings.Join(present, string(os.PathListSeparator))
}

// SearchPathFromEnvironment reads the search path a shell reported off the `PATH=` line of the
// environment between the marks. Empty when the shell said nothing usable.
func SearchPathFromEnvironment(output string) string {
	start := strings.Index(output, environmentMark)
	if start < 0 {
		return ""
	}
	body := output[start+len(environmentMark):]
	end := strings.Index(body, environmentMark)
	if end < 0 {
		return ""
	}
	for line := range strings.SplitSeq(body[:end], "\n") {
		if value, ok := strings.CutPrefix(line, "PATH="); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// MergeSearchPaths joins several search paths into one, first mention winning, so the order the
// shell gave is the order kept.
func MergeSearchPaths(paths ...string) string {
	var folders []string
	seen := map[string]bool{}
	for _, value := range paths {
		for folder := range strings.SplitSeq(value, string(os.PathListSeparator)) {
			if folder != "" && !seen[folder] {
				seen[folder] = true
				folders = append(folders, folder)
			}
		}
	}
	return strings.Join(folders, string(os.PathListSeparator))
}

// searchPath is the path children get. Until the login shell answers it is what this process was
// started with, then the standard tool folders. This process's own environment is never changed.
var searchPath = struct {
	sync.RWMutex
	value   string
	init    sync.Once
	pending chan struct{}
}{}

func heldSearchPath() string {
	searchPath.init.Do(func() {
		searchPath.value = MergeSearchPaths(os.Getenv("PATH"), installedToolFolders())
	})
	searchPath.RLock()
	defer searchPath.RUnlock()
	return searchPath.value
}

// SearchPath is the search path children get.
func SearchPath() string {
	return heldSearchPath()
}

// awaitSearchPath waits for an adoption in progress, so a child started meanwhile is found where the
// user's shell finds it.
func awaitSearchPath(ctx context.Context) {
	searchPath.RLock()
	pending := searchPath.pending
	searchPath.RUnlock()
	if pending == nil {
		return
	}
	select {
	case <-pending:
	case <-ctx.Done():
	}
}

// AdoptLoginShellPath asks the user's login shell for its search path and holds the answer: the
// shell's leads, the one held so far follows, and the standard tool folders last. macOS starts an app
// from the Dock with four system folders and nothing else, so without this every command the app
// runs is looked for where the user installed nothing. Children started while it runs wait for it.
func AdoptLoginShellPath(ctx context.Context) {
	current := heldSearchPath()
	done := make(chan struct{})
	searchPath.Lock()
	if searchPath.pending == nil {
		searchPath.pending = done
	}
	mine := searchPath.pending == done
	searchPath.Unlock()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	reported := SearchPathFromEnvironment(loginShellEnvironment(ctx, shell, current))
	merged := MergeSearchPaths(reported, current, installedToolFolders())

	searchPath.Lock()
	searchPath.value = merged
	if mine {
		searchPath.pending = nil
		close(done)
	}
	searchPath.Unlock()
}

// loginShellEnvironment runs the shell login and interactive, because a search path can be written in
// the files only one reads. A start-up file that fails afterwards has already printed the
// environment, and the marks find it.
func loginShellEnvironment(ctx context.Context, shell, path string) string {
	ctx, cancel := context.WithTimeout(ctx, loginShellTimeout)
	defer cancel()
	command := exec.Command(shell, loginShellArguments...)
	command.Env = overlayEnv(os.Environ(), append([]string{"PATH=" + path}, childVariables()...))
	output := newBoundedBuffer(loginShellMaxOutput)
	command.Stdout = output
	command.WaitDelay = time.Second
	if loginShellOwnSession {
		ownSession(command)
	}
	if err := command.Start(); err != nil {
		return ""
	}
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stopLoginShell(command.Process.Pid)
		case <-output.full:
			stopLoginShell(command.Process.Pid)
		case <-finished:
		}
	}()
	_ = command.Wait()
	close(finished)
	return output.String()
}
