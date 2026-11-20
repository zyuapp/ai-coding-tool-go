package devices

import (
	"os"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// x11Keysyms are the keysyms of the keys a binding names; letters by their lowercase keysym.
var x11Keysyms = map[string]xproto.Keysym{
	",": 0x2c, ".": 0x2e, "/": 0x2f, "\\": 0x5c, "[": 0x5b, "]": 0x5d, "-": 0x2d, "=": 0x3d, ";": 0x3b, "'": 0x27, "`": 0x60,
	"ArrowLeft": 0xff51, "ArrowUp": 0xff52, "ArrowRight": 0xff53, "ArrowDown": 0xff54, "Enter": 0xff0d, "Space": 0x20,
	"Tab": 0xff09, "Escape": 0xff1b, "Backspace": 0xff08, "Delete": 0xffff, "Home": 0xff50, "End": 0xff57,
	"PageUp": 0xff55, "PageDown": 0xff56,
}

func x11Keysym(key string) (xproto.Keysym, bool) {
	if keysym, ok := x11Keysyms[key]; ok {
		return keysym, true
	}
	if len(key) == 1 && key[0] >= 'A' && key[0] <= 'Z' {
		return xproto.Keysym(key[0] - 'A' + 'a'), true
	}
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		return xproto.Keysym(key[0]), true
	}
	if len(key) >= 2 && key[0] == 'F' {
		number := 0
		for _, digit := range key[1:] {
			if digit < '0' || digit > '9' {
				return 0, false
			}
			number = number*10 + int(digit-'0')
		}
		if number >= 1 && number <= 12 {
			return xproto.Keysym(0xffbe + number - 1), true
		}
	}
	return 0, false
}

type x11Grab struct {
	keycode   xproto.Keycode
	modifiers uint16
	fire      func()
}

// x11Hotkeys grab keys on the X display's root window. A desktop with no X display refuses every key.
type x11Hotkeys struct {
	mu    sync.Mutex
	conn  *xgb.Conn
	root  xproto.Window
	grabs []x11Grab
}

func newPlatformHotkeys() GlobalHotkeys { return &x11Hotkeys{} }

// ignoredModifiers are Caps Lock and Num Lock, which must not stop a hotkey from firing.
var ignoredModifiers = []uint16{0, xproto.ModMaskLock, xproto.ModMask2, xproto.ModMaskLock | xproto.ModMask2}

func (h *x11Hotkeys) connect() bool {
	if h.conn != nil {
		return true
	}
	if os.Getenv("DISPLAY") == "" {
		return false
	}
	conn, err := xgb.NewConn()
	if err != nil {
		return false
	}
	h.conn = conn
	h.root = xproto.Setup(conn).DefaultScreen(conn).Root
	go h.listen(conn)
	return true
}

func (h *x11Hotkeys) listen(conn *xgb.Conn) {
	for {
		event, err := conn.WaitForEvent()
		if event == nil && err == nil {
			return
		}
		press, ok := event.(xproto.KeyPressEvent)
		if !ok {
			continue
		}
		state := press.State &^ (xproto.ModMaskLock | xproto.ModMask2)
		h.mu.Lock()
		var fire func()
		for _, grab := range h.grabs {
			if grab.keycode == press.Detail && grab.modifiers == state {
				fire = grab.fire
			}
		}
		h.mu.Unlock()
		if fire != nil {
			go fire()
		}
	}
}

func (h *x11Hotkeys) keycode(keysym xproto.Keysym) (xproto.Keycode, bool) {
	setup := xproto.Setup(h.conn)
	count := byte(setup.MaxKeycode - setup.MinKeycode + 1)
	mapping, err := xproto.GetKeyboardMapping(h.conn, setup.MinKeycode, count).Reply()
	if err != nil || mapping.KeysymsPerKeycode == 0 {
		return 0, false
	}
	per := int(mapping.KeysymsPerKeycode)
	for index := 0; index*per < len(mapping.Keysyms); index++ {
		for column := range per {
			if mapping.Keysyms[index*per+column] == keysym {
				return setup.MinKeycode + xproto.Keycode(index), true
			}
		}
	}
	return 0, false
}

func (h *x11Hotkeys) Register(stroke domain.Keystroke, fire func()) bool {
	keysym, ok := x11Keysym(stroke.Key)
	if !ok {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.connect() {
		return false
	}
	keycode, ok := h.keycode(keysym)
	if !ok {
		return false
	}
	// The binding's Mod is Ctrl off macOS, and its Ctrl the Super key.
	var modifiers uint16
	if stroke.Mod {
		modifiers |= xproto.ModMaskControl
	}
	if stroke.Ctrl {
		modifiers |= xproto.ModMask4
	}
	if stroke.Alt {
		modifiers |= xproto.ModMask1
	}
	if stroke.Shift {
		modifiers |= xproto.ModMaskShift
	}
	var grabbed []uint16
	for _, extra := range ignoredModifiers {
		if err := xproto.GrabKeyChecked(h.conn, true, h.root, modifiers|extra, keycode, xproto.GrabModeAsync, xproto.GrabModeAsync).Check(); err != nil {
			for _, done := range grabbed {
				xproto.UngrabKey(h.conn, keycode, h.root, done)
			}
			return false
		}
		grabbed = append(grabbed, modifiers|extra)
	}
	h.grabs = append(h.grabs, x11Grab{keycode: keycode, modifiers: modifiers, fire: fire})
	return true
}

func (h *x11Hotkeys) UnregisterAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn == nil {
		return
	}
	for _, grab := range h.grabs {
		for _, extra := range ignoredModifiers {
			xproto.UngrabKey(h.conn, grab.keycode, h.root, grab.modifiers|extra)
		}
	}
	h.grabs = nil
	h.conn.Sync()
}
