// Package appplugin is where the app's own plugin lives, the skills both engines qualify with its name.
package appplugin

import (
	"path/filepath"
	"strings"
	"sync"
)

// Name is the plugin name both engines qualify the app's skills with.
const Name = "aicodingtool"

var held struct {
	sync.RWMutex
	root string
}

// SetRoot is set once by the main process. Empty means no app skills.
func SetRoot(directory string) {
	held.Lock()
	held.root = directory
	held.Unlock()
}

// Root is where the plugin lives on disk, "" when there is none.
func Root() string {
	held.RLock()
	defer held.RUnlock()
	return held.root
}

// SkillsRoot is the plugin's skills folder, which Codex reads as an extra skill root; "" without a plugin.
func SkillsRoot() string {
	root := Root()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "skills")
}

// UnqualifiedSkillName reads `aicodingtool:review-thread` and `review-thread` as the same app skill.
func UnqualifiedSkillName(name string) string {
	return strings.TrimPrefix(name, Name+":")
}

// Path is where the plugin sits: beside the other resources when packaged, in the source tree in development.
func Path(packaged bool, resourcesPath, appPath string) string {
	if packaged {
		return filepath.Join(resourcesPath, "app-plugin")
	}
	return filepath.Join(appPath, "assets", "app-plugin")
}
