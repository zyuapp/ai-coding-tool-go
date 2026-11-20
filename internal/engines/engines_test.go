package engines

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestEveryEngineIsRegistered(t *testing.T) {
	registry := New("")
	for _, engine := range domain.AgentEngines {
		module := registry.Modules[engine]
		if module == nil || module.Services() == nil || module.Binary().Command == "" {
			t.Fatalf("%s is not registered whole", engine)
		}
	}
	if len(registry.Services()) != len(domain.AgentEngines) {
		t.Fatal("a service per engine")
	}
}
