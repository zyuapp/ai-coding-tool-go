package platform

import "path/filepath"

func appDataDir(home string) string {
	return filepath.Join(home, "Library", "Application Support")
}
