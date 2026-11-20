package desktop

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Channels the desktop pushes at the window, as preload.ts listens on them.
const (
	ChannelWorkspaceUpdate  = "workspace-runtime:update"
	ChannelWorkspaceSurface = "workspace-runtime:surface"
	ChannelTerminalData     = "terminal:data"
	ChannelSimulatorFrame   = "simulator:frame"
	ChannelScreenshot       = "window:screenshot"
	ChannelShortcutRefused  = "window:shortcut-refused"
	ChannelShortcut         = "window:shortcut"
	ChannelShortcutCaptured = "window:shortcut-captured"
	ChannelOpenThread       = "window:open-thread"
	ChannelDictationLevel   = "dictation:level"
	ChannelWindowActive     = "window:active"
	ChannelGlassTint        = "window:glass-tint"
	// ChannelKeyboard hands the bridge's keyboard the bindings and capture state it matches against.
	ChannelKeyboard = "window:keyboard"
	// ChannelReducedTransparency says whether the person asked the system for less transparency,
	// which WebKit's media queries cannot ask.
	ChannelReducedTransparency = "window:reduced-transparency"
	// ChannelFileDrop carries the paths of files dropped on the window, for pathForFile.
	ChannelFileDrop = "window:file-drop"
)

// Ticket orders the window's calls: a page numbers its calls from 1, and lists the arguments it
// left undefined, which JSON cannot carry.
type Ticket struct {
	Page   string `json:"page"`
	Seq    int    `json:"seq"`
	Absent []int  `json:"absent"`
}

// How long a call waits for one numbered before it that never arrived.
const turnTimeout = time.Second

// sequencer starts the window's calls in the order the window made them, as Electron's IPC delivers
// them, although Wails hands each call to a goroutine of its own.
type sequencer struct {
	mu      sync.Mutex
	page    string
	next    int
	changed chan struct{}
}

func (order *sequencer) turn(ticket Ticket, start func()) {
	order.mu.Lock()
	if order.changed == nil {
		order.changed = make(chan struct{})
	}
	if ticket.Page != order.page {
		order.page, order.next = ticket.Page, 1
	}
	deadline := time.NewTimer(turnTimeout)
	defer deadline.Stop()
	for waiting := true; waiting && order.page == ticket.Page && ticket.Seq > order.next; {
		changed := order.changed
		order.mu.Unlock()
		select {
		case <-changed:
		case <-deadline.C:
			waiting = false
		}
		order.mu.Lock()
	}
	start()
	if order.page == ticket.Page && ticket.Seq >= order.next {
		order.next = ticket.Seq + 1
	}
	close(order.changed)
	order.changed = make(chan struct{})
	order.mu.Unlock()
}

// Bridge is what the window binds: one entry for every desktop call by name, and the workspace's
// request and migrate. Pushes go the other way through the App.
type Bridge struct {
	app   *App
	order sequencer

	readyOnce sync.Once
	readyMu   sync.Mutex
	ready     bool
	waiting   []func()
}

func newBridge(app *App) *Bridge {
	return &Bridge{app: app}
}

func withAbsent(args []jsontext.Value, absent []int) []jsontext.Value {
	for _, index := range absent {
		if index >= 0 && index < len(args) {
			args[index] = nil
		}
	}
	return args
}

func await(ctx context.Context, reply Reply) (jsontext.Value, error) {
	select {
	case result := <-reply:
		if result.Err != nil {
			return nil, result.Err
		}
		if result.Value == nil {
			return jsontext.Value("null"), nil
		}
		return result.Value, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Call answers `desktop:call`: the name is checked against the one table, then each argument
// against its guard, before anything runs.
func (bridge *Bridge) Call(ticket Ticket, name string, args []jsontext.Value) (jsontext.Value, error) {
	args = withAbsent(args, ticket.Absent)
	var reply Reply
	bridge.order.turn(ticket, func() { reply = bridge.start(name, args) })
	return await(bridge.app.context(), reply)
}

func (bridge *Bridge) start(name string, args []jsontext.Value) Reply {
	if err := checkCall(name, args); err != nil {
		return Settled(nil, err)
	}
	if frameCalls[name] {
		return bridge.run(name, args)
	}
	bridge.readyMu.Lock()
	defer bridge.readyMu.Unlock()
	if bridge.ready {
		return bridge.run(name, args)
	}
	reply := make(chan Result, 1)
	bridge.waiting = append(bridge.waiting, func() {
		started := bridge.run(name, args)
		go func() { reply <- <-started }()
	})
	return reply
}

// markReady starts the calls that waited for the services, in the order they arrived.
func (bridge *Bridge) markReady() {
	bridge.readyOnce.Do(func() {
		bridge.readyMu.Lock()
		waiting := bridge.waiting
		bridge.waiting = nil
		bridge.ready = true
		bridge.readyMu.Unlock()
		for _, start := range waiting {
			start()
		}
	})
}

func (bridge *Bridge) run(name string, args []jsontext.Value) Reply {
	app := bridge.app
	switch name {
	case CallSetTheme:
		var theme WindowTheme
		if err := json.Unmarshal(args[0], &theme); err != nil {
			return Settled(nil, err)
		}
		app.setWindowTheme(theme)
		return Settled(nil, nil)
	case CallSetWindowGlassRegions:
		// The window draws its vibrancy over the panes Electron lays, so none are laid.
		return Settled(nil, nil)
	case CallDescribeFiles:
		ctx := app.context()
		return Async(func() (jsontext.Value, error) {
			described, err := describeFiles(ctx, args[0])
			if err != nil {
				return nil, err
			}
			return json.Marshal(described)
		})
	}
	if app.options.Calls == nil {
		return Settled(nil, errors.New("The desktop is not ready."))
	}
	return app.options.Calls.Call(app.context(), name, args)
}

// Request answers `workspace-runtime:request`. An absent input asks for a whole snapshot.
func (bridge *Bridge) Request(ticket Ticket, input jsontext.Value) (jsontext.Value, error) {
	if len(ticket.Absent) > 0 {
		input = nil
	}
	backend := bridge.app.options.Workspace
	var reply Reply
	bridge.order.turn(ticket, func() {
		if backend == nil {
			reply = Settled(nil, errors.New("The workspace runtime has closed."))
			return
		}
		reply = backend.Request(input)
	})
	return await(bridge.app.context(), reply)
}

// How many stored values a window may hand over. Two are expected; a bound keeps a stranger's payload small.
const (
	maxMigratedValues = 8
	maxMigratedLength = 16_000_000
)

func storedValues(raw jsontext.Value) (map[string]string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	entries, ok := value.(map[string]any)
	if !ok || len(entries) > maxMigratedValues {
		return nil, false
	}
	values := make(map[string]string, len(entries))
	for key, item := range entries {
		text, ok := item.(string)
		if !strings.HasPrefix(key, "aicodingtool.") || !ok || jsLength(text) > maxMigratedLength {
			return nil, false
		}
		values[key] = text
	}
	return values, true
}

// Migrate answers `workspace-runtime:migrate`.
func (bridge *Bridge) Migrate(ticket Ticket, raw jsontext.Value) error {
	values, ok := storedValues(raw)
	backend := bridge.app.options.Workspace
	var reply Reply
	bridge.order.turn(ticket, func() {
		switch {
		case !ok:
			reply = Settled(nil, errors.New("Invalid stored values."))
		case backend == nil:
			reply = Settled(nil, errors.New("The workspace runtime has closed."))
		default:
			reply = backend.Migrate(values)
		}
	})
	_, err := await(bridge.app.context(), reply)
	return err
}

// OpenExternal follows a link outward, which only ever names a web page.
func (bridge *Bridge) OpenExternal(link string) error {
	page, err := webPageURL(link)
	if err != nil {
		return err
	}
	bridge.app.openExternal(page)
	return nil
}

func webPageURL(value string) (string, error) {
	if jsLength(value) > 8_192 {
		return "", errors.New("Invalid page URL.")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return "", errors.New("Invalid page URL.")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("Only web pages open outside the app.")
	}
	return value, nil
}

// workspaceView forwards the runtime's publications to the window.
type workspaceView struct{ app *App }

func (view workspaceView) Update(update jsontext.Value) {
	view.app.Send(ChannelWorkspaceUpdate, update)
}
func (view workspaceView) Surface(effect jsontext.Value) {
	view.app.Send(ChannelWorkspaceSurface, effect)
}

// Loaded is the page's bridge saying it listens, as Electron's did-finish-load does.
func (bridge *Bridge) Loaded() { bridge.app.pageLoaded() }

// TitlebarDoubleClick does what the system does for a double-click on a title bar.
func (bridge *Bridge) TitlebarDoubleClick() { bridge.app.nativeTitlebarDoubleClick() }

// LocalFonts lists the font families installed on this machine, for the font picker.
func (bridge *Bridge) LocalFonts() []string { return localFontFamilies() }
