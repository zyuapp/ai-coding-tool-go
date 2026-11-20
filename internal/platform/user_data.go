package platform

import (
	"encoding/json/v2"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	// AppName is the installed app's name. It takes the Electron app's profile, so it replaces that app.
	AppName = "AI Coding Tool"
	// DevAppName is a source build's name, with a profile of its own.
	DevAppName = "AI Coding Tool Go Dev"
	// ElectronDevProfileName is the Electron source build's profile, which a source build's first
	// launch copies.
	ElectronDevProfileName = "AI Coding Tool Dev"
	firstName              = "Threadline"
)

// packaged is set at link time for an installed build:
// -ldflags "-X github.com/zyuapp/ai-coding-tool-go/internal/platform.packaged=true".
var packaged string

// Packaged reports whether this is the installed app rather than a source build.
func Packaged() bool { return packaged == "true" }

// Profile is where the app keeps its data and its checkouts.
type Profile struct {
	Name          string
	UserData      string
	WorktreesRoot string
}

// AppProfile is the installed app's profile, or a source build's own, which never adopts the
// installed app's data.
func AppProfile(appData, home string, isPackaged bool) Profile {
	if isPackaged {
		return Profile{
			Name:          AppName,
			UserData:      AdoptUserDataFolder(appData, AppName),
			WorktreesRoot: filepath.Join(home, ".aicodingtool", "worktrees"),
		}
	}
	return Profile{
		Name:          DevAppName,
		UserData:      filepath.Join(appData, DevAppName),
		WorktreesRoot: filepath.Join(home, ".aicodingtool-go-dev", "worktrees"),
	}
}

// AppDataDir is the per-user application data folder, as Electron's `appData` names it.
func AppDataDir() string {
	home, _ := os.UserHomeDir()
	return appDataDir(home)
}

// disposable is what a folder can hold and still count as empty: Chromium's lock, and the Finder's leavings.
var disposable = map[string]bool{"SingletonLock": true, "SingletonCookie": true, "SingletonSocket": true, ".DS_Store": true}

// AdoptUserDataFolder moves the folder the app first shipped with (Threadline) onto the name it has
// now, once, and answers with the folder to read: the adopted one, or the old one when the move cannot
// happen, so a launch never starts on an empty store. A target that already holds data keeps it, and
// the old folder is left where it is.
func AdoptUserDataFolder(appData, appName string) string {
	legacy := filepath.Join(appData, firstName)
	adopted := filepath.Join(appData, appName)
	if legacy == adopted {
		return adopted
	}
	if _, err := os.Stat(legacy); err != nil {
		return adopted
	}
	err := clearDisposable(adopted)
	if err == nil {
		err = os.Rename(legacy, adopted)
	}
	if err == nil {
		err = repointWorkspaces(filepath.Join(adopted, "workspaces.v1.json"), legacy, adopted)
		if err == nil {
			return adopted
		}
	}
	log.Println("Could not move the app data folder off its first name:", err)
	return legacy
}

// clearDisposable empties a folder that holds nothing worth keeping, and refuses the rest, so no store is buried.
func clearDisposable(folder string) error {
	entries, err := os.ReadDir(folder)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	kept := 0
	for _, entry := range entries {
		if !disposable[entry.Name()] {
			kept++
		}
	}
	if kept > 0 {
		return fmt.Errorf("%s already holds %d file(s)", folder, kept)
	}
	for _, entry := range entries {
		_ = os.Remove(filepath.Join(folder, entry.Name()))
	}
	return os.Remove(folder)
}

// repointWorkspaces moves the workspace roots stored inside a folder with it, since they are stored absolute.
func repointWorkspaces(registry, from, to string) error {
	before, err := os.ReadFile(registry)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	after := strings.ReplaceAll(string(before), jsonEscaped(from+string(filepath.Separator)), jsonEscaped(to+string(filepath.Separator)))
	if after == string(before) {
		return nil
	}
	return os.WriteFile(registry, []byte(after), 0o644)
}

// jsonEscaped is a string as it appears inside a JSON string literal, as `JSON.stringify` writes it.
func jsonEscaped(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	return string(encoded[1 : len(encoded)-1])
}
