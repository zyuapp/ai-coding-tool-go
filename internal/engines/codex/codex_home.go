package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/google/uuid"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// Inputs shared with the user's Codex install. Never link runtime directories or databases.
var sharedInputs = []string{
	"config.toml", "auth.json", ".env", "AGENTS.md", "AGENTS.override.md",
	"rules", "skills", "agents", "hooks", "hooks.json", "requirements.toml", "managed_config.toml",
	"plugins/cache", ".tmp/marketplaces", "mcp-oauth-locks",
}

var writableDirectories = map[string]bool{"plugins/cache": true, ".tmp/marketplaces": true, "mcp-oauth-locks": true}

var preparing = struct {
	sync.Mutex
	homes map[string]*sync.Mutex
}{homes: map[string]*sync.Mutex{}}

// Hard links one file to another; replaced in tests to stand for a second filesystem.
var hardLink = os.Link

func missing(err error) bool { return errors.Is(err, fs.ErrNotExist) }

func readlink(file string) (string, bool) {
	target, err := os.Readlink(file)
	return target, err == nil
}

func resolvePath(base, target string) string {
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Join(base, target)
}

func realpath(file string) (string, error) {
	resolved, err := filepath.EvalSymlinks(file)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func share(source, destination string, adoptDirectory bool) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	// A dangling link is intentional: sign-in or a later config edit creates the shared target.
	err := os.Symlink(source, destination)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return err
	}
	target, linked := readlink(destination)
	if !linked && adoptDirectory {
		if info, err := os.Lstat(destination); err != nil {
			return err
		} else if info.IsDir() {
			// Older launches may have created a cache. Retain it locally; the source home owns installs.
			if err := os.Rename(destination, destination+".before-sharing-"+uuid.NewString()); err != nil {
				return err
			}
			return os.Symlink(source, destination)
		}
	}
	if !linked || resolvePath(filepath.Dir(destination), target) != source {
		return fmt.Errorf("Cannot share Codex settings: %s already exists and is not a link to %s.", destination, source)
	}
	return nil
}

type fileIdentity struct {
	Dev float64 `json:"dev"`
	Ino float64 `json:"ino"`
}

// MCP's file backend refuses symlinks. A hard link preserves its regular-file and shared-write
// semantics; shared refresh locks coordinate both homes. Recheck inodes after logout/replacement.
func shareMcpCredentials(source, destination string) error {
	original := filepath.Join(source, ".credentials.json")
	target := filepath.Join(destination, ".credentials.json")
	marker := filepath.Join(destination, ".aicodingtool-mcp-credentials.json")
	if _, err := os.Lstat(original); err != nil {
		if !missing(err) {
			return err
		}
		empty := filepath.Join(source, ".credentials-"+uuid.NewString()+".tmp")
		err := func() error {
			defer os.Remove(empty)
			file, err := os.OpenFile(empty, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, err = file.WriteString("{}")
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
			if err := hardLink(empty, original); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	canonical, err := realpath(original)
	if err != nil {
		return err
	}
	sourceInfo, err := os.Stat(canonical)
	if err != nil {
		return err
	}
	sourceIdentity := identityOf(sourceInfo)
	targetInfo, err := os.Lstat(target)
	if err != nil && !missing(err) {
		return err
	}
	remember := func() error {
		data, _ := jsonx.Marshal(sourceIdentity)
		return os.WriteFile(marker, data, 0o600)
	}
	if targetInfo != nil && targetInfo.Mode().IsRegular() && identityOf(targetInfo) == sourceIdentity {
		return remember()
	}
	symlinked := targetInfo != nil && targetInfo.Mode()&fs.ModeSymlink != 0
	if symlinked {
		if linkTarget, _ := os.Readlink(target); linkTarget != original {
			return fmt.Errorf("Unexpected Codex credentials link: %s", target)
		}
	}
	if targetInfo != nil && !symlinked {
		// Only replace a hard link from an earlier launch. An independently created credential file
		// belongs to its writer and must not be discarded during migration.
		var previous *fileIdentity
		if data, err := os.ReadFile(marker); err == nil {
			var read fileIdentity
			if jsonx.Unmarshal(data, &read) == nil {
				previous = &read
			}
		}
		if previous == nil || identityOf(targetInfo) != *previous {
			return fmt.Errorf("Cannot share Codex MCP credentials: %s contains independent credentials.", target)
		}
	}
	temporary := target + "." + uuid.NewString() + ".tmp"
	defer os.Remove(temporary)
	if err := hardLink(canonical, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	return remember()
}

// A link left by an earlier relocation is followed, and recreated if its target was removed.
func privateDirectory(privateHome string) (string, error) {
	directory := privateHome
	if target, linked := readlink(privateHome); linked {
		directory = resolvePath(filepath.Dir(privateHome), target)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	return realpath(privateHome)
}

// Storage an ephemeral title run writes: databases indexing no conversation, and their logs.
var ephemeralStorage = regexp.MustCompile(`^[a-z]+_\d+\.sqlite(-wal|-shm)?$`)

// A home that never started Codex holds only links to shared inputs and what title runs left, so
// nothing is lost moving it. Any session, credential, or retained cache keeps it in place.
func disposable(directory string, top bool) (bool, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 || (top && (entry.Name() == "log" || ephemeralStorage.MatchString(entry.Name()))) {
			continue
		}
		if !entry.IsDir() {
			return false, nil
		}
		if ok, err := disposable(filepath.Join(directory, entry.Name()), false); err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func crossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

// Hard links cannot cross filesystems, so the private home moves into the shared home's
// filesystem. The app data path becomes a link to it and keeps naming the same home later. Thread
// history then lives in the shared home, so its folder says what it holds.
func relocate(privateHome, home, source string) (string, error) {
	credentials, err := realpath(filepath.Join(source, ".credentials.json"))
	if err != nil {
		return "", err
	}
	unrelocatable := func(from string) error {
		return fmt.Errorf("Codex MCP sign-ins can only be shared when %s is on the same filesystem as %s.", from, credentials)
	}
	info, err := os.Lstat(privateHome)
	if err != nil {
		return "", err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return "", unrelocatable(home)
	}
	if ok, err := disposable(privateHome, true); err != nil {
		return "", err
	} else if !ok {
		return "", unrelocatable(home)
	}
	absoluteHome, _ := filepath.Abs(privateHome)
	digest := sha256.Sum256([]byte(absoluteHome))
	destination := filepath.Join(source, "aicodingtool-threads", hex.EncodeToString(digest[:])[:16])
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return "", err
	}
	// Share before moving: credentials linked from a third filesystem would fail here too.
	if err := shareMcpCredentials(source, destination); err != nil {
		if crossDevice(err) {
			return "", unrelocatable(destination)
		}
		return "", err
	}
	if err := os.RemoveAll(privateHome); err != nil {
		return "", err
	}
	if err := os.Symlink(destination, privateHome); err != nil {
		return "", err
	}
	return realpath(privateHome)
}

// Recheck before each launch, including after logout removed a link or a profile was added.
func PreparePrivateCodexHome(privateHome, sourceHome string) (string, error) {
	key, _ := filepath.Abs(privateHome)
	preparing.Lock()
	lock := preparing.homes[key]
	if lock == nil {
		lock = &sync.Mutex{}
		preparing.homes[key] = lock
	}
	preparing.Unlock()
	lock.Lock()
	defer lock.Unlock()
	return prepare(privateHome, sourceHome)
}

func prepare(privateHome, sourceHome string) (string, error) {
	if err := os.MkdirAll(sourceHome, 0o700); err != nil {
		return "", err
	}
	home, err := privateDirectory(privateHome)
	if err != nil {
		return "", err
	}
	source, err := realpath(sourceHome)
	if err != nil {
		if !missing(err) {
			return "", err
		}
		source, _ = filepath.Abs(sourceHome)
	}
	if source == home {
		return "", errors.New("Private Codex storage must be separate from the shared Codex home.")
	}
	destination := home
	if err := shareMcpCredentials(source, home); err != nil {
		if !crossDevice(err) {
			return "", err
		}
		if destination, err = relocate(privateHome, home, source); err != nil {
			return "", err
		}
	}
	names := append([]string(nil), sharedInputs...)
	entries, err := os.ReadDir(source)
	if err != nil && !missing(err) {
		return "", err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".config.toml") {
			names = append(names, entry.Name())
		}
	}
	var first error
	for _, name := range names {
		writable := writableDirectories[name]
		err := func() error {
			if writable {
				if err := os.MkdirAll(filepath.Join(source, name), 0o700); err != nil {
					return err
				}
			}
			return share(filepath.Join(source, name), filepath.Join(destination, name), writable)
		}()
		if err != nil && first == nil {
			first = err
		}
	}
	if first != nil {
		return "", first
	}
	return destination, nil
}

// The app's environment as its children see it.
func childEnvironment() map[string]string { return platform.Environment() }

// Where Codex children keep their state, prepared for launch, or "" when they use the user's home.
func PrivateCodexHome(environment map[string]string) (string, error) {
	privateHome := environment[agent.PrivateCodexHomeEnv]
	if privateHome == "" {
		return "", nil
	}
	return PreparePrivateCodexHome(privateHome, SharedCodexHome(environment))
}

// The environment a conversation's Codex runs with: environment, pointed at the private home when there is one.
func CodexChildEnvironment(environment map[string]string) (map[string]string, error) {
	home, err := PrivateCodexHome(environment)
	if err != nil || home == "" {
		return environment, err
	}
	child := make(map[string]string, len(environment)+2)
	for key, value := range environment {
		child[key] = value
	}
	child["CODEX_HOME"] = home
	child["CODEX_SQLITE_HOME"] = home
	return child, nil
}

func SharedCodexHome(environment map[string]string) string {
	if home := strings.TrimSpace(environment["CODEX_HOME"]); home != "" {
		return home
	}
	user, _ := os.UserHomeDir()
	return filepath.Join(user, ".codex")
}
