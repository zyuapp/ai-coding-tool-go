package computers

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

const (
	releasesURL   = "https://github.com/zyuapp/ai-coding-tool-go/releases"
	checkInterval = time.Hour
)

// UpdateTargetKind is what an update replaces.
type UpdateTargetKind string

const (
	UpdateTargetBundle   UpdateTargetKind = "bundle"
	UpdateTargetAppImage UpdateTargetKind = "appimage"
)

// UpdateTarget is what an update replaces: the macOS bundle the binary sits in, or the AppImage that holds it.
type UpdateTarget struct {
	Kind UpdateTargetKind
	// Bundle is the .app for a bundle; Image the AppImage file for an AppImage.
	Bundle string
	Image  string
}

// UpdateTargetFor is what this executable's update replaces, or nil where it cannot replace itself.
// environment reads a variable, as os.Getenv does.
func UpdateTargetFor(goos, executable string, environment func(string) string) *UpdateTarget {
	switch goos {
	case "darwin":
		bundle := filepath.Clean(filepath.Join(executable, "..", "..", ".."))
		if strings.HasSuffix(bundle, ".app") {
			return &UpdateTarget{Kind: UpdateTargetBundle, Bundle: bundle}
		}
	case "linux":
		if image := environment("APPIMAGE"); image != "" {
			return &UpdateTarget{Kind: UpdateTargetAppImage, Image: image}
		}
	}
	return nil
}

type releaseFile struct {
	url    string
	sha512 string
	size   float64
}

// ReleaseManifest is what a `latest-*.yml` electron-builder publishes says: its version and the files it lists.
type ReleaseManifest struct {
	Version string
	files   []releaseFile
}

var (
	manifestVersion = regexp.MustCompile(`(?m)^version:\s*(\S+)\s*$`)
	manifestItem    = regexp.MustCompile(`^\s+-\s+url:\s*(\S+)\s*$`)
	manifestField   = regexp.MustCompile(`^\s+(sha512|size):\s*(\S+)\s*$`)
	quoted          = regexp.MustCompile(`^(['"])(.*)(['"])$`)
	lineBreak       = regexp.MustCompile(`\r?\n`)
	leadingSpace    = regexp.MustCompile(`^\s`)
)

func unquote(value string) string {
	if match := quoted.FindStringSubmatch(value); match != nil && match[1] == match[3] {
		return match[2]
	}
	return value
}

// ParseReleaseManifest reads a `latest-*.yml`, or nil when it names no version.
func ParseReleaseManifest(text string) *ReleaseManifest {
	version := manifestVersion.FindStringSubmatch(text)
	if version == nil {
		return nil
	}
	type partial struct {
		url, sha512 string
		size        *float64
	}
	var files []*partial
	var current *partial
	for _, line := range lineBreak.Split(text, -1) {
		if item := manifestItem.FindStringSubmatch(line); item != nil {
			current = &partial{url: unquote(item[1])}
			files = append(files, current)
			continue
		}
		field := manifestField.FindStringSubmatch(line)
		switch {
		case field != nil && current != nil:
			if field[1] == "size" {
				size, err := strconv.ParseFloat(field[2], 64)
				if err != nil {
					size = math.NaN()
				}
				current.size = &size
			} else {
				current.sha512 = unquote(field[2])
			}
		case field == nil && !leadingSpace.MatchString(line):
			current = nil
		}
	}
	manifest := &ReleaseManifest{Version: unquote(version[1]), files: []releaseFile{}}
	for _, file := range files {
		if file.url != "" && file.sha512 != "" && file.size != nil && !math.IsNaN(*file.size) && !math.IsInf(*file.size, 0) {
			manifest.files = append(manifest.files, releaseFile{url: file.url, sha512: file.sha512, size: *file.size})
		}
	}
	return manifest
}

func manifestName(goos, goarch string) string {
	if goos == "darwin" {
		return "latest-mac.yml"
	}
	if goarch == "arm64" {
		return "latest-linux-arm64.yml"
	}
	return "latest-linux.yml"
}

func (m *ReleaseManifest) file(target UpdateTarget) *releaseFile {
	suffix := ".AppImage"
	if target.Kind == UpdateTargetBundle {
		suffix = ".zip"
	}
	for index := range m.files {
		if strings.HasSuffix(m.files[index].url, suffix) {
			return &m.files[index]
		}
	}
	return nil
}

// VerifyBundle accepts only a bundle signed by whoever signed the one it replaces, as Squirrel does
// for the desktop app.
func VerifyBundle(ctx context.Context, current, next string) error {
	stdout, _, err := platform.Run(ctx, "/usr/bin/codesign", []string{"-d", "-r-", current}, platform.RunOptions{})
	if err != nil {
		return err
	}
	requirement := regexp.MustCompile(`(?m)^designated => (.+)$`).FindStringSubmatch(string(stdout))
	if requirement == nil {
		return errors.New("The installed app has no signature to check the update against.")
	}
	_, _, err = platform.Run(ctx, "/usr/bin/codesign", []string{"--verify", "--deep", "--strict", "-R", "=" + requirement[1], next}, platform.RunOptions{})
	return err
}

type staged struct {
	version string
	path    string
	cleanup string
}

// ServeUpdaterOptions describe a headless server's updates.
type ServeUpdaterOptions struct {
	Version string
	Target  UpdateTarget
	// GOOS and GOARCH name the build to fetch; this process's own when empty.
	GOOS, GOARCH string
	Releases     string
	// Fetch answers a GET; http.DefaultClient when nil.
	Fetch        func(ctx context.Context, url string) (*http.Response, error)
	VerifyBundle func(ctx context.Context, current, next string) error
	OnState      func(update domain.AppUpdate)
	// Restart is told the version just put in place.
	Restart func(version string)
	Say     func(line string)
}

// ServeUpdater updates a headless server without Electron: finds the newest release, downloads and
// checks it, and swaps it in place of the installed app. Restart is told once the new build is in place.
type ServeUpdater struct {
	options  ServeUpdaterOptions
	manifest string
	ctx      context.Context
	cancel   context.CancelFunc

	mu         sync.Mutex
	state      domain.AppUpdate
	found      *ReleaseManifest
	staged     *staged
	checking   chan struct{}
	installing bool
	disposed   bool
	// Downloads and installs under way, which disposal waits out so it never removes what they are moving.
	pending sync.WaitGroup
}

func NewServeUpdater(options ServeUpdaterOptions) *ServeUpdater {
	if options.Releases == "" {
		options.Releases = releasesURL
	}
	if options.Fetch == nil {
		options.Fetch = func(ctx context.Context, url string) (*http.Response, error) {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return nil, err
			}
			return http.DefaultClient.Do(request)
		}
	}
	if options.VerifyBundle == nil {
		options.VerifyBundle = VerifyBundle
	}
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.Say == nil {
		options.Say = func(string) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ServeUpdater{options: options, manifest: manifestName(options.GOOS, options.GOARCH), ctx: ctx, cancel: cancel, state: domain.NoAppUpdate}
}

// State is where a newer build stands.
func (u *ServeUpdater) State() domain.AppUpdate {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state
}

// setState must be called with mu held.
func (u *ServeUpdater) setState(next domain.AppUpdate) {
	u.state = next
	if u.options.OnState != nil {
		u.options.OnState(next)
	}
}

// holding: a build on its way or in hand is not given up for whatever a later check finds.
func (u *ServeUpdater) holding() bool {
	switch u.state.(type) {
	case domain.AppUpdateDownloading, domain.AppUpdateReady:
		return true
	}
	return false
}

func (u *ServeUpdater) fetch(url string) (*http.Response, error) {
	response, err := u.options.Fetch(u.ctx, url)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		response.Body.Close()
		return nil, fmt.Errorf("%s answered %d.", url, response.StatusCode)
	}
	return response, nil
}

func (u *ServeUpdater) fetchManifest() (*ReleaseManifest, error) {
	response, err := u.fetch(u.options.Releases + "/latest/download/" + u.manifest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	text, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	manifest := ParseReleaseManifest(string(text))
	if manifest == nil || manifest.file(u.options.Target) == nil {
		return nil, errors.New(u.manifest + " names no build for this computer.")
	}
	return manifest, nil
}

// Check looks for a newer release. Checks asked while one runs share it.
func (u *ServeUpdater) Check() {
	u.mu.Lock()
	if u.holding() {
		u.mu.Unlock()
		return
	}
	if u.checking != nil {
		done := u.checking
		u.mu.Unlock()
		<-done
		return
	}
	done := make(chan struct{})
	u.checking = done
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.checking = nil
		u.mu.Unlock()
		close(done)
	}()
	manifest, err := u.fetchManifest()
	if err != nil {
		u.options.Say("Update check failed: " + err.Error())
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.holding() {
		return
	}
	if domain.CompareVersions(manifest.Version, u.options.Version) <= 0 {
		u.found = nil
		if _, idle := u.state.(domain.AppUpdateIdle); !idle {
			u.setState(domain.NoAppUpdate)
		}
		return
	}
	u.found = manifest
	if available, ok := u.state.(domain.AppUpdateAvailable); !ok || available.Version != manifest.Version {
		u.setState(domain.AppUpdateAvailable{Version: manifest.Version})
	}
}

// Download fetches and checks the build found, beside what it replaces.
func (u *ServeUpdater) Download() {
	u.mu.Lock()
	manifest := u.found
	if _, available := u.state.(domain.AppUpdateAvailable); !available || manifest == nil || u.disposed {
		u.mu.Unlock()
		return
	}
	u.pending.Add(1)
	defer u.pending.Done()
	u.setState(domain.AppUpdateDownloading{Version: manifest.Version, Percent: 0})
	u.mu.Unlock()
	u.options.Say("Downloading AI Coding Tool " + manifest.Version + "…")
	update, err := u.stage(manifest, func(percent float64) {
		u.mu.Lock()
		defer u.mu.Unlock()
		if downloading, ok := u.state.(domain.AppUpdateDownloading); ok && percent != downloading.Percent {
			downloading.Percent = percent
			u.setState(downloading)
		}
	})
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		u.options.Say("Could not download " + manifest.Version + ": " + err.Error())
		u.setState(domain.AppUpdateAvailable{Version: manifest.Version})
		return
	}
	u.staged = update
	u.setState(domain.AppUpdateReady{Version: manifest.Version})
}

// Install puts the downloaded build in place of the installed one, then tells Restart.
func (u *ServeUpdater) Install() {
	u.mu.Lock()
	update := u.staged
	if _, ready := u.state.(domain.AppUpdateReady); !ready || update == nil || u.installing || u.disposed {
		u.mu.Unlock()
		return
	}
	u.installing = true
	u.pending.Add(1)
	defer u.pending.Done()
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.installing = false
		u.mu.Unlock()
	}()
	u.options.Say("Installing AI Coding Tool " + update.version + "…")
	if err := swap(u.options.Target, update); err != nil {
		u.options.Say("Could not install " + update.version + ": " + err.Error())
		_ = os.RemoveAll(update.cleanup)
		u.mu.Lock()
		u.staged = nil
		u.setState(domain.AppUpdateAvailable{Version: update.version})
		u.mu.Unlock()
		return
	}
	u.mu.Lock()
	u.staged = nil
	u.mu.Unlock()
	u.options.Say("Installed " + update.version + ". Restarting…")
	if u.options.Restart != nil {
		u.options.Restart(update.version)
	}
}

// Start checks now and every hour after, like the desktop app, until the returned stop is called.
func (u *ServeUpdater) Start() (stop func()) {
	go u.Check()
	ticker := time.NewTicker(checkInterval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				go u.Check()
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			ticker.Stop()
			close(done)
		})
	}
}

// Dispose cancels a download and waits out an install. A build nobody installed is left for the
// next check to fetch again.
func (u *ServeUpdater) Dispose() {
	u.mu.Lock()
	u.disposed = true
	u.mu.Unlock()
	u.cancel()
	u.pending.Wait()
	u.mu.Lock()
	update := u.staged
	u.staged = nil
	u.mu.Unlock()
	if update != nil {
		_ = os.RemoveAll(update.cleanup)
	}
}

// fetchFile streams the file to destination, hashing as it goes, then checks it whole.
func (u *ServeUpdater) fetchFile(manifest *ReleaseManifest, file *releaseFile, destination string, progress func(float64)) error {
	location := file.url
	if !strings.HasPrefix(location, "https://") {
		location = u.options.Releases + "/download/v" + manifest.Version + "/" + encodeURIComponent(file.url)
	}
	response, err := u.fetch(location)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	hash := sha512.New()
	received := 0.0
	buffer := make([]byte, 256*1024)
	for {
		if err := u.ctx.Err(); err != nil {
			out.Close()
			return errors.New("This operation was aborted")
		}
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			hash.Write(buffer[:read])
			received += float64(read)
			progress(math.Min(100, math.Floor(received/file.size*100)))
			if _, err := out.Write(buffer[:read]); err != nil {
				out.Close()
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			if u.ctx.Err() != nil {
				return errors.New("This operation was aborted")
			}
			return readErr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if received != file.size || base64.StdEncoding.EncodeToString(hash.Sum(nil)) != file.sha512 {
		return errors.New(file.url + " did not match its checksum.")
	}
	return nil
}

// stage downloads the build beside what it replaces, so the swap is a rename on one volume.
func (u *ServeUpdater) stage(manifest *ReleaseManifest, progress func(float64)) (*staged, error) {
	target := u.options.Target
	file := manifest.file(target)
	if target.Kind == UpdateTargetAppImage {
		destination := filepath.Join(filepath.Dir(target.Image), "."+filepath.Base(target.Image)+"."+application.NewID()+".download")
		if err := u.fetchFile(manifest, file, destination, progress); err != nil {
			_ = os.RemoveAll(destination)
			return nil, err
		}
		if err := os.Chmod(destination, 0o755); err != nil {
			_ = os.RemoveAll(destination)
			return nil, err
		}
		return &staged{version: manifest.Version, path: destination, cleanup: destination}, nil
	}
	download, err := os.MkdirTemp("", "aic-update-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(download)
	unpacked := filepath.Join(filepath.Dir(target.Bundle), "."+filepath.Base(target.Bundle)+"."+application.NewID()+".update")
	fail := func(err error) (*staged, error) {
		_ = os.RemoveAll(unpacked)
		return nil, err
	}
	archive := filepath.Join(download, "update.zip")
	if err := u.fetchFile(manifest, file, archive, progress); err != nil {
		return fail(err)
	}
	if _, _, err := platform.Run(u.ctx, "/usr/bin/ditto", []string{"-x", "-k", archive, unpacked}, platform.RunOptions{}); err != nil {
		return fail(err)
	}
	entries, err := os.ReadDir(unpacked)
	if err != nil {
		return fail(err)
	}
	bundle := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".app") {
			bundle = entry.Name()
			break
		}
	}
	if bundle == "" {
		return fail(errors.New(file.url + " holds no app."))
	}
	if err := u.options.VerifyBundle(u.ctx, target.Bundle, filepath.Join(unpacked, bundle)); err != nil {
		return fail(err)
	}
	return &staged{version: manifest.Version, path: filepath.Join(unpacked, bundle), cleanup: unpacked}, nil
}

// swap puts the new build at the path the old one held, which the `aic` script and desktop entry name.
func swap(target UpdateTarget, update *staged) error {
	if target.Kind == UpdateTargetAppImage {
		return os.Rename(update.path, target.Image)
	}
	previous := filepath.Join(update.cleanup, "previous.app")
	if err := os.Rename(target.Bundle, previous); err != nil {
		return err
	}
	if err := os.Rename(update.path, target.Bundle); err != nil {
		_ = os.Rename(previous, target.Bundle)
		return err
	}
	_ = os.RemoveAll(update.cleanup)
	return nil
}

// encodeURIComponent escapes as JavaScript's encodeURIComponent does.
func encodeURIComponent(text string) string {
	escaped := url.QueryEscape(text)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	for _, kept := range []string{"!", "'", "(", ")", "*"} {
		escaped = strings.ReplaceAll(escaped, url.QueryEscape(kept), kept)
	}
	return escaped
}
