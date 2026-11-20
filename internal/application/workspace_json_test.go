package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func TestSetsTravelAsTaggedObjectsAndComeBackAsSets(t *testing.T) {
	type shape struct {
		Running jsonx.Set[string]                `json:"running"`
		Result  contracts.WorkspaceCommandResult `json:"result"`
	}
	value := shape{Running: jsonx.SetOf("b", "a"), Result: contracts.WorkspaceCommandResultFalse{Message: "no"}}
	encoded, err := StringifyWorkspaceJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"running":{"$set":["a","b"]},"result":{"ok":false,"message":"no"}}` {
		t.Fatalf("encoded %s", encoded)
	}
	var decoded shape
	if err := ParseWorkspaceJSON(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Running.Has("a") || !decoded.Running.Has("b") || len(decoded.Running) != 2 {
		t.Fatalf("decoded %v", decoded.Running)
	}
	if failed, ok := decoded.Result.(contracts.WorkspaceCommandResultFalse); !ok || failed.Message != "no" {
		t.Fatal("a union did not decode beside the set")
	}
	if err := ParseWorkspaceJSON([]byte(`{"running":{"other":[]}}`), &decoded); err == nil {
		t.Fatal("an object that is no set decoded as one")
	}
}
