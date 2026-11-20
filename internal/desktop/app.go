package desktop

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/zyuapp/ai-coding-tool-go/internal/updates"
)

// WindowMaterial mirrors contracts/ipc.ts: what the platform draws under the page.
type WindowMaterial string

const (
	MaterialGlass    WindowMaterial = "glass"
	MaterialVibrancy WindowMaterial = "vibrancy"
	MaterialNone     WindowMaterial = "none"
)

// Options is everything the window is opened with.
type Options struct {
	// Assets is the built renderer (frontend/dist).
	Assets fs.FS
	// ProfileDir holds window-placement.v1.json and window-theme.v1.json.
	ProfileDir string
	// AppName names the app in its menus.
	AppName string
	// Size opens the window at this content size, centered, whatever placement was remembered.
	Size *[2]int

	Workspace WorkspaceBackend
	Calls     DesktopCalls
	Images    ImageReads
	// Ready closes once the services behind the window's calls are up; nil means they already are.
	Ready <-chan struct{}
	// OpenProject registers a folder the `aic` command named and shows it in the window.
	OpenProject func(root string) error
	// BeforeQuit answers whether to hold the quit: it starts saving what must be saved, and quits
	// again once that is done.
	BeforeQuit func() (prevent bool)
	// WindowReady runs once the native window exists.
	WindowReady func()
	// OnWake runs when the machine wakes from sleep.
	OnWake func()
}

// App is the window and everything the desktop does for it.
type App struct {
	options  Options
	material WindowMaterial
	bridge   *Bridge

	mu        sync.Mutex
	ctx       context.Context
	theme     WindowTheme
	active    *bool
	keyboard  keyboardState
	closed    bool
	quitting  bool
	normal    *WindowPlacement
	placement *time.Timer
	unread    bool

	updateItem *menu.MenuItem

	themes     themeWriter
	placements placementWriter
	stopView   func()

	// started closes once the window exists; done once the app has quit.
	started chan struct{}
	done    chan struct{}
}

type keyboardState struct {
	Overrides jsontext.Value `json:"overrides"`
	Capturing bool           `json:"capturing"`
}

// New prepares the window; Run opens it.
func New(options Options) *App {
	if options.AppName == "" {
		options.AppName = "AI Coding Tool"
	}
	app := &App{options: options, material: windowMaterial(), started: make(chan struct{}), done: make(chan struct{})}
	app.bridge = newBridge(app)
	app.theme = LoadWindowTheme(filepath.Join(options.ProfileDir, ThemeFile))
	app.keyboard = keyboardState{Overrides: jsontext.Value("{}")}
	if options.Ready == nil {
		app.bridge.markReady()
	} else {
		go func() {
			<-options.Ready
			app.bridge.markReady()
		}()
	}
	return app
}

// windowMaterial: macOS draws a material under the window, glass where it has it; other desktops
// keep an opaque one.
func windowMaterial() WindowMaterial {
	if platformName != "macos" {
		return MaterialNone
	}
	if nativeGlassAvailable() {
		return MaterialGlass
	}
	return MaterialVibrancy
}

func (app *App) context() context.Context {
	app.mu.Lock()
	defer app.mu.Unlock()
	if app.ctx == nil {
		return context.Background()
	}
	return app.ctx
}

// Run opens the window and returns once the app quits.
func (app *App) Run() error {
	placement := app.startPlacement()
	minWidth, minHeight := MinWindowWidth, MinWindowHeight
	if size := app.options.Size; size != nil {
		minWidth, minHeight = min(minWidth, size[0]), min(minHeight, size[1])
	}
	r, g, b := canvasRGB(app.theme.Canvas)
	background := &options.RGBA{R: r, G: g, B: b, A: 255}
	translucent := app.material != MaterialNone
	if translucent {
		background = &options.RGBA{}
	}
	return wails.Run(&options.App{
		Title:             app.options.AppName,
		Width:             int(placement.Width),
		Height:            int(placement.Height),
		MinWidth:          minWidth,
		MinHeight:         minHeight,
		StartHidden:       true,
		HideWindowOnClose: !quitsWithLastWindow(),
		BackgroundColour:  background,
		AssetServer: &assetserver.Options{
			Assets:     app.options.Assets,
			Middleware: func(next http.Handler) http.Handler { return app.desktopScript(appSchemes(app.options.Images, next)) },
		},
		Menu:               app.appMenu(),
		LogLevel:           logger.WARNING,
		LogLevelProduction: logger.ERROR,
		OnStartup:          func(ctx context.Context) { app.startup(ctx, placement) },
		OnBeforeClose:      func(ctx context.Context) bool { return app.beforeQuit() },
		OnShutdown:         func(ctx context.Context) { app.shutdown() },
		Bind:               []any{app.bridge},
		DragAndDrop:        &options.DragAndDrop{EnableFileDrop: true},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: translucent,
			WindowIsTranslucent:  translucent,
			Preferences: &mac.Preferences{
				TabFocusesLinks:   mac.Enabled,
				FullscreenEnabled: mac.Enabled,
			},
			OnUrlOpen: app.openURL,
		},
		Linux: &linux.Options{ProgramName: app.options.AppName, WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand},
	})
}

// quitsWithLastWindow: macOS keeps the app running with no window.
func quitsWithLastWindow() bool { return platformName != "macos" }

// desktopScript answers /__app/desktop.js, which the page loads before any module: the platform
// and material, which the window needs before its first paint.
func (app *App) desktopScript(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/__app/desktop.js" {
			next.ServeHTTP(writer, request)
			return
		}
		identity, _ := json.Marshal(map[string]any{"platform": platformName, "windowMaterial": string(app.material), "reducedTransparency": nativeReducedTransparency()})
		writer.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		_, _ = writer.Write([]byte("window.__aicodingtoolDesktop=" + string(identity) + ";\n"))
	})
}

func (app *App) startPlacement() WindowPlacement {
	if size := app.options.Size; size != nil {
		return WindowPlacement{Width: float64(size[0]), Height: float64(size[1])}
	}
	return LoadWindowPlacement(filepath.Join(app.options.ProfileDir, PlacementFile))
}

func (app *App) startup(ctx context.Context, placement WindowPlacement) {
	app.mu.Lock()
	app.ctx = ctx
	app.mu.Unlock()
	app.nativeInstall(app.material)
	app.applyWindowTheme(app.theme)
	if app.options.Size == nil {
		placement = FitPlacement(placement, nativeScreens())
	}
	if placement.Maximized == nil || !*placement.Maximized {
		normal := placement
		app.normal = &normal
	}
	app.nativePlaceAndShow(placement)
	if backend := app.options.Workspace; backend != nil {
		app.stopView = backend.Subscribe(workspaceView{app})
		backend.SetViewVisible(true)
	}
	runtime.OnFileDrop(ctx, func(x, y int, paths []string) {
		app.SendValue(ChannelFileDrop, map[string]any{"x": x, "y": y, "paths": paths})
	})
	close(app.started)
	if app.options.WindowReady != nil {
		go app.options.WindowReady()
	}
	if root := ProjectPathFromArgv(os.Args[1:]); root != "" {
		app.openProject(root)
	}
}

// pageLoaded repeats what the page keeps but cannot have heard before its bridge listened.
func (app *App) pageLoaded() {
	app.mu.Lock()
	active, keyboard := app.active, app.keyboard
	app.mu.Unlock()
	if active != nil {
		app.SendValue(ChannelWindowActive, *active)
	}
	if app.material == MaterialGlass {
		app.SendValue(ChannelGlassTint, app.nativeGlassTint())
	}
	app.SendValue(ChannelKeyboard, keyboard)
}

// Send pushes one message at the window on a channel of its own, as webContents.send does. The
// payload is JSON, which is also a JavaScript expression, so it reaches the page without a second encoding.
func (app *App) Send(channel string, payload jsontext.Value) {
	app.mu.Lock()
	ctx := app.ctx
	app.mu.Unlock()
	if ctx == nil || !payload.IsValid() {
		return
	}
	name, _ := json.Marshal(channel)
	runtime.WindowExecJS(ctx, "window.__aicodingtoolPush&&window.__aicodingtoolPush("+string(name)+","+string(payload)+");")
}

// SendValue encodes payload and pushes it.
func (app *App) SendValue(channel string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Could not encode %s: %v", channel, err)
		return
	}
	app.Send(channel, data)
}

// MarkReady starts the calls that waited for the services.
func (app *App) MarkReady() { app.bridge.markReady() }

// SetShortcuts hands the window's keyboard the user's bindings (ShortcutOverrides).
func (app *App) SetShortcuts(overrides jsontext.Value) {
	app.mu.Lock()
	app.keyboard.Overrides = overrides
	state := app.keyboard
	app.mu.Unlock()
	app.SendValue(ChannelKeyboard, state)
}

// SetShortcutCapture sends every keystroke to settings while they wait for one.
func (app *App) SetShortcutCapture(capturing bool) {
	app.mu.Lock()
	app.keyboard.Capturing = capturing
	state := app.keyboard
	app.mu.Unlock()
	app.SendValue(ChannelKeyboard, state)
}

// SetBadgeCount draws how many threads carry an unseen mark on the app icon, and enables the dock
// menu's "Mark All as Read" while there are any.
func (app *App) SetBadgeCount(count int) {
	app.nativeSetBadge(count)
	app.mu.Lock()
	changed := (count > 0) != app.unread
	app.unread = count > 0
	app.mu.Unlock()
	if changed {
		app.nativeSetDockUnread(count > 0)
	}
}

// Reveal brings the window back to the user.
func (app *App) Reveal() {
	app.mu.Lock()
	quitting, closed := app.quitting, app.closed
	app.closed = false
	app.mu.Unlock()
	if quitting {
		return
	}
	if closed {
		app.reopened()
	}
	app.nativeReveal()
}

// reopened gives a window that was closed a fresh page, as Electron makes a new window.
func (app *App) reopened() {
	runtime.WindowReloadApp(app.context())
	if backend := app.options.Workspace; backend != nil {
		backend.SetViewVisible(true)
	}
}

func (app *App) reopen() { app.Reveal() }

// windowActive: the native window keeps focus while its browser panel has it, which the page's own focus does not.
func (app *App) windowActive(active bool) {
	app.mu.Lock()
	app.active = &active
	app.mu.Unlock()
	if backend := app.options.Workspace; backend != nil {
		backend.SetViewInUse(active)
	}
	app.SendValue(ChannelWindowActive, active)
	// The system redraws its glass when a window comes back into use, so the page asks after it then too.
	if app.material == MaterialGlass {
		app.SendValue(ChannelGlassTint, app.nativeGlassTint())
	}
}

func (app *App) windowChanged(visibility bool) {
	state, ok := app.nativeWindowState()
	if !ok {
		return
	}
	if visibility {
		if backend := app.options.Workspace; backend != nil {
			backend.SetViewVisible(state.visible && !state.mini)
		}
		return
	}
	app.recordPlacement(state, false)
}

// recordPlacement writes where the window sits; the user drags in many small steps, so it is
// written once the dragging stops.
func (app *App) recordPlacement(state nativeState, now bool) {
	if app.options.Size != nil {
		return
	}
	app.mu.Lock()
	if !state.maximized && !state.fullScreen && !state.mini {
		normal := state.frame
		app.normal = &normal
	}
	placement := state.frame
	if app.normal != nil {
		placement = *app.normal
	}
	maximized, fullScreen := state.maximized, state.fullScreen
	placement.Maximized, placement.FullScreen = &maximized, &fullScreen
	file := filepath.Join(app.options.ProfileDir, PlacementFile)
	if app.placement != nil {
		app.placement.Stop()
	}
	if now {
		app.placement = nil
		app.mu.Unlock()
		app.placements.remember(file, placement)
		return
	}
	app.placement = time.AfterFunc(400*time.Millisecond, func() { app.placements.remember(file, placement) })
	app.mu.Unlock()
}

// windowClosing: on macOS the close button hides the window and the app keeps running.
func (app *App) windowClosing() {
	if state, ok := app.nativeWindowState(); ok {
		app.recordPlacement(state, true)
	}
	if quitsWithLastWindow() {
		return
	}
	app.mu.Lock()
	app.closed = true
	quitting := app.quitting
	app.mu.Unlock()
	backend := app.options.Workspace
	if backend == nil {
		return
	}
	backend.SetViewVisible(false)
	if !quitting {
		go func() {
			if result := <-backend.Request(jsontext.Value(`{"type":"view.closed"}`)); result.Err != nil {
				log.Printf("Could not release closed window panels: %v", result.Err)
			}
		}()
	}
}

func (app *App) beforeQuit() (prevent bool) {
	app.mu.Lock()
	app.quitting = true
	app.mu.Unlock()
	if state, ok := app.nativeWindowState(); ok && state.visible {
		app.recordPlacement(state, true)
	}
	return app.options.BeforeQuit != nil && app.options.BeforeQuit()
}

// quitFailed brings the window back to say why the workspace could not be saved.
func (app *App) quitFailed(err error) {
	app.mu.Lock()
	app.quitting = false
	app.mu.Unlock()
	app.Reveal()
	runtime.MessageDialog(app.context(), runtime.MessageDialogOptions{Type: runtime.ErrorDialog, Title: "Could not save the workspace", Message: err.Error()})
}

// closeWindow closes the window as its close button does.
func (app *App) closeWindow() {
	if quitsWithLastWindow() {
		runtime.Quit(app.context())
		return
	}
	runtime.WindowHide(app.context())
	app.windowClosing()
}

// focusPage takes the keyboard back from a page in the browser panel. Only a window already in
// front takes it, since focusing one that is not raises it over whatever the user is in.
func (app *App) focusPage() {
	app.mu.Lock()
	active := app.active != nil && *app.active
	app.mu.Unlock()
	if active {
		app.nativeFocusPage()
	}
}

func (app *App) shutdown() {
	defer close(app.done)
	if app.stopView != nil {
		app.stopView()
	}
	app.mu.Lock()
	if app.placement != nil {
		app.placement.Stop()
	}
	app.mu.Unlock()
}

// setWindowTheme: the frame follows the window's theme, and remembers it for the next launch's first paint.
func (app *App) setWindowTheme(theme WindowTheme) {
	app.mu.Lock()
	same := theme.same(app.theme)
	app.mu.Unlock()
	if same {
		return
	}
	app.applyWindowTheme(theme)
	go app.themes.remember(filepath.Join(app.options.ProfileDir, ThemeFile), theme)
}

func (app *App) applyWindowTheme(theme WindowTheme) {
	app.mu.Lock()
	app.theme = theme
	app.mu.Unlock()
	switch {
	case theme.follows():
		app.nativeSetAppearance(0)
	case theme.Variant == "light":
		app.nativeSetAppearance(2)
	default:
		app.nativeSetAppearance(1)
	}
	if app.material == MaterialNone {
		r, g, b := canvasRGB(theme.Canvas)
		app.nativeSetBackground(r, g, b, 255)
	}
}

func (app *App) openExternal(page string) { runtime.BrowserOpenURL(app.context(), page) }

func (app *App) openURL(link string) {
	if root := ProjectPathFromURL(link); root != "" {
		app.openProject(root)
		return
	}
	app.Reveal()
}

func (app *App) secondInstance(args []string) {
	if root := ProjectPathFromArgv(args); root != "" {
		app.openProject(root)
		return
	}
	app.Reveal()
}

func (app *App) openProject(root string) {
	if app.options.OpenProject == nil {
		log.Printf("Could not open the folder the aic command named: %s", root)
	} else if err := app.options.OpenProject(root); err != nil {
		log.Printf("Could not open the folder the aic command named: %v", err)
	}
	app.Reveal()
}

// dispatch runs a command the menus or the dock raise through the same runtime as the window's buttons.
func (app *App) dispatch(command string) {
	backend := app.options.Workspace
	if backend == nil {
		return
	}
	input, _ := json.Marshal(map[string]string{"type": command})
	if result := <-backend.Request(input); result.Err != nil {
		log.Printf("%s failed: %v", command, result.Err)
	} else if failure := failedResult(result.Value); failure != "" {
		log.Printf("%s failed: %s", command, failure)
	}
}

func failedResult(value jsontext.Value) string {
	var result struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if json.Unmarshal(value, &result) != nil || result.OK {
		return ""
	}
	return strings.TrimSpace(result.Message)
}

func (app *App) markAllRead() { app.dispatch("task.read-all") }

// menuCommand: a closed window is reopened to show the command's result.
func (app *App) menuCommand(command string) {
	app.Reveal()
	app.dispatch(command)
}

// messageBox shows a box over the window and answers the index of the button pressed; false when
// there is no window to show it on.
func (app *App) messageBox(box updates.MessageBox) (int, bool) {
	app.mu.Lock()
	ctx := app.ctx
	app.mu.Unlock()
	if ctx == nil {
		return 0, false
	}
	kind := runtime.InfoDialog
	if box.Type == "warning" {
		kind = runtime.WarningDialog
	}
	message := box.Message
	if box.Detail != "" {
		message += "\n\n" + box.Detail
	}
	options := runtime.MessageDialogOptions{Type: kind, Title: box.Title, Message: message, Buttons: box.Buttons}
	if box.DefaultID >= 0 && box.DefaultID < len(box.Buttons) {
		options.DefaultButton = box.Buttons[box.DefaultID]
	}
	if box.CancelID >= 0 && box.CancelID < len(box.Buttons) {
		options.CancelButton = box.Buttons[box.CancelID]
	}
	pressed, err := runtime.MessageDialog(ctx, options)
	if err != nil {
		return box.CancelID, true
	}
	for index, label := range box.Buttons {
		if label == pressed {
			return index, true
		}
	}
	return box.CancelID, true
}
