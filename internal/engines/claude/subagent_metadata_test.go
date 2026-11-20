package claude

import (
	"encoding/json/jsontext"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func reply(fields string) string {
	row := `{"type":"assistant","agentId":"child","sessionId":"session","message":{"model":"claude-opus-5"},"effort":"medium"`
	if fields != "" {
		// Later duplicates win, as with an object spread.
		row += "," + fields
	}
	return row + "}"
}

func TestSavedMetadataUsesTheLatestRealChildRequest(t *testing.T) {
	for _, effort := range []string{`"low"`, `"medium"`, `"high"`, `"xhigh"`, `"max"`, `64`} {
		rows := []string{reply(""), reply(`"perTurnEffort":` + effort), reply(`"agentId":"other","effort":"high"`), reply(`"sessionId":"other"`), reply(`"message":{"model":"<synthetic>"}`)}
		got := ClaudeTranscriptMetadata(strings.Join(rows, "\n")+"\n"+`{"unfinished":`, "child", "session")
		want := strings.Trim(effort, `"`)
		assertEqual(t, got, domain.SubagentMetadata{Model: ptrTo("claude-opus-5"), Effort: &want}, fmt.Sprint("effort ", effort))
	}
	got := ClaudeTranscriptMetadata(`{"type":"assistant","agentId":"child","sessionId":"session","message":{"model":"claude-opus-5"}}`, "child", "session")
	assertEqual(t, got, domain.SubagentMetadata{Model: ptrTo("claude-opus-5")}, "no effort")
}

func TestRecoveryLocatesASavedChildBoundsReadsAndToleratesMissingData(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "encoded-project", "session", "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-child.jsonl")
	ignored, _ := jsontext.AppendQuote(nil, strings.Repeat("x", 3*1024*1024))
	if err := os.WriteFile(path, []byte(`{"ignored":`+string(ignored)+"}\n"+reply("")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := "session"
	assertEqual(t, readClaudeSubagentMetadata("child", &session, root), domain.SubagentMetadata{Model: ptrTo("claude-opus-5"), Effort: ptrTo("medium")}, "tail")
	if err := os.WriteFile(path, []byte(reply(`"effort":"max"`)), 0o644); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, *readClaudeSubagentMetadata("child", &session, root).Effort, "max", "rewritten")
	assertEqual(t, readClaudeSubagentMetadata("missing", &session, root), domain.SubagentMetadata{}, "missing")
	assertEqual(t, readClaudeSubagentMetadata("child", nil, root), domain.SubagentMetadata{}, "no session")
	assertEqual(t, readClaudeSubagentMetadata("../child", &session, root), domain.SubagentMetadata{}, "escaping id")
	escaping := "../session"
	assertEqual(t, readClaudeSubagentMetadata("child", &escaping, root), domain.SubagentMetadata{}, "escaping session")
}
