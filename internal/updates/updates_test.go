package updates

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// fakeUpdater stands in for electron-updater: a check waits on resolve or reject unless check is set.
type fakeUpdater struct {
	events UpdaterEvents

	mu        sync.Mutex
	checks    int
	downloads int
	installs  int
	check     func() (*CheckResult, error)
	download  func() error
	settled   chan checkAnswer
}

type checkAnswer struct {
	result *CheckResult
	err    error
}

func (f *fakeUpdater) CheckForUpdates(context.Context) (*CheckResult, error) {
	f.mu.Lock()
	f.checks++
	check := f.check
	f.mu.Unlock()
	if check != nil {
		return check()
	}
	answer := <-f.settled
	return answer.result, answer.err
}

func (f *fakeUpdater) DownloadUpdate(context.Context) error {
	f.mu.Lock()
	f.downloads++
	download := f.download
	f.mu.Unlock()
	if download != nil {
		return download()
	}
	return nil
}

func (f *fakeUpdater) QuitAndInstall() error {
	f.mu.Lock()
	f.installs++
	f.mu.Unlock()
	return nil
}

func (f *fakeUpdater) count(field *int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *field
}

type fixture struct {
	t        *testing.T
	updates  *Updates
	updater  *fakeUpdater
	mu       sync.Mutex
	dialogs  []MessageBox
	response int
	states   []domain.AppUpdate
	spinning []bool
	installs []string
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, response: 1, updater: &fakeUpdater{settled: make(chan checkAnswer, 1)}}
	f.updates = New(UpdateHost{
		Dialog: func(box MessageBox) (int, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.dialogs = append(f.dialogs, box)
			return f.response, true
		},
		OnInstall:  func(version string) { f.mu.Lock(); f.installs = append(f.installs, version); f.mu.Unlock() },
		OnChecking: func(checking bool) { f.mu.Lock(); f.spinning = append(f.spinning, checking); f.mu.Unlock() },
		OnState:    func(update domain.AppUpdate) { f.mu.Lock(); f.states = append(f.states, update); f.mu.Unlock() },
	}, Options{
		Version:          "0.5.9",
		Packaged:         true,
		AutomaticUpdates: true,
		Recovery:         "Open downloads.",
		OpenExternal:     func(string) error { return nil },
		NewUpdater: func(events UpdaterEvents) Updater {
			f.updater.events = events
			return f.updater
		},
	})
	return f
}

func (f *fixture) titles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var titles []string
	for _, box := range f.dialogs {
		titles = append(titles, box.Title)
	}
	return titles
}

func (f *fixture) lastState() domain.AppUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[len(f.states)-1]
}

func (f *fixture) statesSince(index int) []domain.AppUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.states[index:])
}

// checking is whether the menu shows a check in progress.
func (f *fixture) checking() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.spinning) > 0 && f.spinning[len(f.spinning)-1]
}

func eventually(t *testing.T, held func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !held() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// offered checks in the background and finds 0.5.10.
func (f *fixture) offered() {
	f.t.Helper()
	background := make(chan error, 1)
	go func() { background <- f.updates.CheckForUpdates(context.Background(), false) }()
	eventually(f.t, func() bool { return f.updater.count(&f.updater.checks) == 1 }, "the check started")
	f.updater.events.UpdateAvailable("0.5.10")
	f.updater.settled <- checkAnswer{result: &CheckResult{IsUpdateAvailable: true}}
	if err := <-background; err != nil {
		f.t.Fatal(err)
	}
}

func TestAManualCheckJoinsABackgroundCheckAndReportsOnce(t *testing.T) {
	for _, outcome := range []string{"current", "available", "error", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			f := newFixture(t)
			done := make(chan error, 3)
			go func() { done <- f.updates.CheckForUpdates(context.Background(), false) }()
			eventually(t, func() bool { return f.updater.count(&f.updater.checks) == 1 }, "the background check started")
			for range 2 {
				go func() { done <- f.updates.CheckForUpdates(context.Background(), true) }()
			}
			eventually(t, f.checking, "the menu says it is checking")
			if f.updater.count(&f.updater.checks) != 1 {
				t.Fatal("the manual checks join the running one")
			}
			switch outcome {
			case "error":
				err := errors.New("Network unavailable")
				f.updater.events.Error(err)
				f.updater.settled <- checkAnswer{err: err}
			case "available":
				f.updater.events.UpdateAvailable("0.5.10")
				f.updater.settled <- checkAnswer{result: &CheckResult{IsUpdateAvailable: true}}
			case "unavailable":
				f.updater.settled <- checkAnswer{}
			default:
				f.updater.settled <- checkAnswer{result: &CheckResult{}}
			}
			for range 3 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			want := map[string][]string{"current": {"No update available"}, "available": nil, "error": {"Update check failed"}, "unavailable": {"Update check failed"}}[outcome]
			if got := f.titles(); !slices.Equal(got, want) {
				t.Fatalf("dialogs %v, want %v", got, want)
			}
			if outcome == "available" {
				eventually(t, func() bool { return f.updater.count(&f.updater.downloads) == 1 }, "a check the user asked for downloads what it finds")
			} else if f.updater.count(&f.updater.downloads) != 0 {
				t.Fatal("nothing to download")
			}
			if f.checking() {
				t.Fatal("the menu is enabled again")
			}
		})
	}
}

func TestBackgroundFailuresStayQuietAndALaterManualCheckSucceeds(t *testing.T) {
	f := newFixture(t)
	background := make(chan error, 1)
	go func() { background <- f.updates.CheckForUpdates(context.Background(), false) }()
	eventually(t, func() bool { return f.updater.count(&f.updater.checks) == 1 }, "started")
	f.updater.settled <- checkAnswer{err: errors.New("Network unavailable")}
	if err := <-background; err == nil || err.Error() != "Network unavailable" {
		t.Fatal(err)
	}
	if len(f.titles()) != 0 {
		t.Fatal("a background failure says nothing")
	}
	f.updater.check = func() (*CheckResult, error) { return &CheckResult{}, nil }
	if err := f.updates.CheckForUpdates(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := f.titles(); !slices.Equal(got, []string{"No update available"}) || f.checking() {
		t.Fatal(got)
	}
}

func TestUpdateErrorsShowAShortRecoveryMessage(t *testing.T) {
	f := newFixture(t)
	err := errors.New("Cannot find latest-mac.yml: HttpError: 404")
	f.updates.ReportUpdateFailure(err, "check")
	f.updates.ReportUpdateFailure(err, "install")
	got := [2][2]string{{f.dialogs[0].Title, f.dialogs[0].Detail}, {f.dialogs[1].Title, f.dialogs[1].Detail}}
	if got != [2][2]string{{"Update check failed", "Please try again in a moment."}, {"Update failed", "Open downloads."}} {
		t.Fatal(got)
	}
}

func TestADownloadReportsProgressUntilItIsReadyToInstall(t *testing.T) {
	f := newFixture(t)
	f.offered()
	if got := f.statesSince(0); !slices.Equal(got, []domain.AppUpdate{domain.AppUpdateAvailable{Version: "0.5.10"}}) {
		t.Fatal(got)
	}
	if len(f.titles()) != 0 || f.updater.count(&f.updater.downloads) != 0 {
		t.Fatal("a background check only offers the update")
	}
	f.updates.InstallUpdate(false)
	if f.updater.count(&f.updater.installs) != 0 {
		t.Fatal("nothing installs before it has downloaded")
	}
	f.updater.download = func() error {
		f.updater.events.DownloadProgress(41.2)
		f.updater.events.DownloadProgress(41.8)
		f.updater.events.UpdateDownloaded("0.5.10")
		return nil
	}
	f.updates.DownloadUpdate(context.Background(), false)
	want := []domain.AppUpdate{
		domain.AppUpdateDownloading{Version: "0.5.10", Percent: 0},
		domain.AppUpdateDownloading{Version: "0.5.10", Percent: 41},
		domain.AppUpdateReady{Version: "0.5.10"},
	}
	if got := f.statesSince(1); !slices.Equal(got, want) {
		t.Fatal(got)
	}
	f.updates.InstallUpdate(false)
	if len(f.installs) != 1 || f.updater.count(&f.updater.installs) != 1 {
		t.Fatal("installs once ready")
	}
}

func TestAFailedDownloadOffersTheUpdateAgain(t *testing.T) {
	for name, download := range map[string]func(f *fixture) error{
		"after an error event": func(f *fixture) error {
			err := errors.New("Network unavailable")
			f.updater.events.Error(err)
			return err
		},
		"before it starts": func(*fixture) error { return errors.New("Checksum is missing") },
		"after it reported downloaded": func(f *fixture) error {
			err := errors.New("Code signature did not pass validation")
			f.updater.events.UpdateDownloaded("0.5.10")
			f.updater.events.Error(err)
			f.updater.events.Error(err)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.offered()
			f.updater.download = func() error { return download(f) }
			f.updates.DownloadUpdate(context.Background(), false)
			time.Sleep(20 * time.Millisecond)
			if f.lastState() != (domain.AppUpdateAvailable{Version: "0.5.10"}) {
				t.Fatal(f.lastState())
			}
			if got := f.titles(); !slices.Equal(got, []string{"Update failed"}) {
				t.Fatalf("reported once, by the download: %v", got)
			}
		})
	}
}

func TestADownloadTheMenuCheckStartedReportsItsFailureWhileTheCheckIsPending(t *testing.T) {
	f := newFixture(t)
	f.updater.download = func() error {
		err := errors.New("ZIP file not provided")
		f.updater.events.Error(err)
		return err
	}
	manual := make(chan error, 1)
	go func() { manual <- f.updates.CheckForUpdates(context.Background(), true) }()
	eventually(t, func() bool { return f.updater.count(&f.updater.checks) == 1 }, "started")
	f.updater.events.UpdateAvailable("0.5.10")
	eventually(t, func() bool { return slices.Equal(f.titles(), []string{"Update failed"}) }, "the download reports while the check is pending")
	f.updater.settled <- checkAnswer{result: &CheckResult{IsUpdateAvailable: true}}
	if err := <-manual; err != nil {
		t.Fatal(err)
	}
	if f.lastState() != (domain.AppUpdateAvailable{Version: "0.5.10"}) || !slices.Equal(f.titles(), []string{"Update failed"}) {
		t.Fatal(f.titles())
	}
}

func TestARunningAppChecksEveryIntervalUntilAnUpdateIsDownloading(t *testing.T) {
	f := newFixture(t)
	f.updates.interval = 40 * time.Millisecond
	var mu sync.Mutex
	offer := false
	f.updater.check = func() (*CheckResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if offer {
			f.updater.events.UpdateAvailable("0.5.10")
			return &CheckResult{IsUpdateAvailable: true}, nil
		}
		return &CheckResult{}, nil
	}
	stop := f.updates.StartUpdateChecks()
	defer stop()
	eventually(t, func() bool { return f.updater.count(&f.updater.checks) >= 2 }, "checks again")
	mu.Lock()
	offer = true
	mu.Unlock()
	eventually(t, func() bool { _, ok := f.updates.AppUpdate().(domain.AppUpdateAvailable); return ok }, "offered")
	block := make(chan struct{})
	defer close(block)
	f.updater.download = func() error { <-block; return nil }
	go f.updates.DownloadUpdate(context.Background(), false)
	eventually(t, func() bool { _, ok := f.updates.AppUpdate().(domain.AppUpdateDownloading); return ok }, "downloading")
	time.Sleep(60 * time.Millisecond)
	checks := f.updater.count(&f.updater.checks)
	time.Sleep(150 * time.Millisecond)
	if f.updater.count(&f.updater.checks) != checks {
		t.Fatal("no check runs while the update downloads")
	}
	if len(f.titles()) != 0 {
		t.Fatal(f.titles())
	}
}

func TestRestartingIntoADownloadedUpdateAsksFirst(t *testing.T) {
	f := newFixture(t)
	f.offered()
	f.updater.download = func() error { f.updater.events.UpdateDownloaded("0.5.10"); return nil }
	f.updates.DownloadUpdate(context.Background(), false)
	f.updates.ConfirmInstall()
	if !slices.Equal(f.titles(), []string{"Update ready"}) || f.updater.count(&f.updater.installs) != 0 {
		t.Fatal("Later keeps the app running")
	}
	f.response = 0
	f.updates.ConfirmInstall()
	if len(f.installs) != 1 || f.updater.count(&f.updater.installs) != 1 {
		t.Fatal("restart and install")
	}
	if err := f.updates.CheckForUpdates(context.Background(), true); err != nil || f.titles()[len(f.titles())-1] != "Update ready" {
		t.Fatal("a check the user asks for once it is ready offers the install")
	}
}

func TestAnUnattendedInstallThatFailsOffersItsVersionAgain(t *testing.T) {
	f := newFixture(t)
	f.offered()
	f.updater.download = func() error { f.updater.events.UpdateDownloaded("0.5.10"); return nil }
	f.updates.DownloadUpdate(context.Background(), true)
	f.updates.InstallUpdate(true)
	f.updater.events.Error(errors.New("could not move the app"))
	if f.lastState() != (domain.AppUpdateAvailable{Version: "0.5.10"}) || len(f.titles()) != 0 {
		t.Fatal("the computer that asked reports it, not this screen")
	}
}

func TestSourceAndManualCopiesSayWhyTheyCannotCheck(t *testing.T) {
	f := newFixture(t)
	f.updates.options.Packaged = false
	_ = f.updates.CheckForUpdates(context.Background(), false)
	_ = f.updates.CheckForUpdates(context.Background(), true)
	f.updates.options.Packaged = true
	f.updates.options.AutomaticUpdates = false
	opened := ""
	f.updates.options.OpenExternal = func(url string) error { opened = url; return nil }
	f.response = 0
	_ = f.updates.CheckForUpdates(context.Background(), true)
	if !slices.Equal(f.titles(), []string{"Cannot check for updates", "Check for updates manually"}) || opened != "https://github.com/zyuapp/ai-coding-tool-go/releases/latest" {
		t.Fatal(f.titles(), opened)
	}
	if f.updates.AppBuild() != (domain.AppBuild{Version: "0.5.9"}) {
		t.Fatal("a copy that cannot install does not update itself")
	}
}

func TestTheDesktopPortSkipsAQuietCheckWhileAnUpdateIsHeld(t *testing.T) {
	f := newFixture(t)
	f.offered()
	f.updater.download = func() error { f.updater.events.UpdateDownloaded("0.5.10"); return nil }
	f.updates.DownloadUpdate(context.Background(), false)
	launched := "0.5.9"
	port := NewDesktop(DesktopHost{Updates: f.updates, LaunchedUpdate: &launched})
	quiet := true
	port.CheckForUpdates(&quiet)
	time.Sleep(20 * time.Millisecond)
	if f.updater.count(&f.updater.checks) != 1 {
		t.Fatal("a quiet check leaves a ready update alone")
	}
	first, _ := port.LaunchedUpdate(context.Background())
	second, _ := port.LaunchedUpdate(context.Background())
	if first == nil || *first != "0.5.9" || second != nil {
		t.Fatal("answered once")
	}
}
