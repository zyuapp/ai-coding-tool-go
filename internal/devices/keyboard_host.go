package devices

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// Window channels the keyboard sends on.
const (
	ChannelShortcut         = "window:shortcut"
	ChannelShortcutCaptured = "window:shortcut-captured"
	ChannelScreenshot       = "window:screenshot"
	ChannelShortcutRefused  = "window:shortcut-refused"
)

// GlobalHotkeys claims keystrokes from the whole desktop without activating the app.
type GlobalHotkeys interface {
	// Register claims stroke, calling fire on a goroutine of its own each time it is pressed. False when the desktop refuses it.
	Register(stroke domain.Keystroke, fire func()) bool
	UnregisterAll()
}

// KeyboardHostOptions is what the keyboard needs from the rest of the app.
type KeyboardHostOptions struct {
	// Send pushes a message to the window; false when there is no window to take it.
	Send func(channel string, payload any) bool
	// Reveal brings the window back to the user.
	Reveal func()
	// Notify shows a desktop notice.
	Notify func(title, body string)
	// WriteAttachment keeps a base64 PNG with its context in the attachments folder, answering its path.
	WriteAttachment func(png string, context any) (string, error)
	// OpenExternal opens a URL in the system's handler for it.
	OpenExternal func(url string) error
	// Capture grabs the frontmost window; a WindowCapture's CaptureFrontmostWindow.
	Capture func(ctx context.Context, sound bool) WindowShot
	// Prepare readies the next capture once the shortcut is held; a WindowCapture's Prepare.
	Prepare func()
	// Hotkeys replaces the platform's own.
	Hotkeys GlobalHotkeys
	// Env replaces os.Getenv for the window-capture capability.
	Env platform.Env
}

// KeyEvent is one edge of a keystroke, as the window or a page in the browser panel saw it.
type KeyEvent struct {
	// Type is "keyDown" or "keyUp".
	Type         string
	Input        domain.KeyInput
	IsAutoRepeat bool
}

// modifierKeys are the keys whose release ends holding to talk.
var modifierKeys = map[string]bool{"Meta": true, "Control": true, "Alt": true, "Shift": true}

// KeyboardHost matches keystrokes against the bindings, and holds the desktop-wide capture shortcut.
// Matching happens here rather than in the window, because a page in the browser panel swallows
// every keystroke it is given, and the window decides what each action means once it lands.
type KeyboardHost struct {
	options KeyboardHostOptions

	// claimMu keeps claims and releases of the desktop shortcut in order, without holding mu while the desktop answers.
	claimMu   sync.Mutex
	mu        sync.Mutex
	shortcuts []*domain.ShortcutBinding
	// capturing is while settings wait for a keystroke: every keystroke goes to them instead of to an action.
	capturing bool
	// captureOptions is how a grab announces itself. The window owns the choice and hands it over as the user changes it.
	captureOptions domain.CaptureOptions
	// desktopBinding is what the desktop is currently holding for us, so an unchanged binding is never registered again.
	desktopBinding *string
	// talkKey is the physical key holding to talk, until it or any modifier with it goes up.
	talkKey *string
}

var _ contracts.ShortcutPortRuntime = (*KeyboardHost)(nil)

func NewKeyboardHost(options KeyboardHostOptions) *KeyboardHost {
	if options.Hotkeys == nil {
		options.Hotkeys = newPlatformHotkeys()
	}
	if options.Env == nil {
		options.Env = os.Getenv
	}
	return &KeyboardHost{options: options, shortcuts: domain.ResolveShortcuts(domain.ShortcutOverrides{}), captureOptions: domain.DefaultCaptureOptions}
}

func (k *KeyboardHost) send(channel string, payload any) bool {
	if k.options.Send == nil {
		return false
	}
	return k.options.Send(channel, payload)
}

func (k *KeyboardHost) notify(title, body string) {
	if k.options.Notify != nil {
		k.options.Notify(title, body)
	}
}

// holdToTalk hears both edges of a keystroke; every other action only its press. Its keys are let
// through, because a release taken here never reaches the page; the window swallows them. Nil when
// the keystroke is not holding to talk.
func (k *KeyboardHost) holdToTalk(event KeyEvent, surface domain.ShortcutSurface) *bool {
	no := false
	if k.talkKey == nil {
		return nil
	}
	if event.Type == "keyUp" && (event.Input.Code == *k.talkKey || modifierKeys[event.Input.Key]) {
		k.talkKey = nil
		k.send(ChannelShortcut, contracts.ShortcutInvocation{Action: "dictation.release", Surface: surface})
		return &no
	}
	if event.Input.Code != *k.talkKey {
		return nil
	}
	if event.IsAutoRepeat {
		return &no
	}
	// A fresh press means its release went to another app, so this one is matched anew.
	k.talkKey = nil
	return nil
}

// HandleKey is whether the app took the keystroke, which is also whether the page or the menu must not see it.
func (k *KeyboardHost) HandleKey(event KeyEvent, surface domain.ShortcutSurface) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if held := k.holdToTalk(event, surface); held != nil {
		return *held
	}
	if event.Type != "keyDown" {
		return false
	}
	stroke := domain.KeystrokeOf(event.Input, platform.OS == "darwin")
	if stroke == nil {
		return false
	}
	if k.capturing {
		switch {
		case stroke.Key == "Escape":
			k.send(ChannelShortcutCaptured, (*string)(nil))
		case stroke.Mod || stroke.Ctrl || stroke.Alt:
			binding := domain.FormatShortcut(*stroke)
			k.send(ChannelShortcutCaptured, &binding)
		default:
			return false
		}
		return true
	}
	binding := domain.ShortcutFor(k.shortcuts, *stroke, surface)
	if binding == nil {
		return false
	}
	if binding.Action == "dictation.hold" {
		if surface != domain.ShortcutSurfaceAny || event.IsAutoRepeat {
			return false
		}
		code := event.Input.Code
		k.talkKey = &code
		k.send(ChannelShortcut, contracts.ShortcutInvocation{Action: binding.Action, Surface: surface})
		return false
	}
	k.send(ChannelShortcut, contracts.ShortcutInvocation{Action: binding.Action, Surface: surface})
	return true
}

// CaptureWindowToComposer grabs the frontmost window and sends it to the composer the user was last in.
func (k *KeyboardHost) CaptureWindowToComposer(ctx context.Context) {
	k.mu.Lock()
	options := k.captureOptions
	k.mu.Unlock()
	shot := k.options.Capture(ctx, options.Sound)
	switch shot.Status {
	case WindowShotCaptured:
		file, err := k.options.WriteAttachment(shot.PNG, shot.Context)
		if err != nil {
			k.notify("Could not keep the screenshot", err.Error())
			return
		}
		k.send(ChannelScreenshot, contracts.WindowScreenshot{App: shot.App, Title: shot.Title, Path: file})
		// Only ever after the capture: neither the flash nor coming forward belongs in the shot.
		if options.Focus {
			FlashWindow(shot.Frame)
			if k.options.Reveal != nil {
				k.options.Reveal()
			}
		} else {
			k.notify("Screenshot attached", fmt.Sprintf("%s — waiting in AI Coding Tool", shot.App))
		}
	case WindowShotDenied:
		k.notify("AI Coding Tool needs Screen Recording", "Grant it in System Settings → Privacy & Security, then try again.")
		if k.options.OpenExternal != nil {
			go func() {
				_ = k.options.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture")
			}()
		}
	case WindowShotNoWindow:
		k.notify("Nothing to capture", fmt.Sprintf("%s has no window on screen.", shot.App))
	default:
		k.notify("Could not capture the window", shot.Message)
	}
}

// ReleaseDesktopShortcut lets the desktop have the capture keystroke back.
func (k *KeyboardHost) ReleaseDesktopShortcut() {
	k.claimMu.Lock()
	defer k.claimMu.Unlock()
	k.releaseDesktopShortcut()
}

// releaseDesktopShortcut runs with claimMu held.
func (k *KeyboardHost) releaseDesktopShortcut() {
	k.options.Hotkeys.UnregisterAll()
	k.mu.Lock()
	k.desktopBinding = nil
	k.mu.Unlock()
}

func sameBinding(left, right *string) bool {
	return (left == nil) == (right == nil) && (left == nil || *left == *right)
}

// ClaimDesktopShortcut claims the capture keystroke from the whole desktop without activating the
// app, so the app the user is in keeps the keyboard and stays the app the capture describes.
func (k *KeyboardHost) ClaimDesktopShortcut() {
	k.claimMu.Lock()
	defer k.claimMu.Unlock()
	k.mu.Lock()
	var wanted *domain.ShortcutBinding
	for _, binding := range k.shortcuts {
		if binding.Surface == domain.ShortcutSurfaceDesktop && binding.Action == "window.capture" {
			wanted = binding
			break
		}
	}
	held := k.desktopBinding
	k.mu.Unlock()
	var accelerator *string
	if wanted != nil {
		if value, ok := domain.DesktopAccelerator(wanted.Binding); ok {
			accelerator = &value
		}
	}
	capability := platform.WindowCaptureCapability(k.options.Env)
	if capability.Status == "unsupported" {
		if held != nil {
			k.releaseDesktopShortcut()
		}
		// A replacement renderer needs the status too, so every preferences handshake sends it anew.
		if accelerator != nil {
			k.send(ChannelShortcutRefused, contracts.DesktopShortcutRefusalUnsupported{Binding: wanted.Binding, Message: capability.Message})
		}
		return
	}
	if sameBinding(accelerator, held) {
		return
	}
	k.releaseDesktopShortcut()
	if accelerator == nil {
		return
	}
	stroke := domain.ParseShortcut(wanted.Binding)
	if stroke == nil || !k.options.Hotkeys.Register(*stroke, func() { k.CaptureWindowToComposer(context.Background()) }) {
		k.send(ChannelShortcutRefused, contracts.DesktopShortcutRefusalTaken{Binding: wanted.Binding})
		return
	}
	k.mu.Lock()
	k.desktopBinding = accelerator
	k.mu.Unlock()
	if k.options.Prepare != nil {
		go k.options.Prepare()
	}
}

func (k *KeyboardHost) SetShortcuts(overrides domain.ShortcutOverrides) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.shortcuts = domain.ResolveShortcuts(overrides)
}

func (k *KeyboardHost) SetShortcutCapture(capturing bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.capturing = capturing
}

func (k *KeyboardHost) SetCaptureOptions(options domain.CaptureOptions) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.captureOptions = options
}
