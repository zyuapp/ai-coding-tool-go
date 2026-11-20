package devices

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"os/exec"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestNativeX11CaptureReadsTheActiveWindowsPixels(t *testing.T) {
	nativeTest(t)
	capture := NewWindowCapture(NewScreenshotContexts(func() string { return "" }))
	shot := capture.CaptureFrontmostWindow(t.Context(), false)
	if shot.Status != WindowShotCaptured {
		t.Fatalf("got %+v", shot)
	}
	data, err := base64.StdEncoding.DecodeString(shot.PNG)
	if err != nil {
		t.Fatal(err)
	}
	picture, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("captured %q (%s), %v, context %+v", shot.Title, shot.App, picture.Bounds(), shot.Context.Accessibility)
	if picture.Bounds().Dx() < 10 || picture.Bounds().Dy() < 10 || shot.Context.Platform != domain.ScreenshotPlatformLinuxX11 {
		t.Fatalf("picture %v", picture.Bounds())
	}
}

func TestNativeX11HotkeyFires(t *testing.T) {
	nativeTest(t)
	if _, err := exec.LookPath("xdotool"); err != nil {
		t.Skip("xdotool is not here")
	}
	hotkeys := newPlatformHotkeys()
	fired := make(chan struct{}, 1)
	if !hotkeys.Register(domain.Keystroke{Key: "S", Alt: true, Shift: true}, func() { fired <- struct{}{} }) {
		t.Fatal("refused")
	}
	defer hotkeys.UnregisterAll()
	if out, err := exec.Command("xdotool", "key", "alt+shift+s").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the hotkey did not fire")
	}
	other := newPlatformHotkeys()
	if other.Register(domain.Keystroke{Key: "S", Alt: true, Shift: true}, func() {}) {
		t.Fatal("a key another client holds was granted")
	}
}
