package computers

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
)

// ServeMobile is the phone bridge a headless host runs itself.
type ServeMobile interface {
	MobileState() domain.MobileServerState
	SetMobileEnabled(ctx context.Context, enabled bool) (*domain.MobileServerState, error)
	CreateMobilePairingCode(ctx context.Context) (*domain.MobilePairingOffer, error)
	RevokeMobileDevice(ctx context.Context, deviceID string) (*domain.MobileServerState, error)
	RefreshTailscale(ctx context.Context) (*domain.MobileServerState, error)
	AnswerMobileRequest(response contracts.MobileResponse)
	PublishMobileView(update contracts.MobileViewUpdate)
	// Tells every paired computer about a notice, for its desktop to show.
	NoticeComputers(notice contracts.ThreadNotice)
	// Pushes a notice to every paired phone.
	NoticePhones(ctx context.Context, notice contracts.ThreadNotice) error
	OnMobileState(listener func(state domain.MobileServerState)) func()
	OnMobileRequest(listener func(request contracts.MobileRequest)) func()
}

// ServeDesktopHost is what a headless host adds to the services: the phone bridge it runs itself, a
// place to say things, and the headless halves of the panels only a window can draw.
type ServeDesktopHost struct {
	// Everything but OpenURL and ComputerUse, which a server answers itself.
	ServiceDesktopHost
	Mobile ServeMobile
	Say    func(line string)
	// The browser panel and simulator as a host with no window answers them.
	Browser   contracts.BrowserRuntime
	Simulator contracts.SimulatorPortRuntime
	// Transcribes a phone's recordings; there is no microphone to hold here.
	Dictation contracts.DictationPortRuntime
	// The updates a server runs itself, around its ServeUpdater.
	Updates contracts.UpdatePortRuntime
}

// headlessDesktop: nothing is drawn here, so the window's own effects are dropped, and what only a
// window can do is refused.
type headlessDesktop struct {
	mobile ServeMobile
}

func (headlessDesktop) OpenFolder(context.Context) (*domain.WorkspaceRecord, error) {
	return nil, NeedsDesktop("Picking a folder")
}

func (headlessDesktop) RevealWorktree(context.Context, string) error {
	return NeedsDesktop("Revealing a checkout")
}

func (headlessDesktop) RestartForComputerUse() {}

func (headlessDesktop) DownloadImage(context.Context, string) error {
	return NeedsDesktop("Saving an image")
}

func (headlessDesktop) OpenSourceLicenses(context.Context) error {
	return NeedsDesktop("The licenses page")
}

func (h headlessDesktop) MobileState(context.Context) (*domain.MobileServerState, error) {
	state := h.mobile.MobileState()
	return &state, nil
}

func (h headlessDesktop) SetMobileEnabled(ctx context.Context, enabled bool) (*domain.MobileServerState, error) {
	return h.mobile.SetMobileEnabled(ctx, enabled)
}

func (h headlessDesktop) CreateMobilePairingCode(ctx context.Context) (*domain.MobilePairingOffer, error) {
	return h.mobile.CreateMobilePairingCode(ctx)
}

func (h headlessDesktop) RevokeMobileDevice(ctx context.Context, deviceID string) (*domain.MobileServerState, error) {
	return h.mobile.RevokeMobileDevice(ctx, deviceID)
}

func (h headlessDesktop) RefreshTailscale(ctx context.Context) (*domain.MobileServerState, error) {
	return h.mobile.RefreshTailscale(ctx)
}

func (h headlessDesktop) OnMobileState(listener func(state domain.MobileServerState)) func() {
	return h.mobile.OnMobileState(listener)
}

func (h headlessDesktop) OnMobileRequest(listener func(request contracts.MobileRequest)) func() {
	return h.mobile.OnMobileRequest(listener)
}

func (h headlessDesktop) AnswerMobileRequest(response contracts.MobileResponse) {
	h.mobile.AnswerMobileRequest(response)
}

func (h headlessDesktop) PublishMobileView(update contracts.MobileViewUpdate) {
	h.mobile.PublishMobileView(update)
}

func (headlessDesktop) OpenFile(context.Context, []string, string, *float64) error {
	return NeedsDesktop("Opening a file")
}

func (headlessDesktop) ListApps(context.Context) ([]*contracts.InstalledApp, error) {
	return []*contracts.InstalledApp{}, nil
}

func (headlessDesktop) OpenFolderInApp(context.Context, string, string) error {
	return NeedsDesktop("Opening a folder in an application")
}

func (headlessDesktop) SetCaptureOptions(domain.CaptureOptions) {}
func (headlessDesktop) SetShortcuts(domain.ShortcutOverrides)   {}
func (headlessDesktop) SetShortcutCapture(bool)                 {}
func (headlessDesktop) CloseWindow()                            {}
func (headlessDesktop) FocusWindow()                            {}
func (headlessDesktop) SetBadgeCount(float64)                   {}

// AnnounceThread: a computer with no desktop has nobody at it, so a phone is always told.
func (h headlessDesktop) AnnounceThread(notice contracts.ThreadNotice) {
	h.mobile.NoticeComputers(notice)
	go func() { _ = h.mobile.NoticePhones(context.Background(), notice) }()
}

// serverComputerUse: a server has no screen to grant anything on.
type serverComputerUse struct{}

func (serverComputerUse) Permissions(context.Context) (*domain.ComputerUsePermissions, error) {
	return &domain.ComputerUsePermissions{}, nil
}

func (serverComputerUse) Enable(context.Context, domain.ComputerUsePermission) (*domain.ComputerUsePermissions, error) {
	return &domain.ComputerUsePermissions{}, nil
}

// ServeDesktop is the runtime's desktop on a machine with no desktop: the services, and refusals
// for the rest. It pairs with no other computer itself.
type ServeDesktop struct {
	*ServiceDesktop
	headlessDesktop
	contracts.BrowserRuntime
	contracts.SimulatorPortRuntime
	contracts.DictationPortRuntime
	contracts.UpdatePortRuntime
	host.ComputerDesktop
}

var _ host.RuntimeDesktop = (*ServeDesktop)(nil)

// NewServeDesktop builds the desktop a headless server's runtime acts on. An engine's sign-in page is
// said on the terminal, for the user to open elsewhere.
func NewServeDesktop(serve ServeDesktopHost) *ServeDesktop {
	services := serve.ServiceDesktopHost
	say := serve.Say
	if say == nil {
		say = func(string) {}
	}
	services.OpenURL = func(_ context.Context, url string) error {
		say("Sign in at " + url)
		return nil
	}
	services.ComputerUse = serverComputerUse{}
	return &ServeDesktop{
		ServiceDesktop:       NewServiceDesktop(services),
		headlessDesktop:      headlessDesktop{mobile: serve.Mobile},
		BrowserRuntime:       serve.Browser,
		SimulatorPortRuntime: serve.Simulator,
		DictationPortRuntime: serve.Dictation,
		UpdatePortRuntime:    serve.Updates,
		ComputerDesktop:      host.NoComputers,
	}
}
