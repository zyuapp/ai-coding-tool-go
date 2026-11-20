package agent

import (
	"path/filepath"
	"strings"
)

// AppPluginName is the plugin name the engines qualify the app's skills with.
const AppPluginName = "aicodingtool"

// AppSkillsRoot is the skills folder of the app's plugin at pluginRoot, or "" when the app has no plugin.
func AppSkillsRoot(pluginRoot string) string {
	if pluginRoot == "" {
		return ""
	}
	return filepath.Join(pluginRoot, "skills")
}

// UnqualifiedSkillName reads `aicodingtool:review-thread` and `review-thread` as the same app skill.
func UnqualifiedSkillName(name string) string {
	return strings.TrimPrefix(name, AppPluginName+":")
}
