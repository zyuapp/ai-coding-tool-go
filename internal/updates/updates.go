// Package updates is the app's own updates from its GitHub releases, the `aic` command, and the
// record of threads an update cut off.
package updates

import (
	"context"
	"errors"
	"log"
	"math"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

const (
	releasesURL   = Releases + "/latest"
	checkInterval = time.Hour
)

// MessageBox is a dialog over the window, as Electron's showMessageBox takes it.
type MessageBox struct {
	// Type is "info" or "warning".
	Type      string
	Title     string
	Message   string
	Detail    string
	Buttons   []string
	DefaultID int
	CancelID  int
}

type UpdateHost struct {
	// Dialog shows a box over the window and answers the button pressed; false when there is no
	// window to show it on. Read when a dialog is shown, so a replaced window still gets it.
	Dialog func(box MessageBox) (response int, shown bool)
	// OnInstall is told the version before the app quits to install it, so the shutdown is not read
	// as a restart request.
	OnInstall  func(version string)
	OnChecking func(checking bool)
	OnState    func(update domain.AppUpdate)
}

// CheckResult is what a check found.
type CheckResult struct {
	IsUpdateAvailable bool
}

// Updater finds, downloads and installs builds, as electron-updater's AppUpdater does for Electron.
// It reports through the UpdaterEvents it was made with.
type Updater interface {
	// CheckForUpdates answers nil when the update service is unavailable.
	CheckForUpdates(ctx context.Context) (*CheckResult, error)
	DownloadUpdate(ctx context.Context) error
	QuitAndInstall() error
}

// UpdaterEvents are the AppUpdater events this app listens to.
type UpdaterEvents struct {
	Error            func(err error)
	UpdateAvailable  func(version string)
	DownloadProgress func(percent float64)
	UpdateDownloaded func(version string)
	// AppImageFilenameUpdated fires after an install moved the AppImage to a new name.
	AppImageFilenameUpdated func(destination string)
}

type Options struct {
	Version string
	// Packaged is an installed build rather than one run from source.
	Packaged bool
	// AutomaticUpdates is whether this install can replace itself.
	AutomaticUpdates bool
	// AppImage is the image this process runs from, "" when none.
	AppImage string
	Home     string
	// DataHome is XDG_DATA_HOME.
	DataHome     string
	NewUpdater   func(events UpdaterEvents) Updater
	OpenExternal func(url string) error
	// Recovery is how to update by hand; platform.ManualUpdateRecovery when empty.
	Recovery string
}

type updateCheck struct {
	done          chan struct{}
	err           error
	userRequested bool
	pending       bool
}

// Updates is the app's update state, asked and answered with dialogs on this screen.
type Updates struct {
	host     UpdateHost
	options  Options
	interval time.Duration

	mu       sync.Mutex
	updater  Updater
	checking *updateCheck
	state    domain.AppUpdate
	// Until the download call settles, which on macOS is after the downloaded event fires.
	downloading bool
	// A failed background check stays in the log; one the user asked for is theirs to hear about.
	announceFailure bool
	confirming      bool
	// The version an unattended install is putting in place, until the app quits for it.
	unattendedInstall *string
}

func New(host UpdateHost, options Options) *Updates {
	if options.Recovery == "" {
		options.Recovery = platform.ManualUpdateRecovery
	}
	if options.OpenExternal == nil {
		options.OpenExternal = platform.OpenExternal
	}
	return &Updates{host: host, options: options, interval: checkInterval, state: domain.NoAppUpdate}
}

func (u *Updates) updaterFor() Updater {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.updater != nil {
		return u.updater
	}
	events := UpdaterEvents{
		Error:            u.onError,
		UpdateAvailable:  u.onUpdateAvailable,
		DownloadProgress: u.onDownloadProgress,
		UpdateDownloaded: func(version string) { u.setState(domain.AppUpdateReady{Version: version}) },
	}
	if u.options.AppImage != "" {
		launchers := AppImageLaunchers{AppImage: u.options.AppImage, Home: u.options.Home, DataHome: u.options.DataHome}
		events.AppImageFilenameUpdated = func(destination string) { RepairAppImageLaunchers(launchers, destination) }
	}
	u.updater = u.options.NewUpdater(events)
	return u.updater
}

func (u *Updates) onError(err error) {
	log.Printf("Update error: %v", err)
	u.mu.Lock()
	// Check failures are reported by the shared check, including setup failures that never emit an
	// updater event, and download failures by DownloadUpdate. Install errors still arrive here.
	if u.downloading {
		u.mu.Unlock()
		return
	}
	// An install nobody watched offers its version again, which the computer that asked reports.
	if u.unattendedInstall != nil {
		u.mu.Unlock()
		u.failUnattendedInstall()
		return
	}
	announce := (u.checking == nil || !u.checking.pending) && u.announceFailure
	u.mu.Unlock()
	if announce {
		go u.ReportUpdateFailure(err, "install")
	}
}

func (u *Updates) onUpdateAvailable(version string) {
	u.mu.Lock()
	// A check after the download has started finds the same version again.
	switch u.state.(type) {
	case domain.AppUpdateDownloading, domain.AppUpdateReady:
		u.mu.Unlock()
		return
	}
	userRequested := u.checking != nil && u.checking.userRequested
	u.mu.Unlock()
	u.setState(domain.AppUpdateAvailable{Version: version})
	if userRequested {
		go u.DownloadUpdate(context.Background(), false)
	}
}

func (u *Updates) onDownloadProgress(percent float64) {
	whole := math.Min(100, math.Floor(percent))
	u.mu.Lock()
	downloading, ok := u.state.(domain.AppUpdateDownloading)
	u.mu.Unlock()
	if ok && whole != downloading.Percent {
		downloading.Percent = whole
		u.setState(downloading)
	}
}

func (u *Updates) setState(next domain.AppUpdate) {
	u.mu.Lock()
	u.state = next
	u.mu.Unlock()
	u.host.OnState(next)
}

// AppUpdate is where a newer build stands.
func (u *Updates) AppUpdate() domain.AppUpdate {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state
}

// AppBuild is the build this app runs, and whether it replaces itself.
func (u *Updates) AppBuild() domain.AppBuild {
	return domain.AppBuild{Version: u.options.Version, SelfUpdating: u.options.Packaged && u.options.AutomaticUpdates}
}

func (u *Updates) holding() bool {
	switch u.AppUpdate().(type) {
	case domain.AppUpdateDownloading, domain.AppUpdateReady:
		return true
	}
	return false
}

// StartUpdateChecks checks now and every hour after, skipping while an update is downloading or
// waiting on a restart. It answers how to stop the checks.
func (u *Updates) StartUpdateChecks() (stop func()) {
	check := func() {
		if u.holding() {
			return
		}
		if err := u.CheckForUpdates(context.Background(), false); err != nil {
			log.Printf("Update check failed: %v", err)
		}
	}
	go check()
	ticker := time.NewTicker(u.interval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				check()
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			ticker.Stop()
			close(done)
		})
	}
}

// CheckForUpdates checks once. A check the user asked for says what it found and downloads a newer
// build; a ready build is offered for install instead. Checks running together share one answer.
func (u *Updates) CheckForUpdates(ctx context.Context, userRequested bool) error {
	if !u.options.Packaged {
		if userRequested {
			u.reportSourceCopy()
		}
		return nil
	}
	if !u.options.AutomaticUpdates {
		if userRequested {
			u.reportManualLinuxUpdates()
		}
		return nil
	}
	u.mu.Lock()
	if _, ready := u.state.(domain.AppUpdateReady); userRequested && ready {
		u.mu.Unlock()
		u.ConfirmInstall()
		return nil
	}
	if running := u.checking; running != nil {
		spin := false
		if userRequested && !running.userRequested {
			running.userRequested = true
			spin = running.pending
		}
		u.mu.Unlock()
		if spin && u.host.OnChecking != nil {
			u.host.OnChecking(true)
		}
		<-running.done
		return running.err
	}
	check := &updateCheck{done: make(chan struct{}), userRequested: userRequested, pending: true}
	u.checking = check
	u.mu.Unlock()
	if userRequested && u.host.OnChecking != nil {
		u.host.OnChecking(true)
	}
	finishProgress := func() {
		u.mu.Lock()
		if !check.pending {
			u.mu.Unlock()
			return
		}
		check.pending = false
		requested := check.userRequested
		u.mu.Unlock()
		if requested && u.host.OnChecking != nil {
			u.host.OnChecking(false)
		}
	}
	defer func() {
		finishProgress()
		u.mu.Lock()
		u.checking = nil
		u.mu.Unlock()
		close(check.done)
	}()
	result, err := u.updaterFor().CheckForUpdates(ctx)
	if err == nil && result == nil {
		err = errors.New("The update service is unavailable. Please try again.")
	}
	finishProgress()
	u.mu.Lock()
	requested := check.userRequested
	u.mu.Unlock()
	if err == nil {
		if requested && !result.IsUpdateAvailable {
			u.reportUpToDate()
		}
		return nil
	}
	if !requested {
		check.err = err
		return err
	}
	u.ReportUpdateFailure(err, "check")
	return nil
}

// DownloadUpdate downloads the update a check found. A failed download offers it again. An
// unattended one, asked from another computer, says nothing on this screen: the computer that asked reports it.
func (u *Updates) DownloadUpdate(ctx context.Context, unattended bool) {
	u.mu.Lock()
	available, ok := u.state.(domain.AppUpdateAvailable)
	updater := u.updater
	if !ok || updater == nil {
		u.mu.Unlock()
		return
	}
	// An unattended download, and the install it leads to, keep their errors off this screen.
	u.announceFailure = !unattended
	u.mu.Unlock()
	u.setState(domain.AppUpdateDownloading{Version: available.Version, Percent: 0})
	u.mu.Lock()
	u.downloading = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.downloading = false
		u.mu.Unlock()
	}()
	if err := updater.DownloadUpdate(ctx); err != nil {
		u.setState(domain.AppUpdateAvailable{Version: available.Version})
		if unattended {
			log.Printf("Update download failed: %v", err)
		} else {
			u.ReportUpdateFailure(err, "install")
		}
	}
}

// InstallUpdate quits and installs the downloaded update.
func (u *Updates) InstallUpdate(unattended bool) {
	u.mu.Lock()
	ready, ok := u.state.(domain.AppUpdateReady)
	if !ok {
		u.mu.Unlock()
		return
	}
	if unattended {
		version := ready.Version
		u.unattendedInstall = &version
	}
	updater := u.updater
	u.mu.Unlock()
	u.host.OnInstall(ready.Version)
	if updater == nil {
		return
	}
	if err := updater.QuitAndInstall(); err != nil {
		log.Printf("Update install failed: %v", err)
		if unattended {
			u.failUnattendedInstall()
		}
	}
}

func (u *Updates) failUnattendedInstall() {
	u.mu.Lock()
	version := u.unattendedInstall
	u.unattendedInstall = nil
	u.mu.Unlock()
	if version != nil {
		u.setState(domain.AppUpdateAvailable{Version: *version})
	}
}

// ConfirmInstall asks before restarting into the downloaded update.
func (u *Updates) ConfirmInstall() {
	u.mu.Lock()
	ready, ok := u.state.(domain.AppUpdateReady)
	if !ok || u.confirming {
		u.mu.Unlock()
		return
	}
	u.confirming = true
	u.mu.Unlock()
	response, shown := u.host.Dialog(MessageBox{
		Type:      "info",
		Title:     "Update ready",
		Message:   "AI Coding Tool " + ready.Version + " is ready to install.",
		Detail:    "Restart AI Coding Tool to finish the update.",
		Buttons:   []string{"Restart and install", "Later"},
		DefaultID: 0,
		CancelID:  1,
	})
	u.mu.Lock()
	u.confirming = false
	u.mu.Unlock()
	if shown && response == 0 {
		u.InstallUpdate(false)
	}
}

func (u *Updates) reportUpToDate() {
	u.host.Dialog(MessageBox{
		Type:    "info",
		Title:   "No update available",
		Message: "AI Coding Tool " + u.options.Version + " is the latest version.",
		Buttons: []string{"OK"},
	})
}

// reportSourceCopy: a copy run from source has no installer behind it, so the check would fail rather than find nothing.
func (u *Updates) reportSourceCopy() {
	u.host.Dialog(MessageBox{
		Type:    "info",
		Title:   "Cannot check for updates",
		Message: "This copy of AI Coding Tool runs from source.",
		Detail:  "Only the installed app updates itself.",
		Buttons: []string{"OK"},
	})
}

// reportManualLinuxUpdates: Linux packages other than the AppImage are updated the way they were installed.
func (u *Updates) reportManualLinuxUpdates() {
	response, shown := u.host.Dialog(MessageBox{
		Type:      "info",
		Title:     "Check for updates manually",
		Message:   "This Linux package is updated manually.",
		Detail:    "Download the latest package and install it the same way you installed this one.",
		Buttons:   []string{"Open downloads", "Later"},
		DefaultID: 0,
		CancelID:  1,
	})
	if shown && response == 0 {
		u.openReleases()
	}
}

func (u *Updates) openReleases() {
	if err := u.options.OpenExternal(releasesURL); err != nil {
		log.Printf("Could not open the downloads page: %v", err)
	}
}

// ReportUpdateFailure says an update the installed copy refuses, and offers the download, rather
// than stopping in the log. phase is "check" or "install".
func (u *Updates) ReportUpdateFailure(err error, phase string) {
	log.Printf("Update %s failed: %v", phase, err)
	box := MessageBox{
		Type:      "warning",
		Title:     "Update failed",
		Message:   "AI Coding Tool could not install the update.",
		Detail:    u.options.Recovery,
		Buttons:   []string{"Open downloads", "Later"},
		DefaultID: 0,
		CancelID:  1,
	}
	if phase == "check" {
		box.Title = "Update check failed"
		box.Message = "AI Coding Tool could not check for updates."
		box.Detail = "Please try again in a moment."
		box.Buttons = []string{"OK"}
		box.CancelID = 0
	}
	response, shown := u.host.Dialog(box)
	if phase == "install" && shown && response == 0 {
		u.openReleases()
	}
}
