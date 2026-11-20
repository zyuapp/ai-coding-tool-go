package desktop

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	wails "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/browser"
	"github.com/zyuapp/ai-coding-tool-go/internal/computers"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/devices"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/mobile"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
	"github.com/zyuapp/ai-coding-tool-go/internal/services"
	"github.com/zyuapp/ai-coding-tool-go/internal/store"
	"github.com/zyuapp/ai-coding-tool-go/internal/updates"
)

// LaunchOptions are what the window process is started with.
type LaunchOptions struct {
	Assets  fs.FS
	Profile platform.Profile
	// Older roots the app made checkouts under, still its own.
	LegacyRoots []string
	Size        *[2]int
	Version     string
	Packaged    bool
	// The installed app's resources folder; "" from source.
	Resources string
	// The source checkout, for a build run from source.
	AppPath string
	// The profile's single-instance lock, which later launches are handed to.
	Lock *platform.DesktopLock
}

type quitState int

const (
	quitRunning quitState = iota
	quitStopping
	quitReady
)

// Controller is the window process: the window, the services behind it, and the runtime between
// them, from launch to quit (main.ts).
type Controller struct {
	options   LaunchOptions
	app       *App
	backend   *RuntimeBackend
	services  *services.Services
	desktop   *runtimeDesktop
	browser   *browser.Runtime
	pages     *browser.Host
	simulator *devices.SimulatorHost
	keyboard  *devices.KeyboardHost
	updates   *updates.Updates
	mobile    *mobile.Bridge
	links     *computers.LinksDesktop
	lockAwake *platform.LockAwake

	ready     chan struct{}
	readyOnce sync.Once
	started   chan struct{}

	mu                     sync.Mutex
	state                  quitState
	restartRequested       bool
	restartArgs            []string
	updateRestartScheduled bool
	pendingProjects        []string
	runtimeListening       bool
	stopUpdateChecks       func()
}

// Launch opens the window and runs the app until it quits.
func Launch(options LaunchOptions) error {
	c := &Controller{options: options, ready: make(chan struct{}), started: make(chan struct{})}
	userData := options.Profile.UserData
	c.mobile = mobile.NewBridge()
	c.desktop = &runtimeDesktop{Bridge: c.mobile, windowDesktop: newWindowDesktop(c)}
	c.backend = NewRuntimeBackend(c.desktop, store.NewJSONStorage(filepath.Join(userData, store.WindowStorageFile)))
	c.pages = browser.NewHost(browser.Options{
		OnKey: func(event browser.KeyEvent) bool {
			if c.keyboard == nil {
				return false
			}
			return c.keyboard.HandleKey(devices.KeyEvent{Type: event.Type, Input: event.KeyInput, IsAutoRepeat: event.IsAutoRepeat}, domain.ShortcutSurfaceBrowser)
		},
		SessionDir: filepath.Join(userData, "WebKitBrowser"),
	})
	c.browser = browser.NewRuntime(c.pages, func(context.Context) error {
		return platform.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles")
	})
	c.simulator = devices.NewSimulatorHost(devices.SimulatorHostOptions{})
	c.desktop.BrowserRuntime = c.browser
	c.desktop.SimulatorPortRuntime = devices.SimulatorDesktop(c.simulator)
	c.desktop.DictationPortRuntime = devices.DictationDesktop(devices.DesktopDictation)
	c.app = New(Options{
		Assets:      options.Assets,
		ProfileDir:  userData,
		AppName:     platform.AppName,
		Size:        options.Size,
		Workspace:   c.backend,
		Calls:       c,
		Images:      c,
		Ready:       c.ready,
		OpenProject: c.openProject,
		BeforeQuit:  c.beforeQuit,
		WindowReady: c.windowReady,
		OnWake:      c.woke,
	})
	if options.Lock != nil {
		options.Lock.Listen(func(launch platform.Launch) { c.secondInstance(launch.Argv) })
	}
	go c.watchSignals()
	go c.startup()
	err := c.app.Run()
	if options.Lock != nil {
		options.Lock.Close()
	}
	c.restartIfAsked()
	return err
}

func (c *Controller) waitReady(ctx context.Context) bool {
	select {
	case <-c.ready:
		return true
	case <-ctx.Done():
		return false
	}
}

// windowReady binds the browser panel to the window once it exists.
func (c *Controller) windowReady() {
	if err := c.pages.Start(); err != nil {
		log.Printf("Could not start the browser panel: %v", err)
	}
	stopLevels := devices.StartDesktopDictationLevels(func(level float64) { c.app.SendValue(ChannelDictationLevel, level) })
	stopFrames := c.simulator.OnSimulatorFrame(func(frame domain.SimulatorFrame) { c.app.SendValue(ChannelSimulatorFrame, frame) })
	go func() {
		<-c.started
		stopData := c.services.Terminals.OnTerminalData(func(event contracts.TerminalDataEvent) { c.app.SendValue(ChannelTerminalData, event) })
		<-c.app.done
		stopData()
		stopLevels()
		stopFrames()
	}()
}

func (c *Controller) config() services.Config {
	return services.Config{
		UserData:      c.options.Profile.UserData,
		WorktreesRoot: c.options.Profile.WorktreesRoot,
		LegacyRoots:   c.options.LegacyRoots,
		Packaged:      c.options.Packaged,
		Resources:     c.options.Resources,
		AppPath:       c.options.AppPath,
		Version:       c.options.Version,
	}
}

func machineName() string {
	name, _ := os.Hostname()
	return strings.TrimSuffix(name, ".local")
}

// startup opens the services beside the window; what the window asks of them before they are up waits.
func (c *Controller) startup() {
	ctx := context.Background()
	opened, err := services.Open(ctx, c.config())
	if err != nil {
		c.fail(err)
		return
	}
	c.services = opened
	userData := c.options.Profile.UserData
	host := opened.Host()
	host.OpenURL = func(_ context.Context, url string) error { return platform.OpenExternal(url) }
	host.ComputerUse = computerUse{opened}
	c.desktop.ServiceDesktop = computers.NewServiceDesktop(host)
	c.updates = c.newUpdates()
	c.desktop.UpdatePortRuntime = updates.NewDesktop(updates.DesktopHost{
		Updates:        c.updates,
		OnAppUpdate:    opened.Events.OnAppUpdate,
		LaunchedUpdate: updates.ReadAppLaunch(userData, c.options.Version),
	})
	c.links = computers.NewLinksDesktop(computers.LinksOptions{File: filepath.Join(userData, computers.ComputersFile), DeviceName: machineName()},
		func() *computers.ComputerReads { return opened.Reads }, c.mobile.AnnounceName)
	c.desktop.ComputerDesktop = c.links
	opened.BindLinks(func() computers.PairedQueries { return c.links.Links })
	opened.Bind(c.backend.Runtime.QueryThreads, c.backend.Runtime.GetState)
	c.keyboard = c.newKeyboard()
	close(c.started)

	if c.options.Packaged {
		go func() {
			if _, err := opened.Cli.Refresh(ctx); err != nil {
				log.Printf("Could not refresh the aic command: %v", err)
			}
		}()
	}
	if !platform.QuitsWithLastWindow {
		c.lockAwake = platform.NewLockAwake(c.workAhead)
	}
	c.keyboard.ClaimDesktopShortcut()
	<-opened.SearchPathReady()
	if c.lockAwake != nil {
		lockAwake := c.lockAwake
		c.backend.Runtime.Subscribe(lockAwake.Update)
	}
	<-c.backend.Start()
	c.mu.Lock()
	c.runtimeListening = true
	c.mu.Unlock()
	go c.flushProjectOpens()
	c.links.Links.Start()
	c.readyOnce.Do(func() { close(c.ready) })
	c.mobile.Start(mobile.BridgeOptions{UserData: userData, Workspace: opened.WorkspaceHooks(c.backend.Publisher, func() string { return c.links.Links.Name() })})
	c.stopUpdateChecks = c.updates.StartUpdateChecks()
	go func() {
		if err := opened.SweepUserData(ctx, func(line string) { log.Println(line) }); err != nil {
			log.Printf("Could not sweep unused app data: %v", err)
		}
	}()
}

// woke redials the paired computers, whose lines only look open after a sleep, and asks Tailscale
// again, which may have been reset meanwhile.
func (c *Controller) woke() {
	select {
	case <-c.ready:
	default:
		return
	}
	c.links.Links.Reconnect(nil)
	c.mobile.Recheck()
}

// fail shows why the app could not start, and quits rather than leaving the window hung.
func (c *Controller) fail(err error) {
	log.Printf("Could not start: %v", err)
	<-c.app.started
	wails.MessageDialog(c.app.context(), wails.MessageDialogOptions{Type: wails.ErrorDialog, Title: "Could not start", Message: err.Error()})
	c.mu.Lock()
	c.state = quitReady
	c.mu.Unlock()
	wails.Quit(c.app.context())
}

// workAhead is what a locked Mac stays awake for: a run, a schedule that will fire, or a thread
// waiting out a usage limit.
func (c *Controller) workAhead() bool {
	state := c.backend.Runtime.GetState()
	if len(state.ActiveRuns) > 0 {
		return true
	}
	for _, automation := range state.Automations {
		if automation.NextRunAt != nil {
			return true
		}
	}
	for _, thread := range state.Threads {
		if thread.LimitPause != nil {
			return true
		}
	}
	return false
}

func (c *Controller) newKeyboard() *devices.KeyboardHost {
	capture := devices.NewWindowCapture(devices.NewScreenshotContexts(func() string {
		return c.services.ComputerUse.Binary()
	}))
	return devices.NewKeyboardHost(devices.KeyboardHostOptions{
		Send: func(channel string, payload any) bool {
			c.app.SendValue(channel, payload)
			return true
		},
		Reveal:          c.app.Reveal,
		Notify:          func(title, body string) { platform.Notify(title, body, nil) },
		WriteAttachment: c.services.Attachments.WriteAttachment,
		OpenExternal:    platform.OpenExternal,
		Capture:         capture.CaptureFrontmostWindow,
		Prepare:         capture.Prepare,
	})
}

func (c *Controller) newUpdates() *updates.Updates {
	home, _ := os.UserHomeDir()
	executable, _ := os.Executable()
	events := c.services.Events
	return updates.New(updates.UpdateHost{
		Dialog: func(box updates.MessageBox) (int, bool) { return c.app.messageBox(box) },
		OnInstall: func(version string) {
			c.mu.Lock()
			c.updateRestartScheduled = true
			c.mu.Unlock()
			updates.RecordUpdateRelaunch(c.options.Profile.UserData, version, application.ResumableAfterUpdate(c.backend.Runtime.GetState()))
		},
		OnChecking: c.app.SetUpdateChecking,
		OnState:    func(update domain.AppUpdate) { events.UpdateChanged.Emit(update) },
	}, updates.Options{
		Version:          c.options.Version,
		Packaged:         c.options.Packaged,
		AutomaticUpdates: platform.AutomaticUpdatesAvailable(os.Getenv),
		AppImage:         platform.RunningAppImage(os.Getenv),
		Home:             home,
		DataHome:         os.Getenv("XDG_DATA_HOME"),
		OpenExternal:     platform.OpenExternal,
		NewUpdater: func(updaterEvents updates.UpdaterEvents) updates.Updater {
			target := updates.TargetFor(platform.OS, executable, os.Getenv)
			if target == nil {
				return nil
			}
			return updates.NewReleaseUpdater(updates.ReleaseUpdaterOptions{Version: c.options.Version, Target: *target, Quit: c.quitApp}, updaterEvents)
		},
	})
}

// computerUse reads and asks for the permissions computer use needs.
type computerUse struct{ services *services.Services }

func (u computerUse) Permissions(ctx context.Context) (*domain.ComputerUsePermissions, error) {
	return u.services.ComputerUse.Permissions(ctx)
}

func (u computerUse) Enable(ctx context.Context, permission domain.ComputerUsePermission) (*domain.ComputerUsePermissions, error) {
	return u.services.ComputerUse.RequestPermission(ctx, permission)
}

// openProject registers a folder the `aic` command named and hands it to the runtime once it listens.
func (c *Controller) openProject(root string) error {
	c.mu.Lock()
	c.pendingProjects = append(c.pendingProjects, root)
	c.mu.Unlock()
	go c.flushProjectOpens()
	return nil
}

func (c *Controller) flushProjectOpens() {
	c.mu.Lock()
	if !c.runtimeListening || len(c.pendingProjects) == 0 {
		c.mu.Unlock()
		return
	}
	pending := c.pendingProjects
	c.pendingProjects = nil
	c.mu.Unlock()
	for _, root := range pending {
		record, err := c.services.Workspaces.RegisterProject(context.Background(), root)
		if err != nil {
			log.Printf("Could not open the folder the aic command named: %v", err)
			continue
		}
		c.services.Events.OpenProject.Emit(*record)
	}
	c.app.Reveal()
}

// secondInstance is a later launch handed to this one; a launch aimed at a quitting app reopens it after.
func (c *Controller) secondInstance(argv []string) {
	args := argv
	if len(args) > 0 {
		args = args[1:]
	}
	if c.queueReopen(args) {
		return
	}
	for _, argument := range args {
		if strings.HasPrefix(argument, "aicodingtool://") {
			c.app.openURL(argument)
			return
		}
	}
	c.app.secondInstance(args)
}

func (c *Controller) queueReopen(args []string) bool {
	c.mu.Lock()
	running := c.state == quitRunning
	c.mu.Unlock()
	if running {
		return false
	}
	c.requestRestart(args)
	return true
}

func (c *Controller) requestRestart(args []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restartRequested = true
	if args != nil || c.restartArgs == nil {
		c.restartArgs = args
	}
}

// restartIfAsked starts the app again once this process has gone, unless an update's install does.
func (c *Controller) restartIfAsked() {
	c.mu.Lock()
	restart := c.restartRequested && !c.updateRestartScheduled
	args := c.restartArgs
	c.mu.Unlock()
	if !restart {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		return
	}
	if bundle := appBundle(executable); bundle != "" {
		err = updates.StartAfterExit("/usr/bin/open", append([]string{"-n", bundle, "--args"}, args...)...)
	} else {
		err = updates.StartAfterExit(executable, args...)
	}
	if err != nil {
		log.Printf("Could not restart: %v", err)
	}
}

func appBundle(executable string) string {
	contents := filepath.Dir(filepath.Dir(executable))
	if filepath.Base(contents) == "Contents" && strings.HasSuffix(filepath.Dir(contents), ".app") {
		return filepath.Dir(contents)
	}
	return ""
}

// quitApp asks the app to quit, as the menu's Quit does.
func (c *Controller) quitApp() {
	<-c.app.started
	wails.Quit(c.app.context())
}

func (c *Controller) watchSignals() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	<-signals
	c.quitApp()
	// A second signal leaves at once.
	<-signals
	os.Exit(1)
}

// beforeQuit holds the quit until everything is saved: the first ask starts the shutdown and the
// shutdown asks again once it is done.
func (c *Controller) beforeQuit() (prevent bool) {
	c.mu.Lock()
	state := c.state
	if state == quitRunning {
		c.state = quitStopping
	}
	c.mu.Unlock()
	switch state {
	case quitReady:
		return false
	case quitStopping:
		return true
	}
	wails.WindowHide(c.app.context())
	go c.finishShutdown()
	return true
}

func (c *Controller) finishShutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A quit during startup lets it finish rather than racing the store's open and the runtime's start.
	select {
	case <-c.ready:
	case <-c.started:
		<-c.backend.Runtime.Loaded()
	case <-time.After(10 * time.Second):
	}
	err := c.shutdown(ctx)
	if err != nil {
		c.mu.Lock()
		c.state = quitRunning
		c.mu.Unlock()
		c.app.quitFailed(err)
		return
	}
	c.mu.Lock()
	c.state = quitReady
	c.mu.Unlock()
	wails.Quit(c.app.context())
}

func (c *Controller) shutdown(ctx context.Context) error {
	s := c.services
	if s == nil {
		return nil
	}
	if err := c.backend.Flush(ctx); err != nil {
		return err
	}
	if c.lockAwake != nil {
		c.lockAwake.Stop()
	}
	if c.stopUpdateChecks != nil {
		c.stopUpdateChecks()
	}
	s.Stop(ctx)
	c.links.Links.Stop()
	c.mobile.Stop()
	c.pages.Stop()
	c.simulator.StopAtQuit()
	devices.StopDesktopDictation()
	if err := c.backend.Flush(ctx); err != nil {
		s.Resume(ctx)
		c.links.Links.Start()
		return err
	}
	c.backend.Close()
	if err := s.Close(); err != nil {
		return errors.Join(errors.New("Could not close task storage."), err)
	}
	return nil
}
