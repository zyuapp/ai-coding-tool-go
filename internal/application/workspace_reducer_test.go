package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestEffortKeysStepAnotherComputersThreadFromWhatItRunsOn(t *testing.T) {
	corePorted(t, func() {
		stepped := func(edit func(*domain.Thread), action string) []WorkspaceInput {
			remote := coreWorkspace(viewThreads(coreTask("r1", edit)), func(state *WorkspaceState) { state.CurrentID = new("r1") })
			state := coreWorkspace(func(state *WorkspaceState) {
				state.DraftModel, state.DraftEffort = "opus", "high"
				computer := &PairedComputer{State: remote}
				computer.ComputerLink = domain.ComputerLink{ID: "linux", Name: "Linux", Host: "linux.ts.net", Status: domain.ComputerStatusConnected, PairedAt: 1, Capabilities: contracts.ComputerCapabilitiesComputerCapabilities()}
				state.Computers.Name = "Mac"
				state.Computers.Active = new("linux")
				state.Computers.Paired = []*PairedComputer{computer}
			})
			inputs := []WorkspaceInput{}
			for _, effect := range Reduce(state, contracts.ViewCommandViewShortcut{Action: action, Surface: domain.ShortcutSurfaceAny}).Effects {
				if forward, ok := effect.(WorkspaceEffectComputerForward); ok {
					inputs = append(inputs, forward.Inputs...)
				}
			}
			return inputs
		}
		on := func(model domain.AgentModel, effort domain.AgentEffort) func(*domain.Thread) {
			return func(thread *domain.Thread) {
				thread.Model = &model
				thread.Effort = &effort
			}
		}
		coreEqualJSON(t, stepped(on("opus", "low"), "effort.increase"), `[{"type":"task.set-effort","engine":"claude","effort":"medium"}]`, "stepped")
		coreEqualJSON(t, stepped(on("opus-next", "high"), "effort.increase"), `[]`, "unknown model")
		coreEqualJSON(t, stepped(on("opus", "galaxy"), "effort.decrease"), `[]`, "unknown effort")
	})
}

func TestForkDialogConfirmsUnderTheTypedName(t *testing.T) {
	corePorted(t, func() {
		before := coreWorkspace(viewThreads(coreTask("first", func(thread *domain.Thread) { thread.Title = "Fix the login" })), func(state *WorkspaceState) { state.CurrentID = new("first") })
		asked := coreRun(before, contracts.ViewCommandViewForkName{SourceID: new("first")})
		coreEqualJSON(t, asked.ForkName, `{"taskId":"first","worktree":false,"title":"Fix the login (fork)"}`, "asked")
		state := coreRun(asked, contracts.ViewCommandViewForkConfirm{Title: "  Try OAuth  "})
		if len(state.Threads) != 2 || state.ForkName != nil {
			t.Fatal("not forked")
		}
		fork := findThread(state.Threads, state.CurrentID)
		if fork == nil || fork.ID == "first" || fork.Title != "Try OAuth" {
			t.Fatal("the fork is not on screen under the typed name")
		}
		blank := coreRun(asked, contracts.ViewCommandViewForkConfirm{Title: "   "})
		if len(blank.Threads) != 1 || blank.ForkName == nil {
			t.Fatal("a blank name forked or closed the dialog")
		}
	})
}

func TestArchiveConfirmWithNothingAskedDoesNothing(t *testing.T) {
	state := coreWorkspace(viewThreads(coreTask("a")))
	if next := Reduce(state, contracts.ViewCommandViewArchiveConfirm{}); !sameState(&next.State, state) || len(next.Effects) != 0 {
		t.Fatal("confirming nothing changed something")
	}
}

func TestEscapeClaimsTheNearestLayer(t *testing.T) {
	escape := func(edit func(*WorkspaceState)) string {
		return coreJSON(t, EscapeCommands(coreWorkspace(viewThreads(coreTask("a")), edit)))
	}
	cases := []struct {
		edit func(*WorkspaceState)
		want string
	}{
		{func(state *WorkspaceState) { state.ProjectAdd = &ProjectAddState{} }, `[{"type":"view.add-project-key","key":"Escape"}]`},
		{func(state *WorkspaceState) { state.ViewingImage = new("image") }, `[{"type":"image.close"}]`},
		{func(state *WorkspaceState) { state.Jump = &JumpState{} }, `[{"type":"view.jump-close"}]`},
		{func(state *WorkspaceState) { state.OpenMenu = new("menu") }, `[{"type":"view.set-menu","menu":null}]`},
		{func(state *WorkspaceState) { state.SettingsOpen = true }, `[{"type":"view.set-settings-open","open":false}]`},
		{func(state *WorkspaceState) { state.Find = &FindState{Target: domain.FindTargetThread{}} }, `[{"type":"view.find-close"}]`},
		{func(state *WorkspaceState) { state.CurrentID = new("a") }, `[{"type":"run.cancel"}]`},
		{func(*WorkspaceState) {}, `[{"type":"run.cancel"}]`},
	}
	for _, c := range cases {
		corePorted(t, func() {
			if got := escape(c.edit); got != c.want {
				t.Errorf("escape %s, want %s", got, c.want)
			}
		})
	}
}

func TestAgentEventsFoldInArrivalOrder(t *testing.T) {
	corePorted(t, func() {
		reload := func(status contracts.AgentSettingsReloadEventStatus) contracts.AgentEvent {
			return contracts.AgentSettingsReloadEvent{Status: status}
		}
		state := coreRun(EmptyWorkspaceState(nil), contracts.EngineCommandEngineReloadSettings{})
		folded := Reduce(state, WorkspaceEventAgentEvents{Events: []contracts.AgentEvent{reload("pending"), reload("reloaded")}})
		if folded.State.AgentSettingsReload != WorkspaceStateAgentSettingsReloadReloaded {
			t.Fatalf("folded to %s", folded.State.AgentSettingsReload)
		}
		if _, ok := AgentEventInput(contracts.RunEventRunStarted{}).(WorkspaceEventRunEvent); !ok {
			t.Fatal("a run's report is not a run event")
		}
	})
}
