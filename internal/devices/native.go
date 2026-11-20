// Package devices is the app's reach into the machine beyond its own window: the iOS simulator panel,
// holding to talk, Liquid Glass under the window, window screenshots with what their apps said through
// accessibility, the flash that marks a capture, and the desktop-wide capture shortcut.
package devices

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// NativeDirEnv names a folder that overrides where the native helpers are.
const NativeDirEnv = "ACT_NATIVE_DIR"

// sourceNativeDir is native/bin in this checkout, where `go run ./native/build` writes.
func sourceNativeDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "native", "bin")
}

// NativeDir is the folder holding the native helpers: ACT_NATIVE_DIR when set; in an installed app
// Contents/Resources/native on macOS and native beside the executable on Linux; in a source build,
// the checkout's native/bin.
func NativeDir() string {
	if named := os.Getenv(NativeDirEnv); named != "" {
		return named
	}
	if platform.Packaged() {
		if executable, err := os.Executable(); err == nil {
			if resolved, err := filepath.EvalSymlinks(executable); err == nil {
				executable = resolved
			}
			return packagedNativeDir(filepath.Dir(executable))
		}
	}
	return filepath.Clean(sourceNativeDir())
}
