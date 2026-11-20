package updates

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func linuxInstaller(t *testing.T, searchPath string) (*domain.CliConfiguration, *CliInstaller) {
	t.Helper()
	home := t.TempDir()
	configuration := domain.CliCliConfiguration("linux", home, "")
	if configuration == nil {
		t.Fatal("linux has a configuration")
	}
	if searchPath == "" {
		searchPath = filepath.Dir(configuration.InstallPath) + string(filepath.ListSeparator) + "/usr/bin"
	}
	return configuration, NewCliInstaller(configuration, "linux", func() string { return searchPath })
}

func status(state domain.CliStatusState, path string, onPath, current *bool) domain.CliStatus {
	return domain.CliStatus{State: state, Path: path, OnPath: onPath, Current: current}
}

func same(t *testing.T, got *domain.CliStatus, err error, want domain.CliStatus) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if got.State != want.State || got.Path != want.Path || fmt.Sprint(deref(got.OnPath)) != fmt.Sprint(deref(want.OnPath)) || fmt.Sprint(deref(got.Current)) != fmt.Sprint(deref(want.Current)) {
		t.Fatalf("got %+v onPath=%v current=%v, want %+v", got, deref(got.OnPath), deref(got.Current), want)
	}
}

func deref(flag *bool) any {
	if flag == nil {
		return nil
	}
	return *flag
}

var yes, no = ptrTo(true), ptrTo(false)

func ptrTo[T any](value T) *T { return &value }

func TestLinuxCliInstallUsesAUserWritablePath(t *testing.T) {
	ctx := context.Background()
	configuration, installer := linuxInstaller(t, "")
	path := configuration.InstallPath
	got, err := installer.Status(ctx)
	same(t, got, err, status("missing", path, yes, nil))
	got, err = installer.Install(ctx)
	same(t, got, err, status("installed", path, yes, yes))
	if contents, _ := os.ReadFile(path); string(contents) != configuration.Script {
		t.Fatal("script written")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o755 {
		t.Fatal(info.Mode())
	}
	os.WriteFile(path, []byte("#!/bin/sh\necho somebody-else\n"), 0o644)
	got, err = installer.Status(ctx)
	same(t, got, err, status("conflict", path, yes, nil))
	got, err = installer.Uninstall(ctx)
	same(t, got, err, status("missing", path, yes, nil))
}

func TestLinuxCliInstallReplacesASymlinkWithoutWritingThroughIt(t *testing.T) {
	ctx := context.Background()
	configuration, installer := linuxInstaller(t, "")
	path := configuration.InstallPath
	protected := filepath.Join(filepath.Dir(filepath.Dir(path)), "keep-me")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(protected, []byte("user data"), 0o644)
	os.Symlink(protected, path)
	got, err := installer.Status(ctx)
	same(t, got, err, status("conflict", path, yes, nil))
	got, err = installer.Install(ctx)
	same(t, got, err, status("installed", path, yes, yes))
	if info, _ := os.Lstat(path); !info.Mode().IsRegular() {
		t.Fatal("the link itself is replaced")
	}
	if contents, _ := os.ReadFile(protected); string(contents) != "user data" {
		t.Fatal("never written through")
	}
}

func TestLinuxCliStatusSaysWhenTheBinFolderIsNotOnPath(t *testing.T) {
	configuration, installer := linuxInstaller(t, "/usr/bin:/bin")
	got, err := installer.Install(context.Background())
	same(t, got, err, status("installed", configuration.InstallPath, no, yes))
}

func TestUnsupportedPlatformsReportStatusAndRefuseChanges(t *testing.T) {
	ctx := context.Background()
	installer := NewCliInstaller(nil, "win32", nil)
	got, err := installer.Status(ctx)
	same(t, got, err, status("unsupported", "/usr/local/bin/aic", nil, nil))
	for _, change := range []func(context.Context) (*domain.CliStatus, error){installer.Install, installer.Uninstall} {
		if _, err := change(ctx); err == nil || !strings.Contains(err.Error(), "macOS or Linux") {
			t.Fatal(err)
		}
	}
	if got, _ := installer.Refresh(ctx); got.State != "unsupported" {
		t.Fatal(got)
	}
}

func TestRefreshKeepsAStaleLauncherInAProtectedFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	for _, platformName := range []string{"linux", "darwin"} {
		ctx := context.Background()
		configuration, _ := linuxInstaller(t, "")
		installer := NewCliInstaller(configuration, platformName, nil)
		installer.Install(ctx)
		stale := "#!/bin/sh\n# aic-cli v1\n"
		os.WriteFile(configuration.InstallPath, []byte(stale), 0o755)
		directory := filepath.Dir(configuration.InstallPath)
		os.Chmod(directory, 0o555)
		before, _ := installer.Status(ctx)
		if *before.Current {
			t.Fatal("stale")
		}
		after, err := installer.Refresh(ctx)
		if err != nil || *after.Current || after.State != before.State {
			t.Fatal(after, err)
		}
		if contents, _ := os.ReadFile(configuration.InstallPath); string(contents) != stale {
			t.Fatal("kept")
		}
		os.Chmod(directory, 0o755)
		if got, _ := installer.Install(ctx); !*got.Current {
			t.Fatal("Settings can still explicitly update the launcher")
		}
	}
}

func TestRefreshRepairsALauncherAfterTheAppMoves(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, platformName := range []string{"linux", "darwin"} {
		ctx := context.Background()
		configuration, _ := linuxInstaller(t, "")
		old := *domain.CliCliConfiguration(platformName, home, "/old/app")
		next := *domain.CliCliConfiguration(platformName, home, "/new/app")
		old.InstallPath, next.InstallPath = configuration.InstallPath, configuration.InstallPath
		NewCliInstaller(&old, platformName, nil).Install(ctx)
		updated := NewCliInstaller(&next, platformName, nil)
		if got, _ := updated.Status(ctx); *got.Current {
			t.Fatal("old script")
		}
		if got, _ := updated.Refresh(ctx); !*got.Current {
			t.Fatal("repaired")
		}
		before, _ := os.Stat(configuration.InstallPath)
		updated.Refresh(ctx)
		after, _ := os.Stat(configuration.InstallPath)
		if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
			t.Fatal("a current launcher is not rewritten")
		}
	}
}

func TestRefreshLeavesAbsentUnrelatedAndSymlinkedCommandsAlone(t *testing.T) {
	ctx := context.Background()
	configuration, _ := linuxInstaller(t, "")
	installer := NewCliInstaller(configuration, "linux", nil)
	path := configuration.InstallPath
	if got, _ := installer.Refresh(ctx); got.State != "missing" {
		t.Fatal(got)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("not created")
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("another command"), 0o644)
	if got, _ := installer.Refresh(ctx); got.State != "conflict" {
		t.Fatal(got)
	}
	os.Remove(path)
	other := path + "-other"
	os.WriteFile(other, []byte("# aic-cli v1\n"), 0o755)
	os.Symlink(other, path)
	if got, _ := installer.Refresh(ctx); got.State != "conflict" {
		t.Fatal(got)
	}
	if contents, _ := os.ReadFile(other); string(contents) != "# aic-cli v1\n" {
		t.Fatal("not written through")
	}
	installer.Uninstall(ctx)
	if got, _ := installer.Refresh(ctx); got.State != "missing" {
		t.Fatal(got)
	}
}

func TestTheElevatedCommandWritesExactBytesAndReplacesSymlinks(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS base64 and mv flags")
	}
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	os.MkdirAll(victim, 0o755)
	os.WriteFile(filepath.Join(victim, "aic"), []byte("preserve"), 0o644)
	target := domain.CliConfiguration{InstallPath: filepath.Join(root, "aic ' $(false)"), Script: "#!/bin/sh\nprintf '%s' 'literal `$HOME` $(false)'\n"}
	os.Symlink(victim, target.InstallPath)
	if output, err := exec.Command("/bin/sh", "-c", CliInstallCommand(target)).CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	if contents, _ := os.ReadFile(target.InstallPath); string(contents) != target.Script {
		t.Fatal(string(contents))
	}
	if info, _ := os.Lstat(target.InstallPath); !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
		t.Fatal(info.Mode())
	}
	if contents, _ := os.ReadFile(filepath.Join(victim, "aic")); string(contents) != "preserve" {
		t.Fatal("the link's target is untouched")
	}
	if jsonString("a\"b\\c\nd") != `"a\"b\\c\nd"` {
		t.Fatal(jsonString("a\"b\\c\nd"))
	}
}

func TestAnUpdateRepairsLaunchersItOwns(t *testing.T) {
	for _, custom := range []bool{false, true} {
		home := t.TempDir()
		dataHome := ""
		if custom {
			dataHome = filepath.Join(home, "custom data")
		}
		base := dataHome
		if base == "" {
			base = filepath.Join(home, ".local/share")
		}
		desktop := filepath.Join(base, "applications", linuxDesktopFile)
		oldImage := filepath.Join(home, "AI Coding Tool-0.5.6.AppImage")
		newImage := filepath.Join(home, `AI Coding Tool "100%"-0.5.7.AppImage`)
		cli := domain.CliCliConfiguration("linux", home, oldImage)
		os.MkdirAll(filepath.Dir(desktop), 0o755)
		os.MkdirAll(filepath.Dir(cli.InstallPath), 0o755)
		entry, _ := appImageDesktopEntry(oldImage)
		os.WriteFile(desktop, []byte(entry), 0o644)
		os.WriteFile(cli.InstallPath, []byte(cli.Script), 0o755)
		RepairAppImageLaunchers(AppImageLaunchers{AppImage: oldImage, Home: home, DataHome: dataHome}, newImage)
		want, _ := appImageDesktopEntry(newImage)
		if contents, _ := os.ReadFile(desktop); string(contents) != want {
			t.Fatal(string(contents))
		}
		if !strings.Contains(want, `Exec="`+filepath.Join(home, `AI Coding Tool \"100%%\"-0.5.7.AppImage`)+`" %U`) {
			t.Fatal(want)
		}
		if contents, _ := os.ReadFile(cli.InstallPath); string(contents) != domain.CliCliConfiguration("linux", home, newImage).Script {
			t.Fatal("cli")
		}
		if info, _ := os.Stat(cli.InstallPath); info.Mode().Perm() != 0o755 {
			t.Fatal(info.Mode())
		}
	}
}

func TestAnUpdatePreservesLaunchersItDoesNotOwn(t *testing.T) {
	for _, kind := range []string{"missing", "unrelated", "symlink", "other-install"} {
		home := t.TempDir()
		desktop := filepath.Join(home, ".local/share/applications", linuxDesktopFile)
		oldImage := filepath.Join(home, "old.AppImage")
		cli := domain.CliCliConfiguration("linux", home, oldImage)
		os.MkdirAll(filepath.Dir(desktop), 0o755)
		os.MkdirAll(filepath.Dir(cli.InstallPath), 0o755)
		external := filepath.Join(home, "external")
		os.WriteFile(external, []byte("keep"), 0o644)
		for _, target := range []string{desktop, cli.InstallPath} {
			switch kind {
			case "symlink":
				os.Symlink(external, target)
			case "unrelated":
				os.WriteFile(target, []byte("keep"), 0o644)
			case "other-install":
				contents := domain.CliCliConfiguration("linux", home, "/other/app.AppImage").Script
				if target == desktop {
					contents, _ = appImageDesktopEntry("/other/app.AppImage")
				}
				os.WriteFile(target, []byte(contents), 0o644)
			}
		}
		read := func() []string {
			var all []string
			for _, target := range []string{desktop, cli.InstallPath} {
				contents, err := os.ReadFile(target)
				all = append(all, fmt.Sprint(string(contents), err == nil))
			}
			return all
		}
		before := read()
		RepairAppImageLaunchers(AppImageLaunchers{AppImage: oldImage, Home: home}, filepath.Join(home, "new.AppImage"))
		if !slices.Equal(read(), before) {
			t.Fatal(kind)
		}
		if contents, _ := os.ReadFile(external); string(contents) != "keep" {
			t.Fatal(kind)
		}
	}
}

func TestTheThreadsAnUpdateCutOffAreHandedToItsBuildOnce(t *testing.T) {
	userData := t.TempDir()
	RecordUpdateRelaunch(userData, "2.0.0", []string{"a", "b"})
	if got := TakeUpdateRelaunch(userData, "2.0.0"); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatal(got)
	}
	if got := TakeUpdateRelaunch(userData, "2.0.0"); len(got) != 0 {
		t.Fatal(got)
	}
	RecordUpdateRelaunch(userData, "2.0.1", []string{"a"})
	if got := TakeUpdateRelaunch(userData, "2.0.0"); len(got) != 0 {
		t.Fatal("an install that never took resumes nothing")
	}
	if got := TakeUpdateRelaunch(userData, "2.0.1"); len(got) != 0 {
		t.Fatal("and its record is gone")
	}
	if contents, _ := os.ReadFile(filepath.Join(userData, relaunchRecord)); contents != nil {
		t.Fatal("gone")
	}
	RecordUpdateRelaunch(userData, "2.0.2", []string{"a"})
	RecordUpdateRelaunch(userData, "2.0.2", nil)
	if _, err := os.Stat(filepath.Join(userData, relaunchRecord)); !os.IsNotExist(err) {
		t.Fatal("no threads removes the record")
	}
}

func TestTheManifestElectronBuilderPublishesIsRead(t *testing.T) {
	text := "version: 0.10.27\nfiles:\n  - url: AI-Coding-Tool-0.10.27-arm64-mac.zip\n    sha512: 'abc=='\n    size: 1234\n  - url: AI-Coding-Tool-0.10.27-mac.zip\n    sha512: def==\n    size: 99\n  - url: broken.zip\npath: AI-Coding-Tool-0.10.27-arm64-mac.zip\nsha512: abc==\n"
	manifest := ParseReleaseManifest(text)
	if manifest.Version != "0.10.27" || len(manifest.Files) != 2 || manifest.Files[0] != (ReleaseFile{URL: "AI-Coding-Tool-0.10.27-arm64-mac.zip", SHA512: "abc==", Size: 1234}) {
		t.Fatalf("%+v", manifest)
	}
	if releaseFile(manifest, UpdateTarget{Bundle: "/A.app"}, "arm64").Size != 1234 || releaseFile(manifest, UpdateTarget{Bundle: "/A.app"}, "amd64").Size != 99 {
		t.Fatal("the build for this architecture")
	}
	if releaseFile(manifest, UpdateTarget{AppImage: "/a.AppImage"}, "amd64") != nil {
		t.Fatal("no AppImage listed")
	}
	if ParseReleaseManifest("files: []") != nil {
		t.Fatal("no version")
	}
}

func TestTargetsAreTheBundleOrTheAppImage(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	if got := TargetFor("darwin", "/Applications/AI Coding Tool.app/Contents/MacOS/aicodingtool", env(nil)); got == nil || got.Bundle != "/Applications/AI Coding Tool.app" {
		t.Fatal(got)
	}
	if TargetFor("darwin", "/Users/me/src/build/bin/aicodingtool", env(nil)) != nil {
		t.Fatal("a source build replaces nothing")
	}
	if got := TargetFor("linux", "/tmp/.mount/aicodingtool", env(map[string]string{"APPIMAGE": "/home/me/AI.AppImage"})); got == nil || got.AppImage != "/home/me/AI.AppImage" {
		t.Fatal(got)
	}
}

func serveRelease(t *testing.T, build []byte, published string) *httptest.Server {
	t.Helper()
	sum := sha512.Sum512(build)
	manifest := fmt.Sprintf("version: 0.6.0\nfiles:\n  - url: %s\n    sha512: %s\n    size: %d\n", published, base64.StdEncoding.EncodeToString(sum[:]), len(build))
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest/download/latest-linux.yml":
			w.Write([]byte(manifest))
		case "/releases/download/v0.6.0/" + strings.ReplaceAll(published, " ", "%20"):
			w.Write(build)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestAReleaseUpdaterInstallsAnAppImageAndRelaunchesIt(t *testing.T) {
	build := []byte("new executable")
	server := serveRelease(t, build, "AI-Coding-Tool-0.6.0.AppImage")
	defer server.Close()
	dir := t.TempDir()
	image := filepath.Join(dir, "AI Coding Tool-0.5.9.AppImage")
	os.WriteFile(image, []byte("old executable"), 0o755)
	var available, downloaded, moved string
	var progress []float64
	var relaunched string
	quit := false
	updater := NewReleaseUpdater(ReleaseUpdaterOptions{
		Version:  "0.5.9",
		Target:   UpdateTarget{AppImage: image},
		Source:   Source{Releases: server.URL + "/releases", Client: server.Client(), Platform: "linux", Arch: "amd64"},
		Quit:     func() { quit = true },
		Relaunch: func(_ UpdateTarget, installed string) error { relaunched = installed; return nil },
	}, UpdaterEvents{
		UpdateAvailable:         func(version string) { available = version },
		DownloadProgress:        func(percent float64) { progress = append(progress, percent) },
		UpdateDownloaded:        func(version string) { downloaded = version },
		AppImageFilenameUpdated: func(destination string) { moved = destination },
	})
	result, err := updater.CheckForUpdates(context.Background())
	if err != nil || !result.IsUpdateAvailable || available != "0.6.0" {
		t.Fatal(result, err)
	}
	if err := updater.DownloadUpdate(context.Background()); err != nil || downloaded != "0.6.0" || progress[len(progress)-1] != 100 {
		t.Fatal(err, progress)
	}
	if err := updater.QuitAndInstall(); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(dir, "AI-Coding-Tool-0.6.0.AppImage")
	if contents, _ := os.ReadFile(next); string(contents) != string(build) || moved != next || relaunched != next || !quit {
		t.Fatal("swapped in under its published name, then relaunched")
	}
	if _, err := os.Stat(image); !os.IsNotExist(err) {
		t.Fatal("the old image is gone")
	}
	if info, _ := os.Stat(next); info.Mode().Perm()&0o100 == 0 {
		t.Fatal("executable")
	}
}

func TestAReleaseThatDoesNotMatchItsChecksumIsRefused(t *testing.T) {
	server := serveRelease(t, []byte("new executable"), "app.AppImage")
	defer server.Close()
	tampered := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".AppImage") {
			w.Write([]byte("evil executable"))
			return
		}
		response, err := server.Client().Get(server.URL + r.URL.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.WriteHeader(response.StatusCode)
		buffer := make([]byte, 1<<16)
		n, _ := response.Body.Read(buffer)
		w.Write(buffer[:n])
	}))
	defer tampered.Close()
	dir := t.TempDir()
	image := filepath.Join(dir, "custom.AppImage")
	os.WriteFile(image, []byte("old"), 0o755)
	updater := NewReleaseUpdater(ReleaseUpdaterOptions{Version: "0.5.9", Target: UpdateTarget{AppImage: image},
		Source: Source{Releases: tampered.URL + "/releases", Client: tampered.Client(), Platform: "linux", Arch: "amd64"}}, UpdaterEvents{})
	if _, err := updater.CheckForUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := updater.DownloadUpdate(context.Background())
	if err == nil || err.Error() != "app.AppImage did not match its checksum." {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("the download is removed")
	}
}
