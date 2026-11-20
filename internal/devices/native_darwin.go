package devices

import "path/filepath"

func packagedNativeDir(executableDir string) string {
	return filepath.Join(executableDir, "..", "Resources", "native")
}
