package updates

import (
	"context"
	"log"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// answeredOnce is a value answered once, then nil from then on.
func answeredOnce(value *string) func(context.Context) (*string, error) {
	var mu sync.Mutex
	held := value
	return func(context.Context) (*string, error) {
		mu.Lock()
		defer mu.Unlock()
		answer := held
		held = nil
		return answer, nil
	}
}

// DesktopHost is what the update port is served from on a desktop.
type DesktopHost struct {
	Updates *Updates
	// OnAppUpdate subscribes to the desktop's "update:changed" event.
	OnAppUpdate func(listener func(update domain.AppUpdate)) func()
	// LaunchedUpdate is the version this launch updated the app to, nil when it is not the first launch since one.
	LaunchedUpdate *string
}

type desktopUpdates struct {
	host     DesktopHost
	launched func(context.Context) (*string, error)
}

// NewDesktop serves the app's own updates, asked and answered with dialogs on this screen.
func NewDesktop(host DesktopHost) contracts.UpdatePortRuntime {
	return desktopUpdates{host: host, launched: answeredOnce(host.LaunchedUpdate)}
}

func set(flag *bool) bool { return flag != nil && *flag }

func (d desktopUpdates) CheckForUpdates(quiet *bool) {
	if set(quiet) && d.host.Updates.holding() {
		return
	}
	go func() {
		if err := d.host.Updates.CheckForUpdates(context.Background(), !set(quiet)); err != nil {
			log.Printf("Update check failed: %v", err)
		}
	}()
}

func (d desktopUpdates) AppUpdate(context.Context) (domain.AppUpdate, error) {
	return d.host.Updates.AppUpdate(), nil
}

func (d desktopUpdates) OnAppUpdate(listener func(update domain.AppUpdate)) func() {
	return d.host.OnAppUpdate(listener)
}

func (d desktopUpdates) LaunchedUpdate(ctx context.Context) (*string, error) { return d.launched(ctx) }

func (d desktopUpdates) AppBuild(context.Context) (*domain.AppBuild, error) {
	build := d.host.Updates.AppBuild()
	return &build, nil
}

func (d desktopUpdates) DownloadUpdate(unattended *bool) {
	go d.host.Updates.DownloadUpdate(context.Background(), set(unattended))
}

func (d desktopUpdates) InstallUpdate(unattended *bool) {
	if set(unattended) {
		d.host.Updates.InstallUpdate(true)
		return
	}
	go d.host.Updates.ConfirmInstall()
}

// ServeUpdater is a headless server's own updater (serve-updates).
type ServeUpdater interface {
	State() domain.AppUpdate
	Check()
	Download()
	Install()
}

// HeadlessHost is what the update port is served from on a server with no screen.
type HeadlessHost struct {
	// Updates is nil on a server that cannot replace its own app.
	Updates        ServeUpdater
	Build          *domain.AppBuild
	OnAppUpdate    func(listener func(update domain.AppUpdate)) func()
	LaunchedUpdate *string
}

type headlessUpdates struct {
	host     HeadlessHost
	launched func(context.Context) (*string, error)
}

// NewHeadless serves updates for a server, which replaces its own app when it can with nobody at a screen to ask.
func NewHeadless(host HeadlessHost) contracts.UpdatePortRuntime {
	return headlessUpdates{host: host, launched: answeredOnce(host.LaunchedUpdate)}
}

func (h headlessUpdates) CheckForUpdates(*bool) {
	if h.host.Updates != nil {
		go h.host.Updates.Check()
	}
}

func (h headlessUpdates) AppUpdate(context.Context) (domain.AppUpdate, error) {
	if h.host.Updates == nil {
		return domain.NoAppUpdate, nil
	}
	return h.host.Updates.State(), nil
}

func (h headlessUpdates) AppBuild(context.Context) (*domain.AppBuild, error) {
	return h.host.Build, nil
}

func (h headlessUpdates) LaunchedUpdate(ctx context.Context) (*string, error) { return h.launched(ctx) }

func (h headlessUpdates) OnAppUpdate(listener func(update domain.AppUpdate)) func() {
	return h.host.OnAppUpdate(listener)
}

func (h headlessUpdates) DownloadUpdate(*bool) {
	if h.host.Updates != nil {
		go h.host.Updates.Download()
	}
}

func (h headlessUpdates) InstallUpdate(*bool) {
	if h.host.Updates != nil {
		go h.host.Updates.Install()
	}
}
