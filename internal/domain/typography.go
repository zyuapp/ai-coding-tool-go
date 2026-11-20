package domain

import (
	"math"
	"regexp"
)

var UiFonts = []*Font{
	{ID: "system", Label: "System"},
	{ID: "inter", Label: "Inter"},
	{ID: "plex-sans", Label: "IBM Plex Sans"},
	{ID: "geist", Label: "Geist"},
}

var MonoFonts = []*Font{
	{ID: "system", Label: "System"},
	{ID: "jetbrains-mono", Label: "JetBrains Mono"},
	{ID: "fira-code", Label: "Fira Code"},
	{ID: "source-code-pro", Label: "Source Code Pro"},
}

const DefaultUiFont = "system"
const DefaultMonoFont = "system"

// customFont names a family the app does not bundle. It is spliced into a CSS font stack, so anything that could close the string is refused.
var customFont = regexp.MustCompile(`^installed:([A-Za-z0-9][A-Za-z0-9 ._-]{0,63})$`)

func CustomFontID(family string) string {
	return "installed:" + family
}

// CustomFontFamily is the family a custom font id names; false for anything else.
func CustomFontFamily(id any) (string, bool) {
	text, ok := id.(string)
	if !ok {
		return "", false
	}
	match := customFont.FindStringSubmatch(text)
	if match == nil {
		return "", false
	}
	return match[1], true
}

func IsNameableFont(family string) bool {
	_, ok := CustomFontFamily(CustomFontID(family))
	return ok
}

func fontsByID(fonts []*Font) map[string]*Font {
	byID := make(map[string]*Font, len(fonts))
	for _, font := range fonts {
		byID[font.ID] = font
	}
	return byID
}

var uiById = fontsByID(UiFonts)
var monoById = fontsByID(MonoFonts)

func fontById(known map[string]*Font, id any) *Font {
	text, ok := id.(string)
	if !ok {
		return nil
	}
	if family, ok := CustomFontFamily(text); ok {
		return &Font{ID: text, Label: family}
	}
	return known[text]
}

func UiFontByID(id any) *Font {
	return fontById(uiById, id)
}

func MonoFontByID(id any) *Font {
	return fontById(monoById, id)
}

func UiFontOrDefault(id any) *Font {
	if font := UiFontByID(id); font != nil {
		return font
	}
	return uiById[DefaultUiFont]
}

func MonoFontOrDefault(id any) *Font {
	if font := MonoFontByID(id); font != nil {
		return font
	}
	return monoById[DefaultMonoFont]
}

var ReadingSize = SizeRange{
	Min:     12,
	Max:     24,
	Default: 15,
	Legacy:  map[string]float64{"small": 13, "regular": 15, "large": 17, "larger": 19},
}

var TerminalSize = SizeRange{
	Min:     10,
	Max:     22,
	Default: 12,
	Legacy:  map[string]float64{"small": 11, "regular": 12, "large": 13, "larger": 15},
}

func ClampSize(r SizeRange, value float64) float64 {
	return min(r.Max, max(r.Min, math.Floor(value+0.5)))
}

// SizeByID is a number in range, a rung this once offered, or false.
func SizeByID(r SizeRange, value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value < r.Min || value > r.Max {
			return 0, false
		}
		return math.Floor(value + 0.5), true
	case string:
		if legacy, ok := r.Legacy[value]; ok {
			return ClampSize(r, legacy), true
		}
	}
	return 0, false
}

func SizeOrDefault(r SizeRange, value any) float64 {
	if size, ok := SizeByID(r, value); ok {
		return size
	}
	return r.Default
}

// StepSize is the next px up (delta 1) or down (delta -1); the ends hold rather than wrap.
func StepSize(r SizeRange, value any, delta float64) float64 {
	return ClampSize(r, SizeOrDefault(r, value)+delta)
}
