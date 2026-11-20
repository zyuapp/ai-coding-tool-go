package domain

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

func TestThemesMatchTheStylesheet(t *testing.T) {
	css, err := os.ReadFile("../../frontend/src/renderer/themes.css")
	if err != nil {
		t.Skip(err)
	}
	blocks := map[string]map[string]string{}
	for _, block := range regexp.MustCompile(`\[data-theme="([a-z-]+)"\] \{([^}]*)\}`).FindAllStringSubmatch(string(css), -1) {
		tokens := blocks[block[1]]
		if tokens == nil {
			tokens = map[string]string{}
			blocks[block[1]] = tokens
		}
		for _, token := range regexp.MustCompile(`(--[a-z0-9-]+|color-scheme):\s*([^;]+);`).FindAllStringSubmatch(block[2], -1) {
			tokens[token[1]] = jsTrim(token[2])
		}
	}
	if len(blocks) != len(Themes) {
		t.Errorf("%d blocks, %d themes", len(blocks), len(Themes))
	}
	for _, theme := range Themes {
		tokens := blocks[theme.ID]
		if tokens["--p-bg-0"] != theme.Canvas || tokens["color-scheme"] != string(theme.Variant) {
			t.Errorf("%s paints %s/%s", theme.ID, tokens["--p-bg-0"], tokens["color-scheme"])
		}
	}
}

func TestEveryFamilyHasOneThemePerGround(t *testing.T) {
	for _, family := range ThemeFamilies() {
		for _, variant := range []ThemeVariant{ThemeVariantDark, ThemeVariantLight} {
			index := slices.IndexFunc(Themes, func(theme *Theme) bool { return theme.Family == family && theme.Variant == variant })
			if index < 0 || ThemeFor(family, variant) != Themes[index] {
				t.Errorf("%s %s", family, variant)
			}
		}
	}
	if len(ThemeFamilies())*2 != len(Themes) {
		t.Error("families")
	}
	if ThemeFor("A Palette We Dropped", ThemeVariantLight).Variant != ThemeVariantLight || ThemeFor("A Palette We Dropped", ThemeVariantDark).Family != ThemeOrDefault(DefaultTheme).Family {
		t.Error("dropped family")
	}
	if ThemeOrDefault("a-theme-we-dropped").ID != DefaultTheme || ThemeOrDefault(nil).ID != DefaultTheme {
		t.Error("dropped theme")
	}
}

func TestModeNamesAGround(t *testing.T) {
	if VariantFor(ThemeModeDark, false) != ThemeVariantDark || VariantFor(ThemeModeLight, true) != ThemeVariantLight || VariantFor(ThemeModeAuto, true) != ThemeVariantDark || VariantFor(ThemeModeAuto, false) != ThemeVariantLight {
		t.Error("variantFor")
	}
	if ThemeModeOrDefault("a-mode-we-dropped") != DefaultThemeMode || ThemeModeOrDefault("auto") != ThemeModeAuto || IsThemeMode("system") {
		t.Error("modes")
	}
}
