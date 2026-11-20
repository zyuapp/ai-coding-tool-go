package updates

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// ReleaseUpdater is the Updater for this app's GitHub releases: it reads the electron-builder
// manifest of the newest release, downloads and checks the build beside what it replaces, swaps it
// in, and starts it once this process has exited.
type ReleaseUpdater struct {
	version string
	target  UpdateTarget
	source  Source
	events  UpdaterEvents
	// quit ends the app after a build is swapped in.
	quit func()
	// relaunch starts the installed build after this process exits.
	relaunch func(target UpdateTarget, installed string) error

	stopping context.Context
	stop     context.CancelFunc

	mu     sync.Mutex
	found  *ReleaseManifest
	staged *Staged
}

type ReleaseUpdaterOptions struct {
	Version string
	Target  UpdateTarget
	// Source is DefaultSource() when its Releases is empty.
	Source Source
	Quit   func()
	// Relaunch is RelaunchAfterExit when nil.
	Relaunch func(target UpdateTarget, installed string) error
}

// NewReleaseUpdater makes the updater Options.NewUpdater answers for an installed app.
func NewReleaseUpdater(options ReleaseUpdaterOptions, events UpdaterEvents) *ReleaseUpdater {
	source := options.Source
	if source.Releases == "" {
		source = DefaultSource()
	}
	relaunch := options.Relaunch
	if relaunch == nil {
		relaunch = RelaunchAfterExit
	}
	stopping, stop := context.WithCancel(context.Background())
	return &ReleaseUpdater{version: options.Version, target: options.Target, source: source, events: events, quit: options.Quit, relaunch: relaunch, stopping: stopping, stop: stop}
}

// joined cancels with either the caller or Dispose.
func (r *ReleaseUpdater) joined(ctx context.Context) (context.Context, context.CancelFunc) {
	joined, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.stopping, cancel)
	return joined, func() {
		stop()
		cancel()
	}
}

func (r *ReleaseUpdater) CheckForUpdates(ctx context.Context) (*CheckResult, error) {
	ctx, cancel := r.joined(ctx)
	defer cancel()
	manifest, err := r.source.FetchManifest(ctx, r.target)
	if err != nil {
		return nil, err
	}
	if domain.CompareVersions(manifest.Version, r.version) <= 0 {
		r.mu.Lock()
		r.found = nil
		r.mu.Unlock()
		return &CheckResult{}, nil
	}
	r.mu.Lock()
	r.found = manifest
	r.mu.Unlock()
	if r.events.UpdateAvailable != nil {
		r.events.UpdateAvailable(manifest.Version)
	}
	return &CheckResult{IsUpdateAvailable: true}, nil
}

func (r *ReleaseUpdater) DownloadUpdate(ctx context.Context) error {
	r.mu.Lock()
	manifest := r.found
	r.mu.Unlock()
	if manifest == nil {
		return errors.New("Please check update first")
	}
	ctx, cancel := r.joined(ctx)
	defer cancel()
	staged, err := r.source.Stage(ctx, r.target, manifest, func(percent float64) {
		if r.events.DownloadProgress != nil {
			r.events.DownloadProgress(percent)
		}
	})
	if err != nil {
		return err
	}
	r.mu.Lock()
	previous := r.staged
	r.staged = staged
	r.mu.Unlock()
	if previous != nil {
		discard(previous)
	}
	if r.events.UpdateDownloaded != nil {
		r.events.UpdateDownloaded(staged.Version)
	}
	return nil
}

// QuitAndInstall swaps the downloaded build in, then quits for it to start. A swap that fails is
// reported as an error event, as Squirrel's are.
func (r *ReleaseUpdater) QuitAndInstall() error {
	r.mu.Lock()
	staged := r.staged
	r.staged = nil
	r.mu.Unlock()
	if staged == nil {
		return errors.New("No update filepath provided, can't quit and install")
	}
	installed, err := Swap(r.target, staged)
	if err != nil {
		discard(staged)
		if r.events.Error != nil {
			r.events.Error(err)
		}
		return nil
	}
	if r.target.AppImage != "" && installed != r.target.AppImage && r.events.AppImageFilenameUpdated != nil {
		r.events.AppImageFilenameUpdated(installed)
	}
	if err := r.relaunch(r.target, installed); err != nil && r.events.Error != nil {
		r.events.Error(err)
	}
	if r.quit != nil {
		r.quit()
	}
	return nil
}

// Dispose cancels a download. A build nobody installed is left for the next check to fetch again.
func (r *ReleaseUpdater) Dispose() {
	r.stop()
	r.mu.Lock()
	staged := r.staged
	r.staged = nil
	r.mu.Unlock()
	if staged != nil {
		discard(staged)
	}
}

func discard(staged *Staged) {
	_ = os.RemoveAll(staged.Cleanup)
}

// RelaunchAfterExit starts the installed build once this process has exited, so the new one claims
// the instance lock.
func RelaunchAfterExit(target UpdateTarget, installed string) error {
	if target.Bundle != "" {
		return StartAfterExit("/usr/bin/open", "-n", installed)
	}
	return StartAfterExit(installed)
}

// StartAfterExit runs a program in its own session once this process has exited.
func StartAfterExit(name string, args ...string) error {
	script := `while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; shift; exec "$@"`
	command := append([]string{"-c", script, "sh", strconv.Itoa(os.Getpid()), name}, args...)
	if !platform.LaunchDetached("/bin/sh", command...) {
		return errors.New("Could not start " + name + " after quitting.")
	}
	return nil
}
