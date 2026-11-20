package application

import (
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func commandsRunCommands(effects []WorkspaceEffect) []contracts.RunCommand {
	commands := []contracts.RunCommand{}
	for _, effect := range effects {
		if sent, ok := effect.(WorkspaceEffectSendRunCommand); ok {
			commands = append(commands, sent.Command.(contracts.RunCommand))
		}
	}
	return commands
}

func TestArchivingRetiresAutomationAndCancelsTheRun(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{coreTask("task-a"), coreTask("task-b")}
			s.Automations = []*domain.AutomationView{threadsAutomation("task-a"), threadsAutomation("task-b")}
			s.ActiveRuns = map[string]ActiveRun{"task-b": coreActive("task-b", "run-b")}
		})
		archived := Reduce(state, contracts.TaskCommandTaskArchive{TaskID: "task-a"})
		coreEqualJSON(t, archived.Effects, `[{"type":"automation.delete","taskId":"task-a"}]`, "idle archive")
		if archived.State.Threads[0].ArchivedAt == nil {
			t.Fatal("not archived")
		}
		running := Reduce(state, contracts.TaskCommandTaskArchive{TaskID: "task-b"})
		coreEqualJSON(t, running.Effects, `[{"type":"automation.delete","taskId":"task-b"},{"type":"send-run-command","command":{"type":"cancel","taskId":"task-b","runId":"run-b"}},{"type":"send-run-command","command":{"type":"archive","taskId":"task-b","channel":"main","archived":true}}]`, "running archive")
	})
}

func TestArchivePromptAsksFirst(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{coreTask("task-a"), coreTask("task-b")}
			s.ActiveRuns = map[string]ActiveRun{"task-b": coreActive("task-b", "run-b")}
			s.OpenMenu = new("thread:task-a")
		})
		asked := Reduce(state, contracts.ViewCommandViewArchivePrompt{ThreadID: new("task-a")})
		if *asked.State.ArchivePrompt != "task-a" || asked.State.OpenMenu != nil || len(asked.Effects) != 0 || asked.State.Threads[0].ArchivedAt != nil {
			t.Fatal("asked")
		}
		cancelled := Reduce(&asked.State, contracts.ViewCommandViewArchivePrompt{})
		if cancelled.State.ArchivePrompt != nil || cancelled.State.Threads[0].ArchivedAt != nil {
			t.Fatal("cancelled")
		}
		if ghost := Reduce(state, contracts.ViewCommandViewArchivePrompt{ThreadID: new("ghost")}); ghost.State.ArchivePrompt != nil {
			t.Fatal("asked about a thread that is not there")
		}
		archived := Reduce(&asked.State, contracts.TaskCommandTaskArchive{TaskID: "task-a"}).State
		if again := Reduce(&archived, contracts.ViewCommandViewArchivePrompt{ThreadID: new("task-a")}); again.State.ArchivePrompt != nil {
			t.Fatal("asked about an archived thread")
		}
	})
}

func TestArchivingFilesTheSessionButNotAnInheritedOne(t *testing.T) {
	corePorted(t, func() {
		continuation := &domain.Continuation{Provider: "codex", Value: "codex-thread"}
		withSession := func(t *domain.Thread) {
			t.Continuation, t.ContinuationStatus = continuation, domain.ContinuationStatusAvailable
		}
		state := coreWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{coreTask("task-a", withSession), coreTask("task-copy", withSession, func(t *domain.Thread) { t.InheritedContinuation = new(true) })}
			s.Automations = []*domain.AutomationView{}
		})
		filing := func(effects []WorkspaceEffect) []contracts.RunCommand {
			archives := []contracts.RunCommand{}
			for _, command := range commandsRunCommands(effects) {
				if _, ok := command.(contracts.ArchiveThreadCommand); ok {
					archives = append(archives, command)
				}
			}
			return archives
		}
		archived := Reduce(state, contracts.TaskCommandTaskArchive{TaskID: "task-a"})
		coreEqualJSON(t, filing(archived.Effects), `[{"type":"archive","taskId":"task-a","channel":"main","continuation":{"provider":"codex","value":"codex-thread"},"archived":true}]`, "filed")
		restored := Reduce(&archived.State, contracts.TaskCommandTaskRestore{TaskID: "task-a"})
		coreEqualJSON(t, filing(restored.Effects), `[{"type":"archive","taskId":"task-a","channel":"main","continuation":{"provider":"codex","value":"codex-thread"},"archived":false}]`, "unfiled")
		if restored.State.Threads[0].ArchivedAt != nil {
			t.Fatal("not restored")
		}
		if again := Reduce(&restored.State, contracts.TaskCommandTaskRestore{TaskID: "task-a"}); len(again.Effects) != 0 {
			t.Fatal("restoring twice")
		}
		copied := Reduce(state, contracts.TaskCommandTaskArchive{TaskID: "task-copy"})
		coreEqualJSON(t, filing(copied.Effects), `[]`, "an inherited session is not filed")
	})
}

func TestArchivingClosesPagesButKeepsShownOnes(t *testing.T) {
	corePorted(t, func() {
		page := func(id string, loading bool) *domain.BrowserTab {
			return &domain.BrowserTab{ID: id, URL: "https://example.com/" + id, Title: id, Loading: loading}
		}
		dock := func(tabs ...*domain.BrowserTab) map[string]*ThreadDock {
			held := *EmptyDock
			held.Open, held.Tab, held.BrowserTabID, held.BrowserTabs = true, "one", new("one"), tabs
			held.Terminals = []*domain.TerminalSession{{ID: "shell", Cwd: "/repo", Title: "Shell", TaskID: new("task-a"), Status: "running"}}
			held.TerminalID = new("shell")
			return map[string]*ThreadDock{"task-a": &held}
		}
		closes := func(effects []WorkspaceEffect) []string {
			tabs := []string{}
			for _, effect := range effects {
				if closed, ok := effect.(WorkspaceEffectBrowserClose); ok {
					tabs = append(tabs, closed.TabID)
				}
			}
			return tabs
		}
		loading := func(state *WorkspaceState) []bool {
			var flags []bool
			for _, tab := range state.Docks["task-a"].BrowserTabs {
				flags = append(flags, tab.Loading)
			}
			return flags
		}
		own := coreWorkspace(func(s *WorkspaceState) {
			s.CurrentID = new("task-a")
			s.Threads = []*domain.Thread{coreTask("task-a"), coreTask("task-b")}
			s.Docks = dock(page("one", true), page("two", false))
		})
		archived := Reduce(own, contracts.TaskCommandTaskArchive{TaskID: "task-a"})
		threadsSame(t, closes(archived.Effects), []string{"one", "two"}, "every page closes")
		if flags := loading(&archived.State); flags[0] || flags[1] || len(archived.State.Docks["task-a"].Terminals) != 1 {
			t.Fatalf("records kept: %v", flags)
		}
		fromChat := coreWorkspace(func(s *WorkspaceState) {
			s.CurrentID = new("chat")
			s.Threads = []*domain.Thread{coreTask("task-a"), coreTask("chat")}
			s.SideChats = []*SideChat{{ID: "chat", SourceThreadID: "task-a"}}
			s.Docks = dock(page("one", true), page("two", true))
		})
		shown := Reduce(fromChat, contracts.TaskCommandTaskArchive{TaskID: "task-a"})
		threadsSame(t, closes(shown.Effects), []string{"two"}, "the shown page stays")
		if flags := loading(&shown.State); !flags[0] || flags[1] {
			t.Fatalf("loading %v", flags)
		}
		browsing := *fromChat
		browsing.CurrentID = new("task-b")
		browsing.ActiveRuns = map[string]ActiveRun{"chat": coreActive("chat", "run-chat")}
		kept := Reduce(&browsing, contracts.TaskCommandTaskArchive{TaskID: "task-a"})
		threadsSame(t, closes(kept.Effects), []string{}, "a running side chat keeps every page")
	})
}

func TestClearingTheArchiveDeletesEveryArchivedTask(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{coreTask("kept"), coreTask("archived-a", thArchived(5)), coreTask("archived-b", thArchived(6))}
			s.CurrentID = new("archived-a")
		})
		cleared := Reduce(state, contracts.TaskCommandTaskClearArchive{}).State
		threadsSame(t, threadsIDs(cleared.Threads), []string{"kept"}, "kept")
		if cleared.CurrentID != nil {
			t.Fatal("still on a deleted thread")
		}
		if again := ReduceThreadCommands(&cleared, contracts.TaskCommandTaskClearArchive{}); len(again.Edits) != 0 {
			t.Fatal("clearing an empty archive")
		}
	})
}

func TestRoleIsGivenSwappedAndTakenOff(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(func(s *WorkspaceState) { s.Threads = []*domain.Thread{coreTask("task-a")} })
		reviewer := Reduce(state, contracts.TaskCommandTaskSetRole{TaskID: new("task-a"), Role: new(domain.ThreadRoleReviewer)})
		if *reviewer.State.Threads[0].Role != domain.ThreadRoleReviewer || len(reviewer.Effects) != 0 {
			t.Fatal("reviewer")
		}
		if same := ReduceThreadCommands(&reviewer.State, contracts.TaskCommandTaskSetRole{TaskID: new("task-a"), Role: new(domain.ThreadRoleReviewer)}); len(same.Edits) != 0 {
			t.Fatal("the same role again changed something")
		}
		coordinator := Reduce(&reviewer.State, contracts.TaskCommandTaskSetRole{TaskID: new("task-a"), Role: new(domain.ThreadRoleCoordinator)})
		if *coordinator.State.Threads[0].Role != domain.ThreadRoleCoordinator {
			t.Fatal("coordinator")
		}
		cleared := Reduce(&coordinator.State, contracts.TaskCommandTaskSetRole{TaskID: new("task-a")})
		if cleared.State.Threads[0].Role != nil {
			t.Fatal("role left behind")
		}
		if missing := ReduceThreadCommands(state, contracts.TaskCommandTaskSetRole{TaskID: new("missing"), Role: new(domain.ThreadRoleReviewer)}); len(missing.Edits) != 0 {
			t.Fatal("a missing thread")
		}
	})
}

func TestRenameAndSuggestedTitles(t *testing.T) {
	corePorted(t, func() {
		corePin(t, 50)
		state := coreWorkspace(func(s *WorkspaceState) { s.Threads = []*domain.Thread{coreTask("task-a")} })
		renamed := Reduce(state, contracts.TaskCommandTaskRename{TaskID: "task-a", Title: "  Fix the login  "})
		thread := renamed.State.Threads[0]
		if thread.Title != "Fix the login" || thread.TitleByUser == nil || thread.UpdatedAt != 50 {
			t.Fatalf("renamed: %s", coreJSON(t, thread))
		}
		coreEqualJSON(t, commandsRunCommands(renamed.Effects), `[{"type":"label","taskId":"task-a","title":"Fix the login"}]`, "label")
		if suggested := ReduceThreadCommands(&renamed.State, WorkspaceEventTitleSuggested{TaskID: "task-a", Title: "Something else"}); len(suggested.Edits) != 0 {
			t.Fatal("a suggestion outranked the user's name")
		}
		titled := Reduce(state, WorkspaceEventTitleSuggested{TaskID: "task-a", Title: "Suggested"}).State
		if titled.Threads[0].Title != "Suggested" || titled.Threads[0].TitleByUser != nil {
			t.Fatal("suggested title")
		}
	})
}

func TestSnoozeFromPriorityAndExpiry(t *testing.T) {
	corePorted(t, func() {
		const at = 1_800_000_000_000.0
		corePin(t, at)
		thread := coreTask("waiting", func(t *domain.Thread) {
			t.Outcome, t.OutcomeUnread = new(domain.ThreadOutcomeFinished), new(true)
			t.Findings = []*domain.AutomationFinding{{ID: "finding", Headline: "Needs review", At: at}}
		})
		state := coreWorkspace(threadsOn([]*domain.Thread{thread}, "waiting"))
		snoozed := Reduce(state, contracts.TaskCommandTaskSnooze{TaskID: "waiting", Hours: 1})
		deadline := at + 3_600_000
		got := snoozed.State.Threads[0]
		if got.SnoozedUntil == nil || *got.SnoozedUntil != deadline || *got.Outcome != domain.ThreadOutcomeFinished || got.OutcomeUnread == nil || *snoozed.State.CurrentID != "waiting" || got.UpdatedAt != thread.UpdatedAt {
			t.Fatalf("snoozed: %s", coreJSON(t, got))
		}
		if scheduled := coreEffect[WorkspaceEffectScheduleSnoozeExpiry](t, snoozed); scheduled.At == nil || *scheduled.At != deadline {
			t.Fatalf("scheduled %s", coreJSON(t, scheduled))
		}
		early := Reduce(&snoozed.State, WorkspaceEventSnoozesElapsed{At: deadline - 1})
		if early.State.Threads[0].SnoozedUntil == nil {
			t.Fatal("woke early")
		}
		expired := Reduce(&early.State, WorkspaceEventSnoozesElapsed{At: deadline})
		if expired.State.Threads[0].SnoozedUntil != nil || coreEffect[WorkspaceEffectScheduleSnoozeExpiry](t, expired).At != nil {
			t.Fatal("did not wake")
		}
		for _, hours := range []float64{0, -1, 2, 1.5} {
			if refused := ReduceThreadCommands(state, contracts.TaskCommandTaskSnooze{TaskID: "waiting", Hours: hours}); len(refused.Edits) != 0 {
				t.Fatalf("snoozed for %v hours", hours)
			}
		}
		quiet := coreWorkspace(func(s *WorkspaceState) { s.Threads = []*domain.Thread{coreTask("quiet")} })
		if refused := ReduceThreadCommands(quiet, contracts.TaskCommandTaskSnooze{TaskID: "quiet", Hours: 1}); len(refused.Edits) != 0 {
			t.Fatal("a thread out of Priority snoozed")
		}
		woken := Reduce(&snoozed.State, contracts.TaskCommandTaskUnsnooze{TaskID: "waiting"})
		if woken.State.Threads[0].SnoozedUntil != nil || coreEffect[WorkspaceEffectScheduleSnoozeExpiry](t, woken).At != nil {
			t.Fatal("unsnooze")
		}
	})
}

func TestDismissMovesOnToTheNextPriorityRow(t *testing.T) {
	corePorted(t, func() {
		finished := func(t *domain.Thread) { t.Outcome, t.OutcomeUnread = new(domain.ThreadOutcomeFinished), new(true) }
		state := coreWorkspace(threadsOn([]*domain.Thread{
			coreTask("first", finished, thCreated(3)),
			coreTask("second", finished, thCreated(2)),
			coreTask("third", finished, thCreated(1)),
		}, "first"))
		dismissed := Reduce(state, contracts.TaskCommandTaskDismiss{TaskID: new("first")}).State
		if threadsFind(dismissed.Threads, "first").Outcome != nil || *dismissed.CurrentID != "second" {
			t.Fatalf("dismissed: current %v %s", *dismissed.CurrentID, coreJSON(t, threadsFind(dismissed.Threads, "first")))
		}
		all := Reduce(&dismissed, contracts.TaskCommandTaskDismissAll{}).State
		for _, thread := range all.Threads {
			if thread.Outcome != nil {
				t.Fatalf("%s still in Priority", thread.ID)
			}
		}
	})
}

func TestDismissAllKeepsToOneProject(t *testing.T) {
	corePorted(t, func() {
		finished := func(t *domain.Thread) { t.Outcome = new(domain.ThreadOutcomeFinished) }
		remote := coreWorkspace(func(s *WorkspaceState) {
			s.Projects = []*domain.Project{{ID: "remote-project", Root: "/linux/app"}}
			s.Threads = []*domain.Thread{coreTask("remote-done", thProject("remote-project"), finished), coreTask("remote-chat", finished)}
		})
		linux := &PairedComputer{ComputerLink: domain.ComputerLink{ID: "linux", Name: "linux", Host: "linux.tail.ts.net", Status: domain.ComputerStatusConnected, PairedAt: 1, Capabilities: contracts.ComputerCapabilitiesComputerCapabilities()}, State: remote}
		state := coreWorkspace(func(s *WorkspaceState) {
			s.Projects = []*domain.Project{{ID: "web", Root: "/work/web"}, {ID: "api", Root: "/work/api"}}
			s.Threads = []*domain.Thread{coreTask("web-done", thProject("web"), finished), coreTask("api-done", thProject("api"), finished)}
			s.Computers.Name, s.Computers.Paired = "This Mac", []*PairedComputer{linux}
		})
		web := Reduce(state, contracts.TaskCommandTaskDismissAll{ProjectID: new("web")})
		if web.State.Threads[0].Outcome != nil || web.State.Threads[1].Outcome == nil {
			t.Fatal("another project's rows lost their dots")
		}
		for _, effect := range web.Effects {
			if _, forwarded := effect.(WorkspaceEffectComputerForward); forwarded {
				t.Fatal("forwarded to a computer that holds no such project")
			}
		}
		there := Reduce(state, contracts.TaskCommandTaskDismissAll{ProjectID: new("remote-project")})
		forward := coreEffect[WorkspaceEffectComputerForward](t, there)
		coreEqualJSON(t, forward.Inputs, `[{"type":"task.dismiss-all","localOnly":true,"projectId":"remote-project"}]`, "forwarded")
		answered := Reduce(remote, forward.Inputs[0]).State
		if answered.Threads[0].Outcome != nil || answered.Threads[1].Outcome == nil {
			t.Fatal("the remote kept to its project")
		}
		var older []string
		for _, name := range contracts.ComputerCapabilitiesComputerCapabilities() {
			if name != "command:task.dismiss-all:projectId" {
				older = append(older, name)
			}
		}
		oldHost := *linux
		oldHost.Capabilities = older
		oldState := *state
		oldState.Computers.Paired = []*PairedComputer{&oldHost}
		refused := Reduce(&oldState, contracts.TaskCommandTaskDismissAll{ProjectID: new("remote-project")})
		if refused.State.ActionError == nil || !strings.HasPrefix(*refused.State.ActionError, "linux:") {
			t.Fatalf("refusal %v", refused.State.ActionError)
		}
	})
}
