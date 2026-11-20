package updates

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// The desktop entry an AppImage registers for itself, written as linux-protocol writes it.
const linuxDesktopFile = "com.zyuapp.aicodingtool.desktop"
const linuxIconName = "com.zyuapp.aicodingtool"

var desktopExecEscapes = regexp.MustCompile("([\\\\\"`$])")

// desktopExec quotes a path for a desktop entry's Exec, which has its own escaping and never invokes a shell.
func desktopExec(value string) (string, error) {
	if !filepath.IsAbs(value) || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("The AppImage path is not safe to register.")
	}
	return `"` + desktopExecEscapes.ReplaceAllString(strings.ReplaceAll(value, "%", "%%"), `\$1`) + `"`, nil
}

func appImageDesktopEntry(appImage string) (string, error) {
	executable, err := desktopExec(appImage)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"[Desktop Entry]",
		"Name=AI Coding Tool",
		"Comment=A fast local desktop client for Claude and Codex coding tasks.",
		"Exec=" + executable + " %U",
		"Terminal=false",
		"Type=Application",
		"Icon=" + linuxIconName,
		"Categories=Development;",
		"StartupWMClass=com.zyuapp.aicodingtool",
		"MimeType=x-scheme-handler/" + domain.CliURLScheme + ";",
		"X-AICodingTool-AppImageIntegration=true",
		"",
	}, "\n"), nil
}

// AppImageLaunchers is the AppImage an update moves, and where its launchers were written.
type AppImageLaunchers struct {
	AppImage string
	Home     string
	// DataHome is XDG_DATA_HOME; ~/.local/share unless it is absolute.
	DataHome string
}

// RepairAppImageLaunchers points the desktop entry and the aic command at the AppImage's new path,
// once the update has moved it. Launchers that are missing, redirected, customized, or another
// installation's are left alone.
func RepairAppImageLaunchers(launchers AppImageLaunchers, destination string) {
	dataHome := launchers.DataHome
	if dataHome == "" || !filepath.IsAbs(dataHome) {
		dataHome = filepath.Join(launchers.Home, ".local", "share")
	}
	oldCli := domain.CliCliConfiguration("linux", launchers.Home, launchers.AppImage)
	newCli := domain.CliCliConfiguration("linux", launchers.Home, destination)
	type launcher struct {
		target        string
		before, after string
		mode          os.FileMode
	}
	var files []launcher
	before, beforeErr := appImageDesktopEntry(launchers.AppImage)
	after, afterErr := appImageDesktopEntry(destination)
	if err := errors.Join(beforeErr, afterErr); err != nil {
		log.Printf("Could not refresh AppImage launchers: %v", err)
	} else {
		files = append(files, launcher{filepath.Join(dataHome, "applications", linuxDesktopFile), before, after, 0o644})
	}
	if oldCli != nil && newCli != nil {
		files = append(files, launcher{oldCli.InstallPath, oldCli.Script, newCli.Script, 0o755})
	}
	for _, file := range files {
		if err := replaceOwned(file.target, file.before, file.after, file.mode); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("Could not refresh AppImage launcher %s: %v", file.target, err)
		}
	}
}

func replaceOwned(target, before, after string, mode os.FileMode) error {
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if string(contents) != before {
		return nil
	}
	staged := target + "." + application.NewID() + ".tmp"
	defer os.Remove(staged)
	if err := writeExclusive(staged, []byte(after), mode); err != nil {
		return err
	}
	return os.Rename(staged, target)
}

// writeExclusive is writeFile with flag "wx": it never writes through an existing path.
func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// AppImageProtocolRegistration is the AppImage to register as the handler of the app's links.
type AppImageProtocolRegistration struct {
	AppImage   string
	Home       string
	IconSource string
	// DataHome is XDG_DATA_HOME; ~/.local/share unless it is absolute.
	DataHome string
	// IsAssociated and Associate ask the desktop which entry opens the app's links, and make it this one.
	IsAssociated func(ctx context.Context, desktopFile string) bool
	Associate    func(ctx context.Context, desktopFile string) error
}

// AppImageProtocolResult is where the registration wrote, and what it changed.
type AppImageProtocolResult struct {
	DesktopPath, IconPath            string
	FilesChanged, AssociationChanged bool
}

type integrationFile struct {
	kind     string // "missing", "symlink", "other" or "file"
	contents []byte
}

func existingIntegrationFile(file string) (integrationFile, error) {
	info, err := os.Lstat(file)
	if errors.Is(err, fs.ErrNotExist) {
		return integrationFile{kind: "missing"}, nil
	}
	if err != nil {
		return integrationFile{}, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return integrationFile{kind: "symlink"}, nil
	}
	if !info.Mode().IsRegular() {
		return integrationFile{kind: "other"}, nil
	}
	contents, err := os.ReadFile(file)
	return integrationFile{kind: "file", contents: contents}, err
}

// isOwnedDesktopEntry recognizes entries written before the ownership marker existed, so moved AppImages still repair.
func isOwnedDesktopEntry(contents []byte) bool {
	lines := map[string]bool{}
	for _, line := range strings.Split(string(contents), "\n") {
		lines[strings.TrimSuffix(line, "\r")] = true
	}
	return lines["[Desktop Entry]"] && lines["Name=AI Coding Tool"] && lines["StartupWMClass=com.zyuapp.aicodingtool"] &&
		lines["MimeType=x-scheme-handler/"+domain.CliURLScheme+";"]
}

// RegisterAppImageProtocol registers this AppImage's current absolute path in the user's XDG folders as
// the handler of the app's links. AppImages are portable and may have no installed desktop entry; a moved
// one is repaired on its next launch.
func RegisterAppImageProtocol(ctx context.Context, options AppImageProtocolRegistration) (AppImageProtocolResult, error) {
	if !filepath.IsAbs(options.Home) {
		return AppImageProtocolResult{}, errors.New("The home directory is not absolute.")
	}
	if !filepath.IsAbs(options.IconSource) {
		return AppImageProtocolResult{}, errors.New("The AppImage icon path is not absolute.")
	}
	dataHome := options.DataHome
	if dataHome == "" || !filepath.IsAbs(dataHome) {
		dataHome = filepath.Join(options.Home, ".local", "share")
	}
	applications := filepath.Join(dataHome, "applications")
	icons := filepath.Join(dataHome, "icons", "hicolor", "256x256", "apps")
	result := AppImageProtocolResult{DesktopPath: filepath.Join(applications, linuxDesktopFile), IconPath: filepath.Join(icons, linuxIconName+".png")}
	entry, err := appImageDesktopEntry(options.AppImage)
	if err != nil {
		return result, err
	}
	icon, err := os.ReadFile(options.IconSource)
	if err != nil {
		return result, err
	}
	currentDesktop, err := existingIntegrationFile(result.DesktopPath)
	if err != nil {
		return result, err
	}
	currentIcon, err := existingIntegrationFile(result.IconPath)
	if err != nil {
		return result, err
	}
	writeDesktop := !(currentDesktop.kind == "file" && string(currentDesktop.contents) == entry)
	owned := currentDesktop.kind == "file" && isOwnedDesktopEntry(currentDesktop.contents)
	if writeDesktop && !owned {
		if currentDesktop.kind == "other" {
			return result, errors.New("Refusing to replace a non-file Linux integration path: " + result.DesktopPath)
		}
		if currentDesktop.kind == "file" {
			return result, errors.New("Refusing to overwrite an unrelated desktop entry: " + result.DesktopPath)
		}
	}
	writeIcon := !(currentIcon.kind == "file" && bytes.Equal(currentIcon.contents, icon))
	// The reverse-DNS icon path is ours even when a user deleted the companion desktop entry.
	if writeIcon && currentIcon.kind == "other" {
		return result, errors.New("Refusing to replace a non-file Linux integration path: " + result.IconPath)
	}
	if writeDesktop || writeIcon {
		if err := errors.Join(os.MkdirAll(applications, 0o755), os.MkdirAll(icons, 0o755)); err != nil {
			return result, err
		}
	}
	nonce := application.NewID()
	if writeIcon {
		if err := replaceStaged(result.IconPath, result.IconPath+"."+nonce+".tmp", icon); err != nil {
			return result, err
		}
	}
	if writeDesktop {
		if err := replaceStaged(result.DesktopPath, result.DesktopPath+"."+nonce+".tmp", []byte(entry)); err != nil {
			return result, err
		}
	}
	result.FilesChanged = writeDesktop || writeIcon
	isAssociated, associate := options.IsAssociated, options.Associate
	if isAssociated == nil {
		isAssociated = schemeAssociated
	}
	if associate == nil {
		associate = associateScheme
	}
	if !isAssociated(ctx, linuxDesktopFile) {
		result.AssociationChanged = true
		if err := associate(ctx, linuxDesktopFile); err != nil {
			return result, err
		}
	}
	return result, nil
}

func replaceStaged(target, staged string, data []byte) error {
	defer os.Remove(staged)
	if err := writeExclusive(staged, data, 0o644); err != nil {
		return err
	}
	return os.Rename(staged, target)
}

const schemeMime = "x-scheme-handler/" + domain.CliURLScheme

func schemeAssociated(ctx context.Context, desktopFile string) bool {
	query, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if output, err := platform.Command(query, "xdg-mime", "query", "default", schemeMime).Output(); err == nil {
		return strings.TrimSpace(string(output)) == desktopFile
	}
	fallback, cancelFallback := context.WithTimeout(ctx, 2*time.Second)
	defer cancelFallback()
	command := platform.Command(fallback, "gio", "mime", schemeMime)
	command.Env = append(command.Environ(), "LC_ALL=C")
	output, err := command.Output()
	if err != nil {
		return false
	}
	first, _, _ := strings.Cut(string(output), "\n")
	return strings.HasSuffix(strings.TrimSpace(first), ": "+desktopFile)
}

func associateScheme(ctx context.Context, desktopFile string) error {
	run, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	first := platform.Command(run, "xdg-mime", "default", desktopFile, schemeMime).Run()
	if first == nil {
		return nil
	}
	fallback, cancelFallback := context.WithTimeout(ctx, 10*time.Second)
	defer cancelFallback()
	if platform.Command(fallback, "gio", "mime", schemeMime, desktopFile).Run() == nil {
		return nil
	}
	return first
}
