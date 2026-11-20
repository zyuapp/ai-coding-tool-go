package contracts

import (
	"slices"
	"testing"
)

func TestPairedThreadQueriesAreBoundedAndReadOnly(t *testing.T) {
	restExpect(t, "IsComputerQuery", IsComputerQuery, restObj(t, `{"kind":"thread-read","threadId":"thread","limit":30}`), true)
	restExpect(t, "IsComputerQuery", IsComputerQuery, restObj(t, `{"kind":"thread-list","project":"all","search":"needle","limit":20}`), true)
	for _, query := range []string{
		`{"kind":"thread-read","threadId":"","limit":30}`,
		`{"kind":"thread-read","threadId":"thread","limit":201}`,
		`{"kind":"thread-list","search":"` + restRepeat("a", 1_001) + `"}`,
		`{"kind":"thread-list","limit":-1}`,
		`{"kind":"thread-list","limit":0.5}`,
		`{"kind":"thread-list","archived":"yes"}`,
		`{"kind":"thread-list","archived":null}`,
		`{"kind":"read-file","path":"/etc/passwd"}`,
		`{"kind":"toString"}`,
	} {
		restExpect(t, "IsComputerQuery", IsComputerQuery, restObj(t, query), false)
	}
}

func TestAttachmentQueriesNameASavedImageAndNothingElse(t *testing.T) {
	restExpect(t, "IsComputerQuery", IsComputerQuery, restObj(t, `{"kind":"attachment","name":"0b3c-AF12.png"}`), true)
	for _, invalid := range []string{"../outside.png", `..\\outside.png`, "/tmp/shot.png", "shot.png.context.json", "", "%2e%2e%2foutside.png"} {
		restExpect(t, "IsComputerQuery", IsComputerQuery, restObj(t, `{"kind":"attachment","name":"`+invalid+`"}`), false)
	}
}

func TestDirectoryQueriesAreBounded(t *testing.T) {
	restExpect(t, "IsComputerQuery", IsComputerQuery, restObj(t, `{"kind":"directories","prefix":""}`), true)
	restExpect(t, "IsComputerQuery", IsComputerQuery, restObj(t, `{"kind":"directories","prefix":"/tmp/"}`), true)
	for _, prefix := range []any{42, restRepeat("x", 4097), "bad\x00path"} {
		restExpect(t, "IsComputerQuery", IsComputerQuery, restWith(t, map[string]any{"kind": "directories"}, "prefix", prefix), false)
	}
}

func TestMessageImageQueriesValidateTheLocalReferenceBounds(t *testing.T) {
	query := restObj(t, `{"kind":"message-image","path":"/tmp/shot.png","root":"","message":"reply"}`)
	for _, thumbnail := range []any{restUndefined, false, true} {
		request := map[string]any{"kind": "query", "requestId": "request", "query": restWith(t, query, "thumbnail", thumbnail)}
		restExpect(t, "IsComputerClientRequest", IsComputerClientRequest, request, true)
	}
	restExpect(t, "IsComputerQuery", IsComputerQuery, restWith(t, query, "path", restRepeat("a", 4092)+".png", "root", restRepeat("r", 4096), "message", restRepeat("m", 256)), true)
	for _, fields := range [][]any{
		{"path", restRepeat("a", 4093) + ".png"}, {"path", "/etc/passwd"}, {"path", "https://host/image.png"},
		{"path", "file:///tmp/shot.png"}, {"path", nil}, {"path", ""},
		{"root", nil}, {"root", restRepeat("r", 4097)}, {"message", ""}, {"message", restRepeat("m", 257)},
		{"message", 1}, {"thumbnail", "1"}, {"thumbnail", nil},
	} {
		restExpect(t, "IsComputerQuery", IsComputerQuery, restWith(t, query, fields...), false)
	}
}

func TestComputerQueryDefinitionsListEveryShapeInOrder(t *testing.T) {
	definitions := ComputerQueryDefinitions()
	kinds := make([]string, len(definitions))
	for i, definition := range definitions {
		kinds[i] = definition.Kind
	}
	if !slices.Equal(kinds, []string{"thread-read", "thread-list", "terminal-output", "directories", "attachment", "message-image", "diff-patch", "branches", "commits", "commands"}) {
		t.Fatalf("kinds = %v", kinds)
	}
	if !slices.Equal(definitions[1].Fields, []string{"project", "archived", "idleForMs", "search", "attachments", "limit"}) {
		t.Fatalf("thread-list fields = %v", definitions[1].Fields)
	}
}

func TestComputerClientRequestsAreReadDefensively(t *testing.T) {
	for _, valid := range []string{
		`{"kind":"pair","version":2,"code":"ABCD","deviceName":"Mac"}`,
		`{"kind":"resume","version":2,"token":"t","lastSequence":0}`,
		`{"kind":"resume","version":2,"token":"t","sessionId":"s","lastSequence":4}`,
		`{"kind":"transfer","requestId":"r"}`,
		`{"kind":"query","requestId":"r","query":{"kind":"branches","workspaceId":"ws"}}`,
		`{"kind":"pong","at":1}`,
	} {
		restExpect(t, "IsComputerClientRequest", IsComputerClientRequest, restObj(t, valid), true)
	}
	inputs := make([]any, 17)
	for i := range inputs {
		inputs[i] = map[string]any{"type": "task.dismiss-all"}
	}
	for _, invalid := range []any{
		restObj(t, `{"kind":"pair","version":2,"code":"ABCD"}`),
		restObj(t, `{"kind":"resume","version":2,"token":"t","sessionId":null,"lastSequence":0}`),
		restObj(t, `{"kind":"resume","version":1.5,"token":"t","lastSequence":0}`),
		restObj(t, `{"kind":"input","requestId":"r","inputs":[]}`),
		map[string]any{"kind": "input", "requestId": "r", "inputs": inputs},
		restObj(t, `{"kind":"hello"}`),
		restJSON(t, `[{"kind":"pong","at":1}]`),
	} {
		restExpect(t, "IsComputerClientRequest", IsComputerClientRequest, invalid, false)
	}
}

func TestComputerServerMessagesCheckOnlyTheEnvelope(t *testing.T) {
	for _, valid := range []string{
		`{"sequence":1,"kind":"paired","deviceId":"d","deviceName":"Mac","token":"t"}`,
		`{"sequence":1,"kind":"workspace","sessionId":"s","update":{"revision":1,"state":{}}}`,
		`{"sequence":1,"kind":"workspace","update":{"revision":2,"patches":[]}}`,
		`{"sequence":1,"kind":"result","requestId":"r","result":{"ok":false,"revision":3}}`,
		`{"sequence":1,"kind":"answer","requestId":"r","ok":true,"result":null}`,
		`{"sequence":1,"kind":"answer","requestId":"r","ok":false,"message":""}`,
		`{"sequence":1,"kind":"error","code":"version","message":"Update"}`,
		`{"sequence":1,"kind":"notice","notice":{"taskId":"t","title":"","headline":""}}`,
		`{"sequence":1,"kind":"capabilities","capabilities":["command:task.send"],"instance":"i"}`,
		`{"sequence":1,"kind":"name","name":"Mac"}`,
		`{"sequence":0,"kind":"ping","at":5}`,
	} {
		restExpect(t, "IsComputerServerMessage", IsComputerServerMessage, restObj(t, valid), true)
	}
	for _, invalid := range []string{
		`{"kind":"name","name":"Mac"}`,
		`{"sequence":1,"kind":"workspace","update":{"revision":2}}`,
		`{"sequence":1,"kind":"workspace","update":{"revision":2,"state":[]}}`,
		`{"sequence":1,"kind":"answer","requestId":"r","ok":true}`,
		`{"sequence":1,"kind":"answer","requestId":"r","ok":false}`,
		`{"sequence":1,"kind":"capabilities","capabilities":[""]}`,
		`{"sequence":1,"kind":"capabilities","capabilities":[],"instance":""}`,
		`{"sequence":1,"kind":"name","name":""}`,
		`{"sequence":1,"kind":"other"}`,
	} {
		restExpect(t, "IsComputerServerMessage", IsComputerServerMessage, restObj(t, invalid), false)
	}
}
