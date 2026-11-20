package updates

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const launchedVersion = "launched-version"

// windowStorageFile tells a profile that launched before the app kept its record from a new one.
const windowStorageFile = "window.v1.json"

// ReadAppLaunch is the version this launch updated the app to, or nil when it is not the first launch
// since an update. Read once at startup, before the window's storage is first written.
func ReadAppLaunch(userData, version string) *string {
	record := filepath.Join(userData, launchedVersion)
	previous := ""
	if data, err := os.ReadFile(record); err == nil {
		previous = strings.TrimSpace(string(data))
	}
	if previous == version {
		return nil
	}
	var updated bool
	if previous == "" {
		_, err := os.Stat(filepath.Join(userData, windowStorageFile))
		updated = err == nil
	} else {
		// Going back to an older build is not an update, so it is recorded without a word.
		updated = domain.CompareVersions(version, previous) > 0
	}
	_ = os.WriteFile(record, []byte(version), 0o666)
	if !updated {
		return nil
	}
	return &version
}
