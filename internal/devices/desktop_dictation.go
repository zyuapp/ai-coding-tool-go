package devices

import (
	"context"
	"os"
	"runtime"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// devModelsEnv lets a source build point at another model folder, to try the download without touching the shared one.
const devModelsEnv = "AIC_DICTATION_MODELS"

var desktopDictation struct {
	sync.Mutex
	host *DictationHost
}

// DesktopDictation is the window's dictation, which asks macOS for the microphone in the app's own
// name. Linux has no such prompt. One host serves the whole process.
func DesktopDictation() *DictationHost {
	desktopDictation.Lock()
	defer desktopDictation.Unlock()
	if desktopDictation.host == nil {
		packaged := platform.Packaged()
		options := DictationHostOptions{
			Helper: DictationHelperFor(NativeDir(), packaged, runtime.GOOS),
			Microphone: func(ctx context.Context) bool {
				if !platform.AsksMicrophoneAccess {
					return true
				}
				return microphoneAccess(ctx)
			},
		}
		if !packaged {
			options.Models = os.Getenv(devModelsEnv)
		}
		desktopDictation.host = NewDictationHost(options)
	}
	return desktopDictation.host
}

// StopDesktopDictation stops the helper, if one was ever started.
func StopDesktopDictation() {
	desktopDictation.Lock()
	host := desktopDictation.host
	desktopDictation.Unlock()
	if host != nil {
		host.Stop()
	}
}

// StartDesktopDictationLevels sends the hold's microphone level to the window as it changes
// (`dictation:level`), until the returned func is called.
func StartDesktopDictationLevels(send func(level float64)) func() {
	return DesktopDictation().OnDictationLevel(send)
}
