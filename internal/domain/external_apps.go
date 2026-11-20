package domain

import "strings"

// externalAppLocation is where an application might be: a macOS bundle (bundle set) searched in every
// standard folder, or a command resolved on PATH by a bare name or given as an absolute path.
type externalAppLocation struct {
	bundle string
	// launch is a bundle-specific launcher for applications that need more than a folder-open event.
	launch  func(bundle, folder string) AppLaunch
	command string
	args    func(folder string) []string
	// icon is the bundle to read the icon from, for an application the platform opens by another name.
	icon string
}

// cataloguedApp lists ways to open a folder on each platform, in the order they are worth trying.
type cataloguedApp struct {
	ExternalApp
	locations map[Platform][]externalAppLocation
}

// bundleFolders is where macOS keeps applications, beyond the user's own folder, which is added per home directory.
var bundleFolders = []string{"/Applications", "/System/Applications", "/System/Applications/Utilities"}

// apps is the catalog, in the order the list draws it.
var apps = []cataloguedApp{
	{
		ExternalApp: ExternalApp{ID: "cursor", Label: "Cursor", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Cursor.app"}},
			PlatformWin32:  {{command: "cursor.cmd"}},
			PlatformLinux:  {{command: "cursor"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "vscode", Label: "Visual Studio Code", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Visual Studio Code.app"}},
			PlatformWin32:  {{command: "code.cmd"}},
			PlatformLinux:  {{command: "code"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "vscode-insiders", Label: "Visual Studio Code Insiders", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Visual Studio Code - Insiders.app"}},
			PlatformWin32:  {{command: "code-insiders.cmd"}},
			PlatformLinux:  {{command: "code-insiders"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "vscodium", Label: "VSCodium", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "VSCodium.app"}},
			PlatformWin32:  {{command: "codium.cmd"}},
			PlatformLinux:  {{command: "codium"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "windsurf", Label: "Windsurf", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Windsurf.app"}},
			PlatformWin32:  {{command: "windsurf.cmd"}},
			PlatformLinux:  {{command: "windsurf"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "zed", Label: "Zed", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Zed.app"}},
			PlatformLinux:  {{command: "zed"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "sublime", Label: "Sublime Text", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Sublime Text.app"}},
			PlatformWin32:  {{command: "subl.exe"}},
			PlatformLinux:  {{command: "subl"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "intellij", Label: "IntelliJ IDEA", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "IntelliJ IDEA.app"}},
			PlatformWin32:  {{command: "idea.bat"}},
			PlatformLinux:  {{command: "idea"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "webstorm", Label: "WebStorm", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "WebStorm.app"}},
			PlatformWin32:  {{command: "webstorm.bat"}},
			PlatformLinux:  {{command: "webstorm"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "pycharm", Label: "PyCharm", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "PyCharm.app"}, {bundle: "PyCharm CE.app"}},
			PlatformWin32:  {{command: "pycharm64.exe"}},
			PlatformLinux:  {{command: "pycharm"}, {command: "pycharm.sh"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "goland", Label: "GoLand", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "GoLand.app"}},
			PlatformWin32:  {{command: "goland64.exe"}},
			PlatformLinux:  {{command: "goland"}, {command: "goland.sh"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "clion", Label: "CLion", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "CLion.app"}},
			PlatformWin32:  {{command: "clion64.exe"}},
			PlatformLinux:  {{command: "clion"}, {command: "clion.sh"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "rider", Label: "Rider", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Rider.app"}},
			PlatformWin32:  {{command: "rider64.exe"}},
			PlatformLinux:  {{command: "rider"}, {command: "rider.sh"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "phpstorm", Label: "PhpStorm", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "PhpStorm.app"}},
			PlatformWin32:  {{command: "phpstorm64.exe"}},
			PlatformLinux:  {{command: "phpstorm"}, {command: "phpstorm.sh"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "rubymine", Label: "RubyMine", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "RubyMine.app"}},
			PlatformWin32:  {{command: "rubymine64.exe"}},
			PlatformLinux:  {{command: "rubymine"}, {command: "rubymine.sh"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "rustrover", Label: "RustRover", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "RustRover.app"}},
			PlatformWin32:  {{command: "rustrover64.exe"}},
			PlatformLinux:  {{command: "rustrover"}, {command: "rustrover.sh"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "xcode", Label: "Xcode", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Xcode.app", launch: func(bundle, folder string) AppLaunch {
				return AppLaunch{Command: bundle + "/Contents/Developer/usr/bin/xed", Args: []string{"--project", folder}}
			}}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "android-studio", Label: "Android Studio", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Android Studio.app"}},
			PlatformWin32:  {{command: "studio64.exe"}},
			PlatformLinux:  {{command: "studio"}, {command: "studio.sh"}, {command: "android-studio"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "github-desktop", Label: "GitHub Desktop", Kind: ExternalAppKindEditor},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "GitHub Desktop.app"}},
			PlatformWin32:  {{command: "github.exe"}},
			PlatformLinux:  {{command: "github-desktop"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "terminal", Label: "Terminal", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Terminal.app"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "iterm", Label: "iTerm", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "iTerm.app"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "warp", Label: "Warp", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Warp.app"}},
			PlatformLinux:  {{command: "warp-terminal"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "ghostty", Label: "Ghostty", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Ghostty.app"}},
			PlatformLinux:  {{command: "ghostty", args: func(folder string) []string { return []string{"--working-directory=" + folder} }}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "cmux", Label: "cmux", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "cmux.app"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "kitty", Label: "kitty", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "kitty.app"}},
			PlatformLinux:  {{command: "kitty", args: func(folder string) []string { return []string{"--directory", folder} }}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "wezterm", Label: "WezTerm", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "WezTerm.app"}},
			PlatformLinux:  {{command: "wezterm", args: func(folder string) []string { return []string{"start", "--cwd", folder} }}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "alacritty", Label: "Alacritty", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Alacritty.app"}},
			PlatformLinux:  {{command: "alacritty", args: func(folder string) []string { return []string{"--working-directory", folder} }}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "tabby", Label: "Tabby", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{bundle: "Tabby.app", launch: func(bundle, folder string) AppLaunch {
				return AppLaunch{Command: bundle + "/Contents/MacOS/Tabby", Args: []string{"open", folder}}
			}}},
			PlatformWin32: {{command: "Tabby.exe", args: func(folder string) []string { return []string{"open", folder} }}},
			PlatformLinux: {{command: "tabby", args: func(folder string) []string { return []string{"open", folder} }}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "windows-terminal", Label: "Windows Terminal", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformWin32: {{command: "wt.exe", args: func(folder string) []string { return []string{"-d", folder} }}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "gnome-terminal", Label: "GNOME Terminal", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformLinux: {{command: "gnome-terminal", args: func(folder string) []string { return []string{"--working-directory=" + folder} }}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "konsole", Label: "Konsole", Kind: ExternalAppKindTerminal},
		locations: map[Platform][]externalAppLocation{
			PlatformLinux: {{command: "konsole", args: func(folder string) []string { return []string{"--workdir", folder} }}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "finder", Label: "Finder", Kind: ExternalAppKindFiles},
		locations: map[Platform][]externalAppLocation{
			PlatformDarwin: {{command: "open", icon: "/System/Library/CoreServices/Finder.app"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "explorer", Label: "File Explorer", Kind: ExternalAppKindFiles},
		locations: map[Platform][]externalAppLocation{
			PlatformWin32: {{command: "explorer.exe"}},
		},
	},
	{
		ExternalApp: ExternalApp{ID: "file-manager", Label: "File manager", Kind: ExternalAppKindFiles},
		locations: map[Platform][]externalAppLocation{
			PlatformLinux: {{command: "xdg-open"}},
		},
	},
}

// bundlePaths is the folders a bare bundle name is searched in, the user's own last.
func bundlePaths(bundle string, home string) []string {
	paths := make([]string, 0, len(bundleFolders)+1)
	for _, folder := range append(bundleFolders[:len(bundleFolders):len(bundleFolders)], home+"/Applications") {
		paths = append(paths, folder+"/"+bundle)
	}
	return paths
}

func candidatesFor(app *cataloguedApp, platform Platform, home string, folder string) []*AppCandidate {
	candidates := []*AppCandidate{}
	for _, location := range app.locations[platform] {
		if location.bundle != "" {
			for _, path := range bundlePaths(location.bundle, home) {
				launch := AppLaunch{Command: "open", Args: []string{"-a", path, folder}}
				if location.launch != nil {
					launch = location.launch(path, folder)
				}
				candidates = append(candidates, &AppCandidate{AppLaunch: launch, ID: app.ID, Probe: &path, Icon: &path})
			}
			continue
		}
		candidate := &AppCandidate{ID: app.ID, AppLaunch: AppLaunch{Command: location.command, Args: []string{folder}}}
		if strings.HasPrefix(location.command, "/") {
			candidate.Probe = &location.command
		}
		if location.icon != "" {
			candidate.Icon = &location.icon
		}
		if location.args != nil {
			candidate.Args = location.args(folder)
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}

// ExternalApps is every application the catalog knows on this platform, in the order the list draws them.
func ExternalApps(platform Platform) []*ExternalApp {
	known := []*ExternalApp{}
	for _, app := range apps {
		if len(app.locations[platform]) > 0 {
			known = append(known, &ExternalApp{ID: app.ID, Label: app.Label, Kind: app.Kind})
		}
	}
	return known
}

// AppCandidates is every place one application might be on this platform, best first.
func AppCandidates(appID string, platform Platform, home string, folder string) []*AppCandidate {
	for index := range apps {
		if apps[index].ID == appID {
			return candidatesFor(&apps[index], platform, home, folder)
		}
	}
	return []*AppCandidate{}
}
