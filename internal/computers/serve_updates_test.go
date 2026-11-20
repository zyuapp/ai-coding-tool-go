package computers

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const testReleases = "https://releases.test"

type fakeRelease struct {
	mu      sync.Mutex
	fetched []string
	fetch   func(ctx context.Context, url string) (*http.Response, error)
}

func respond(status int, body io.Reader) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(body)}
}

func release(name string, body []byte, sha string) *fakeRelease {
	if sha == "" {
		sum := sha512.Sum512(body)
		sha = base64.StdEncoding.EncodeToString(sum[:])
	}
	manifest := "version: 0.2.0\nfiles:\n  - url: " + name + "\n    sha512: " + sha + "\n    size: " + strconv.Itoa(len(body)) + "\npath: " + name + "\nsha512: " + sha + "\nreleaseDate: '2026-11-10T00:00:00.000Z'\n"
	fake := &fakeRelease{}
	fake.fetch = func(_ context.Context, url string) (*http.Response, error) {
		fake.mu.Lock()
		fake.fetched = append(fake.fetched, url)
		fake.mu.Unlock()
		if strings.HasSuffix(url, ".yml") {
			return respond(200, strings.NewReader(manifest)), nil
		}
		if url == testReleases+"/download/v0.2.0/"+name {
			return respond(200, bytes.NewReader(body)), nil
		}
		return respond(404, strings.NewReader("missing")), nil
	}
	return fake
}

type updaterRun struct {
	updates  *ServeUpdater
	mu       sync.Mutex
	states   []domain.AppUpdate
	restarts int
}

func updater(target UpdateTarget, fetch func(context.Context, string) (*http.Response, error), goos string) *updaterRun {
	run := &updaterRun{}
	run.updates = NewServeUpdater(ServeUpdaterOptions{
		Version:      "0.1.0",
		Target:       target,
		GOOS:         goos,
		GOARCH:       "amd64",
		Releases:     testReleases,
		Fetch:        fetch,
		VerifyBundle: func(context.Context, string, string) error { return nil },
		OnState: func(update domain.AppUpdate) {
			run.mu.Lock()
			defer run.mu.Unlock()
			run.states = append(run.states, update)
		},
		Restart: func(string) {
			run.mu.Lock()
			defer run.mu.Unlock()
			run.restarts++
		},
	})
	return run
}

func names(t *testing.T, folder string) []string {
	t.Helper()
	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, entry := range entries {
		found = append(found, entry.Name())
	}
	return found
}

func TestServerReplacesItsAppImageWithTheCheckedDownload(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "AICodingTool-0.1.0-x86_64.AppImage")
	_ = os.WriteFile(image, []byte("old build"), 0o755)
	fake := release("AICodingTool-0.2.0-x86_64.AppImage", []byte("new build"), "")
	run := updater(UpdateTarget{Kind: UpdateTargetAppImage, Image: image}, fake.fetch, "linux")

	run.updates.Check()
	if !strings.HasSuffix(fake.fetched[0], "/latest/download/latest-linux.yml") {
		t.Fatal(fake.fetched)
	}
	if state := run.updates.State(); state != (domain.AppUpdateAvailable{Version: "0.2.0"}) {
		t.Fatal(state)
	}
	run.updates.Download()
	if !slices.ContainsFunc(run.states, func(state domain.AppUpdate) bool {
		downloading, ok := state.(domain.AppUpdateDownloading)
		return ok && downloading.Percent == 100
	}) {
		t.Fatal(run.states)
	}
	if state := run.updates.State(); state != (domain.AppUpdateReady{Version: "0.2.0"}) {
		t.Fatal(state)
	}
	run.updates.Install()
	if run.restarts != 1 {
		t.Fatal(run.restarts)
	}
	if found := names(t, root); !slices.Equal(found, []string{"AICodingTool-0.1.0-x86_64.AppImage"}) {
		t.Fatal(found)
	}
	if data, _ := os.ReadFile(image); string(data) != "new build" {
		t.Fatal(string(data))
	}
}

func TestDownloadThatDoesNotMatchItsChecksumIsThrownAway(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "AICodingTool.AppImage")
	_ = os.WriteFile(image, []byte("old build"), 0o755)
	sum := sha512.Sum512([]byte("new build"))
	fake := release("AICodingTool-0.2.0-x86_64.AppImage", []byte("tampered"), base64.StdEncoding.EncodeToString(sum[:]))
	run := updater(UpdateTarget{Kind: UpdateTargetAppImage, Image: image}, fake.fetch, "linux")
	run.updates.Check()
	run.updates.Download()
	run.updates.Install()
	if state := run.updates.State(); state != (domain.AppUpdateAvailable{Version: "0.2.0"}) {
		t.Fatal(state)
	}
	if run.restarts != 0 {
		t.Fatal(run.restarts)
	}
	if found := names(t, root); !slices.Equal(found, []string{"AICodingTool.AppImage"}) {
		t.Fatal(found)
	}
	if data, _ := os.ReadFile(image); string(data) != "old build" {
		t.Fatal(string(data))
	}
}

// stalledBody sends one byte, then nothing until the request is cancelled.
type stalledBody struct {
	ctx  context.Context
	sent bool
}

func (b *stalledBody) Read(buffer []byte) (int, error) {
	if !b.sent {
		b.sent = true
		buffer[0] = 1
		return 1, nil
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *stalledBody) Close() error { return nil }

func TestStoppingCancelsADownloadAndLeavesNothingBehind(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "AICodingTool.AppImage")
	_ = os.WriteFile(image, []byte("old build"), 0o755)
	released := release("AICodingTool-0.2.0-x86_64.AppImage", []byte("new build"), "")
	stalled := func(ctx context.Context, url string) (*http.Response, error) {
		if strings.HasSuffix(url, ".yml") {
			return released.fetch(ctx, url)
		}
		return &http.Response{StatusCode: 200, Body: &stalledBody{ctx: ctx}}, nil
	}
	run := updater(UpdateTarget{Kind: UpdateTargetAppImage, Image: image}, stalled, "linux")
	run.updates.Check()
	downloading := make(chan struct{})
	go func() {
		run.updates.Download()
		close(downloading)
	}()
	for {
		if _, ok := run.updates.State().(domain.AppUpdateDownloading); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	run.updates.Dispose()
	<-downloading
	if found := names(t, root); !slices.Equal(found, []string{"AICodingTool.AppImage"}) {
		t.Fatal(found)
	}
	if data, _ := os.ReadFile(image); string(data) != "old build" {
		t.Fatal(string(data))
	}
}

func TestMacServerSwapsItsBundleForTheOneInTheArchive(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs ditto")
	}
	root := t.TempDir()
	bundle := filepath.Join(root, "Applications", "AI Coding Tool.app")
	binary := filepath.Join(bundle, "Contents", "MacOS", "AI Coding Tool")
	_ = os.MkdirAll(filepath.Dir(binary), 0o755)
	_ = os.WriteFile(binary, []byte("old build"), 0o755)
	built := filepath.Join(root, "build", "AI Coding Tool.app", "Contents", "MacOS", "AI Coding Tool")
	_ = os.MkdirAll(filepath.Dir(built), 0o755)
	_ = os.WriteFile(built, []byte("new build"), 0o755)
	archive := filepath.Join(root, "update.zip")
	if output, err := exec.Command("/usr/bin/ditto", "-c", "-k", "--keepParent", filepath.Join(root, "build", "AI Coding Tool.app"), archive).CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	body, _ := os.ReadFile(archive)
	fake := release("AICodingTool-0.2.0-arm64.zip", body, "")
	run := updater(UpdateTarget{Kind: UpdateTargetBundle, Bundle: bundle}, fake.fetch, "darwin")
	run.updates.Check()
	if !strings.HasSuffix(fake.fetched[0], "/latest-mac.yml") {
		t.Fatal(fake.fetched)
	}
	run.updates.Download()
	run.updates.Install()
	if run.restarts != 1 {
		t.Fatal(run.restarts, run.updates.State())
	}
	if data, _ := os.ReadFile(binary); string(data) != "new build" {
		t.Fatal(string(data))
	}
	if found := names(t, filepath.Dir(bundle)); !slices.Equal(found, []string{"AI Coding Tool.app"}) {
		t.Fatal(found)
	}
}

func TestReleaseManifestAndTarget(t *testing.T) {
	manifest := ParseReleaseManifest("version: '1.2.3'\nfiles:\n  - url: a.zip\n    sha512: \"abc\"\n    size: 10\n  - url: b.AppImage\n    sha512: def\n  - url: c.AppImage\n    size: x\n    sha512: ghi\nother: 1\n    size: 5\n")
	if manifest == nil || manifest.Version != "1.2.3" || len(manifest.files) != 1 || manifest.files[0] != (releaseFile{url: "a.zip", sha512: "abc", size: 10}) {
		t.Fatalf("%#v", manifest)
	}
	if ParseReleaseManifest("files: []\n") != nil {
		t.Fatal("a manifest with no version")
	}
	if target := UpdateTargetFor("darwin", "/Applications/AI Coding Tool.app/Contents/MacOS/AI Coding Tool", os.Getenv); target == nil || target.Bundle != "/Applications/AI Coding Tool.app" {
		t.Fatal(target)
	}
	if UpdateTargetFor("darwin", "/usr/local/bin/aic", os.Getenv) != nil {
		t.Fatal("a loose binary is no bundle")
	}
	environment := func(key string) string { return map[string]string{"APPIMAGE": "/opt/AI.AppImage"}[key] }
	if target := UpdateTargetFor("linux", "/tmp/.mount/aic", environment); target == nil || target.Image != "/opt/AI.AppImage" {
		t.Fatal(target)
	}
	if manifestName("linux", "arm64") != "latest-linux-arm64.yml" || manifestName("linux", "amd64") != "latest-linux.yml" {
		t.Fatal("manifest names")
	}
	if encodeURIComponent("AI Coding Tool (1)+!.zip") != "AI%20Coding%20Tool%20(1)%2B!.zip" {
		t.Fatal(encodeURIComponent("AI Coding Tool (1)+!.zip"))
	}
}
