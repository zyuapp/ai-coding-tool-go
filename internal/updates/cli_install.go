package updates

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// CliInstaller installs, repairs and removes the `aic` command.
type CliInstaller struct {
	configuration *domain.CliConfiguration
	platform      string
	searchPath    func() string
}

// NewCliInstaller takes nil where the platform has no terminal integration. searchPath is the PATH
// a Linux shell reads; the app's own search path when nil.
func NewCliInstaller(configuration *domain.CliConfiguration, platformName string, searchPath func() string) *CliInstaller {
	if searchPath == nil {
		searchPath = platform.SearchPath
	}
	return &CliInstaller{configuration: configuration, platform: platformName, searchPath: searchPath}
}

// NewRuntimeCliInstaller is the installer for this app. What the script runs for `aic serve` is the
// AppImage that holds this app on Linux, else the binary itself.
func NewRuntimeCliInstaller() *CliInstaller {
	installed := os.Getenv("APPIMAGE")
	if installed == "" {
		installed, _ = os.Executable()
	}
	home, _ := os.UserHomeDir()
	return NewCliInstaller(domain.CliCliConfiguration(platform.OS, home, installed), platform.OS, nil)
}

func (c *CliInstaller) result(state domain.CliStatusState) *domain.CliStatus {
	target := domain.CliInstallPath
	if c.configuration != nil {
		target = c.configuration.InstallPath
	}
	status := &domain.CliStatus{State: state, Path: target}
	if c.platform == "linux" {
		onPath := false
		for _, entry := range filepath.SplitList(c.searchPath()) {
			if resolved, err := filepath.Abs(entry); err == nil && resolved == filepath.Dir(target) {
				onPath = true
				break
			}
		}
		status.OnPath = &onPath
	}
	return status
}

func (c *CliInstaller) Status(context.Context) (*domain.CliStatus, error) {
	if c.configuration == nil {
		return c.result(domain.CliStatusStateUnsupported), nil
	}
	info, err := os.Lstat(c.configuration.InstallPath)
	if errors.Is(err, fs.ErrNotExist) {
		return c.result(domain.CliStatusStateMissing), nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return c.result(domain.CliStatusStateConflict), nil
	}
	contents, err := os.ReadFile(c.configuration.InstallPath)
	if errors.Is(err, fs.ErrNotExist) {
		return c.result(domain.CliStatusStateMissing), nil
	}
	if err != nil {
		return nil, err
	}
	if !domain.IsCliScript(string(contents)) {
		return c.result(domain.CliStatusStateConflict), nil
	}
	status := c.result(domain.CliStatusStateInstalled)
	current := string(contents) == c.configuration.Script
	status.Current = &current
	return status, nil
}

func (c *CliInstaller) supported() (*domain.CliConfiguration, error) {
	if c.configuration == nil {
		return nil, errors.New("The aic command can only be installed on macOS or Linux.")
	}
	return c.configuration, nil
}

func (c *CliInstaller) writeInstall(ctx context.Context, allowElevation bool) (*domain.CliStatus, error) {
	target, err := c.supported()
	if err != nil {
		return nil, err
	}
	if err := writeScript(target); err != nil {
		if !allowElevation || !isPermissionError(err) || c.platform != "darwin" {
			return nil, err
		}
		if err := elevate(ctx, CliInstallCommand(*target)); err != nil {
			return nil, err
		}
	}
	return c.Status(ctx)
}

func writeScript(target *domain.CliConfiguration) error {
	if err := os.MkdirAll(filepath.Dir(target.InstallPath), 0o777); err != nil {
		return err
	}
	staged := fmt.Sprintf("%s.%d.%s.tmp", target.InstallPath, os.Getpid(), application.NewID())
	defer os.Remove(staged)
	if err := writeExclusive(staged, []byte(target.Script), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return err
	}
	return os.Rename(staged, target.InstallPath)
}

// Install writes the command, asking for the administrator password when its directory needs one.
func (c *CliInstaller) Install(ctx context.Context) (*domain.CliStatus, error) {
	return c.writeInstall(ctx, true)
}

func (c *CliInstaller) Uninstall(ctx context.Context) (*domain.CliStatus, error) {
	target, err := c.supported()
	if err != nil {
		return nil, err
	}
	if err := os.Remove(target.InstallPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if !isPermissionError(err) || c.platform != "darwin" {
			return nil, err
		}
		if err := elevate(ctx, "/bin/rm -f "+shellQuote(target.InstallPath)); err != nil {
			return nil, err
		}
	}
	return c.Status(ctx)
}

// Refresh repairs only our existing launcher; startup must respect an uninstall or another command.
func (c *CliInstaller) Refresh(ctx context.Context) (*domain.CliStatus, error) {
	current, err := c.Status(ctx)
	if err != nil {
		return nil, err
	}
	if current.State != domain.CliStatusStateInstalled || current.Current != nil && *current.Current {
		return current, nil
	}
	refreshed, err := c.writeInstall(ctx, false)
	if err != nil {
		// Only an explicit Settings action may ask for administrator permission.
		if isPermissionError(err) {
			return current, nil
		}
		return nil, err
	}
	return refreshed, nil
}

// Port serves the cli port.
func (c *CliInstaller) Port() contracts.CliPortRuntime { return cliPort{c} }

type cliPort struct{ installer *CliInstaller }

func (p cliPort) CliStatus(ctx context.Context) (*domain.CliStatus, error) {
	return p.installer.Status(ctx)
}

func (p cliPort) InstallCli(ctx context.Context) (*domain.CliStatus, error) {
	return p.installer.Install(ctx)
}

func (p cliPort) UninstallCli(ctx context.Context) (*domain.CliStatus, error) {
	return p.installer.Uninstall(ctx)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// CliInstallCommand is the elevated install: only known bytes cross elevation, and the elevated
// process owns its private staging directory.
func CliInstallCommand(target domain.CliConfiguration) string {
	return strings.Join([]string{
		"set -eu",
		"umask 077",
		`stage=$(/usr/bin/mktemp -d /private/tmp/aic-cli-install.XXXXXXXX)`,
		`trap '/bin/rm -rf "$stage"' EXIT`,
		`/usr/bin/printf '%s' ` + shellQuote(base64.StdEncoding.EncodeToString([]byte(target.Script))) + ` | /usr/bin/base64 -D > "$stage/aic"`,
		`/bin/chmod 755 "$stage/aic"`,
		"/bin/mkdir -p " + shellQuote(filepath.Dir(target.InstallPath)),
		`/bin/mv -fh "$stage/aic" ` + shellQuote(target.InstallPath),
	}, "\n")
}

func isPermissionError(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS)
}

var cancelledElevation = regexp.MustCompile(`User canceled|-128`)

// elevate runs a shell command as root: /usr/local/bin belongs to root on a stock Mac, so writing
// there asks for the password once.
func elevate(ctx context.Context, command string) error {
	script := "do shell script " + jsonString(command) + " with administrator privileges"
	if _, _, err := platform.Run(ctx, "/usr/bin/osascript", []string{"-e", script}, platform.RunOptions{}); err != nil {
		message := err.Error()
		if cancelledElevation.MatchString(message) {
			return errors.New("Cancelled.")
		}
		if trimmed := strings.TrimSpace(message); trimmed != "" {
			return errors.New(trimmed)
		}
		return errors.New("The command could not be installed.")
	}
	return nil
}

// jsonString is JSON.stringify of a string, which AppleScript reads as a string literal.
func jsonString(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}
