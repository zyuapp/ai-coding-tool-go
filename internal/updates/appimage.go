package updates

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
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
