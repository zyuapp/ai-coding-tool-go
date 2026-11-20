package tools

import (
	"fmt"
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func offersOf(tools []BoundTool, offered bool) Offers {
	return func(name string) bool {
		if !offered {
			return false
		}
		return name == "" || slices.Contains(names(tools), name)
	}
}

func TestServersComeInTheOrderARunIsOfferedThem(t *testing.T) {
	var got []string
	for _, server := range Servers() {
		got = append(got, fmt.Sprintf("%s:%s", server.Name, server.Grants))
	}
	want := "[aicodingtool-computer-use:read-only aicodingtool-automation:all aicodingtool-threads:read-only aicodingtool-coordination:all aicodingtool-browser:read-only aicodingtool-simulator:read-only aicodingtool-terminal:none]"
	if fmt.Sprint(got) != want {
		t.Fatal(got)
	}
	readOnly := ReadOnlyToolNames(ThreadServerName, ThreadServer.Definitions)
	if len(readOnly) != 3 || !readOnly["mcp__aicodingtool-threads__list_threads"] || !readOnly["mcp__aicodingtool-threads__wait_for_thread"] {
		t.Fatal(readOnly)
	}
	if len(AutomationServer.Definitions) != 7 || len(CoordinationServer.Definitions) != 6 {
		t.Fatal(AutomationServer.Definitions, CoordinationServer.Definitions)
	}
}

func TestAReviewIsOfferedNeitherThreadNorCoordinationTools(t *testing.T) {
	member := domain.CoordinationRoleMember
	run := &Run{Threads: &fakeThreads{}, Coordination: &fakeCoordination{}, CoordinationRole: &member}
	if _, ok := ThreadServer.Serve(run); !ok {
		t.Fatal("threads not offered")
	}
	if tools, ok := CoordinationServer.Serve(run); !ok || len(tools) != 2 {
		t.Fatal("coordination not offered")
	}
	run.Operation = contracts.RunOperationReview{}
	if _, ok := ThreadServer.Serve(run); ok {
		t.Fatal("a review is offered thread tools")
	}
	if _, ok := CoordinationServer.Serve(run); ok {
		t.Fatal("a review is offered coordination tools")
	}
	if _, ok := CoordinationServer.Serve(&Run{Coordination: &fakeCoordination{}}); ok {
		t.Fatal("a run with no role is offered coordination tools")
	}
	if _, ok := ComputerUseSetupServer.Serve(&Run{ComputerUse: domain.ComputerUseRunConfigUnavailable{}}); ok {
		t.Fatal("setup offered without being needed")
	}
}

func TestBriefsSayOnlyWhatTheRunWasOffered(t *testing.T) {
	dialect := BriefDialect{NativeWork: "Claude Code sessions or subagents", NativeWorktree: ptr("EnterWorktree")}
	threads := ThreadToolsFor(&fakeThreads{}, nil, "")
	brief := ThreadServer.Brief(offersOf(threads, true), dialect, BriefRun{})
	if len(brief) != 2 {
		t.Fatal(brief)
	}
	assertMatch(t, brief[0], `not Claude Code sessions or subagents\. Conversation history`)
	assertMatch(t, brief[1], `To do the work in this thread, call move_to_worktree with "new"`)
	assertMatch(t, brief[1], `Never make one yourself for that with git worktree or EnterWorktree\.$`)

	coordinator := ThreadToolsFor(&fakeThreads{}, nil, domain.CoordinationRoleCoordinator)
	brief = ThreadServer.Brief(offersOf(coordinator, true), BriefDialect{NativeWork: "x"}, BriefRun{})
	assertMatch(t, brief[1], ` Start a thread with start_thread and worktree to do it\. Never make one yourself for that with git worktree\.$`)
	if got := ThreadServer.Brief(offersOf(nil, false), dialect, BriefRun{}); got != nil {
		t.Fatal(got)
	}

	browser := BrowserServer.Brief(offersOf(BrowserToolsFor(emptyBrowser, true), true), dialect, BriefRun{})
	assertMatch(t, browser[0], `Opening, clicking and typing in it are work for a thread\.$`)
	simulator := SimulatorServer.Brief(offersOf(SimulatorToolsFor(&fakeSimulator{}, false), true), dialect, BriefRun{})
	assertMatch(t, simulator[0], `^To check an iOS app on a simulator`)
	if got := AutomationServer.Brief(offersOf(AutomationToolsFor(&fakeAutomations{}, domain.CoordinationRoleCoordinator), true), dialect, BriefRun{}); got != nil {
		t.Fatal(got)
	}
	if got := TerminalServer.Brief(offersOf(nil, true), dialect, BriefRun{}); len(got) != 1 {
		t.Fatal(got)
	}
	role := domain.CoordinationRoleMember
	if got := CoordinationServer.Brief(offersOf(nil, false), dialect, BriefRun{CoordinationRole: &role}); len(got) != 1 || got[0] != MemberInstructions {
		t.Fatal(got)
	}
}
