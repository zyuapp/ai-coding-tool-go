package claude

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/claude/protocol"
)

func titled(title string) string {
	encoded, _ := jsontext.AppendQuote(nil, title)
	return `{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"title":` + string(encoded) + `}}`
}

func titleFactory(t *testing.T, opened **fakeQuery, raws ...string) QueryFactory {
	messages := decodeAll(t, raws...)
	return func(_ context.Context, options protocol.Options) (Query, error) {
		q := newFake(options, true, messages...)
		q.endAfterSent = 1
		if opened != nil {
			*opened = q
		}
		return q, nil
	}
}

func suggest(t *testing.T, text string, attachments []string, factory QueryFactory) *string {
	t.Helper()
	return suggestTaskTitle(context.Background(), factory, text, attachments)
}

func TestNamingAThreadReachesNothingAndReturnsACleanTitle(t *testing.T) {
	var opened *fakeQuery
	title := suggest(t, "Inspect the app and tell me what breaks", nil, titleFactory(t, &opened, titled(`"App breakage review."`)))
	assertEqual(t, *title, "App breakage review", "title")
	options := opened.options
	assertEqual(t, options.Model, "haiku", "model")
	assertEqual(t, options.Cwd, os.TempDir(), "cwd")
	assertEqual(t, options.SettingSources, []string{}, "settings")
	assertEqual(t, options.Tools, []string{}, "tools")
	if len(options.JSONSchema) == 0 {
		t.Fatal("structured output is asked for")
	}
	assertEqual(t, options.SystemPrompt.Kind, protocol.SystemPromptCustom, "a custom prompt replaces the Claude Code preset")
	assertEqual(t, len(options.McpServers)+len(options.SdkMcpServers), 0, "no servers")
	assertEqual(t, options.MaxTurns, 1, "one turn")
	assertEqual(t, opened.isClosed(), true, "closed")
}

func TestALongTitleIsClippedAndALongMessageTrimmed(t *testing.T) {
	var opened *fakeQuery
	title := suggest(t, strings.Repeat("x", 5_000), nil, titleFactory(t, &opened, titled(strings.Repeat("A", 80))))
	assertEqual(t, *title, strings.Repeat("A", 49)+"…", "title")
	sent := opened.sent[0]
	assertEqual(t, sent.IsBlocks, false, "a plain string prompt")
	assertEqual(t, strings.Contains(sent.Text, strings.Repeat("x", 2_000)), true, "kept")
	assertEqual(t, strings.Contains(sent.Text, strings.Repeat("x", 2_001)), false, "trimmed")
}

func TestAThreadKeepsItsTitleWhenNamingProducesNothing(t *testing.T) {
	none := func(text string, raws ...string) {
		t.Helper()
		if title := suggest(t, text, nil, titleFactory(t, nil, raws...)); title != nil {
			t.Fatalf("%q named %q", text, *title)
		}
	}
	none("hi", titled("  "))
	none("Reply Ready and await my first request", titled("Ready Reply"))
	none("Add multiply to math.js", titled("Name this thread"))
	none("hi", `{"type":"result","subtype":"error_during_execution","errors":["nope"]}`)
	none("hi")
	none("hi", `{"type":"result","subtype":"success","is_error":false,"result":"I don't have access to your previous chat threads"}`)
	broken := func(_ context.Context, options protocol.Options) (Query, error) {
		q := newFake(options, true)
		q.failure = context.Canceled
		return q, nil
	}
	if title := suggest(t, "hi", nil, broken); title != nil {
		t.Fatal("a broken session names nothing")
	}
}

func TestAScreenshotReachesTheNamerAsBytes(t *testing.T) {
	directory := t.TempDir()
	shot := filepath.Join(directory, "shot.png")
	if err := os.WriteFile(shot, []byte("screenshot bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	var opened *fakeQuery
	title := suggest(t, "", []string{shot, filepath.Join(directory, "missing.png")}, titleFactory(t, &opened, titled("Sidebar overlap")))
	assertEqual(t, *title, "Sidebar overlap", "title")
	content := opened.sent[0]
	assertEqual(t, content.IsBlocks, true, "blocks")
	assertEqual(t, []string{content.Blocks[0].Type, content.Blocks[1].Type}, []string{"image", "text"}, "block types")
	assertEqual(t, content.Blocks[0].Source.Data, base64.StdEncoding.EncodeToString([]byte("screenshot bytes")), "bytes")
	if !strings.Contains(*content.Blocks[1].Text, "screenshots") {
		t.Fatal(*content.Blocks[1].Text)
	}
}

func TestAMessageWithNoReadableScreenshotStaysAPlainString(t *testing.T) {
	directory := t.TempDir()
	oversized := filepath.Join(directory, "large.png")
	if err := os.WriteFile(oversized, make([]byte, 1024*1024+1), 0o644); err != nil {
		t.Fatal(err)
	}
	var opened *fakeQuery
	suggest(t, "Inspect the app", []string{"/nowhere/shot.png", oversized}, titleFactory(t, &opened, titled("App review")))
	assertEqual(t, opened.sent[0].IsBlocks, false, "plain string")
}
