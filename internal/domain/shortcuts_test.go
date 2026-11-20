package domain

import (
	"testing"
)

func shortcutsInput(code, key string, meta, control, alt, shift bool) KeyInput {
	return KeyInput{Key: key, Code: code, Meta: meta, Control: control, Alt: alt, Shift: shift}
}

func shortcutsText(text string) *string { return &text }

func TestKeystrokeReadFromPhysicalKey(t *testing.T) {
	if got := KeystrokeOf(shortcutsInput("KeyN", "˜", true, false, true, false), true); *got != (Keystroke{Key: "N", Mod: true, Alt: true}) {
		t.Fatalf("⌥ turns N into a dead key on macOS, and the binding is still N: %+v", got)
	}
	for _, check := range []struct {
		input KeyInput
		want  string
	}{
		{shortcutsInput("Comma", ",", true, false, false, false), "Mod+,"},
		{shortcutsInput("Digit1", "1", true, false, false, false), "Mod+1"},
		{shortcutsInput("ArrowLeft", "ArrowLeft", true, false, true, false), "Mod+Alt+ArrowLeft"},
	} {
		if got := FormatShortcut(*KeystrokeOf(check.input, true)); got != check.want {
			t.Fatalf("%s = %s", check.want, got)
		}
	}
	if KeystrokeOf(shortcutsInput("KeyB", "", false, true, false, false), true).Mod {
		t.Fatal("on macOS the command key is ⌘")
	}
	if !KeystrokeOf(shortcutsInput("KeyB", "", false, true, false, false), false).Mod {
		t.Fatal("and Ctrl everywhere else")
	}
	if KeystrokeOf(shortcutsInput("MetaLeft", "Meta", true, false, false, false), true) != nil {
		t.Fatal("a modifier on its own is no keystroke")
	}
	if key, ok := ShortcutKey("", "k"); !ok || key != "K" {
		t.Fatal("a layout that reports no code still knows what the key produced")
	}
}

func TestShortcutBindingRoundTrips(t *testing.T) {
	for _, action := range ShortcutActions {
		stroke := ParseShortcut(action.DefaultBinding)
		if stroke == nil {
			t.Fatalf("%s has a readable default", action.ID)
		}
		if FormatShortcut(*stroke) != action.DefaultBinding {
			t.Fatalf("%s is written the way it parses", action.ID)
		}
		if _, problem := ShortcutProblem(action.DefaultBinding); problem {
			t.Fatalf("%s is bindable", action.ID)
		}
	}
	for _, value := range []any{"Mod+Mod+N", "Meta+N", "", 42.0} {
		if ParseShortcut(value) != nil {
			t.Fatalf("%v is no binding", value)
		}
	}
}

func TestShortcutNeedsModifierAndLeavesDesktopKeys(t *testing.T) {
	problem := func(binding string) bool { _, ok := ShortcutProblem(binding); return ok }
	if !problem("N") || !problem("Shift+N") || problem("Alt+N") || !problem("Mod+Q") || problem("Mod+Shift+Q") || !problem("Mod+Tab") {
		t.Fatal("a shortcut has to carry a modifier, and cannot take what the desktop keeps")
	}
	if text, _ := ShortcutProblem("N"); text != ShortcutModifierRequired {
		t.Fatal(text)
	}
	if text, _ := ShortcutProblem("Mod+F"); text != ShortcutTaken {
		t.Fatal(text)
	}
}

func TestWithShortcutTakesKeystrokeFromHolder(t *testing.T) {
	stolen := WithShortcut(ShortcutOverrides{}, "run.allow", shortcutsText("Mod+Shift+D"))
	featuresExpectJSON(t, stolen, `{"run.allow":"Mod+Shift+D","run.deny":null}`)

	for _, setting := range ShortcutSettings(stolen) {
		if setting.ID == "run.deny" && (setting.Binding != nil || !setting.Changed) {
			t.Fatal("run.deny lost its keystroke")
		}
	}

	restored := WithShortcut(stolen, "run.allow", shortcutsText("Mod+Shift+A"))
	featuresExpectJSON(t, restored, `{"run.deny":null}`)
	featuresExpectJSON(t, WithShortcut(restored, "run.deny", shortcutsText("Mod+Shift+D")), `{}`)
}

func TestFixedKeystrokesNotOnOffer(t *testing.T) {
	for _, action := range ShortcutActions {
		for _, fixed := range FixedShortcuts {
			if fixed.Action == action.ID {
				t.Fatalf("%s is fixed", action.ID)
			}
		}
	}
	for _, binding := range []string{"Mod+F", "Mod+1"} {
		if _, problem := ShortcutProblem(binding); !problem {
			t.Fatalf("%s is the app's own", binding)
		}
	}
}

func TestResolveShortcutsOnlyBoundAndUnclaimed(t *testing.T) {
	bindings := ResolveShortcuts(ShortcutOverrides{"run.allow": nil})
	for _, binding := range bindings {
		if binding.Action == "run.allow" {
			t.Fatal("an unbound action is not matched")
		}
	}
	action := func(binding string, surface ShortcutSurface) string {
		if found := ShortcutFor(bindings, *ParseShortcut(binding), surface); found != nil {
			return found.Action
		}
		return ""
	}
	if action("Mod+W", ShortcutSurfaceAny) != "tab.close" || action("Mod+R", ShortcutSurfaceAny) != "" || action("Mod+R", ShortcutSurfaceBrowser) != "page.reload" ||
		action("Mod+W", ShortcutSurfaceBrowser) != "tab.close" || action("Mod+Alt+B", ShortcutSurfaceAny) != "dock.toggle" {
		t.Fatal("matching")
	}
	shared := []string{}
	for _, binding := range ResolveShortcuts(ShortcutOverrides{"run.deny": shortcutsText("Mod+W")}) {
		if binding.Binding == "Mod+W" {
			shared = append(shared, binding.Action)
		}
	}
	if len(shared) != 1 || shared[0] != "tab.close" {
		t.Fatalf("a fixed keystroke is not one an override can take: %v", shared)
	}
}

func TestDisplayShortcutPerPlatform(t *testing.T) {
	for _, check := range []struct {
		binding string
		mac     bool
		want    string
	}{
		{"Mod+Shift+N", true, "⇧⌘N"},
		{"Mod+Shift+N", false, "Ctrl+Shift+N"},
		{"Mod+Alt+ArrowLeft", true, "⌥⌘←"},
		{"Mod+,", true, "⌘,"},
		{"nonsense", true, ""},
	} {
		if got := DisplayShortcut(check.binding, check.mac); got != check.want {
			t.Fatalf("%s (mac %v) = %q, want %q", check.binding, check.mac, got, check.want)
		}
	}
}

func TestDesktopAccelerator(t *testing.T) {
	for binding, want := range map[string]string{"Alt+Shift+S": "Alt+Shift+S", "Mod+Ctrl+ArrowLeft": "CommandOrControl+Control+Left", "Mod+Escape": "CommandOrControl+Esc"} {
		if got, ok := DesktopAccelerator(binding); !ok || got != want {
			t.Fatalf("%s = %s", binding, got)
		}
	}
	for _, binding := range []string{"S", "nonsense"} {
		if _, ok := DesktopAccelerator(binding); ok {
			t.Fatalf("%s is no accelerator", binding)
		}
	}
	bindings := ResolveShortcuts(ShortcutOverrides{})
	var capture *ShortcutBinding
	for _, binding := range bindings {
		if binding.Action == "window.capture" {
			capture = binding
		}
	}
	if capture == nil || capture.Surface != ShortcutSurfaceDesktop {
		t.Fatal("window.capture is the desktop's")
	}
	stroke := *ParseShortcut(capture.Binding)
	if ShortcutFor(bindings, stroke, ShortcutSurfaceAny) != nil || ShortcutFor(bindings, stroke, ShortcutSurfaceBrowser) != nil || ShortcutFor(bindings, stroke, ShortcutSurfaceDesktop).Action != "window.capture" {
		t.Fatal("the window never takes the desktop's keystroke")
	}
}
