package domain

import "slices"

var Themes = []*Theme{
	{ID: "aicodingtool-dark", Label: "AI Coding Tool Dark", Family: "AI Coding Tool", Variant: ThemeVariantDark, Canvas: "#0e1117"},
	{ID: "aicodingtool-light", Label: "AI Coding Tool Light", Family: "AI Coding Tool", Variant: ThemeVariantLight, Canvas: "#faf9f6"},
	{ID: "abyss", Label: "Abyss", Family: "Abyss", Variant: ThemeVariantDark, Canvas: "#001024"},
	{ID: "shoal", Label: "Shoal", Family: "Abyss", Variant: ThemeVariantLight, Canvas: "#f2fafe"},
	{ID: "aurora-night", Label: "Aurora Night", Family: "Aurora", Variant: ThemeVariantDark, Canvas: "#051423"},
	{ID: "aurora-day", Label: "Aurora Day", Family: "Aurora", Variant: ThemeVariantLight, Canvas: "#f3fafc"},
	{ID: "ayu-dark", Label: "Ayu Dark", Family: "Ayu", Variant: ThemeVariantDark, Canvas: "#0b0e14"},
	{ID: "ayu-light", Label: "Ayu Light", Family: "Ayu", Variant: ThemeVariantLight, Canvas: "#f8f9fa"},
	{ID: "broadsheet-press", Label: "Broadsheet Press", Family: "Broadsheet", Variant: ThemeVariantDark, Canvas: "#140c09"},
	{ID: "broadsheet-salmon", Label: "Broadsheet Salmon", Family: "Broadsheet", Variant: ThemeVariantLight, Canvas: "#ffe9db"},
	{ID: "catppuccin-mocha", Label: "Catppuccin Mocha", Family: "Catppuccin", Variant: ThemeVariantDark, Canvas: "#11111b"},
	{ID: "catppuccin-latte", Label: "Catppuccin Latte", Family: "Catppuccin", Variant: ThemeVariantLight, Canvas: "#eff1f5"},
	{ID: "clearwater-deep", Label: "Clearwater Deep", Family: "Clearwater", Variant: ThemeVariantDark, Canvas: "#081829"},
	{ID: "clearwater-shallows", Label: "Clearwater Shallows", Family: "Clearwater", Variant: ThemeVariantLight, Canvas: "#f0f8fe"},
	{ID: "cyanotype", Label: "Cyanotype", Family: "Cyanotype", Variant: ThemeVariantDark, Canvas: "#00172d"},
	{ID: "cyanotype-print", Label: "Cyanotype Print", Family: "Cyanotype", Variant: ThemeVariantLight, Canvas: "#faf7ec"},
	{ID: "dracula", Label: "Dracula", Family: "Dracula", Variant: ThemeVariantDark, Canvas: "#21222c"},
	{ID: "alucard", Label: "Alucard", Family: "Dracula", Variant: ThemeVariantLight, Canvas: "#fffbeb"},
	{ID: "dune-dusk", Label: "Dune Dusk", Family: "Dune", Variant: ThemeVariantDark, Canvas: "#1e1710"},
	{ID: "dune-noon", Label: "Dune Noon", Family: "Dune", Variant: ThemeVariantLight, Canvas: "#fbf2df"},
	{ID: "ember-dark", Label: "Ember Dark", Family: "Ember", Variant: ThemeVariantDark, Canvas: "#150f0d"},
	{ID: "ember-light", Label: "Ember Light", Family: "Ember", Variant: ThemeVariantLight, Canvas: "#fcf6ee"},
	{ID: "everforest-dark", Label: "Everforest Dark", Family: "Everforest", Variant: ThemeVariantDark, Canvas: "#2d353b"},
	{ID: "everforest-light", Label: "Everforest Light", Family: "Everforest", Variant: ThemeVariantLight, Canvas: "#fdf6e3"},
	{ID: "github-dark", Label: "GitHub Dark", Family: "GitHub", Variant: ThemeVariantDark, Canvas: "#0d1117"},
	{ID: "github-light", Label: "GitHub Light", Family: "GitHub", Variant: ThemeVariantLight, Canvas: "#f6f8fa"},
	{ID: "glacier-crevasse", Label: "Glacier Crevasse", Family: "Glacier", Variant: ThemeVariantDark, Canvas: "#00141f"},
	{ID: "glacier-ice", Label: "Glacier Ice", Family: "Glacier", Variant: ThemeVariantLight, Canvas: "#e9f9ff"},
	{ID: "graphite", Label: "Graphite", Family: "Graphite", Variant: ThemeVariantDark, Canvas: "#121314"},
	{ID: "paper", Label: "Paper", Family: "Graphite", Variant: ThemeVariantLight, Canvas: "#f9fafb"},
	{ID: "gruvbox-dark", Label: "Gruvbox Dark", Family: "Gruvbox", Variant: ThemeVariantDark, Canvas: "#1d2021"},
	{ID: "gruvbox-light", Label: "Gruvbox Light", Family: "Gruvbox", Variant: ThemeVariantLight, Canvas: "#fbf1c7"},
	{ID: "kanagawa-wave", Label: "Kanagawa Wave", Family: "Kanagawa", Variant: ThemeVariantDark, Canvas: "#1f1f28"},
	{ID: "kanagawa-lotus", Label: "Kanagawa Lotus", Family: "Kanagawa", Variant: ThemeVariantLight, Canvas: "#f2ecbc"},
	{ID: "kintsugi-lacquer", Label: "Kintsugi Lacquer", Family: "Kintsugi", Variant: ThemeVariantDark, Canvas: "#0d0b09"},
	{ID: "kintsugi-porcelain", Label: "Kintsugi Porcelain", Family: "Kintsugi", Variant: ThemeVariantLight, Canvas: "#fbf9f3"},
	{ID: "klein-blue", Label: "Klein Blue", Family: "Klein", Variant: ThemeVariantDark, Canvas: "#000b55"},
	{ID: "klein-gallery", Label: "Klein Gallery", Family: "Klein", Variant: ThemeVariantLight, Canvas: "#fafcff"},
	{ID: "lights-out", Label: "Lights Out", Family: "Lights Out", Variant: ThemeVariantDark, Canvas: "#000000"},
	{ID: "lights-on", Label: "Lights On", Family: "Lights Out", Variant: ThemeVariantLight, Canvas: "#ffffff"},
	{ID: "material-darker", Label: "Material Darker", Family: "Material", Variant: ThemeVariantDark, Canvas: "#212121"},
	{ID: "material-lighter", Label: "Material Lighter", Family: "Material", Variant: ThemeVariantLight, Canvas: "#fafafa"},
	{ID: "molten", Label: "Molten", Family: "Molten", Variant: ThemeVariantDark, Canvas: "#0b0807"},
	{ID: "molten-ash", Label: "Molten Ash", Family: "Molten", Variant: ThemeVariantLight, Canvas: "#f9f6f4"},
	{ID: "monokai", Label: "Monokai", Family: "Monokai", Variant: ThemeVariantDark, Canvas: "#1e1f1c"},
	{ID: "monokai-light", Label: "Monokai Light", Family: "Monokai", Variant: ThemeVariantLight, Canvas: "#fbf9f2"},
	{ID: "moss-dark", Label: "Moss Dark", Family: "Moss", Variant: ThemeVariantDark, Canvas: "#101911"},
	{ID: "moss-light", Label: "Moss Light", Family: "Moss", Variant: ThemeVariantLight, Canvas: "#f7f8ed"},
	{ID: "neon-dusk", Label: "Neon Dusk", Family: "Neon Dusk", Variant: ThemeVariantDark, Canvas: "#0f0a29"},
	{ID: "neon-dawn", Label: "Neon Dawn", Family: "Neon Dusk", Variant: ThemeVariantLight, Canvas: "#f9f7ff"},
	{ID: "nier-automata-dark", Label: "NieR: Automata Dark", Family: "NieR: Automata", Variant: ThemeVariantDark, Canvas: "#2f2d26"},
	{ID: "nier-automata", Label: "NieR: Automata", Family: "NieR: Automata", Variant: ThemeVariantLight, Canvas: "#ccc8b1"},
	{ID: "night-owl", Label: "Night Owl", Family: "Night Owl", Variant: ThemeVariantDark, Canvas: "#011627"},
	{ID: "light-owl", Label: "Light Owl", Family: "Night Owl", Variant: ThemeVariantLight, Canvas: "#fbfbfb"},
	{ID: "nord", Label: "Nord", Family: "Nord", Variant: ThemeVariantDark, Canvas: "#2e3440"},
	{ID: "nord-snow", Label: "Nord Snow Storm", Family: "Nord", Variant: ThemeVariantLight, Canvas: "#eceff4"},
	{ID: "one-dark", Label: "One Dark", Family: "One", Variant: ThemeVariantDark, Canvas: "#21252b"},
	{ID: "one-light", Label: "One Light", Family: "One", Variant: ThemeVariantLight, Canvas: "#fafafa"},
	{ID: "orchid-night", Label: "Orchid Night", Family: "Orchid", Variant: ThemeVariantDark, Canvas: "#170f23"},
	{ID: "orchid-day", Label: "Orchid Day", Family: "Orchid", Variant: ThemeVariantLight, Canvas: "#faf6fd"},
	{ID: "oxblood", Label: "Oxblood", Family: "Oxblood", Variant: ThemeVariantDark, Canvas: "#290509"},
	{ID: "oxblood-bone", Label: "Oxblood Bone", Family: "Oxblood", Variant: ThemeVariantLight, Canvas: "#fcf5ec"},
	{ID: "riso-night", Label: "Riso Night", Family: "Riso", Variant: ThemeVariantDark, Canvas: "#0a1320"},
	{ID: "riso-paper", Label: "Riso Paper", Family: "Riso", Variant: ThemeVariantLight, Canvas: "#faf6ea"},
	{ID: "rose-pine", Label: "Rosé Pine", Family: "Rosé Pine", Variant: ThemeVariantDark, Canvas: "#191724"},
	{ID: "rose-pine-dawn", Label: "Rosé Pine Dawn", Family: "Rosé Pine", Variant: ThemeVariantLight, Canvas: "#faf4ed"},
	{ID: "sakura-night", Label: "Sakura Night", Family: "Sakura", Variant: ThemeVariantDark, Canvas: "#1c0f17"},
	{ID: "sakura-bloom", Label: "Sakura Bloom", Family: "Sakura", Variant: ThemeVariantLight, Canvas: "#fff4f6"},
	{ID: "sodium-night", Label: "Sodium Night", Family: "Sodium", Variant: ThemeVariantDark, Canvas: "#0b1015"},
	{ID: "sodium-fog", Label: "Sodium Fog", Family: "Sodium", Variant: ThemeVariantLight, Canvas: "#f0f4f7"},
	{ID: "solarized-dark", Label: "Solarized Dark", Family: "Solarized", Variant: ThemeVariantDark, Canvas: "#002b36"},
	{ID: "solarized-light", Label: "Solarized Light", Family: "Solarized", Variant: ThemeVariantLight, Canvas: "#fdf6e3"},
	{ID: "terracotta-kiln", Label: "Terracotta Kiln", Family: "Terracotta", Variant: ThemeVariantDark, Canvas: "#2b0f05"},
	{ID: "terracotta-adobe", Label: "Terracotta Adobe", Family: "Terracotta", Variant: ThemeVariantLight, Canvas: "#fef0e2"},
	{ID: "tokyo-night", Label: "Tokyo Night", Family: "Tokyo Night", Variant: ThemeVariantDark, Canvas: "#16161e"},
	{ID: "tokyo-night-day", Label: "Tokyo Night Day", Family: "Tokyo Night", Variant: ThemeVariantLight, Canvas: "#e1e2e7"},
	{ID: "verdigris-dark", Label: "Verdigris Dark", Family: "Verdigris", Variant: ThemeVariantDark, Canvas: "#061a1a"},
	{ID: "verdigris-light", Label: "Verdigris Light", Family: "Verdigris", Variant: ThemeVariantLight, Canvas: "#f0f9f8"},
	{ID: "vitesse-dark", Label: "Vitesse Dark", Family: "Vitesse", Variant: ThemeVariantDark, Canvas: "#121212"},
	{ID: "vitesse-light", Label: "Vitesse Light", Family: "Vitesse", Variant: ThemeVariantLight, Canvas: "#ffffff"},
}

const DefaultTheme = "aicodingtool-dark"
const DefaultThemeMode = ThemeModeDark

var themeByID = func() map[string]*Theme {
	byID := make(map[string]*Theme, len(Themes))
	for _, theme := range Themes {
		byID[theme.ID] = theme
	}
	return byID
}()

// ThemeByID is nil for a theme the app no longer ships, or an id that is not a string.
func ThemeByID(id any) *Theme {
	text, ok := id.(string)
	if !ok {
		return nil
	}
	return themeByID[text]
}

func ThemeOrDefault(id any) *Theme {
	if theme := ThemeByID(id); theme != nil {
		return theme
	}
	return themeByID[DefaultTheme]
}

func IsThemeMode(value any) bool {
	switch value := value.(type) {
	case ThemeMode:
		return value == ThemeModeDark || value == ThemeModeLight || value == ThemeModeAuto
	case string:
		return value == "dark" || value == "light" || value == "auto"
	}
	return false
}

func ThemeModeOrDefault(value any) ThemeMode {
	if !IsThemeMode(value) {
		return DefaultThemeMode
	}
	if mode, ok := value.(ThemeMode); ok {
		return mode
	}
	return ThemeMode(value.(string))
}

// ThemeFamilies is the families the picker draws, in the order they were declared.
func ThemeFamilies() []string {
	families := []string{}
	for _, theme := range Themes {
		if !slices.Contains(families, theme.Family) {
			families = append(families, theme.Family)
		}
	}
	return families
}

func VariantFor(mode ThemeMode, systemDark bool) ThemeVariant {
	if mode == ThemeModeAuto {
		if systemDark {
			return ThemeVariantDark
		}
		return ThemeVariantLight
	}
	return ThemeVariant(mode)
}

// ThemeFor is the theme in a family that paints on this ground, falling back to the default's family.
func ThemeFor(family any, variant ThemeVariant) *Theme {
	if name, ok := family.(string); ok {
		for _, theme := range Themes {
			if theme.Family == name && theme.Variant == variant {
				return theme
			}
		}
	}
	fallback := ThemeOrDefault(DefaultTheme).Family
	for _, theme := range Themes {
		if theme.Family == fallback && theme.Variant == variant {
			return theme
		}
	}
	return nil
}
