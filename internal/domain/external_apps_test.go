package domain

import (
	"slices"
	"testing"
)

const externalAppsHome = "/Users/dev"

func externalAppIDs(platform Platform) []string {
	ids := []string{}
	for _, app := range ExternalApps(platform) {
		ids = append(ids, app.ID)
	}
	return ids
}

func TestExternalAppsPerPlatform(t *testing.T) {
	if !slices.Contains(externalAppIDs(PlatformDarwin), "iterm") || slices.Contains(externalAppIDs(PlatformLinux), "iterm") {
		t.Fatal("iTerm is macOS's")
	}
	if !slices.Contains(externalAppIDs("win32"), "windows-terminal") {
		t.Fatal("windows-terminal")
	}
	if len(externalAppIDs("freebsd")) != 0 {
		t.Fatal("an unknown platform has nothing to open")
	}
}

func TestExternalAppsOneFileManager(t *testing.T) {
	for platform, label := range map[Platform]string{"darwin": "Finder", "win32": "File Explorer", "linux": "File manager"} {
		labels := []string{}
		for _, app := range ExternalApps(platform) {
			if app.Kind == "files" {
				labels = append(labels, app.Label)
			}
		}
		if !slices.Equal(labels, []string{label}) {
			t.Fatalf("%s: %v", platform, labels)
		}
	}
}

func TestAppCandidatesMacBundleFolders(t *testing.T) {
	probes := []string{}
	for _, candidate := range AppCandidates("cursor", PlatformDarwin, externalAppsHome, "/repo") {
		probes = append(probes, featuresDeref(candidate.Probe))
	}
	if !slices.Equal(probes, []string{"/Applications/Cursor.app", "/System/Applications/Cursor.app", "/System/Applications/Utilities/Cursor.app", "/Users/dev/Applications/Cursor.app"}) {
		t.Fatal(probes)
	}
}

func TestAppCandidatesOpenThroughBundle(t *testing.T) {
	featuresExpectJSON(t, AppCandidates("zed", PlatformDarwin, externalAppsHome, "/repo")[0],
		`{"id":"zed","probe":"/Applications/Zed.app","icon":"/Applications/Zed.app","command":"open","args":["-a","/Applications/Zed.app","/repo"]}`)
}

func TestAppCandidatesLauncherArgs(t *testing.T) {
	args := func(id string, platform Platform) [][]string {
		all := [][]string{}
		for _, candidate := range AppCandidates(id, platform, externalAppsHome, "/repo") {
			all = append(all, candidate.Args)
		}
		return all
	}
	featuresExpectJSON(t, args("vscode", PlatformLinux), `[["/repo"]]`)
	featuresExpectJSON(t, args("kitty", PlatformLinux), `[["--directory","/repo"]]`)
	featuresExpectJSON(t, args("wezterm", PlatformLinux), `[["start","--cwd","/repo"]]`)
	featuresExpectJSON(t, args("windows-terminal", "win32"), `[["-d","/repo"]]`)
	featuresExpectJSON(t, args("tabby", PlatformLinux), `[["open","/repo"]]`)
}

func TestAppCandidatesBundleLaunchers(t *testing.T) {
	home := "/Users/Dev User"
	folder := "/repos/project's files $(example)"
	for _, check := range [][4]string{
		{"xcode", "Xcode.app", "Contents/Developer/usr/bin/xed", "--project"},
		{"tabby", "Tabby.app", "Contents/MacOS/Tabby", "open"},
	} {
		candidates := AppCandidates(check[0], PlatformDarwin, home, folder)
		candidate := candidates[len(candidates)-1]
		bundle := home + "/Applications/" + check[1]
		if candidate.ID != check[0] || featuresDeref(candidate.Probe) != bundle || featuresDeref(candidate.Icon) != bundle ||
			candidate.Command != bundle+"/"+check[2] || !slices.Equal(candidate.Args, []string{check[3], folder}) {
			t.Fatalf("%s = %s", check[0], featuresJSON(t, candidate))
		}
	}
}

func TestAppCandidatesPathLauncherHasNoProbe(t *testing.T) {
	linux := AppCandidates("vscode", PlatformLinux, externalAppsHome, "/repo")[0]
	if linux.Probe != nil || linux.Icon != nil {
		t.Fatal("PATH decides whether it is there")
	}
}

func TestAppCandidatesFileManager(t *testing.T) {
	finder := AppCandidates("finder", PlatformDarwin, externalAppsHome, "/repo")[0]
	if finder.Command != "open" || !slices.Equal(finder.Args, []string{"/repo"}) || featuresDeref(finder.Icon) != "/System/Library/CoreServices/Finder.app" {
		t.Fatal(featuresJSON(t, finder))
	}
	if len(AppCandidates("emacs", PlatformDarwin, externalAppsHome, "/repo")) != 0 {
		t.Fatal("an application the catalog does not know has nowhere to open")
	}
}
