package devices

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

type fakeHotkeys struct {
	mu         sync.Mutex
	registered []domain.Keystroke
	fire       func()
	refuse     bool
	released   int
}

func (h *fakeHotkeys) Register(stroke domain.Keystroke, fire func()) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.refuse {
		return false
	}
	h.registered = append(h.registered, stroke)
	h.fire = fire
	return true
}

func (h *fakeHotkeys) UnregisterAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.released++
	h.fire = nil
}

type sent struct {
	channel string
	payload any
}

type keyboardFixture struct {
	mu       sync.Mutex
	sent     []sent
	notices  [][2]string
	revealed int
	opened   []string
	shot     WindowShot
	written  []string
	hotkeys  *fakeHotkeys
	host     *KeyboardHost
	env      map[string]string
}

func newKeyboardFixture() *keyboardFixture {
	f := &keyboardFixture{hotkeys: &fakeHotkeys{}, env: map[string]string{"DISPLAY": ":0"}}
	f.host = NewKeyboardHost(KeyboardHostOptions{
		Send: func(channel string, payload any) bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.sent = append(f.sent, sent{channel, payload})
			return true
		},
		Reveal: func() { f.revealed++ },
		Notify: func(title, body string) { f.notices = append(f.notices, [2]string{title, body}) },
		WriteAttachment: func(png string, _ any) (string, error) {
			if png == "unwritable" {
				return "", errors.New("disk full")
			}
			f.written = append(f.written, png)
			return "/attachments/shot.png", nil
		},
		OpenExternal: func(url string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.opened = append(f.opened, url)
			return nil
		},
		Capture: func(context.Context, bool) WindowShot { return f.shot },
		Hotkeys: f.hotkeys,
		Env:     func(name string) string { return f.env[name] },
	})
	return f
}

func keyDown(key, code string, modifiers ...string) KeyEvent {
	input := domain.KeyInput{Key: key, Code: code}
	for _, modifier := range modifiers {
		switch modifier {
		case "meta":
			input.Meta = true
		case "control":
			input.Control = true
		case "alt":
			input.Alt = true
		case "shift":
			input.Shift = true
		}
	}
	return KeyEvent{Type: "keyDown", Input: input}
}

func TestKeystrokesMatchTheirActionOnTheirSurface(t *testing.T) {
	f := newKeyboardFixture()
	if f.host.HandleKey(keyDown("x", "KeyX"), domain.ShortcutSurfaceAny) {
		t.Fatal("an unbound key was taken")
	}
	capture := keyDown("S", "KeyS", "alt", "shift")
	if f.host.HandleKey(capture, domain.ShortcutSurfaceAny) {
		t.Fatal("a desktop binding was taken inside the window")
	}
	f.host.SetShortcutCapture(true)
	if !f.host.HandleKey(capture, domain.ShortcutSurfaceAny) {
		t.Fatal("settings did not get the keystroke")
	}
	if !f.host.HandleKey(keyDown("Escape", "Escape"), domain.ShortcutSurfaceAny) || f.host.HandleKey(keyDown("a", "KeyA"), domain.ShortcutSurfaceAny) {
		t.Fatal("capture took the wrong keys")
	}
	binding := "Alt+Shift+S"
	want := []sent{{ChannelShortcutCaptured, &binding}, {ChannelShortcutCaptured, (*string)(nil)}}
	if !reflect.DeepEqual(f.sent, want) {
		t.Fatalf("sent %#v", f.sent)
	}
}

func TestHoldingToTalkHearsBothEdges(t *testing.T) {
	f := newKeyboardFixture()
	hold := keyDown("d", "KeyD", "alt")
	if f.host.HandleKey(hold, domain.ShortcutSurfaceAny) {
		t.Fatal("holding to talk swallowed its key")
	}
	repeat := hold
	repeat.IsAutoRepeat = true
	if f.host.HandleKey(repeat, domain.ShortcutSurfaceAny) {
		t.Fatal("a repeat was taken")
	}
	if f.host.HandleKey(KeyEvent{Type: "keyUp", Input: domain.KeyInput{Key: "Alt", Code: "AltLeft"}}, domain.ShortcutSurfaceAny) {
		t.Fatal("the release was taken")
	}
	want := []sent{
		{ChannelShortcut, contracts.ShortcutInvocation{Action: "dictation.hold", Surface: domain.ShortcutSurfaceAny}},
		{ChannelShortcut, contracts.ShortcutInvocation{Action: "dictation.release", Surface: domain.ShortcutSurfaceAny}},
	}
	if !reflect.DeepEqual(f.sent, want) {
		t.Fatalf("sent %#v", f.sent)
	}
	if f.host.HandleKey(hold, domain.ShortcutSurfaceBrowser) || len(f.sent) != 2 {
		t.Fatal("holding to talk started inside a page")
	}
}

func TestDesktopShortcutIsClaimedOnceAndRefusalsAreSaid(t *testing.T) {
	f := newKeyboardFixture()
	f.host.ClaimDesktopShortcut()
	f.host.ClaimDesktopShortcut()
	if len(f.hotkeys.registered) != 1 || f.hotkeys.registered[0] != (domain.Keystroke{Key: "S", Alt: true, Shift: true}) {
		t.Fatalf("registered %v", f.hotkeys.registered)
	}
	f.host.ReleaseDesktopShortcut()
	f.hotkeys.refuse = true
	f.host.ClaimDesktopShortcut()
	if !reflect.DeepEqual(f.sent, []sent{{ChannelShortcutRefused, contracts.DesktopShortcutRefusalTaken{Binding: "Alt+Shift+S"}}}) {
		t.Fatalf("sent %#v", f.sent)
	}
	unbound := ""
	f.host.SetShortcuts(domain.ShortcutOverrides{"window.capture": &unbound})
	f.host.ClaimDesktopShortcut()
	if len(f.sent) != 1 {
		t.Fatalf("an unbound capture was refused: %#v", f.sent)
	}
}

func TestUnsupportedCaptureIsSaidOnEveryHandshake(t *testing.T) {
	if platform.OS != "linux" {
		t.Skip("capture is always available on macOS")
	}
	f := newKeyboardFixture()
	f.env = map[string]string{"WAYLAND_DISPLAY": "wayland-0"}
	f.host.ClaimDesktopShortcut()
	f.host.ClaimDesktopShortcut()
	if len(f.sent) != 2 || len(f.hotkeys.registered) != 0 {
		t.Fatalf("sent %#v", f.sent)
	}
	if refusal, ok := f.sent[0].payload.(contracts.DesktopShortcutRefusalUnsupported); !ok || refusal.Binding != "Alt+Shift+S" || refusal.Message == "" {
		t.Fatalf("refusal %#v", f.sent[0].payload)
	}
}

func TestGrabbedWindowGoesToTheComposerOrSaysWhyNot(t *testing.T) {
	f := newKeyboardFixture()
	f.shot = WindowShot{Status: WindowShotCaptured, App: "Editor", Title: "Draft", PNG: "cG5n"}
	f.host.CaptureWindowToComposer(t.Context())
	if !reflect.DeepEqual(f.sent, []sent{{ChannelScreenshot, contracts.WindowScreenshot{App: "Editor", Title: "Draft", Path: "/attachments/shot.png"}}}) || f.revealed != 1 {
		t.Fatalf("sent %#v, revealed %d", f.sent, f.revealed)
	}
	f.host.SetCaptureOptions(domain.CaptureOptions{Sound: false, Focus: false})
	f.host.CaptureWindowToComposer(t.Context())
	f.shot.PNG = "unwritable"
	f.host.CaptureWindowToComposer(t.Context())
	f.shot = WindowShot{Status: WindowShotNoWindow, App: "Finder"}
	f.host.CaptureWindowToComposer(t.Context())
	f.shot = WindowShot{Status: WindowShotFailed, Message: "it broke"}
	f.host.CaptureWindowToComposer(t.Context())
	f.shot = WindowShot{Status: WindowShotDenied}
	f.host.CaptureWindowToComposer(t.Context())
	want := [][2]string{
		{"Screenshot attached", "Editor — waiting in AI Coding Tool"},
		{"Could not keep the screenshot", "disk full"},
		{"Nothing to capture", "Finder has no window on screen."},
		{"Could not capture the window", "it broke"},
		{"AI Coding Tool needs Screen Recording", "Grant it in System Settings → Privacy & Security, then try again."},
	}
	if !reflect.DeepEqual(f.notices, want) || f.revealed != 1 {
		t.Fatalf("notices %v", f.notices)
	}
	for range 100 {
		f.mu.Lock()
		opened := len(f.opened)
		f.mu.Unlock()
		if opened == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !reflect.DeepEqual(f.opened, []string{"x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"}) {
		t.Fatalf("opened %v", f.opened)
	}
}
