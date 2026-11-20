package devices

/*
#cgo LDFLAGS: -framework Carbon -framework Foundation
#include <stdint.h>

int actRegisterHotkey(uint32_t identifier, uint32_t keyCode, uint32_t modifiers);
void actUnregisterHotkey(uint32_t identifier);
*/
import "C"

import (
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// macKeyCodes are the virtual key codes of an ANSI keyboard, by the key a binding names.
var macKeyCodes = map[string]uint32{
	"A": 0x00, "S": 0x01, "D": 0x02, "F": 0x03, "H": 0x04, "G": 0x05, "Z": 0x06, "X": 0x07, "C": 0x08, "V": 0x09,
	"B": 0x0B, "Q": 0x0C, "W": 0x0D, "E": 0x0E, "R": 0x0F, "Y": 0x10, "T": 0x11, "1": 0x12, "2": 0x13, "3": 0x14,
	"4": 0x15, "6": 0x16, "5": 0x17, "=": 0x18, "9": 0x19, "7": 0x1A, "-": 0x1B, "8": 0x1C, "0": 0x1D, "]": 0x1E,
	"O": 0x1F, "U": 0x20, "[": 0x21, "I": 0x22, "P": 0x23, "Enter": 0x24, "L": 0x25, "J": 0x26, "'": 0x27, "K": 0x28,
	";": 0x29, "\\": 0x2A, ",": 0x2B, "/": 0x2C, "N": 0x2D, "M": 0x2E, ".": 0x2F, "Tab": 0x30, "Space": 0x31, "`": 0x32,
	"Backspace": 0x33, "Escape": 0x35, "F5": 0x60, "F6": 0x61, "F7": 0x62, "F3": 0x63, "F8": 0x64, "F9": 0x65,
	"F11": 0x67, "F10": 0x6D, "F12": 0x6F, "Home": 0x73, "PageUp": 0x74, "Delete": 0x75, "F4": 0x76, "End": 0x77,
	"F2": 0x78, "PageDown": 0x79, "F1": 0x7A, "ArrowLeft": 0x7B, "ArrowRight": 0x7C, "ArrowDown": 0x7D, "ArrowUp": 0x7E,
}

// Carbon's modifier flags.
const (
	carbonCommand = 0x0100
	carbonShift   = 0x0200
	carbonOption  = 0x0800
	carbonControl = 0x1000
)

var carbonHotkeys = struct {
	sync.Mutex
	next  uint32
	fires map[uint32]func()
}{fires: map[uint32]func(){}}

//export actHotkeyPressed
func actHotkeyPressed(identifier C.uint32_t) {
	carbonHotkeys.Lock()
	fire := carbonHotkeys.fires[uint32(identifier)]
	carbonHotkeys.Unlock()
	if fire != nil {
		go fire()
	}
}

// macHotkeys are Carbon's hot keys, which reach the app whichever app is in front.
type macHotkeys struct {
	mu  sync.Mutex
	ids []uint32
}

func newPlatformHotkeys() GlobalHotkeys { return &macHotkeys{} }

func (h *macHotkeys) Register(stroke domain.Keystroke, fire func()) bool {
	code, ok := macKeyCodes[stroke.Key]
	if !ok {
		return false
	}
	var modifiers uint32
	if stroke.Mod {
		modifiers |= carbonCommand
	}
	if stroke.Ctrl {
		modifiers |= carbonControl
	}
	if stroke.Alt {
		modifiers |= carbonOption
	}
	if stroke.Shift {
		modifiers |= carbonShift
	}
	carbonHotkeys.Lock()
	carbonHotkeys.next++
	id := carbonHotkeys.next
	carbonHotkeys.fires[id] = fire
	carbonHotkeys.Unlock()
	if C.actRegisterHotkey(C.uint32_t(id), C.uint32_t(code), C.uint32_t(modifiers)) == 0 {
		carbonHotkeys.Lock()
		delete(carbonHotkeys.fires, id)
		carbonHotkeys.Unlock()
		return false
	}
	h.mu.Lock()
	h.ids = append(h.ids, id)
	h.mu.Unlock()
	return true
}

func (h *macHotkeys) UnregisterAll() {
	h.mu.Lock()
	ids := h.ids
	h.ids = nil
	h.mu.Unlock()
	for _, id := range ids {
		C.actUnregisterHotkey(C.uint32_t(id))
		carbonHotkeys.Lock()
		delete(carbonHotkeys.fires, id)
		carbonHotkeys.Unlock()
	}
}
