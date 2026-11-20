package application

import (
	"reflect"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestACommandAboutAWholeComputerIsCarriedThere(t *testing.T) {
	cmpPorted(t, func() {
		all := contracts.ComputerCapabilitiesComputerCapabilities()
		withAll := func(link *domain.ComputerLink) { link.Capabilities = all }
		state := cmpWithComputers(cmpWorkspace(), []*PairedComputer{
			cmpPaired("mac", cmpWorkspace(), withAll),
			cmpPaired("old", cmpWorkspace(), func(link *domain.ComputerLink) { link.Capabilities = []string{"command:task.send"} }),
			cmpPaired("away", cmpWorkspace(), withAll, cmpStatus(domain.ComputerStatusOffline)),
		})
		mac := *state.Computers.Paired[0]
		cmpSameJSON(t, RouteInput(state, contracts.ExternalAppCommandAppUpdate{ComputerID: new("mac")}), InputRouteComputer{Computer: mac, Inputs: []WorkspaceInput{contracts.ExternalAppCommandAppUpdate{}}}, "app.update")
		cmpSameJSON(t, RouteInput(state, contracts.EngineCommandEngineUpdate{Engine: domain.AgentEngineCodex, ComputerID: new("mac")}), InputRouteComputer{Computer: mac, Inputs: []WorkspaceInput{contracts.EngineCommandEngineUpdate{Engine: domain.AgentEngineCodex}}}, "engine.update")
		cmpSameJSON(t, RouteInput(state, contracts.ExternalAppCommandAppCancelUpdate{ComputerID: new("old")}), InputRouteRefuse{Reason: new("unsupported"), Message: contracts.RemoteUnsupported}, "old")
		if _, ok := RouteInput(state, contracts.ExternalAppCommandAppUpdate{ComputerID: new("away")}).(InputRouteRefuse); !ok {
			t.Error("a computer that is away refuses")
		}
		cmpSameJSON(t, RouteInput(state, contracts.ExternalAppCommandAppUpdate{}), local, "app.update here")
		cmpSameJSON(t, RouteInput(state, contracts.EngineCommandEngineUpdate{Engine: domain.AgentEngineCodex}), local, "engine.update here")
	})
}

type upkeepLine struct {
	Version *string
	Text    string
	Action  string
}

func TestAPairedComputersRowListsItsAppAndEngines(t *testing.T) {
	cmpPorted(t, func() {
		own := &domain.AppBuild{Version: "0.7.11", SelfUpdating: true}
		upkeep := func(remote func(*WorkspaceState), links ...func(*domain.ComputerLink)) *ComputerUpkeep {
			state := cmpWorkspace(func(state *WorkspaceState) { state.AppBuild = &domain.AppBuild{Version: "0.7.10", SelfUpdating: true} }, remote)
			links = append([]func(*domain.ComputerLink){func(link *domain.ComputerLink) {
				link.Capabilities = contracts.ComputerCapabilitiesComputerCapabilities()
			}}, links...)
			return ComputerUpdatesComputerUpkeep([]*PairedComputer{cmpPaired("mac", state, links...)}, own)[0]
		}
		lines := func(remote func(*WorkspaceState), links ...func(*domain.ComputerLink)) map[string]upkeepLine {
			lines := map[string]upkeepLine{}
			for _, item := range upkeep(remote, links...).Items {
				line := upkeepLine{Version: item.Version, Text: item.State.Text}
				if item.Action != nil {
					line.Action = item.Action.Label
				}
				lines[item.Key] = line
			}
			return lines
		}
		app := func(remote func(*WorkspaceState), links ...func(*domain.ComputerLink)) upkeepLine {
			return lines(remote, links...)["app"]
		}
		check := func(got, want any, label string) {
			t.Helper()
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s:\n got %+v\nwant %+v", label, got, want)
			}
		}
		status := func(status *domain.EngineStatus) func(*WorkspaceState) {
			return func(state *WorkspaceState) { state.EngineStatus = status }
		}
		v := func(text string) *string { return &text }

		check(lines(status(&domain.EngineStatus{Codex: &domain.EngineReadiness{Access: domain.EngineAccessMissing}, Claude: &domain.EngineReadiness{Access: domain.EngineAccessReady, Version: v("2.1.301")}})), map[string]upkeepLine{
			"app":    {v("0.7.10"), "Up to date", ""},
			"claude": {v("2.1.301"), "Up to date", ""},
			"codex":  {nil, "Not installed", ""},
		}, "a build that knows fewer engines lists only its own")
		checking := map[string]upkeepLine{"app": {v("0.7.10"), "Up to date", ""}}
		for _, engine := range domain.AgentEngines {
			checking[string(engine)] = upkeepLine{nil, "Checking…", ""}
		}
		check(lines(status(nil)), checking, "until it answers")
		check(lines(func(state *WorkspaceState) {
			state.EngineUpdateQueue = []domain.AgentEngine{"gemini"}
			state.EngineStatus = &domain.EngineStatus{Claude: &domain.EngineReadiness{Access: domain.EngineAccessReady, Version: v("2.1.301")}}
		})["gemini"],
			upkeepLine{nil, "Waiting to update", ""}, "an engine only a newer build knows is named by its id")

		available := domain.AppUpdateAvailable{Version: "0.7.11"}
		ready := domain.AppUpdateReady{Version: "0.7.11"}
		check(app(func(state *WorkspaceState) { state.AppUpdate = available }), upkeepLine{v("0.7.10"), "0.7.11 available", "Update"}, "available")
		check(app(func(state *WorkspaceState) {
			state.AppUpdate, state.AppUpdateRequest = domain.AppUpdateDownloading{Version: "0.7.11", Percent: 42}, domain.AppUpdateRequestWaiting{}
		}), upkeepLine{v("0.7.10"), "Downloading 0.7.11 · 42%", ""}, "downloading")
		check(app(func(state *WorkspaceState) {
			state.AppUpdate, state.AppUpdateRequest = available, domain.AppUpdateRequestFailed{Message: "Couldn't download 0.7.11."}
		}), upkeepLine{v("0.7.10"), "Couldn't download 0.7.11", "Try again"}, "failed")
		check(app(func(state *WorkspaceState) { state.AppBuild = &domain.AppBuild{Version: "0.7.9"} }), upkeepLine{v("0.7.9"), "0.7.11 available · update it there", ""}, "not self-updating")
		check(app(func(state *WorkspaceState) { state.AppBuild = nil }, func(link *domain.ComputerLink) {
			link.Capabilities = []string{"command:task.send", "command:engine.update", "command:engine.update:engine"}
		}), upkeepLine{nil, "Update it there once to manage it from here", ""}, "an old build")
		cmpPorted(t, func() {
			check(app(func(state *WorkspaceState) {
				state.AppUpdate, state.AppUpdateRequest = ready, domain.AppUpdateRequestWaiting{}
				state.ActiveRuns = map[string]ActiveRun{"a": cmpRun("a", "r"), "b": cmpRun("b", "s")}
			}), upkeepLine{v("0.7.10"), "Waiting for 2 threads", "Cancel"}, "waiting")
		})

		engines := func(engineStatus *domain.EngineStatus, extra ...func(*WorkspaceState)) [2]upkeepLine {
			all := lines(func(state *WorkspaceState) {
				state.EngineStatus = engineStatus
				for _, edit := range extra {
					edit(state)
				}
			})
			return [2]upkeepLine{all["claude"], all["codex"]}
		}
		check(engines(&domain.EngineStatus{
			Claude: &domain.EngineReadiness{Access: domain.EngineAccessReady, Version: v("2.1.0"), Required: v("2.1.250"), Latest: v("2.2.0"), Fix: v("claude update")},
			Codex:  &domain.EngineReadiness{Access: domain.EngineAccessOutdated, Version: v("0.160.0"), Required: v("0.162.1"), Fix: v("npm i -g @openai/codex")},
		}), [2]upkeepLine{{v("2.1.0"), "Needs 2.1.250 for newer models", "Update"}, {v("0.160.0"), "Needs 0.162.1 to run", "Update"}}, "required")
		check(engines(&domain.EngineStatus{
			Claude: &domain.EngineReadiness{Access: domain.EngineAccessReady, Version: v("2.1.291"), Latest: v("2.1.301")},
			Codex:  &domain.EngineReadiness{Access: domain.EngineAccessReady, Version: v("0.162.1"), Latest: v("0.163.0"), Fix: v("npm i -g @openai/codex")},
		}), [2]upkeepLine{{v("2.1.291"), "2.1.301 available · update it there", ""}, {v("0.162.1"), "0.163.0 available", "Update"}}, "latest")
		check(engines(&domain.EngineStatus{Codex: &domain.EngineReadiness{Access: domain.EngineAccessReady, Version: v("0.162.1"), Latest: v("0.163.0"), Fix: v("npm i -g @openai/codex")}}, func(state *WorkspaceState) {
			state.EngineUpdating, state.EngineUpdateQueue = new(domain.AgentEngineClaude), []domain.AgentEngine{domain.AgentEngineCodex}
		}), [2]upkeepLine{{nil, "Updating…", ""}, {v("0.162.1"), "Waiting to update", ""}}, "updating")
		var command contracts.AppCommand
		for _, item := range upkeep(status(&domain.EngineStatus{Codex: &domain.EngineReadiness{Access: domain.EngineAccessReady, Version: v("0.162.1"), Latest: v("0.163.0"), Fix: v("npm i")}})).Items {
			if item.Key == "codex" && item.Action != nil {
				command = item.Action.Command
			}
		}
		cmpSameJSON(t, command, contracts.EngineCommandEngineUpdate{Engine: domain.AgentEngineCodex, ComputerID: new("mac")}, "the step names that computer")

		texts := func(row *ComputerUpkeep) []string {
			var parts []string
			for _, part := range row.Status {
				parts = append(parts, part.Text)
			}
			return parts
		}
		check(texts(upkeep(func(*WorkspaceState) {})), []string{"Connected"}, "connected")
		restarting := upkeep(func(state *WorkspaceState) {
			state.AppUpdateRequest = domain.AppUpdateRequestInstalling{Version: "0.7.11"}
		}, cmpStatus(domain.ComputerStatusConnecting))
		check([]any{texts(restarting), restarting.Action, len(restarting.Items)}, []any{[]string{"Restarting into 0.7.11…"}, (*UpkeepAction)(nil), 0}, "it comes back on its own")
		away := upkeep(func(*WorkspaceState) {}, cmpStatus(domain.ComputerStatusOffline, "The bridge was turned off."))
		check([]any{texts(away), away.Action.Label, len(away.Items)}, []any{[]string{"Offline", "The bridge was turned off"}, "Reconnect", 0}, "away")
	})
}

func TestSameComputerUpkeepComparesWhatThePageShows(t *testing.T) {
	quiet := UpkeepPartToneQuiet
	row := func(text string) []*ComputerUpkeep {
		return []*ComputerUpkeep{{ID: "mac", Status: []*UpkeepPart{{Text: "Connected"}}, Items: []*UpkeepItem{{Key: "app", State: UpkeepPart{Text: text, Tone: &quiet}, Action: &UpkeepAction{Label: "Update", Command: contracts.ExternalAppCommandAppUpdate{ComputerID: new("mac")}}}}}}
	}
	if !SameComputerUpkeep(row("Up to date"), row("Up to date")) {
		t.Error("rows that read the same are the same")
	}
	if SameComputerUpkeep(row("Up to date"), row("0.7.11 available")) {
		t.Error("a changed line is not")
	}
	other := row("Up to date")
	other[0].Items[0].Action.Command = contracts.ExternalAppCommandAppUpdate{ComputerID: new("old")}
	if SameComputerUpkeep(row("Up to date"), other) {
		t.Error("a step for another computer is not")
	}
}

// cmpRun is a run going on `taskID`, as the TS fixtures' activeRun() makes one.
func cmpRun(taskID, runID string) ActiveRun {
	return ActiveRunRunning{
		TaskID: taskID, RunID: runID, Status: ActiveRunRunningStatusRunning, Origin: ActiveRunRunningOriginComposer,
		ReportedIssues: []string{}, Before: ThreadMark{UpdatedAt: 1},
	}
}
