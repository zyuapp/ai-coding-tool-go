package services

import (
	"context"
	"path/filepath"
	"runtime"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/browser"
	"github.com/zyuapp/ai-coding-tool-go/internal/computers"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/devices"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
	"github.com/zyuapp/ai-coding-tool-go/internal/mobile"
	"github.com/zyuapp/ai-coding-tool-go/internal/store"
	"github.com/zyuapp/ai-coding-tool-go/internal/updates"
)

// ServeBuild opens the services of a headless server (`aic serve`) on its claimed data folder. The
// server answers only through the bridge: nothing is drawn, so computer use and the browser panel
// are refused and a phone's recordings are transcribed here.
func ServeBuild(config Config) func(ctx context.Context, environment computers.ServeEnvironment) (*computers.ServeParts, error) {
	return func(ctx context.Context, environment computers.ServeEnvironment) (*computers.ServeParts, error) {
		config.UserData = environment.UserData
		config.WorktreesRoot = environment.WorktreesRoot
		config.LegacyRoots = environment.LegacyRoots
		config.Headless = true
		s, err := Open(ctx, config)
		if err != nil {
			return nil, err
		}
		bridge := mobile.NewBridge()
		var build *domain.AppBuild
		if config.Version != "" {
			build = &domain.AppBuild{Version: config.Version, SelfUpdating: environment.Updates != nil}
		}
		var serveUpdates updates.ServeUpdater
		if environment.Updates != nil {
			serveUpdates = environment.Updates
		}
		var launched *string
		if config.Version != "" && config.Resources != "" {
			launched = updates.ReadAppLaunch(config.UserData, config.Version)
		}
		dictation := devices.NewDictationHost(devices.DictationHostOptions{Helper: devices.DictationHelperFor(devices.NativeDir(), config.Packaged, runtime.GOOS)})
		desktop := computers.NewServeDesktop(computers.ServeDesktopHost{
			ServiceDesktopHost: s.Host(),
			Mobile:             serveMobile{bridge},
			Say:                environment.Say,
			Browser:            browser.Headless{},
			Simulator:          devices.HeadlessSimulatorDesktop{},
			Dictation:          dictation,
			Updates:            updates.NewHeadless(updates.HeadlessHost{Updates: serveUpdates, Build: build, OnAppUpdate: s.Events.OnAppUpdate, LaunchedUpdate: launched}),
		})
		storage := store.NewJSONStorage(filepath.Join(config.UserData, store.WindowStorageFile))
		workspace := host.CreateWorkspaceRuntime(host.WorkspaceRuntimeHost{Desktop: desktop, Storage: storage.Window()})
		publisher := host.CreateRuntimePublisher(workspace)
		s.Bind(workspace.QueryThreads, workspace.GetState)
		hostName := computers.ServedName(config.UserData)
		return &computers.ServeParts{
			Runtime: serveRuntime{workspace},
			Flush: func(ctx context.Context) error {
				if failure, ok := publisher.Flush(ctx).(contracts.ComputerServerMessageResultResultFalse); ok {
					return errorText(failure.Message)
				}
				return nil
			},
			Mobile: &serveMobileHost{bridge: bridge, userData: config.UserData, workspace: s.WorkspaceHooks(publisher, hostName)},
			ServePairingSocket: func(_ context.Context, socketPath string, pair func(ctx context.Context) (*domain.MobilePairingOffer, error)) (func(ctx context.Context) error, error) {
				stop, err := mobile.ServePairingSocket(socketPath, pair)
				if err != nil {
					return nil, err
				}
				return func(context.Context) error { return stop() }, nil
			},
			SweepAttachments: func(ctx context.Context) (int, int64, error) {
				paths, err := s.Tasks.AttachmentPaths(ctx)
				if err != nil {
					return 0, 0, err
				}
				swept, err := sweepAttachments(s.Attachments.Directory(), paths)
				return swept.Files, swept.Bytes, err
			},
			Stop: func(ctx context.Context) {
				s.Stop(ctx)
				dictation.Stop()
				publisher.Dispose()
				_ = s.Close()
			},
		}, nil
	}
}

type errorText string

func (e errorText) Error() string { return string(e) }

// serveRuntime is the workspace runtime as a server drives it.
type serveRuntime struct{ runtime *host.Runtime }

func (r serveRuntime) Start(ctx context.Context) error {
	select {
	case <-r.runtime.Start():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r serveRuntime) Dispatch(ctx context.Context, input application.WorkspaceInput) error {
	return r.runtime.Dispatch(ctx, input)
}

func (r serveRuntime) State() *application.WorkspaceState { return r.runtime.GetState() }
func (r serveRuntime) Dispose()                           { r.runtime.Dispose() }

// serveMobile is the phone bridge as a headless host's desktop reaches it.
type serveMobile struct{ *mobile.Bridge }

func (m serveMobile) MobileState() domain.MobileServerState {
	state, _ := m.Bridge.MobileState(context.Background())
	if state == nil {
		return domain.MobileServerState{}
	}
	return *state
}

func (m serveMobile) NoticePhones(ctx context.Context, notice contracts.ThreadNotice) error {
	m.Bridge.NotifyPhones(ctx, notice)
	return nil
}

// serveMobileHost starts and stops the bridge a server runs.
type serveMobileHost struct {
	bridge    *mobile.Bridge
	userData  string
	workspace mobile.WorkspaceHooks
	stopState func()
}

func (h *serveMobileHost) Start(_ context.Context, port *int, local bool, onState func(domain.MobileServerState)) error {
	h.stopState = h.bridge.OnMobileState(onState)
	options := mobile.BridgeOptions{UserData: h.userData, Workspace: h.workspace, Port: port}
	if local {
		options.Tailscale = noTailscale{}
	}
	h.bridge.Start(options)
	return nil
}

func (h *serveMobileHost) SetMobileEnabled(ctx context.Context, enabled bool) (*domain.MobileServerState, error) {
	return h.bridge.SetMobileEnabled(ctx, enabled)
}

func (h *serveMobileHost) MobileState() domain.MobileServerState {
	return serveMobile{h.bridge}.MobileState()
}

func (h *serveMobileHost) CreateMobilePairingCode(ctx context.Context) (*domain.MobilePairingOffer, error) {
	return h.bridge.CreateMobilePairingCode(ctx)
}

func (h *serveMobileHost) Stop(context.Context) error {
	if h.stopState != nil {
		h.stopState()
	}
	h.bridge.Stop()
	return nil
}

// noTailscale is a loopback-only server's Tailscale: missing, and refusing to serve.
type noTailscale struct{}

func (noTailscale) Read(context.Context, *int, *string) domain.TailscaleState {
	return domain.TailscaleState{Status: domain.TailscaleStateStatusMissing}
}

func (noTailscale) Start(context.Context, int) mobile.TailscaleAction {
	return mobile.TailscaleAction{Message: computers.TailscaleOffForServer}
}

func (noTailscale) Stop(context.Context, int) mobile.TailscaleAction {
	return mobile.TailscaleAction{OK: true}
}
