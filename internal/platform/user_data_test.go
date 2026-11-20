package platform

import (
	"bytes"
	"encoding/json/v2"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func seedLegacy(t *testing.T, root string, registry any) string {
	t.Helper()
	legacy := filepath.Join(root, "Threadline")
	os.MkdirAll(filepath.Join(legacy, "attachments"), 0o755)
	os.WriteFile(filepath.Join(legacy, "tasks.v3.sqlite"), []byte("store"), 0o644)
	if registry != nil {
		data, _ := json.Marshal(registry)
		os.WriteFile(filepath.Join(legacy, "workspaces.v1.json"), data, 0o644)
	}
	return legacy
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestASourceBuildKeepsItsOwnProfileWhileTheInstalledAppAdoptsLegacyData(t *testing.T) {
	root := t.TempDir()
	legacy := seedLegacy(t, root, nil)
	home := filepath.Join(root, "home")
	development := AppProfile(root, home, false)
	if development != (Profile{Name: "AI Coding Tool Go Dev", UserData: filepath.Join(root, "AI Coding Tool Go Dev"), WorktreesRoot: filepath.Join(home, ".aicodingtool-go-dev", "worktrees")}) {
		t.Fatalf("got %#v", development)
	}
	if read(t, filepath.Join(legacy, "tasks.v3.sqlite")) != "store" {
		t.Fatal("a source build touched the legacy folder")
	}
	production := AppProfile(root, home, true)
	if production != (Profile{Name: "AI Coding Tool", UserData: filepath.Join(root, "AI Coding Tool"), WorktreesRoot: filepath.Join(home, ".aicodingtool", "worktrees")}) {
		t.Fatalf("got %#v", production)
	}
	if read(t, filepath.Join(production.UserData, "tasks.v3.sqlite")) != "store" {
		t.Fatal("legacy data not adopted")
	}
}

func TestWorkspaceRootsInsideTheFolderMoveWithIt(t *testing.T) {
	root := t.TempDir()
	lookalike := filepath.Join(root, "Threadline-notes")
	legacy := seedLegacy(t, root, map[string]any{"version": 1, "workspaces": []map[string]string{
		{"id": "a", "root": filepath.Join(root, "Threadline", "projectless")},
		{"id": "b", "root": filepath.Join(root, "Threadline", "worktrees", "wt1")},
		{"id": "c", "root": "/Users/someone/workspace/thing"},
		{"id": "d", "root": lookalike},
	}})
	adopted := AdoptUserDataFolder(root, AppName)
	var registry struct {
		Workspaces []struct {
			Root string `json:"root"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(adopted, "workspaces.v1.json"))), &registry); err != nil {
		t.Fatal(err)
	}
	var roots []string
	for _, workspace := range registry.Workspaces {
		roots = append(roots, workspace.Root)
	}
	want := []string{filepath.Join(adopted, "projectless"), filepath.Join(adopted, "worktrees", "wt1"), "/Users/someone/workspace/thing", lookalike}
	if !slices.Equal(roots, want) {
		t.Fatalf("got %q", roots)
	}
	if exists(legacy) {
		t.Fatal("legacy folder left")
	}
}

func TestAFolderThatHoldsDataKeepsItAndTheFirstStays(t *testing.T) {
	root := t.TempDir()
	legacy := seedLegacy(t, root, nil)
	occupied := filepath.Join(root, AppName)
	os.MkdirAll(occupied, 0o755)
	os.WriteFile(filepath.Join(occupied, "tasks.v3.sqlite"), []byte("newer store"), 0o644)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	if got := AdoptUserDataFolder(root, AppName); got != legacy {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(logged.String(), "Could not move the app data folder off its first name:") || !strings.Contains(logged.String(), "already holds 1 file(s)") {
		t.Fatalf("logged %q", logged.String())
	}
	if read(t, filepath.Join(occupied, "tasks.v3.sqlite")) != "newer store" || !exists(filepath.Join(legacy, "tasks.v3.sqlite")) {
		t.Fatal("data moved")
	}
}

func TestAFolderHoldingOnlyTheLockIsMovedOnto(t *testing.T) {
	root := t.TempDir()
	seedLegacy(t, root, nil)
	occupied := filepath.Join(root, AppName)
	os.MkdirAll(occupied, 0o755)
	for _, name := range []string{"SingletonCookie", "SingletonSocket", ".DS_Store"} {
		os.WriteFile(filepath.Join(occupied, name), nil, 0o644)
	}
	os.Symlink("host-1234", filepath.Join(occupied, "SingletonLock"))
	if got := AdoptUserDataFolder(root, AppName); got != occupied {
		t.Fatalf("got %q", got)
	}
	if read(t, filepath.Join(occupied, "tasks.v3.sqlite")) != "store" || exists(filepath.Join(occupied, "SingletonLock")) {
		t.Fatal("not moved onto")
	}
}

func TestALaunchWithNoFirstFolderMovesNothing(t *testing.T) {
	root := t.TempDir()
	if got := AdoptUserDataFolder(root, AppName); got != filepath.Join(root, AppName) || exists(got) {
		t.Fatalf("got %q", got)
	}
}

type fakeDatabase struct {
	backups []string
	repoint [3]string
}

func (f *fakeDatabase) Backup(from, to string) error {
	f.backups = append(f.backups, filepath.Base(from))
	return copyFile(from, to)
}

func (f *fakeDatabase) RepointAttachments(database, from, to string) (ImportReport, error) {
	f.repoint = [3]string{database, from, to}
	return ImportReport{Threads: 2, Projects: 1}, nil
}

func TestASourceBuildsFirstLaunchCopiesTheElectronDevProfile(t *testing.T) {
	appData, home := t.TempDir(), t.TempDir()
	source := filepath.Join(appData, "AI Coding Tool Dev")
	for path, body := range map[string]string{
		"tasks.v3.sqlite":                 "db",
		"tasks.v3.sqlite-wal":             "wal",
		"settings.v1.json":                "{}",
		"session-processes.json":          "[]",
		"update-relaunch.json":            "{}",
		"launched-version":                "0.1.0",
		"Preferences":                     "zoom",
		"Cache/data":                      "chromium",
		"attachments/a.png":               "png",
		"codex-private/config.toml":       "x",
		"codex-private/state.sqlite":      "state",
		"codex-private/state.sqlite-wal":  "wal",
		"codex-private/cache/blob":        "cached",
		"codex-private/.credentials.json": "secret",
		"workspaces.v1.json":              `{"root":"` + filepath.Join(source, "projectless", "p") + `"}`,
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(source, path)), 0o755)
		os.WriteFile(filepath.Join(source, path), []byte(body), 0o644)
	}
	database := &fakeDatabase{}
	profile, report, err := PrepareProfile(appData, home, false, "1.2.3", database)
	if err != nil || report == nil || report.Threads != 2 {
		t.Fatalf("got %#v %v", report, err)
	}
	target := profile.UserData
	for _, kept := range []string{"tasks.v3.sqlite", "settings.v1.json", "Preferences", "attachments/a.png", "codex-private/config.toml", "codex-private/state.sqlite"} {
		if !exists(filepath.Join(target, kept)) {
			t.Errorf("%s not copied", kept)
		}
	}
	for _, skipped := range []string{"tasks.v3.sqlite-wal", "session-processes.json", "update-relaunch.json", "Cache", "codex-private/state.sqlite-wal", "codex-private/cache", "codex-private/.credentials.json", "AI Coding Tool Go Dev.importing"} {
		if exists(filepath.Join(target, skipped)) || exists(filepath.Join(appData, skipped)) {
			t.Errorf("%s copied", skipped)
		}
	}
	if read(t, filepath.Join(target, "launched-version")) != "1.2.3" {
		t.Error("launched version not this app's")
	}
	if !strings.Contains(read(t, filepath.Join(target, "workspaces.v1.json")), filepath.Join(target, "projectless", "p")) {
		t.Error("registry not repointed")
	}
	if database.repoint[1] != filepath.Join(source, "attachments")+"/" || database.repoint[2] != filepath.Join(target, "attachments")+"/" {
		t.Errorf("repointed %q", database.repoint)
	}
	if !slices.Equal(ImportedWorktreesRoots(target), []string{filepath.Join(home, ".aicodingtool-dev", "worktrees"), filepath.Join(source, "worktrees")}) {
		t.Errorf("roots %q", ImportedWorktreesRoots(target))
	}
	if read(t, filepath.Join(source, "launched-version")) != "0.1.0" || !exists(filepath.Join(source, "tasks.v3.sqlite-wal")) {
		t.Error("the source was written")
	}

	os.WriteFile(filepath.Join(source, "settings.v1.json"), []byte(`{"changed":true}`), 0o644)
	if _, report, err := PrepareProfile(appData, home, false, "1.2.3", database); err != nil || report != nil {
		t.Fatalf("a profile with data imported again: %v %v", report, err)
	}
}
