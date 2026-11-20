package desktop

import (
	"encoding/json/v2"
	"os"
	"strconv"
	"sync"
)

// WindowTheme mirrors contracts/ipc.ts: the theme's canvas and ground, which the frame is drawn from.
type WindowTheme struct {
	Variant string `json:"variant"`
	Canvas  string `json:"canvas"`
	// Whether the renderer took this ground from the system, in which case native chrome asks it too.
	Follow *bool `json:"follow,omitzero"`
}

// DefaultWindowTheme is the theme of a first launch.
var DefaultWindowTheme = WindowTheme{Variant: "dark", Canvas: "#0e1117"}

const ThemeFile = "window-theme.v1.json"

func (theme WindowTheme) follows() bool { return theme.Follow != nil && *theme.Follow }

func (theme WindowTheme) same(other WindowTheme) bool {
	return theme.Variant == other.Variant && theme.Canvas == other.Canvas && theme.follows() == other.follows()
}

func parseWindowTheme(data []byte) (WindowTheme, bool) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil || !isWindowThemeValue(value, true) {
		return WindowTheme{}, false
	}
	var theme WindowTheme
	if err := json.Unmarshal(data, &theme); err != nil {
		return WindowTheme{}, false
	}
	return theme, true
}

// LoadWindowTheme reads the remembered theme; an unreadable file means the default.
func LoadWindowTheme(file string) WindowTheme {
	data, err := os.ReadFile(file)
	if err != nil {
		return DefaultWindowTheme
	}
	theme, ok := parseWindowTheme(data)
	if !ok {
		return DefaultWindowTheme
	}
	return theme
}

// canvasRGB reads a `#rrggbb` canvas; the theme guard has already checked its shape.
func canvasRGB(canvas string) (r, g, b uint8) {
	value, err := strconv.ParseUint(canvas[1:], 16, 32)
	if err != nil {
		return 0x0e, 0x11, 0x17
	}
	return uint8(value >> 16), uint8(value >> 8), uint8(value)
}

type themeWriter struct {
	mu sync.Mutex
}

func (writer *themeWriter) remember(file string, theme WindowTheme) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	data, err := json.Marshal(theme)
	if err != nil {
		return
	}
	_ = os.WriteFile(file, data, 0o644)
}
