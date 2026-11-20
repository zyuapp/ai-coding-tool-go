package updates

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// Releases is where this app's builds are published.
const Releases = "https://github.com/zyuapp/ai-coding-tool-go/releases"

// UpdateTarget is what an update replaces: the macOS bundle the binary sits in, or the AppImage that holds it.
type UpdateTarget struct {
	// Bundle is the .app on macOS; AppImage the image on Linux. One is set.
	Bundle   string
	AppImage string
}

// TargetFor is what this process would replace, nil for a copy that cannot replace itself.
func TargetFor(platformName, executable string, env platform.Env) *UpdateTarget {
	switch platformName {
	case "darwin":
		bundle := filepath.Clean(filepath.Join(executable, "..", "..", ".."))
		if strings.HasSuffix(bundle, ".app") {
			return &UpdateTarget{Bundle: bundle}
		}
	case "linux":
		if image := env("APPIMAGE"); image != "" {
			return &UpdateTarget{AppImage: image}
		}
	}
	return nil
}

type ReleaseFile struct {
	URL    string
	SHA512 string
	Size   float64
}

type ReleaseManifest struct {
	Version string
	Files   []ReleaseFile
}

var (
	manifestVersion = regexp.MustCompile(`(?m)^version:\s*(\S+)\s*$`)
	manifestItem    = regexp.MustCompile(`^\s+-\s+url:\s*(\S+)\s*$`)
	manifestField   = regexp.MustCompile(`^\s+(sha512|size):\s*(\S+)\s*$`)
	manifestLine    = regexp.MustCompile(`\r?\n`)
	quoted          = regexp.MustCompile(`^(['"])(.*)(['"])$`)
	leadingSpace    = regexp.MustCompile(`^\s`)
)

func unquote(value string) string {
	if match := quoted.FindStringSubmatch(value); match != nil && match[1] == match[3] {
		return match[2]
	}
	return value
}

// jsNumber is Number(text) for the decimal forms a manifest writes; NaN otherwise.
func jsNumber(text string) float64 {
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return math.NaN()
	}
	return value
}

// ParseReleaseManifest reads the `latest-*.yml` electron-builder publishes: its version and the files it lists.
func ParseReleaseManifest(text string) *ReleaseManifest {
	version := manifestVersion.FindStringSubmatch(text)
	if version == nil {
		return nil
	}
	type partial struct {
		url, sha512 string
		size        float64
		hasSize     bool
	}
	var files []*partial
	var current *partial
	for _, line := range manifestLine.Split(text, -1) {
		if item := manifestItem.FindStringSubmatch(line); item != nil {
			current = &partial{url: unquote(item[1])}
			files = append(files, current)
			continue
		}
		field := manifestField.FindStringSubmatch(line)
		if field != nil && current != nil {
			if field[1] == "size" {
				current.size, current.hasSize = jsNumber(field[2]), true
			} else {
				current.sha512 = unquote(field[2])
			}
		} else if !leadingSpace.MatchString(line) {
			current = nil
		}
	}
	manifest := &ReleaseManifest{Version: unquote(version[1]), Files: []ReleaseFile{}}
	for _, file := range files {
		if file.url != "" && file.sha512 != "" && file.hasSize && !math.IsNaN(file.size) && !math.IsInf(file.size, 0) {
			manifest.Files = append(manifest.Files, ReleaseFile{URL: file.url, SHA512: file.sha512, Size: file.size})
		}
	}
	return manifest
}

func manifestName(platformName, arch string) string {
	if platformName == "darwin" {
		return "latest-mac.yml"
	}
	if arch == "arm64" {
		return "latest-linux-arm64.yml"
	}
	return "latest-linux.yml"
}

// releaseFile is the build for this target: a zipped bundle, preferring this machine's architecture
// when the manifest lists several, or an AppImage.
func releaseFile(manifest *ReleaseManifest, target UpdateTarget, arch string) *ReleaseFile {
	suffix := ".AppImage"
	if target.Bundle != "" {
		suffix = ".zip"
	}
	var first *ReleaseFile
	for index := range manifest.Files {
		file := &manifest.Files[index]
		if !strings.HasSuffix(file.URL, suffix) {
			continue
		}
		if first == nil {
			first = file
		}
		if target.Bundle != "" && strings.Contains(file.URL, "arm64") == (arch == "arm64") {
			return file
		}
	}
	return first
}

// Source is where releases are fetched from, and what checks a macOS bundle before it is used.
type Source struct {
	Releases string
	Client   *http.Client
	Verify   func(ctx context.Context, current, next string) error
	Platform string
	Arch     string
}

// DefaultSource fetches this app's GitHub releases for this machine.
func DefaultSource() Source {
	return Source{Releases: Releases, Client: http.DefaultClient, Verify: VerifyBundle, Platform: platform.OS, Arch: runtime.GOARCH}
}

func (s Source) get(ctx context.Context, url string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "AI Coding Tool")
	response, err := s.Client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		response.Body.Close()
		return nil, fmt.Errorf("%s answered %d.", url, response.StatusCode)
	}
	return response, nil
}

// FetchManifest is the newest release's manifest, refused when it names no build for this target.
func (s Source) FetchManifest(ctx context.Context, target UpdateTarget) (*ReleaseManifest, error) {
	name := manifestName(s.Platform, s.Arch)
	response, err := s.get(ctx, s.Releases+"/latest/download/"+name)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	text, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	manifest := ParseReleaseManifest(string(text))
	if manifest == nil || releaseFile(manifest, target, s.Arch) == nil {
		return nil, fmt.Errorf("%s names no build for this computer.", name)
	}
	return manifest, nil
}

// encodeURIComponent is JavaScript's.
func encodeURIComponent(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || strings.IndexByte("-_.!~*'()", b) >= 0 {
			out.WriteByte(b)
		} else {
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}

// fetchFile streams the file to destination, hashing as it goes, then checks it whole.
func (s Source) fetchFile(ctx context.Context, manifest *ReleaseManifest, file *ReleaseFile, destination string, progress func(percent float64)) error {
	url := file.URL
	if !strings.HasPrefix(url, "https://") {
		url = s.Releases + "/download/v" + manifest.Version + "/" + encodeURIComponent(file.URL)
	}
	response, err := s.get(ctx, url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	hash := sha512.New()
	received := 0.0
	buffer := make([]byte, 256<<10)
	for {
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			hash.Write(buffer[:read])
			received += float64(read)
			progress(received / file.Size * 100)
			if _, err := out.Write(buffer[:read]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if received != file.Size || base64.StdEncoding.EncodeToString(hash.Sum(nil)) != file.SHA512 {
		return fmt.Errorf("%s did not match its checksum.", file.URL)
	}
	return nil
}

// Staged is a downloaded build waiting to be swapped in. Cleanup is what to remove if it never is.
type Staged struct {
	Version string
	Path    string
	Cleanup string
	// Name is the file the release published, which an AppImage named for its version takes.
	Name string
}

// Stage downloads the build beside what it replaces, so the swap is a rename on one volume.
func (s Source) Stage(ctx context.Context, target UpdateTarget, manifest *ReleaseManifest, progress func(percent float64)) (*Staged, error) {
	file := releaseFile(manifest, target, s.Arch)
	if file == nil {
		return nil, errors.New("No build for this computer.")
	}
	name := filepath.Base(file.URL)
	if target.AppImage != "" {
		destination := filepath.Join(filepath.Dir(target.AppImage), "."+filepath.Base(target.AppImage)+"."+application.NewID()+".download")
		if err := s.fetchFile(ctx, manifest, file, destination, progress); err != nil {
			os.Remove(destination)
			return nil, err
		}
		if err := os.Chmod(destination, 0o755); err != nil {
			os.Remove(destination)
			return nil, err
		}
		return &Staged{Version: manifest.Version, Path: destination, Cleanup: destination, Name: name}, nil
	}
	download, err := os.MkdirTemp("", "aic-update-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(download)
	unpacked := filepath.Join(filepath.Dir(target.Bundle), "."+filepath.Base(target.Bundle)+"."+application.NewID()+".update")
	staged, err := func() (*Staged, error) {
		archive := filepath.Join(download, "update.zip")
		if err := s.fetchFile(ctx, manifest, file, archive, progress); err != nil {
			return nil, err
		}
		if _, _, err := platform.Run(ctx, "/usr/bin/ditto", []string{"-x", "-k", archive, unpacked}, platform.RunOptions{}); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(unpacked)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".app") {
				app := filepath.Join(unpacked, entry.Name())
				if err := s.Verify(ctx, target.Bundle, app); err != nil {
					return nil, err
				}
				return &Staged{Version: manifest.Version, Path: app, Cleanup: unpacked, Name: name}, nil
			}
		}
		return nil, fmt.Errorf("%s holds no app.", file.URL)
	}()
	if err != nil {
		os.RemoveAll(unpacked)
		return nil, err
	}
	return staged, nil
}

var designated = regexp.MustCompile(`(?m)^designated => (.+)$`)

// VerifyBundle accepts only a bundle signed by whoever signed the one it replaces, as Squirrel does.
func VerifyBundle(ctx context.Context, current, next string) error {
	stdout, _, err := platform.Run(ctx, "/usr/bin/codesign", []string{"-d", "-r-", current}, platform.RunOptions{})
	if err != nil {
		return err
	}
	requirement := designated.FindStringSubmatch(string(stdout))
	if requirement == nil {
		return errors.New("The installed app has no signature to check the update against.")
	}
	_, _, err = platform.Run(ctx, "/usr/bin/codesign", []string{"--verify", "--deep", "--strict", "-R", "=" + requirement[1], next}, platform.RunOptions{})
	return err
}

var versionedName = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Swap puts the new build in place of the old one and answers where it now is. An AppImage whose
// name carries its version takes the published name beside it, as AppImageUpdater does; any other
// name the user chose is kept.
func Swap(target UpdateTarget, update *Staged) (string, error) {
	if target.AppImage != "" {
		destination := target.AppImage
		existing := filepath.Base(target.AppImage)
		if update.Name != "" && update.Name != existing && versionedName.MatchString(existing) {
			destination = filepath.Join(filepath.Dir(target.AppImage), update.Name)
		}
		if err := os.Rename(update.Path, destination); err != nil {
			return "", err
		}
		if destination != target.AppImage {
			_ = os.Remove(target.AppImage)
		}
		return destination, nil
	}
	previous := filepath.Join(update.Cleanup, "previous.app")
	if err := os.Rename(target.Bundle, previous); err != nil {
		return "", err
	}
	if err := os.Rename(update.Path, target.Bundle); err != nil {
		_ = os.Rename(previous, target.Bundle)
		return "", err
	}
	_ = os.RemoveAll(update.Cleanup)
	return target.Bundle, nil
}
