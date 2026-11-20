package domain

import (
	"strings"
	"testing"
)

func toolCallDescribe(t *testing.T, engine EngineID, name string, input string) ToolCall {
	t.Helper()
	return DescribeToolCall(engine, name, &input)
}

func TestToolCallNamedByArgument(t *testing.T) {
	featuresExpectJSON(t, toolCallDescribe(t, "claude", "Bash", `{"command":"git status --short"}`), `{"family":"shell","label":"Bash","sigil":"$","argument":"git status --short"}`)
	featuresExpectJSON(t, toolCallDescribe(t, "claude", "WebSearch", `{"query":"electron menu roles"}`), `{"family":"web","label":"WebSearch","argument":"electron menu roles"}`)
	featuresExpectJSON(t, toolCallDescribe(t, "claude", "Task", `{"description":"audit the reducer","prompt":"…"}`), `{"family":"agent","label":"Task","argument":"audit the reducer"}`)
}

func TestToolCallShortensPaths(t *testing.T) {
	for path, want := range map[string]string{
		"/Users/x/workspace/ai-coding-tool/src/renderer/styles.css": "…/renderer/styles.css",
		"src/renderer/styles.css":                                   "src/renderer/styles.css",
		"/etc/hosts":                                                "/etc/hosts",
	} {
		if got := toolCallDescribe(t, "claude", "Read", `{"file_path":"`+path+`"}`).Argument; got != want {
			t.Fatalf("%s = %s", path, got)
		}
	}
}

func TestToolCallSearchSaysWhere(t *testing.T) {
	if toolCallDescribe(t, "claude", "Grep", `{"pattern":"work-row","path":"src/renderer"}`).Argument != "work-row in src/renderer" ||
		toolCallDescribe(t, "claude", "Grep", `{"pattern":"work-row"}`).Argument != "work-row" {
		t.Fatal("a search says where it looked")
	}
}

func TestToolCallFlattensToOneLine(t *testing.T) {
	if got := toolCallDescribe(t, "claude", "Bash", `{"command":"cat <<'EOF' > f\n  one\n  two\nEOF"}`).Argument; got != "cat <<'EOF' > f one two EOF" {
		t.Fatal(got)
	}
	long := toolCallDescribe(t, "claude", "Bash", `{"command":"`+strings.Repeat("x", 400)+`"}`).Argument
	if featuresUTF16Len(long) != 240 || !strings.HasSuffix(long, "…") {
		t.Fatalf("long = %d", featuresUTF16Len(long))
	}
}

func TestToolCallNothingToName(t *testing.T) {
	if toolCallDescribe(t, "claude", "Bash", "not json").Argument != "" ||
		toolCallDescribe(t, "claude", "ListAgents", `{}`).Argument != "" ||
		toolCallDescribe(t, "claude", "Edit", `{"old_string":"a","new_string":"b"}`).Argument != "" ||
		DescribeToolCall("claude", "Bash", nil).Argument != "" {
		t.Fatal("a call carrying nothing to name falls back to no argument at all")
	}
}

func TestToolFamilyForUnknownTools(t *testing.T) {
	for _, check := range []struct {
		engine EngineID
		name   string
		want   ToolFamily
	}{
		{"claude", "browser_click", ToolFamilyWeb},
		{"claude", "mcp__aicodingtool-terminal__terminal_read", ToolFamilyRead},
		{"claude", "MultiEdit", ToolFamilyWrite},
		{"codex", "file_change", ToolFamilyWrite},
		{"codex", "mcp_tool_call", ToolFamilyOther},
		{"codex", "browser_click", ToolFamilyWeb},
		{"gemini", "run_shell", ToolFamilyOther},
	} {
		if got := ToolCallToolFamily(check.engine, check.name); got != check.want {
			t.Fatalf("%s %s = %s", check.engine, check.name, got)
		}
	}
}

func TestToolCallCodexAndUnknownEngines(t *testing.T) {
	featuresExpectJSON(t, toolCallDescribe(t, "codex", "command_execution", `{"command":"npm test"}`), `{"family":"shell","label":"command_execution","sigil":"$","argument":"npm test"}`)
	if got := toolCallDescribe(t, "codex", "file_change", `{"path":"/Users/x/repo/src/main/index.ts"}`).Argument; got != "…/main/index.ts" {
		t.Fatal(got)
	}
	featuresExpectJSON(t, toolCallDescribe(t, "codex", "web_search", `{"query":"codex app-server"}`), `{"family":"web","label":"web_search","argument":"codex app-server"}`)
	featuresExpectJSON(t, toolCallDescribe(t, "gemini", "run_shell", `{"command":"ls -la"}`), `{"family":"other","label":"run_shell","argument":"ls -la"}`)
	if toolCallDescribe(t, "gemini", "browser_open", `{"url":"https://example.com"}`).Family != ToolFamilyWeb {
		t.Fatal("our own tools are known whichever engine calls them")
	}
}

func TestToolCallOwnToolsReadAsVerbs(t *testing.T) {
	start := `{"title":"Review the refactor","prompt":"You are the reviewer…","role":"reviewer"}`
	expected := `{"family":"thread","label":"Start thread","verb":true,"argument":"Review the refactor"}`
	featuresExpectJSON(t, toolCallDescribe(t, "claude", "mcp__aicodingtool-threads__start_thread", start), expected)
	featuresExpectJSON(t, toolCallDescribe(t, "codex", "start_thread", start), expected)
	featuresExpectJSON(t, toolCallDescribe(t, "opencode", "start_thread", start), expected)
	if got := toolCallDescribe(t, "claude", "mcp__aicodingtool-threads__start_thread", `{"prompt":"Fix\nthe dock"}`).Argument; got != "Fix the dock" {
		t.Fatal(got)
	}
}

func TestToolCallCarriesThread(t *testing.T) {
	id := "dedaef8d-9c0c-4f99-a08f-52122b6b5875"
	featuresExpectJSON(t, toolCallDescribe(t, "claude", "mcp__aicodingtool-threads__wait_for_thread", `{"threadId":"`+id+`","timeoutSeconds":900}`), `{"family":"thread","label":"Wait for thread","verb":true,"argument":"","thread":"`+id+`"}`)
	featuresExpectJSON(t, toolCallDescribe(t, "codex", "message_thread", `{"threadId":"`+id+`","text":"Round 4.\nFixed: all"}`), `{"family":"thread","label":"Message thread","verb":true,"argument":"Round 4. Fixed: all","thread":"`+id+`"}`)
	if toolCallDescribe(t, "claude", "mcp__aicodingtool-threads__move_to_worktree", `{"worktreeId":"new"}`).Argument != "New worktree" ||
		toolCallDescribe(t, "claude", "mcp__aicodingtool-threads__move_to_worktree", `{"worktreeId":null}`).Argument != "Project checkout" {
		t.Fatal("move_to_worktree")
	}
	if got := toolCallDescribe(t, "codex", "report_status", `{"state":"blocked","summary":"waiting"}`).Argument; got != "blocked · waiting" {
		t.Fatal(got)
	}
	if got := toolCallDescribe(t, "codex", "propose_threads", `{"threads":[{"title":"A"},null,{"title":" "},{"title":"B"}]}`).Argument; got != "A, B" {
		t.Fatal(got)
	}
}

func TestToolCallAutomationTerminalBrowser(t *testing.T) {
	featuresExpectJSON(t, toolCallDescribe(t, "claude", "mcp__aicodingtool-automation__notify", `{"headline":"CI is red","detail":"…","key":"ci"}`), `{"family":"automation","label":"Notify","verb":true,"argument":"CI is red"}`)
	featuresExpectJSON(t, toolCallDescribe(t, "claude", "mcp__aicodingtool-browser__browser_open", `{"url":"https://example.com","newTab":true}`), `{"family":"web","label":"Open page","verb":true,"argument":"https://example.com"}`)
	featuresExpectJSON(t, toolCallDescribe(t, "codex", "browser_click", `{"ref":"e12"}`), `{"family":"web","label":"Click","verb":true,"argument":""}`)
	if toolCallDescribe(t, "claude", "mcp__aicodingtool-terminal__terminal_read", `{}`).Label != "Read terminal" {
		t.Fatal("terminal_read")
	}
}

func TestToolLabelForOtherServers(t *testing.T) {
	if ToolLabel("mcp__cua-driver__get_window_state") != "cua-driver · get window state" || ToolLabel("mcp__cua-driver__status") != "cua-driver · status" ||
		ToolLabel("mcp__aicodingtool-threads__archive_thread") != "Archive thread" || ToolLabel("Bash") != "Bash" {
		t.Fatal("labels")
	}
	if toolCallDescribe(t, "claude", "mcp__cua-driver__status", `{}`).Verb != nil {
		t.Fatal("another server's tool is no verb")
	}
}
