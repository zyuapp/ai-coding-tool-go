package domain

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var modifiers = []string{"Mod", "Ctrl", "Alt", "Shift"}

var shortcutsPunctuation = map[string]string{
	"Comma":        ",",
	"Period":       ".",
	"Slash":        "/",
	"Backslash":    "\\",
	"BracketLeft":  "[",
	"BracketRight": "]",
	"Minus":        "-",
	"Equal":        "=",
	"Semicolon":    ";",
	"Quote":        "'",
	"Backquote":    "`",
}

var shortcutsNamedKeys = jsonx.SetOf(
	"ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown",
	"Enter", "Space", "Tab", "Escape", "Backspace", "Delete",
	"Home", "End", "PageUp", "PageDown",
)

var punctuationKeys = func() jsonx.Set[string] {
	keys := jsonx.Set[string]{}
	for _, key := range shortcutsPunctuation {
		keys[key] = struct{}{}
	}
	return keys
}()

var functionKey = regexp.MustCompile(`^F([1-9]|1[0-2])$`)

var (
	bindableLetter = regexp.MustCompile(`^[A-Z0-9]$`)
	letterCode     = regexp.MustCompile(`^Key[A-Z]$`)
	digitCode      = regexp.MustCompile(`^Digit[0-9]$`)
)

func isFunctionKey(key string) bool {
	return functionKey.MatchString(key)
}

// isBindableKey reports whether a key is one a binding may name at all.
func isBindableKey(key string) bool {
	return bindableLetter.MatchString(key) || punctuationKeys.Has(key) || shortcutsNamedKeys.Has(key) || isFunctionKey(key)
}

// ShortcutKey is the key a binding names, taken from the physical key rather than the character it produced; false for none.
func ShortcutKey(code string, key string) (string, bool) {
	if letterCode.MatchString(code) {
		return code[3:], true
	}
	if digitCode.MatchString(code) {
		return code[5:], true
	}
	if punctuation, ok := shortcutsPunctuation[code]; ok {
		return punctuation, true
	}
	if shortcutsNamedKeys.Has(code) || isFunctionKey(code) {
		return code, true
	}
	// A layout that reports no code of its own still knows what the key produced.
	produced := key
	if featuresUTF16Len(key) == 1 {
		produced = strings.ToUpper(key)
	}
	if isBindableKey(produced) {
		return produced, true
	}
	return "", false
}

// KeystrokeOf is nil for a key no binding can name.
func KeystrokeOf(input KeyInput, mac bool) *Keystroke {
	key, ok := ShortcutKey(input.Code, input.Key)
	if !ok {
		return nil
	}
	if mac {
		return &Keystroke{Key: key, Mod: input.Meta, Ctrl: input.Control, Alt: input.Alt, Shift: input.Shift}
	}
	return &Keystroke{Key: key, Mod: input.Control, Ctrl: input.Meta, Alt: input.Alt, Shift: input.Shift}
}

func FormatShortcut(stroke Keystroke) string {
	parts := make([]string, 0, 5)
	if stroke.Mod {
		parts = append(parts, "Mod")
	}
	if stroke.Ctrl {
		parts = append(parts, "Ctrl")
	}
	if stroke.Alt {
		parts = append(parts, "Alt")
	}
	if stroke.Shift {
		parts = append(parts, "Shift")
	}
	return strings.Join(append(parts, stroke.Key), "+")
}

// ParseShortcut takes a JSON-decoded value; anything unreadable is nil, no binding at all.
func ParseShortcut(text any) *Keystroke {
	binding, ok := text.(string)
	if !ok || binding == "" {
		return nil
	}
	parts := strings.Split(binding, "+")
	key := parts[len(parts)-1]
	if !isBindableKey(key) {
		return nil
	}
	stroke := &Keystroke{Key: key}
	for _, part := range parts[:len(parts)-1] {
		switch {
		case part == "Mod" && !stroke.Mod:
			stroke.Mod = true
		case part == "Ctrl" && !stroke.Ctrl:
			stroke.Ctrl = true
		case part == "Alt" && !stroke.Alt:
			stroke.Alt = true
		case part == "Shift" && !stroke.Shift:
			stroke.Shift = true
		default:
			return nil
		}
	}
	return stroke
}

var macModifierSymbols = map[string]string{"Ctrl": "⌃", "Alt": "⌥", "Shift": "⇧", "Mod": "⌘"}
var otherModifierNames = map[string]string{"Mod": "Ctrl", "Ctrl": "Meta", "Alt": "Alt", "Shift": "Shift"}

var keySymbols = map[string]string{
	"ArrowLeft":  "←",
	"ArrowRight": "→",
	"ArrowUp":    "↑",
	"ArrowDown":  "↓",
	"Enter":      "↩",
	"Escape":     "Esc",
	"Backspace":  "⌫",
	"Delete":     "⌦",
	"Tab":        "⇥",
	"PageUp":     "⇞",
	"PageDown":   "⇟",
	"Home":       "↖",
	"End":        "↘",
}

// ShortcutKeys is the keys of a binding, one to a token, so the screen can draw each as its own cap.
func ShortcutKeys(binding string, mac bool) []string {
	stroke := ParseShortcut(binding)
	if stroke == nil {
		return []string{}
	}
	held := map[string]bool{"Mod": stroke.Mod, "Ctrl": stroke.Ctrl, "Alt": stroke.Alt, "Shift": stroke.Shift}
	// macOS puts its modifiers in one order and writes them as symbols.
	order, names := modifiers, otherModifierNames
	if mac {
		order, names = []string{"Ctrl", "Alt", "Shift", "Mod"}, macModifierSymbols
	}
	keys := []string{}
	for _, modifier := range order {
		if held[modifier] {
			keys = append(keys, names[modifier])
		}
	}
	key := stroke.Key
	if symbol, ok := keySymbols[key]; ok && mac {
		key = symbol
	}
	return append(keys, key)
}

// DisplayShortcut is how a binding reads as one string.
func DisplayShortcut(binding string, mac bool) string {
	if mac {
		return strings.Join(ShortcutKeys(binding, mac), "")
	}
	return strings.Join(ShortcutKeys(binding, mac), "+")
}

// FixedShortcuts is the keystrokes the app owns outright.
var FixedShortcuts = []*ShortcutBinding{
	{Action: "thread.new", Binding: "Mod+N", Surface: ShortcutSurfaceAny},
	{Action: "thread.new-pick-project", Binding: "Mod+Shift+N", Surface: ShortcutSurfaceAny},
	{Action: "composer.focus", Binding: "Mod+L", Surface: ShortcutSurfaceAny},

	{Action: "find.open", Binding: "Mod+F", Surface: ShortcutSurfaceAny},
	{Action: "find.next", Binding: "Mod+G", Surface: ShortcutSurfaceAny},
	{Action: "find.previous", Binding: "Mod+Shift+G", Surface: ShortcutSurfaceAny},

	{Action: "nav.back", Binding: "Mod+[", Surface: ShortcutSurfaceAny},
	{Action: "nav.forward", Binding: "Mod+]", Surface: ShortcutSurfaceAny},
	{Action: "page.reload", Binding: "Mod+R", Surface: ShortcutSurfaceBrowser},

	{Action: "tab.new", Binding: "Mod+T", Surface: ShortcutSurfaceAny},
	{Action: "terminal.focus", Binding: "Mod+J", Surface: ShortcutSurfaceAny},
	{Action: "tab.close", Binding: "Mod+W", Surface: ShortcutSurfaceAny},
	{Action: "dock.toggle", Binding: "Mod+\\", Surface: ShortcutSurfaceAny},
	{Action: "dock.toggle", Binding: "Mod+Alt+B", Surface: ShortcutSurfaceAny},
	{Action: "dock.expand", Binding: "Mod+Shift+\\", Surface: ShortcutSurfaceAny},
	{Action: "sidebar.toggle", Binding: "Mod+B", Surface: ShortcutSurfaceAny},
	{Action: "settings.toggle", Binding: "Mod+,", Surface: ShortcutSurfaceAny},
	{Action: "slot-1", Binding: "Mod+1", Surface: ShortcutSurfaceAny},
	{Action: "slot-2", Binding: "Mod+2", Surface: ShortcutSurfaceAny},
	{Action: "slot-3", Binding: "Mod+3", Surface: ShortcutSurfaceAny},
	{Action: "slot-4", Binding: "Mod+4", Surface: ShortcutSurfaceAny},
	{Action: "slot-5", Binding: "Mod+5", Surface: ShortcutSurfaceAny},
	{Action: "slot-6", Binding: "Mod+6", Surface: ShortcutSurfaceAny},
	{Action: "slot-7", Binding: "Mod+7", Surface: ShortcutSurfaceAny},
	{Action: "slot-8", Binding: "Mod+8", Surface: ShortcutSurfaceAny},
	{Action: "slot-9", Binding: "Mod+9", Surface: ShortcutSurfaceAny},
}

var fixedBindings = func() jsonx.Set[string] {
	bindings := jsonx.Set[string]{}
	for _, fixed := range FixedShortcuts {
		bindings[fixed.Binding] = struct{}{}
	}
	return bindings
}()

const (
	ShortcutModifierRequired = "A shortcut needs ⌘, ⌥, or ⌃ so it cannot swallow what you type."
	ShortcutReserved         = "That keystroke belongs to the desktop."
	ShortcutTaken            = "That keystroke is one the app already answers."
)

// ShortcutProblem is why a keystroke cannot be bound; false when it can.
func ShortcutProblem(binding string) (string, bool) {
	stroke := ParseShortcut(binding)
	if stroke == nil {
		return ShortcutReserved, true
	}
	if !stroke.Mod && !stroke.Ctrl && !stroke.Alt {
		return ShortcutModifierRequired, true
	}
	if stroke.Key == "Tab" || stroke.Key == "Q" && stroke.Mod && !stroke.Ctrl && !stroke.Alt && !stroke.Shift {
		return ShortcutReserved, true
	}
	if fixedBindings.Has(FormatShortcut(*stroke)) {
		return ShortcutTaken, true
	}
	return "", false
}

// ShortcutActions is the actions worth a row in settings.
var ShortcutActions = []*ShortcutAction{
	{ID: "effort.increase", Group: "Threads", Label: "Increase effort", Description: "Use the next higher effort supported by this model", Surface: ShortcutSurfaceAny, DefaultBinding: "Mod+Shift+ArrowUp"},
	{ID: "effort.decrease", Group: "Threads", Label: "Decrease effort", Description: "Use the next lower effort supported by this model", Surface: ShortcutSurfaceAny, DefaultBinding: "Mod+Shift+ArrowDown"},
	{ID: "project.start", Group: "Threads", Label: "Start in a folder", Description: "Type a folder's path and start a thread there", Surface: ShortcutSurfaceAny, DefaultBinding: "Mod+O"},
	{ID: "thread.jump", Group: "Threads", Label: "Jump to a thread or a setting", Description: "Search your threads and every setting, and open one", Surface: ShortcutSurfaceAny, DefaultBinding: "Mod+K"},
	{ID: "run.allow", Group: "Threads", Label: "Allow", Description: "Answer the approval this thread is waiting on", Surface: ShortcutSurfaceAny, DefaultBinding: "Mod+Shift+A"},
	{ID: "run.deny", Group: "Threads", Label: "Deny", Description: "Refuse the approval this thread is waiting on", Surface: ShortcutSurfaceAny, DefaultBinding: "Mod+Shift+D"},
	{ID: "dictation.hold", Group: "Threads", Label: "Dictate", Description: "Hold to talk, or tap for hands-free", Surface: ShortcutSurfaceAny, DefaultBinding: "Alt+D"},
	{ID: "zen.toggle", Group: "Threads", Label: "Zen mode", Description: "Hide everything but this thread and its composer", Surface: ShortcutSurfaceAny, DefaultBinding: "Mod+Shift+F"},

	{ID: "window.capture", Group: "Capture", Label: "Grab the window you are in", Description: "Attach the frontmost app's window to this thread, from anywhere on the desktop", Surface: ShortcutSurfaceDesktop, DefaultBinding: "Alt+Shift+S"},
}

var actionsById = func() map[string]*ShortcutAction {
	byID := make(map[string]*ShortcutAction, len(ShortcutActions))
	for _, action := range ShortcutActions {
		byID[action.ID] = action
	}
	return byID
}()

// ShortcutsShortcutAction is nil for an action without a settings row.
func ShortcutsShortcutAction(id string) *ShortcutAction {
	return actionsById[id]
}

var slotAction = regexp.MustCompile(`^slot-([1-9])$`)

// SlotShortcutIndex is which position `slot-*` names, counting from zero; false for any other action.
func SlotShortcutIndex(actionID string) (int, bool) {
	match := slotAction.FindStringSubmatch(actionID)
	if match == nil {
		return 0, false
	}
	return int(match[1][0]-'0') - 1, true
}

// SlotCount is how many sidebar thread positions the number shortcuts can reach.
const SlotCount = 9

// ShortcutSettings is what each action is bound to now, for the settings list.
func ShortcutSettings(overrides ShortcutOverrides) []*ShortcutSetting {
	settings := make([]*ShortcutSetting, 0, len(ShortcutActions))
	for _, action := range ShortcutActions {
		binding := &action.DefaultBinding
		if override, ok := overrides[action.ID]; ok {
			binding = override
		}
		var copied *string
		if binding != nil {
			value := *binding
			copied = &value
		}
		settings = append(settings, &ShortcutSetting{ShortcutAction: *action, Binding: copied, Changed: copied == nil || *copied != action.DefaultBinding})
	}
	return settings
}

// ResolveShortcuts is the bindings the main process matches against, fixed ones first.
func ResolveShortcuts(overrides ShortcutOverrides) []*ShortcutBinding {
	taken := maps.Clone(fixedBindings)
	bindings := slices.Clone(FixedShortcuts)
	for _, setting := range ShortcutSettings(overrides) {
		if setting.Binding == nil || *setting.Binding == "" || taken.Has(*setting.Binding) {
			continue
		}
		if _, problem := ShortcutProblem(*setting.Binding); problem {
			continue
		}
		taken[*setting.Binding] = struct{}{}
		bindings = append(bindings, &ShortcutBinding{Action: setting.ID, Binding: *setting.Binding, Surface: setting.Surface})
	}
	return bindings
}

var acceleratorKeys = map[string]string{
	"ArrowLeft":  "Left",
	"ArrowRight": "Right",
	"ArrowUp":    "Up",
	"ArrowDown":  "Down",
	"Enter":      "Return",
	"Escape":     "Esc",
}

// DesktopAccelerator is a binding written the way the desktop registers one; false for a keystroke the desktop cannot hold.
func DesktopAccelerator(binding string) (string, bool) {
	stroke := ParseShortcut(binding)
	if stroke == nil {
		return "", false
	}
	if _, problem := ShortcutProblem(binding); problem {
		return "", false
	}
	parts := make([]string, 0, 5)
	if stroke.Mod {
		parts = append(parts, "CommandOrControl")
	}
	if stroke.Ctrl {
		parts = append(parts, "Control")
	}
	if stroke.Alt {
		parts = append(parts, "Alt")
	}
	if stroke.Shift {
		parts = append(parts, "Shift")
	}
	key := stroke.Key
	if accelerator, ok := acceleratorKeys[key]; ok {
		key = accelerator
	}
	return strings.Join(append(parts, key), "+"), true
}

// ShortcutFor is which action a keystroke asks for on the surface it was pressed; nil for none.
func ShortcutFor(bindings []*ShortcutBinding, stroke Keystroke, surface ShortcutSurface) *ShortcutBinding {
	binding := FormatShortcut(stroke)
	for _, candidate := range bindings {
		if candidate.Binding == binding && (candidate.Surface == ShortcutSurfaceAny || candidate.Surface == surface) {
			return candidate
		}
	}
	return nil
}

// ShortcutsShortcutOverrides keeps only the bindings that differ from the default.
func ShortcutsShortcutOverrides(overrides ShortcutOverrides) ShortcutOverrides {
	kept := ShortcutOverrides{}
	for id, binding := range overrides {
		action, ok := actionsById[id]
		if !ok || binding != nil && *binding == action.DefaultBinding {
			continue
		}
		kept[id] = binding
	}
	return kept
}

// WithShortcut is the overrides after action takes binding (nil takes the keystroke away); whoever held it loses it.
func WithShortcut(overrides ShortcutOverrides, action string, binding *string) ShortcutOverrides {
	next := maps.Clone(overrides)
	if next == nil {
		next = ShortcutOverrides{}
	}
	if binding != nil && *binding != "" {
		for _, setting := range ShortcutSettings(overrides) {
			if setting.ID != action && setting.Binding != nil && *setting.Binding == *binding {
				next[setting.ID] = nil
			}
		}
	}
	next[action] = binding
	return ShortcutsShortcutOverrides(next)
}
