package application

import (
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var (
	startsBrief   = &domain.ThreadBrief{Intent: "fix the login test", DoneWhen: "it passes", Delivers: "commit"}
	startsProject = &domain.Project{ID: "project-a", Root: "/repo", WorkspaceID: new("workspace-a")}
)

func startsSend(edits ...func(*NewThreadSend)) NewThreadSend {
	send := NewThreadSend{Text: new("Fix it")}
	for _, edit := range edits {
		edit(&send)
	}
	return send
}

func startsStarted(t *testing.T, start NewThreadStart) *NewThreadSend {
	t.Helper()
	if start.Error != nil {
		t.Fatalf("refused: %s", *start.Error)
	}
	if start.Elsewhere != nil {
		t.Fatal("started elsewhere")
	}
	return start.Command
}

func startsRefused(t *testing.T, start NewThreadStart) string {
	t.Helper()
	if start.Error == nil {
		t.Fatalf("started: %s", coreJSON(t, start.Command))
	}
	return *start.Error
}

func startsHeldWorktree(id string) *domain.Worktree {
	return &domain.Worktree{ID: id, ProjectID: startsProject.ID, Root: "/worktrees/repo-" + id, WorkspaceID: "worktree-" + id, BaseCommit: "abcdef1234", CreatedAt: 2, LastUsedAt: 2}
}

func startsCoordinated() *WorkspaceState {
	model := func(id string, effort domain.AgentEffort) func(*domain.Thread) {
		return func(t *domain.Thread) { t.Model = new(id); t.Effort = new(effort) }
	}
	inWorktree := func(id string) func(*domain.Thread) { return func(t *domain.Thread) { t.WorktreeID = new(id) } }
	return coreWorkspace(func(s *WorkspaceState) {
		s.Projects = []*domain.Project{startsProject}
		s.Threads = []*domain.Thread{
			coreTask("lead", thRole(domain.ThreadRoleCoordinator), model("sonnet", "low"), thProject(startsProject.ID), inWorktree("wt-lead"), func(t *domain.Thread) {
				t.ThreadModel = &domain.ThreadModelChoice{Model: "opus", Effort: new(domain.AgentEffort("high"))}
			}),
			coreTask("worker", thUnder("lead"), model("gpt-6-luna", "medium"), thProject(startsProject.ID), func(t *domain.Thread) { t.Engine = domain.AgentEngineCodex }),
			coreTask("solo", model("sonnet", "max"), thProject(startsProject.ID), inWorktree("wt-solo")),
		}
	})
}

func startsLinux(state *WorkspaceState) *PairedComputer {
	return &PairedComputer{ComputerLink: domain.ComputerLink{ID: "linux", Name: "Linux", Host: "linux.test", PairedAt: 1, Status: domain.ComputerStatusConnected, Capabilities: contracts.ComputerCapabilitiesComputerCapabilities()}, State: state}
}

func TestThreadUnderACoordinatorStartsUnderIt(t *testing.T) {
	state := startsCoordinated()
	absent := jsonx.Opt[*PairedComputer]{}
	if got := startsRefused(t, ThreadStartsNewThreadSend(state, threadsFind(state.Threads, "lead"), startsSend(), absent)); got != BriefRequired {
		t.Fatalf("refusal %q", got)
	}
	coreEqualJSON(t, startsStarted(t, ThreadStartsNewThreadSend(state, threadsFind(state.Threads, "lead"), startsSend(func(s *NewThreadSend) { s.Brief = startsBrief }), absent)),
		`{"type":"task.send","text":"Fix it","brief":{"intent":"fix the login test","doneWhen":"it passes","delivers":"commit"},"engine":"claude","model":"opus","effort":"high","project":"project-a","worktreeId":"wt-lead","coordinatorId":"lead","from":"lead"}`, "lead's thread")
	coreEqualJSON(t, startsStarted(t, ThreadStartsNewThreadSend(state, threadsFind(state.Threads, "worker"), startsSend(func(s *NewThreadSend) { s.Role = new(domain.ThreadRoleReviewer) }), absent)),
		`{"type":"task.send","text":"Fix it","role":"reviewer","engine":"claude","model":"opus","effort":"high","project":"project-a","coordinatorId":"lead","from":"worker"}`, "worker's thread")
	for _, caller := range []string{"worker", "solo"} {
		if startsStarted(t, ThreadStartsNewThreadSend(state, threadsFind(state.Threads, caller), startsSend(func(s *NewThreadSend) { s.Brief = startsBrief }), absent)).Brief != nil {
			t.Fatalf("%s handed a brief", caller)
		}
	}
}

func TestThreadUnderNobodyStartsOnTheCallersModel(t *testing.T) {
	state := startsCoordinated()
	solo := threadsFind(state.Threads, "solo")
	coreEqualJSON(t, startsStarted(t, ThreadStartsNewThreadSend(state, solo, startsSend(), jsonx.Opt[*PairedComputer]{})),
		`{"type":"task.send","text":"Fix it","engine":"claude","model":"sonnet","effort":"max","project":"project-a","worktreeId":"wt-solo","from":"solo"}`, "solo")
	placed := startsStarted(t, ThreadStartsNewThreadSend(state, solo, startsSend(func(s *NewThreadSend) {
		s.Project, s.Worktree, s.Model = new("elsewhere"), new(true), new("haiku")
	}), jsonx.Opt[*PairedComputer]{}))
	if *placed.Project != "elsewhere" || !*placed.Worktree || placed.WorktreeID != nil || *placed.Model != "haiku" || placed.Effort != nil {
		t.Fatalf("placed: %s", coreJSON(t, placed))
	}
}

func TestThreadNamedOntoAPairedComputerStartsThere(t *testing.T) {
	remote := &domain.Project{ID: "remote-project", Root: "/remote", WorkspaceID: new("remote-workspace")}
	linux := startsLinux(coreWorkspace(func(s *WorkspaceState) { s.Projects = []*domain.Project{remote} }))
	state := startsCoordinated()
	state.Computers.Paired = []*PairedComputer{linux}
	solo := threadsFind(state.Threads, "solo")
	there := ThreadStartsNewThreadSend(state, solo, startsSend(func(s *NewThreadSend) { s.Project = new(remote.ID) }), jsonx.Opt[*PairedComputer]{})
	if there.Elsewhere != linux || there.Error != nil {
		t.Fatal("not elsewhere")
	}
	coreEqualJSON(t, there.Command, `{"type":"task.send","text":"Fix it","project":"remote-project","engine":"claude","model":"sonnet","effort":"max"}`, "elsewhere command")
	if ThreadStartsNewThreadSend(state, solo, startsSend(), jsonx.Some(linux)).Elsewhere != linux {
		t.Fatal("a named computer")
	}
	if got := startsRefused(t, ThreadStartsNewThreadSend(state, solo, startsSend(func(s *NewThreadSend) { s.Project = new(remote.ID) }), jsonx.Some[*PairedComputer](nil))); got != `That project or worktree is on Linux. Pass computer "Linux" to start the thread there.` {
		t.Fatalf("refusal %q", got)
	}
	led := ThreadStartsNewThreadSend(state, threadsFind(state.Threads, "lead"), startsSend(func(s *NewThreadSend) { s.Brief = startsBrief }), jsonx.Some(linux))
	if led.Elsewhere == nil || led.Lead == nil || led.Lead.ID != "lead" {
		t.Fatal("a coordinator's start elsewhere")
	}
}

func TestSuggestionIsCheckedAndPlaced(t *testing.T) {
	other := &domain.Project{ID: "project-b", Root: "/other", WorkspaceID: new("workspace-b")}
	state := startsCoordinated()
	state.Projects = []*domain.Project{startsProject, other}
	otherWorktree := startsHeldWorktree("wt-other")
	otherWorktree.ProjectID = other.ID
	state.Worktrees = []*domain.Worktree{startsHeldWorktree("wt-lead"), otherWorktree}
	lead := threadsFind(state.Threads, "lead")
	suggest := func(edit func(*domain.ProposalRequest)) (*domain.ThreadProposal, error) {
		request := domain.ProposalRequest{Title: " Fix login ", Prompt: "Fix it", Brief: startsBrief}
		edit(&request)
		return ThreadProposal(state, lead, request, 5, jsonx.Opt[*PairedComputer]{})
	}
	kept := func(edit func(*domain.ProposalRequest)) *domain.ThreadProposal {
		t.Helper()
		proposal, err := suggest(edit)
		if err != nil {
			t.Fatal(err)
		}
		return proposal
	}
	plain := kept(func(*domain.ProposalRequest) {})
	plainCopy := *plain
	plainCopy.ID = ""
	coreEqualJSON(t, plainCopy, `{"id":"","title":"Fix login","named":true,"prompt":"Fix it","brief":{"intent":"fix the login test","doneWhen":"it passes","delivers":"commit"},"place":{"project":"project-a","worktreeId":"wt-lead"},"proposedAt":5}`, "plain")
	named := kept(func(r *domain.ProposalRequest) { r.Model, r.Role = new("gpt-6-luna"), new(domain.ThreadRoleReviewer) })
	if *named.Model != "gpt-6-luna" || *named.Engine != "codex" || *named.Role != domain.ThreadRoleReviewer || named.Effort != nil {
		t.Fatalf("named: %s", coreJSON(t, named))
	}
	coreEqualJSON(t, kept(func(r *domain.ProposalRequest) { r.Project, r.Worktree = new("/other"), new(true) }).Place, `{"project":"project-b","worktree":true}`, "by path")
	coreEqualJSON(t, kept(func(r *domain.ProposalRequest) { r.Worktree = new(false) }).Place, `{"project":"project-a","worktree":false}`, "shared checkout")
	for _, item := range []struct {
		edit func(*domain.ProposalRequest)
		want string
	}{
		{func(r *domain.ProposalRequest) { r.Brief = nil }, BriefRequired},
		{func(r *domain.ProposalRequest) { r.Project = new("nowhere") }, `No project matches "nowhere"`},
		{func(r *domain.ProposalRequest) { r.WorktreeID = new("wt-gone") }, WorktreeMissingError},
		{func(r *domain.ProposalRequest) { r.Project, r.WorktreeID = new(startsProject.ID), new("wt-other") }, WorktreeElsewhereError},
	} {
		if _, err := suggest(item.edit); err == nil || !strings.Contains(err.Error(), item.want) {
			t.Errorf("want %q, got %v", item.want, err)
		}
	}
	sent, err := ProposalSend(kept(func(r *domain.ProposalRequest) {
		r.Model, r.Effort, r.Role = new("gpt-6-luna"), new(domain.AgentEffort("low")), new(domain.ThreadRoleReviewer)
	}))
	if err != nil {
		t.Fatal(err)
	}
	coreEqualJSON(t, sent, `{"type":"task.send","text":"Fix it","title":"Fix login","brief":{"intent":"fix the login test","doneWhen":"it passes","delivers":"commit"},"project":"project-a","worktreeId":"wt-lead","engine":"codex","model":"gpt-6-luna","effort":"low","role":"reviewer"}`, "proposal send")
	plainSend, err := ProposalSend(plain)
	if err != nil {
		t.Fatal(err)
	}
	coreEqualJSON(t, startsStarted(t, ThreadStartsNewThreadSend(state, lead, *plainSend, jsonx.Some[*PairedComputer](nil))),
		startsStarted(t, ThreadStartsNewThreadSend(state, lead, startsSend(func(s *NewThreadSend) { s.Brief, s.Title = startsBrief, new("Fix login") }), jsonx.Opt[*PairedComputer]{})), "started as the coordinator would")
	gemini := *plain
	gemini.Engine = new("gemini")
	if _, err := ProposalSend(&gemini); err == nil || err.Error() != "This version can't start threads on gemini. Update to start it here." {
		t.Fatalf("unknown engine: %v", err)
	}
	untitled, err := ThreadProposal(state, lead, domain.ProposalRequest{Prompt: "Fix the login form", Brief: startsBrief}, 5, jsonx.Opt[*PairedComputer]{})
	if err != nil || untitled.Title != "Fix the login form" || untitled.Named != nil {
		t.Fatalf("untitled: %v %s", err, coreJSON(t, untitled))
	}
	if untitledSend, _ := ProposalSend(untitled); untitledSend.Title != nil {
		t.Fatal("an unnamed suggestion starts named")
	}
}

func TestThreadAskedForOnAPairedComputer(t *testing.T) {
	remote := &domain.Project{ID: "remote-project", Root: startsProject.Root, WorkspaceID: new("remote-workspace")}
	linux := startsLinux(coreWorkspace(func(s *WorkspaceState) { s.Projects = []*domain.Project{remote} }))
	state := startsCoordinated()
	state.Computers.Paired = []*PairedComputer{linux}
	made, err := ThreadProposal(state, threadsFind(state.Threads, "lead"), domain.ProposalRequest{Title: "GPU", Prompt: "Run the GPU tests", Brief: startsBrief, Worktree: new(true)}, 5, jsonx.Some(linux))
	if err != nil {
		t.Fatal(err)
	}
	coreEqualJSON(t, made.Place, `{"computer":{"id":"linux","name":"Linux"},"project":"remote-project","worktree":true}`, "place there")
	asked, err := ThreadProposal(state, threadsFind(state.Threads, "worker"), domain.ProposalRequest{Title: "Profile", Prompt: "Profile it"}, 5, jsonx.Some(linux))
	if err != nil || *asked.From != "worker" || asked.Brief != nil {
		t.Fatalf("asked: %v %s", err, coreJSON(t, asked))
	}
	if _, err := ThreadProposal(state, threadsFind(state.Threads, "solo"), domain.ProposalRequest{Title: "Mine", Prompt: "Go"}, 5, jsonx.Some(linux)); err == nil || err.Error() != "Only a coordinator and the threads under it ask for threads on another computer." {
		t.Fatalf("solo: %v", err)
	}
	if _, err := ThreadProposal(state, threadsFind(state.Threads, "lead"), domain.ProposalRequest{Title: "GPU", Prompt: "Go", Brief: startsBrief, Project: new("nowhere")}, 5, jsonx.Some(linux)); err == nil || !strings.HasPrefix(err.Error(), `On Linux: No project matches "nowhere"`) {
		t.Fatalf("unknown there: %v", err)
	}
}

func TestThreadUnderARemoteCoordinatorStartsHere(t *testing.T) {
	lead := &domain.HeldLead{RemoteLead: domain.RemoteLead{RemoteSender: domain.RemoteSender{ThreadID: "lead-there", Title: "Lead", Computer: "Laptop"}}}
	state := coreWorkspace(func(s *WorkspaceState) {
		s.Projects = []*domain.Project{startsProject}
		s.Threads = []*domain.Thread{coreTask("member", thProject(startsProject.ID), func(t *domain.Thread) {
			t.RemoteLead, t.Model, t.Effort = lead, new("sonnet"), new(domain.AgentEffort("low"))
		})}
	})
	command := startsStarted(t, ThreadStartsNewThreadSend(state, state.Threads[0], startsSend(), jsonx.Opt[*PairedComputer]{}))
	coreEqualJSON(t, command, `{"type":"task.send","text":"Fix it","engine":"claude","model":"sonnet","effort":"low","project":"project-a","lead":{"threadId":"lead-there","title":"Lead","computer":"Laptop"},"from":"member"}`, "remote led")
	allowed := *state.Threads[0]
	held := *lead
	held.ProposalID = new("p1")
	allowed.RemoteLead = &held
	if got := startsStarted(t, ThreadStartsNewThreadSend(state, &allowed, startsSend(), jsonx.Opt[*PairedComputer]{})).Lead; got.ProposalID != nil {
		t.Fatal("the suggestion crossed over")
	}
	linux := startsLinux(coreWorkspace())
	if got := startsRefused(t, ThreadStartsNewThreadSend(state, state.Threads[0], startsSend(), jsonx.Some(linux))); got != RemoteLedHere {
		t.Fatalf("refusal %q", got)
	}
}

func TestCoordinatorStartsTakeCoordinatorEfforts(t *testing.T) {
	state := startsCoordinated()
	solo := threadsFind(state.Threads, "solo")
	absent := jsonx.Opt[*PairedComputer]{}
	coordinator := func(s *NewThreadSend) { s.Role = new(domain.ThreadRoleCoordinator) }
	if got := startsRefused(t, ThreadStartsNewThreadSend(state, solo, startsSend(coordinator, func(s *NewThreadSend) { s.Model, s.Effort = new("gpt-6.2-sol"), new(domain.AgentEffort("ultra")) }), absent)); got != "The gpt-6.2-sol model does not support ultra effort for a coordinator." {
		t.Fatalf("refusal %q", got)
	}
	sol := *solo
	sol.Model, sol.Engine, sol.Effort = new("gpt-6.2-sol"), domain.AgentEngineCodex, new(domain.AgentEffort("ultra"))
	if got := startsStarted(t, ThreadStartsNewThreadSend(state, &sol, startsSend(coordinator), absent)).Effort; *got != "max" {
		t.Fatalf("inherited effort %s", *got)
	}
	if got := startsStarted(t, ThreadStartsNewThreadSend(state, solo, startsSend(func(s *NewThreadSend) { s.Model, s.Effort = new("gpt-6.2-sol"), new(domain.AgentEffort("ultra")) }), absent)).Effort; *got != "ultra" {
		t.Fatalf("effort %s", *got)
	}
	for _, edit := range []func(*NewThreadSend){func(s *NewThreadSend) { s.Worktree = new(true) }, func(s *NewThreadSend) { s.WorktreeID = new("wt-solo") }} {
		if got := startsRefused(t, ThreadStartsNewThreadSend(state, solo, startsSend(coordinator, edit), absent)); got != CoordinatorWorktreeStart {
			t.Fatalf("worktree refusal %q", got)
		}
	}
	started := startsStarted(t, ThreadStartsNewThreadSend(state, solo, startsSend(coordinator), absent))
	if *started.Project != startsProject.ID || started.WorktreeID != nil {
		t.Fatal("a coordinator took the caller's checkout")
	}
	if *startsStarted(t, ThreadStartsNewThreadSend(state, solo, startsSend(func(s *NewThreadSend) { s.Role = new(domain.ThreadRoleReviewer) }), absent)).WorktreeID != "wt-solo" {
		t.Fatal("a reviewer left the caller's checkout")
	}
	if _, err := ThreadProposal(state, threadsFind(state.Threads, "lead"), domain.ProposalRequest{Prompt: "Lead the release", Role: new(domain.ThreadRoleCoordinator), Worktree: new(true), Brief: startsBrief}, 1, absent); err == nil || err.Error() != CoordinatorWorktreeStart {
		t.Fatalf("suggested coordinator in a worktree: %v", err)
	}
}
