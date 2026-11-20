package desktop

import (
	"encoding/json/v2"
	"os"
	"strconv"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// DefaultWindowTheme is the theme of a first launch.
var DefaultWindowTheme = contracts.WindowTheme{Variant: contracts.WindowThemeVariantDark, Canvas: "#0e1117"}

const ThemeFile = "window-theme.v1.json"

func themeFollows(theme contracts.WindowTheme) bool { return theme.Follow != nil && *theme.Follow }

func sameTheme(a, b contracts.WindowTheme) bool {
	return a.Variant == b.Variant && a.Canvas == b.Canvas && themeFollows(a) == themeFollows(b)
}

func parseWindowTheme(data []byte) (contracts.WindowTheme, bool) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil || !isWindowThemeValue(value, true) {
		return contracts.WindowTheme{}, false
	}
	var theme contracts.WindowTheme
	if err := json.Unmarshal(data, &theme); err != nil {
		return contracts.WindowTheme{}, false
	}
	return theme, true
}

// LoadWindowTheme reads the remembered theme; an unreadable file means the default.
func LoadWindowTheme(file string) contracts.WindowTheme {
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

func (writer *themeWriter) remember(file string, theme contracts.WindowTheme) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	data, err := json.Marshal(theme)
	if err != nil {
		return
	}
	_ = os.WriteFile(file, data, 0o644)
}
