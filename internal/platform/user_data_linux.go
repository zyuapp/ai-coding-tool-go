package platform

import (
	"os"
	"path/filepath"
)

func appDataDir(home string) string {
	if config := os.Getenv("XDG_CONFIG_HOME"); config != "" {
		return config
	}
	return filepath.Join(home, ".config")
}
