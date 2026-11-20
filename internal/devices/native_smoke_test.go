package devices

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/devices/speech"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// These drive the real helpers in native/bin (`go run ./native/build -onnx`). Run them on a Mac with
// Xcode, outside the sandbox:
//
//	AIC_DEVICES_NATIVE_TEST=1 go test ./internal/devices -run Native -v
//
// AIC_DEVICES_ONNX_MODELS names a folder holding the Linux model, for the ONNX helper's test, and
// AIC_DEVICES_SPEECH_WAV a recording of "Hello world, this is a test of the dictation helper." where
// there is no `say`. On Linux, the X11 tests want an X display whose active window is another program's.
func nativeTest(t *testing.T) {
	t.Helper()
	if os.Getenv("AIC_DEVICES_NATIVE_TEST") != "1" {
		t.Skip("set AIC_DEVICES_NATIVE_TEST=1 to drive the native helpers")
	}
}

// spokenWAV is macOS speaking a sentence, as base64 WAV.
func spokenWAV(t *testing.T) string {
	t.Helper()
	if named := os.Getenv("AIC_DEVICES_SPEECH_WAV"); named != "" {
		data, err := os.ReadFile(named)
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(data)
	}
	file := filepath.Join(t.TempDir(), "speech.wav")
	if err := exec.Command("say", "-o", file, "--data-format=LEI16@16000", "Hello world, this is a test of the dictation helper.").Run(); err != nil {
		t.Skip("say is not here")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func expectHeard(t *testing.T, host *DictationHost, audio string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	text, err := host.TranscribeAudio(ctx, audio)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(text), "hello world") || !strings.Contains(strings.ToLower(text), "dictation helper") {
		t.Fatalf("heard %q", text)
	}
}

func TestNativeMacDictationHelperTranscribes(t *testing.T) {
	nativeTest(t)
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Apple silicon only")
	}
	host := NewDictationHost(DictationHostOptions{Helper: DictationHelperFor(sourceNativeDir(), false, "darwin")})
	t.Cleanup(host.Stop)
	if status := host.readStatus(); status.Model != domain.DictationModelReady {
		t.Skipf("the shared model is %s", status.Model)
	}
	expectHeard(t, host, spokenWAV(t))
}

func TestNativeOnnxDictationHelperTranscribes(t *testing.T) {
	nativeTest(t)
	models := os.Getenv("AIC_DEVICES_ONNX_MODELS")
	if models == "" {
		t.Skip("set AIC_DEVICES_ONNX_MODELS to the Linux model's folder")
	}
	host := NewDictationHost(DictationHostOptions{
		Helper: DictationHelperFor(sourceNativeDir(), true, "linux"),
		Models: models,
		Place:  &speech.ModelPlace{Folder: models, Files: speech.LinuxModelFiles},
	})
	t.Cleanup(host.Stop)
	expectHeard(t, host, spokenWAV(t))
}

func TestNativeSimulatorRoundTrip(t *testing.T) {
	nativeTest(t)
	ctx := t.Context()
	host := NewSimulatorHost(SimulatorHostOptions{Helper: filepath.Join(sourceNativeDir(), "aic-simulator")})
	t.Cleanup(host.StopAtQuit)
	// Newer Xcodes keep SimulatorKit in Contents/SharedFrameworks, where the helper does not look; a
	// developer folder of links lets it load from there.
	shared := "/Applications/Xcode.app/Contents/SharedFrameworks/SimulatorKit.framework"
	if host.xcode(ctx) == "" {
		if _, err := os.Stat(shared); err == nil {
			shim := filepath.Join(t.TempDir(), "Developer")
			if err := os.MkdirAll(filepath.Join(shim, "Library", "PrivateFrameworks"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(shared, filepath.Join(shim, "Library", "PrivateFrameworks", "SimulatorKit.framework")); err != nil {
				t.Fatal(err)
			}
			host.xcodeDir = shim
			t.Logf("SimulatorKit is only in %s; the app would say to install Xcode", shared)
		}
	}
	listed, err := host.ListSimulators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := listed.Availability.(domain.SimulatorAvailabilityAvailable); !ok {
		t.Skipf("unavailable: %+v", listed.Availability)
	}
	var device *domain.SimulatorDevice
	for _, candidate := range listed.Devices {
		if strings.HasPrefix(candidate.Name, "iPhone") {
			device = candidate
			break
		}
	}
	if device == nil {
		t.Skip("no iPhone simulator")
	}
	t.Logf("using %s (%s, %s)", device.Name, device.Runtime, device.State)
	if err := host.BootSimulator(ctx, device.UDID); err != nil {
		t.Fatal(err)
	}
	var configs, chunks atomic.Int32
	stop := host.OnSimulatorFrame(func(frame domain.SimulatorFrame) {
		switch frame.(type) {
		case domain.SimulatorFrameConfig:
			configs.Add(1)
		case domain.SimulatorFrameChunk:
			chunks.Add(1)
		}
	})
	defer stop()
	if err := host.WatchSimulator(ctx, device.UDID); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && (configs.Load() == 0 || chunks.Load() == 0); {
		time.Sleep(100 * time.Millisecond)
	}
	if configs.Load() == 0 || chunks.Load() == 0 {
		t.Fatalf("frames: %d configs, %d chunks", configs.Load(), chunks.Load())
	}
	snapshot, err := host.DescribeSimulator(ctx, device.UDID, device.Name)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Width <= 0 || snapshot.Height <= 0 || len(snapshot.Elements) == 0 {
		t.Fatalf("snapshot %+v", snapshot)
	}
	t.Logf("screen %vx%v with %d elements; first %s %v", snapshot.Width, snapshot.Height, len(snapshot.Elements), snapshot.Elements[0].Role, snapshot.Elements[0].Frame)
	if err := host.SimulatorInput(ctx, device.UDID, domain.SimulatorInputTapElement{Ref: snapshot.Elements[0].Ref}); err != nil {
		t.Fatal(err)
	}
	if err := host.SimulatorInput(ctx, device.UDID, domain.SimulatorInputButton{Button: domain.SimulatorButtonHome}); err != nil {
		t.Fatal(err)
	}
	if err := host.SimulatorInput(ctx, device.UDID, domain.SimulatorInputRotate{Orientation: domain.SimulatorOrientationLandscapeLeft}); err != nil {
		t.Fatal(err)
	}
	if err := host.SimulatorInput(ctx, device.UDID, domain.SimulatorInputRotate{Orientation: domain.SimulatorOrientationPortrait}); err != nil {
		t.Fatal(err)
	}
	shot, err := host.CaptureSimulator(ctx, device.UDID)
	if err != nil {
		t.Fatal(err)
	}
	if png, err := os.ReadFile(shot.Path); err != nil || len(png) < 8 || string(png[1:4]) != "PNG" || shot.Width <= 0 {
		t.Fatalf("shot %+v: %v", shot, err)
	}
	if err := host.UnwatchSimulator(ctx, device.UDID); err != nil {
		t.Fatal(err)
	}
	if err := host.ShutdownSimulator(ctx, device.UDID); err != nil {
		t.Fatal(err)
	}
}
