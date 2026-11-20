package appplugin

import "testing"

func TestThePluginSitsWithTheResourcesOrInTheSourceTree(t *testing.T) {
	if Path(true, "/App/Resources", "/src") != "/App/Resources/app-plugin" || Path(false, "/App/Resources", "/src") != "/src/assets/app-plugin" {
		t.Fatal("paths")
	}
	if UnqualifiedSkillName("aicodingtool:review-thread") != "review-thread" || UnqualifiedSkillName("review-thread") != "review-thread" {
		t.Fatal("names")
	}
	SetRoot("")
	if SkillsRoot() != "" {
		t.Fatal("no plugin, no skills")
	}
	SetRoot("/plugin")
	defer SetRoot("")
	if SkillsRoot() != "/plugin/skills" {
		t.Fatal(SkillsRoot())
	}
}
