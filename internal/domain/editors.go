package domain

import (
	"regexp"
	"slices"
)

// editorEntry is one editor the app knows: its launcher locations by platform, best first, and how it takes a file and line.
type editorEntry struct {
	id        string
	locations map[Platform][]string
	args      func(file string, line *float64) []string
}

// gotoArgs passes `--goto` with the line as part of the path; without a line the plain path opens the file.
func gotoArgs(file string, line *float64) []string {
	if editorHasLine(line) {
		return []string{"-g", file + ":" + featuresJSNumber(*line)}
	}
	return []string{file}
}

// editorHasLine is a line JavaScript reads as truthy: present, not 0 and not NaN.
func editorHasLine(line *float64) bool {
	return line != nil && *line != 0 && *line == *line
}

func suffixArgs(file string, line *float64) []string {
	if editorHasLine(line) {
		return []string{file + ":" + featuresJSNumber(*line)}
	}
	return []string{file}
}

func jetbrainsArgs(file string, line *float64) []string {
	if editorHasLine(line) {
		return []string{"--line", featuresJSNumber(*line), file}
	}
	return []string{file}
}

// editors are tried in order, so this doubles as which editor wins when several are installed.
var editors = []editorEntry{
	{
		id: "cursor",
		locations: map[Platform][]string{
			PlatformDarwin: {"/Applications/Cursor.app/Contents/Resources/app/bin/cursor", "cursor"},
			PlatformWin32:  {"cursor.cmd"},
			PlatformLinux:  {"cursor"},
		},
		args: gotoArgs,
	},
	{
		id: "vscode",
		locations: map[Platform][]string{
			PlatformDarwin: {"/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code", "code"},
			PlatformWin32:  {"code.cmd"},
			PlatformLinux:  {"code"},
		},
		args: gotoArgs,
	},
	{
		id: "windsurf",
		locations: map[Platform][]string{
			PlatformDarwin: {"/Applications/Windsurf.app/Contents/Resources/app/bin/windsurf", "windsurf"},
			PlatformWin32:  {"windsurf.cmd"},
			PlatformLinux:  {"windsurf"},
		},
		args: gotoArgs,
	},
	{
		id: "zed",
		locations: map[Platform][]string{
			PlatformDarwin: {"/Applications/Zed.app/Contents/MacOS/cli", "zed"},
			PlatformLinux:  {"zed"},
		},
		args: suffixArgs,
	},
	{
		id: "sublime",
		locations: map[Platform][]string{
			PlatformDarwin: {"/Applications/Sublime Text.app/Contents/SharedSupport/bin/subl", "subl"},
			PlatformWin32:  {"subl.exe"},
			PlatformLinux:  {"subl"},
		},
		args: suffixArgs,
	},
	{
		id: "jetbrains",
		locations: map[Platform][]string{
			PlatformDarwin: {"idea", "webstorm"},
			PlatformWin32:  {"idea.bat", "webstorm.bat"},
			PlatformLinux:  {"idea", "webstorm"},
		},
		args: jetbrainsArgs,
	},
}

// EditorCandidates is every launcher worth trying on this platform, best first. A nil line opens the file without one.
func EditorCandidates(platform Platform, file string, line *float64) []*Candidate {
	candidates := []*Candidate{}
	for _, editor := range editors {
		for _, command := range editor.locations[platform] {
			candidates = append(candidates, &Candidate{Launch: Launch{Command: command, Args: editor.args(file, line)}, ID: editor.id})
		}
	}
	return candidates
}

func EditorLaunch(platform Platform, command, file string, line *float64) *Launch {
	for _, editor := range editors {
		if slices.Contains(editor.locations[platform], command) {
			return &Launch{Command: command, Args: editor.args(file, line)}
		}
	}
	return nil
}

func TextHandlerLaunch(platform Platform, file string) *Launch {
	switch platform {
	case PlatformDarwin:
		return &Launch{Command: "open", Args: []string{"-t", file}}
	case PlatformWin32:
		return &Launch{Command: "notepad.exe", Args: []string{file}}
	}
	return nil
}

var webPagePattern = regexp.MustCompile(`(?i)\.x?html?$`)

func OpensInBrowser(file string, line *float64) bool {
	return line == nil && webPagePattern.MatchString(file)
}
