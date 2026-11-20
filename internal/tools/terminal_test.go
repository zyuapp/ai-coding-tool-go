package tools

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func terminalSnapshot(edit func(*domain.TerminalSnapshot)) domain.TerminalSnapshot {
	snapshot := domain.TerminalSnapshot{TerminalID: "terminal-1", Title: "ai-coding-tool", Cwd: "/repo", Status: domain.TerminalSnapshotStatusRunning, Lines: []string{"$ yarn dev", "ready in 412 ms"}}
	if edit != nil {
		edit(&snapshot)
	}
	return snapshot
}

type fakeTerminal struct {
	calls  []contracts.TerminalRead
	result contracts.TerminalReadResult
	err    error
}

func (f *fakeTerminal) Read(_ context.Context, read contracts.TerminalRead) (contracts.TerminalReadResult, error) {
	f.calls = append(f.calls, read)
	if f.result == nil {
		return contracts.TerminalReadResultSnapshot{Snapshot: terminalSnapshot(nil)}, f.err
	}
	return f.result, f.err
}

func TestTheTerminalSurfaceReadsAndNeverWrites(t *testing.T) {
	if got := fmt.Sprint(names(TerminalToolsFor(&fakeTerminal{}))); got != "[terminal_list terminal_read]" {
		t.Fatal(got)
	}
}

func TestReadingATerminalReturnsItsLinesUnderAHeading(t *testing.T) {
	bridge := &fakeTerminal{}
	text := textOf(callBound(t, TerminalToolsFor(bridge), "terminal_read", `{}`))
	assertJSON(t, bridge.calls, `[{"op":"snapshot"}]`)
	if text != "ai-coding-tool — /repo · running\n\n$ yarn dev\nready in 412 ms" {
		t.Fatal(text)
	}
}

func TestAFilteredReadSaysHowMuchItKept(t *testing.T) {
	bridge := &fakeTerminal{result: contracts.TerminalReadResultSnapshot{Snapshot: terminalSnapshot(func(s *domain.TerminalSnapshot) {
		s.Lines, s.Omitted, s.Matched = []string{"error: missing token"}, 240, ptr(3.0)
	})}}
	text := textOf(callBound(t, TerminalToolsFor(bridge), "terminal_read", `{"lines":50,"match":"error"}`))
	assertJSON(t, bridge.calls, `[{"op":"snapshot","lines":50,"match":"error"}]`)
	assertMatch(t, text, `3 matching lines`)
	assertMatch(t, text, `240 earlier lines not shown`)
}

func TestATerminalThatHasExitedReadsAsOne(t *testing.T) {
	bridge := &fakeTerminal{result: contracts.TerminalReadResultSnapshot{Snapshot: terminalSnapshot(func(s *domain.TerminalSnapshot) {
		s.Status, s.ExitCode, s.Lines = domain.TerminalSnapshotStatusExited, ptr(130.0), nil
	})}}
	text := textOf(callBound(t, TerminalToolsFor(bridge), "terminal_read", `{"terminalId":"terminal-1"}`))
	assertMatch(t, text, `exited \(130\)`)
	assertMatch(t, text, `has printed nothing`)
}

func TestListingTerminalsNamesEachOne(t *testing.T) {
	listed := textOf(callBound(t, TerminalToolsFor(&fakeTerminal{result: contracts.TerminalReadResultTerminals{Terminals: []*domain.TerminalSession{
		{ID: "terminal-1", Title: "ai-coding-tool", Cwd: "/repo", TaskID: ptr("task-1"), Status: domain.TerminalSessionStatusRunning},
		{ID: "terminal-2", Title: "logs", Cwd: "/repo", Status: domain.TerminalSessionStatusExited, ExitCode: ptr(0.0)},
	}}}), "terminal_list", `{}`))
	assertMatch(t, listed, `ai-coding-tool \[terminal-1\] · /repo · running`)
	assertMatch(t, listed, `logs \[terminal-2\] · /repo · exited \(0\)`)

	empty := textOf(callBound(t, TerminalToolsFor(&fakeTerminal{result: contracts.TerminalReadResultNoTerminal{}}), "terminal_read", `{}`))
	assertMatch(t, empty, `no terminal open`)
}

func TestABridgeThatFailsAnswersAsAnError(t *testing.T) {
	result := callBound(t, TerminalToolsFor(&fakeTerminal{err: errors.New("the window is not open")}), "terminal_read", `{}`)
	if !result.IsError || textOf(result) != "Terminal error: the window is not open" {
		t.Fatalf("%+v", result)
	}
}
