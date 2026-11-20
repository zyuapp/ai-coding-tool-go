package platform

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// importMarker remembers where an imported profile came from, so its checkouts stay the app's own.
const importMarker = "imported-from.v1.json"

const (
	taskDatabase = "tasks.v3.sqlite"
	// launchedVersion records the version a profile last launched; an import records this app's, since
	// moving to it is not an update of it.
	launchedVersion = "launched-version"
	// chromiumPreferences hold the page zoom the Electron window was left at.
	chromiumPreferences = "Preferences"
	codexHome           = "codex-private"
)

// skippedProfileFiles belong to the build that wrote them: threads it meant to resume after its own
// update, and the engine processes it was watching, which are not this app's to signal.
var skippedProfileFiles = []string{launchedVersion, "update-relaunch.json", "session-processes.json"}

var importedFolders = []string{"attachments", "message-images", "generated-images", "projectless"}

// codexSkipped are Codex runtime leftovers a new home rebuilds, and the credentials link the app makes again.
var codexSkipped = []string{".credentials.json", ".aicodingtool-mcp-credentials.json", ".tmp", "tmp", "cache", "log", "logs_2.sqlite", "shell_snapshots", "thread-writer-locks"}

// ProfileDatabase is what an import needs from the store: SQLite files copied consistently while the
// Electron app may be writing them, without writing to the source.
type ProfileDatabase interface {
	// Backup copies a live SQLite file, opening the source read only.
	Backup(from, to string) error
	// RepointAttachments rewrites the attachment paths stored in the copied task database from one
	// profile's attachments folder to another's, and counts its threads and projects.
	RepointAttachments(database, fromAttachments, toAttachments string) (ImportReport, error)
}

// ImportSource is an Electron profile this build can import, with the root its checkouts were made under.
type ImportSource struct {
	UserData      string
	WorktreesRoot string
}

// WorktreesRoots are every root the source's checkouts may be under.
func (s ImportSource) WorktreesRoots() []string {
	return []string{s.WorktreesRoot, filepath.Join(s.UserData, "worktrees")}
}

// ImportReport counts what an import brought over.
type ImportReport struct {
	Threads  int
	Projects int
}

// ElectronDevSource is the Electron source build's profile, which a source build's first launch copies.
func ElectronDevSource(appData, home string) ImportSource {
	return ImportSource{UserData: filepath.Join(appData, ElectronDevProfileName), WorktreesRoot: filepath.Join(home, ".aicodingtool-dev", "worktrees")}
}

// IsEmptyProfile reports whether a profile folder holds nothing of its own yet.
func IsEmptyProfile(folder string) bool {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	for _, entry := range entries {
		if !disposable[entry.Name()] {
			return false
		}
	}
	return true
}

// PrepareProfile is the profile this launch opens, created. A source build whose profile is still
// empty first copies the Electron source build's, when that one has a thread store; the source is only
// ever read. The report is nil when nothing was imported.
func PrepareProfile(appData, home string, isPackaged bool, version string, database ProfileDatabase) (Profile, *ImportReport, error) {
	profile := AppProfile(appData, home, isPackaged)
	var report *ImportReport
	if !isPackaged && IsEmptyProfile(profile.UserData) {
		source := ElectronDevSource(appData, home)
		if info, err := os.Stat(filepath.Join(source.UserData, taskDatabase)); err == nil && info.Mode().IsRegular() {
			imported, err := ImportProfile(source, profile.UserData, version, database)
			if err != nil {
				return profile, nil, fmt.Errorf("could not copy %s: %w", source.UserData, err)
			}
			report = &imported
		}
	}
	return profile, report, os.MkdirAll(profile.UserData, 0o755)
}

// ImportedWorktreesRoots are the roots an import recorded; empty when the profile was never imported.
func ImportedWorktreesRoots(userData string) []string {
	data, err := os.ReadFile(filepath.Join(userData, importMarker))
	if err != nil {
		return nil
	}
	var marker struct {
		WorktreesRoots []string `json:"worktreesRoots"`
	}
	if json.Unmarshal(data, &marker) != nil {
		return nil
	}
	return marker.WorktreesRoots
}

// ImportProfile copies `source` into the empty profile at `target`. The copy is built beside the
// target and moved into place whole, so a failed import leaves the target as it was.
func ImportProfile(source ImportSource, target, version string, database ProfileDatabase) (ImportReport, error) {
	if database == nil {
		return ImportReport{}, errors.New("no database to copy the thread store with")
	}
	if !IsEmptyProfile(target) {
		return ImportReport{}, fmt.Errorf("%s already holds data.", target)
	}
	staging := target + ".importing"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return ImportReport{}, err
	}
	report, err := buildImport(source, target, staging, version, database)
	if err != nil {
		_ = os.RemoveAll(staging)
		return ImportReport{}, err
	}
	if entries, err := os.ReadDir(target); err == nil {
		for _, entry := range entries {
			_ = os.Remove(filepath.Join(target, entry.Name()))
		}
		if err := os.Remove(target); err != nil {
			_ = os.RemoveAll(staging)
			return ImportReport{}, err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.RemoveAll(staging)
		return ImportReport{}, err
	}
	return report, nil
}

func buildImport(source ImportSource, target, staging, version string, database ProfileDatabase) (ImportReport, error) {
	from := source.UserData
	if err := database.Backup(filepath.Join(from, taskDatabase), filepath.Join(staging, taskDatabase)); err != nil {
		return ImportReport{}, err
	}
	separator := string(filepath.Separator)
	report, err := database.RepointAttachments(filepath.Join(staging, taskDatabase), filepath.Join(from, "attachments")+separator, filepath.Join(target, "attachments")+separator)
	if err != nil {
		return ImportReport{}, err
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return ImportReport{}, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".json") && !slices.Contains(skippedProfileFiles, name) && entry.Type().IsRegular() {
			if err := copyFile(filepath.Join(from, name), filepath.Join(staging, name)); err != nil {
				return ImportReport{}, err
			}
		}
	}
	if info, err := os.Stat(filepath.Join(from, chromiumPreferences)); err == nil && info.Mode().IsRegular() {
		if err := copyFile(filepath.Join(from, chromiumPreferences), filepath.Join(staging, chromiumPreferences)); err != nil {
			return ImportReport{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(staging, launchedVersion), []byte(version), 0o644); err != nil {
		return ImportReport{}, err
	}
	if err := repointWorkspaces(filepath.Join(staging, "workspaces.v1.json"), from, target); err != nil {
		return ImportReport{}, err
	}
	for _, folder := range importedFolders {
		if err := copyTree(filepath.Join(from, folder), filepath.Join(staging, folder), nil, database); err != nil {
			return ImportReport{}, err
		}
	}
	if err := copyTree(filepath.Join(from, codexHome), filepath.Join(staging, codexHome), codexSkipped, database); err != nil {
		return ImportReport{}, err
	}
	marker, err := json.Marshal(struct {
		Version        int      `json:"version"`
		UserData       string   `json:"userData"`
		WorktreesRoots []string `json:"worktreesRoots"`
		ImportedAt     float64  `json:"importedAt"`
	}{1, from, source.WorktreesRoots(), float64(time.Now().UnixMilli())})
	if err != nil {
		return ImportReport{}, err
	}
	return report, os.WriteFile(filepath.Join(staging, importMarker), marker, 0o644)
}

// copyTree copies a folder, keeping links as links. SQLite files go through the backup so a live one
// is consistent; their journals and lock files are left behind.
func copyTree(from, to string, skipped []string, database ProfileDatabase) error {
	entries, err := os.ReadDir(from)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if slices.Contains(skipped, name) || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") || strings.HasSuffix(name, "-journal") {
			continue
		}
		path, destination := filepath.Join(from, name), filepath.Join(to, name)
		switch kind := entry.Type(); {
		case kind&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err == nil {
				err = os.Symlink(link, destination)
			}
			if err != nil {
				return err
			}
		case kind.IsDir():
			if err := copyTree(path, destination, nil, database); err != nil {
				return err
			}
		case strings.HasSuffix(name, ".sqlite"):
			if err := database.Backup(path, destination); err != nil {
				return err
			}
		case kind.IsRegular():
			if err := copyFile(path, destination); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	destination, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return err
	}
	return destination.Close()
}
