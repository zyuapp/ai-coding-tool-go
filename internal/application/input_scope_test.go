package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func scopeState() *WorkspaceState {
	return coreWorkspace(func(state *WorkspaceState) {
		state.Threads = []*domain.Thread{coreTask("current"), coreTask("other")}
		state.CurrentID = new("current")
		state.ActiveRuns = map[string]ActiveRun{"current": coreActive("current", "run")}
	})
}

func scopeOn(state *WorkspaceState, computer string) *WorkspaceState {
	copied := *state
	copied.Computers.Active = &computer
	return &copied
}

func TestScopeNamesTheThreadOnScreen(t *testing.T) {
	state := scopeState()
	coreEqualJSON(t, InputScopeInputScope(state, contracts.TaskCommandTaskSend{}).Input, contracts.TaskCommandTaskSend{TaskID: new("current")}, "send")
	coreEqualJSON(t, InputScopeInputScope(state, contracts.RunControlCommandRunCancel{}).Input, contracts.RunControlCommandRunCancel{TaskID: new("current")}, "cancel")
	coreEqualJSON(t, InputScopeInputScope(state, contracts.ViewCommandViewSetPrompt{Prompt: "typed"}).Input, contracts.ViewCommandViewSetPrompt{TaskID: new("current"), Prompt: "typed"}, "prompt")
	coreEqualJSON(t, InputScopeInputScope(state, contracts.ViewCommandViewFindOpen{}).Input, contracts.ViewCommandViewFindOpen{Target: domain.FindTargetThread{TaskID: new("current")}}, "find")
	send := contracts.TaskCommandTaskSend{Text: new("starts a new thread")}
	coreEqualJSON(t, InputScopeInputScope(state, send).Input, send, "new-thread send")
	coreEqualJSON(t, InputScopeInputScope(scopeOn(state, "elsewhere"), contracts.RunControlCommandRunCancel{}).Input, contracts.RunControlCommandRunCancel{}, "remote cancel")
}

func TestNamedCurrentThreadReducesAsTheImplicitOne(t *testing.T) {
	corePorted(t, func() {
		state := scopeState()
		for _, input := range []WorkspaceInput{contracts.RunControlCommandRunCancel{}, contracts.ViewCommandViewSetPrompt{Prompt: "typed"}} {
			implicit := Reduce(state, input)
			named := Reduce(state, InputScopeInputScope(state, input).Input)
			coreEqualJSON(t, named.State, implicit.State, "state")
			coreEqualJSON(t, named.Effects, implicit.Effects, "effects")
		}
	})
}

func TestOnlyTouchedThreadsOrderAnInput(t *testing.T) {
	corePorted(t, func() {
		state := scopeState()
		keys := func(input WorkspaceInput) []string { return InputScopeInputScope(state, input).Keys.Sorted() }
		if got := keys(contracts.TaskCommandTaskSelect{TaskID: "other"}); len(got) != 0 {
			t.Fatalf("select keys %v", got)
		}
		if got := keys(contracts.ViewCommandViewSetSettingsOpen{Open: true}); len(got) != 0 {
			t.Fatalf("settings keys %v", got)
		}
		if got := keys(contracts.RunControlCommandRunCancel{TaskID: new("other")}); !slices.Equal(got, []string{"other"}) {
			t.Fatalf("cancel keys %v", got)
		}
		events := WorkspaceEventAgentEvents{Events: []contracts.AgentEvent{
			contracts.RunEventRunStarted{RunEventBase: contracts.RunEventBase{TaskID: "current", RunID: "run", Sequence: 1}, AgentInitiated: new(true)},
			contracts.RunEventAssistantDelta{RunEventBase: contracts.RunEventBase{TaskID: "other", RunID: "other-run", Sequence: 1}, MessageID: "m", Text: "hi"},
		}}
		if got := keys(events); !slices.Equal(got, []string{"current", "other"}) {
			t.Fatalf("event keys %v", got)
		}
		cleared := *state
		cleared.Find = nil
		if query := InputScopeInputScope(&cleared, contracts.ViewCommandViewFindQuery{Query: "needle"}); !query.Keys.Has("view:find") {
			t.Fatal("a query follows the bar opened before it")
		}
	})
}

func TestUnnamedCommandRunsOnlyWhileItsScreenStays(t *testing.T) {
	corePorted(t, func() {
		state := scopeState()
		screen := ScreenOf(state)
		if got := InputScopeInputScope(state, contracts.SideChatCommandSideChatOpen{ChatID: "chat"}).Screen; got == nil || *got != screen {
			t.Fatal("side chat open is not held to the screen")
		}
		escape := InputScopeInputScope(state, contracts.ViewCommandViewEscape{})
		if !slices.Equal(escape.Keys.Sorted(), []string{"current"}) || escape.Screen == nil || *escape.Screen != screen {
			t.Fatalf("escape scope %v %v", escape.Keys.Sorted(), escape.Screen)
		}
		settings := *state
		settings.SettingsOpen = true
		if closing := InputScopeInputScope(&settings, contracts.ViewCommandViewEscape{}); len(closing.Keys) != 0 {
			t.Fatalf("closing keys %v", closing.Keys.Sorted())
		}
		if find := InputScopeInputScope(state, contracts.ViewCommandViewShortcut{Action: "find.open", Surface: domain.ShortcutSurfaceAny}); find.Screen == nil || *find.Screen != screen {
			t.Fatal("a keystroke is read against the screen it was pressed on")
		}
		other := *state
		other.CurrentID = new("other")
		tabbed := *state
		tabbed.KeyboardTab = new("other")
		for _, moved := range []*WorkspaceState{&other, scopeOn(state, "elsewhere"), &tabbed} {
			if ScreenOf(moved) == screen {
				t.Fatal("a moved screen reads the same")
			}
		}
	})
}

func TestPairedComputerCommandRunsOnlyWhileItsScreenStays(t *testing.T) {
	corePorted(t, func() {
		state := scopeState()
		remote := scopeOn(state, "elsewhere")
		screen := ScreenOf(remote)
		if got := InputScopeInputScope(remote, contracts.TaskCommandTaskSend{}).Screen; got == nil || *got != screen {
			t.Fatal("remote send not held to its screen")
		}
		if got := InputScopeInputScope(remote, contracts.RunControlCommandRunCancel{}).Screen; got == nil || *got != screen {
			t.Fatal("remote cancel not held to its screen")
		}
		if got := InputScopeInputScope(remote, contracts.TaskCommandTaskSend{Text: new("starts a new thread")}).Screen; got != nil {
			t.Fatal("a new-thread send is held to the screen")
		}
		if got := InputScopeInputScope(state, contracts.TaskCommandTaskSend{}).Screen; got != nil {
			t.Fatal("a named command needs no screen")
		}
		showing := func(current string) *WorkspaceState {
			copied := *remote
			shown := coreWorkspace(func(paired *WorkspaceState) {
				paired.Threads = []*domain.Thread{coreTask("a"), coreTask("b")}
				paired.CurrentID = &current
			})
			computer := &PairedComputer{State: shown}
			computer.ID = "elsewhere"
			computer.Status = domain.ComputerStatusConnected
			copied.Computers.Paired = []*PairedComputer{computer}
			return &copied
		}
		if ScreenOf(showing("a")) == ScreenOf(showing("b")) {
			t.Fatal("switching threads on the paired computer does not change the screen")
		}
	})
}
