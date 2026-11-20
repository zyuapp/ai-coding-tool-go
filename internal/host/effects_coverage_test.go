package host

import (
	"os"
	"regexp"
	"testing"
)

// effectNames reads the effect types the WorkspaceEffect union registers.
func effectNames(t *testing.T) map[string]bool {
	source, err := os.ReadFile("../application/workspace_reducer_types_types.go")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)RegisterUnion\[WorkspaceEffect\]\("type", map\[any\]WorkspaceEffect\{(.*?)\n\t\}\)`).FindSubmatch(source)
	if block == nil {
		t.Fatal("no WorkspaceEffect union")
	}
	names := map[string]bool{}
	for _, match := range regexp.MustCompile(`"([^"]+)":`).FindAllSubmatch(block[1], -1) {
		names[string(match[1])] = true
	}
	return names
}

// Every effect the reducer can describe has the one handler that carries it out, and no handler waits for an effect that does not exist.
func TestEveryEffectHasAHandler(t *testing.T) {
	names := effectNames(t)
	for name := range names {
		if HostModulesEffectHandlers[name] == nil {
			t.Errorf("no handler for %q", name)
		}
	}
	for name := range HostModulesEffectHandlers {
		if !names[name] {
			t.Errorf("handler for unknown effect %q", name)
		}
	}
}
