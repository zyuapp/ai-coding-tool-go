package application

import (
	"slices"
	"testing"
)

const (
	panelsPhone  = `{"udid":"PHONE","name":"iPhone 17","runtime":"iOS 26.4","state":"shutdown"}`
	panelsPad    = `{"udid":"PAD","name":"iPad Air 11-inch (M4)","runtime":"iOS 26.4","state":"booted"}`
	panelsListed = `{"type":"simulator.listed","availability":{"status":"available"},"devices":[` + panelsPhone + `,` + panelsPad + `]}`
)

func panelsReleased(t *testing.T, transition WorkspaceTransition) []any {
	t.Helper()
	return panelsEffects(t, transition, func(kind string) bool { return kind == "simulator.release" })
}

func TestOpeningTheSimulatorPicksTheRunningDevice(t *testing.T) {
	panelsPorted(t, func() {
		opened, transition := panelsReduce(t, panelsRun(t, panelsWorkspace(), panelsListed), `{"type":"simulator.open"}`)
		if deref(panelsDock(opened).SimulatorID) != "PAD" || panelsDock(opened).Tab != SimulatorPanel || !panelsDock(opened).Open {
			t.Errorf("the running device is shown in the dock in front: %+v", panelsDock(opened))
		}
		panelsEqual(t, panelsEffects(t, transition, func(kind string) bool { return len(kind) > 9 && kind[:9] == "simulator" }), `[{"type":"simulator.list"}]`, "nothing already up is booted")
	})
}

func TestADevicePickedBeforeTheListIsChosenOnceItArrives(t *testing.T) {
	panelsPorted(t, func() {
		opened, _ := panelsReduce(t, panelsWorkspace(), `{"type":"simulator.open"}`)
		if panelsDock(opened).SimulatorID != nil {
			t.Fatal("no device yet")
		}
		arrived, transition := panelsReduce(t, opened, `{"type":"simulator.listed","availability":{"status":"available"},"devices":[`+panelsPhone+`]}`)
		if deref(panelsDock(arrived).SimulatorID) != "PHONE" || !slices.Equal(arrived.Simulator.Booting, []string{"PHONE"}) {
			t.Error("the default device is taken and booted")
		}
		panelsEqual(t, panelsEffectOf(t, transition, "simulator.boot"), `{"type":"simulator.boot","udid":"PHONE","followUp":true}`, "the boot is asked")
		booted, _ := panelsReduce(t, arrived, `{"type":"simulator.booted","udid":"PHONE"}`)
		if booted.Simulator.Devices[0].State != "booted" || len(booted.Simulator.Booting) != 0 {
			t.Error("the device is up")
		}
	})
}

func TestARunOpensTheSimulatorInItsOwnDock(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsRun(t, panelsWorkspace(panelsOn(panelsTask("mine"), panelsTask("agent"))), panelsListed)
		opened, _ := panelsReduce(t, state, `{"type":"simulator.open","taskId":"agent","device":"iphone 17"}`)
		agent := panelsDock(opened, "agent")
		if deref(agent.SimulatorID) != "PHONE" || !slices.Contains(agent.Panels, SimulatorPanel) || agent.Tab == SimulatorPanel || panelsDock(opened, "mine").SimulatorID != nil {
			t.Error("a run opens the panel in its own dock without taking the user's view, and names a device by name")
		}
		_, missing := panelsReduce(t, state, `{"type":"simulator.open","taskId":"agent","device":"Pixel"}`)
		panelsEqual(t, missing.Result, `{"ok":false,"message":"No simulator is called Pixel."}`, "an unknown name is refused")
	})
}

func TestARunsGestureMarksItDrivingUntilThatStretchGoesIdle(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsRun(t, panelsWorkspace(panelsOn(panelsTask("agent"))), panelsListed, `{"type":"simulator.open","taskId":"agent","device":"PAD"}`)
		tapped, transition := panelsReduce(t, state, `{"type":"simulator.input","taskId":"agent","input":{"kind":"tap","x":0.5,"y":0.5}}`)
		panelsEqual(t, panelsEffectOf(t, transition, "simulator.input"), `{"type":"simulator.input","udid":"PAD","input":{"kind":"tap","x":0.5,"y":0.5},"taskId":"agent","seq":1}`, "the gesture goes to the dock's device")
		panelsEqual(t, tapped.Simulator.Driver, `{"taskId":"agent","udid":"PAD","seq":1}`, "the run drives it")
		again := panelsRun(t, tapped, `{"type":"simulator.input","taskId":"agent","input":{"kind":"button","button":"home"}}`)
		if older, _ := panelsReduce(t, again, `{"type":"simulator.idle","seq":1}`); older.Simulator.Driver == nil || older.Simulator.Driver.Seq != 2 {
			t.Error("an older idle report cannot end newer driving")
		}
		if idle, _ := panelsReduce(t, again, `{"type":"simulator.idle","seq":2}`); idle.Simulator.Driver != nil {
			t.Error("the stretch ends")
		}
	})
}

func TestAGestureOnADeviceStillStarting(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsRun(t, panelsWorkspace(panelsOn(panelsTask("agent"))), panelsListed, `{"type":"simulator.select","udid":"PHONE"}`)
		touch := `{"kind":"touch","phase":"down","x":0.1,"y":0.1}`
		dropped, transition := panelsReduce(t, state, `{"type":"simulator.input","udid":"PHONE","input":`+touch+`}`)
		if len(transition.Effects) != 0 || dropped.Simulator.Driver != state.Simulator.Driver {
			t.Error("a person's pointer leaves the state alone")
		}
		_, refused := panelsReduce(t, state, `{"type":"simulator.input","taskId":"agent","input":`+touch+`}`)
		panelsEqual(t, refused.Result, `{"ok":false,"message":"The simulator is still starting."}`, "a run is told why")
	})
}

func TestAFailedBootIsShownInThePanel(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsRun(t, panelsWorkspace(), panelsListed, `{"type":"simulator.select","udid":"PHONE"}`)
		failed, _ := panelsReduce(t, state, `{"type":"simulator.failed","udid":"PHONE","message":"Unable to boot"}`)
		if failed.Simulator.Errors["PHONE"] != "Unable to boot" || failed.ActionError != nil {
			t.Error("the panel says it, not the workspace banner")
		}
		retried, transition := panelsReduce(t, failed, `{"type":"simulator.select","udid":"PHONE"}`)
		if _, held := retried.Simulator.Errors["PHONE"]; held {
			t.Error("trying again clears it")
		}
		panelsEqual(t, panelsEffectOf(t, transition, "simulator.boot"), `{"type":"simulator.boot","udid":"PHONE","followUp":true}`, "and boots again")
	})
}

func TestAnUnsupportedMacRefusesThePanel(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsRun(t, panelsWorkspace(), `{"type":"simulator.listed","availability":{"status":"unsupported","reason":"Install Xcode to use the simulator."},"devices":[]}`)
		_, refused := panelsReduce(t, state, `{"type":"simulator.open"}`)
		panelsEqual(t, refused.Result, `{"ok":false,"message":"Install Xcode to use the simulator."}`, "the reason is the answer")
	})
}

func TestTheDevicePickedIsRememberedAndATurnIsKept(t *testing.T) {
	panelsPorted(t, func() {
		picked, transition := panelsReduce(t, panelsRun(t, panelsWorkspace(), panelsListed), `{"type":"simulator.select","udid":"PHONE"}`)
		if deref(picked.SimulatorDevice) != "PHONE" || !panelsHasEffect(t, transition, `{"type":"persist-preferences","preferences":{"simulatorDevice":"PHONE"}}`) {
			t.Error("the device picked is remembered")
		}
		elsewhere, _ := panelsReduce(t, panelsRun(t, picked, `{"type":"simulator.booted","udid":"PHONE"}`, `{"type":"task.select","taskId":"other"}`), `{"type":"simulator.open"}`)
		if deref(panelsDock(elsewhere).SimulatorID) != "PHONE" {
			t.Error("not the iPad already running")
		}
		asked, transition := panelsReduce(t, elsewhere, `{"type":"simulator.input","udid":"PHONE","input":{"kind":"rotate","orientation":"landscape-left"}}`)
		panelsEqual(t, panelsEffectOf(t, transition, "simulator.input"), `{"type":"simulator.input","udid":"PHONE","input":{"kind":"rotate","orientation":"landscape-left"}}`, "the turn is asked")
		if _, turned := asked.Simulator.Orientations["PHONE"]; turned {
			t.Error("the panel turns once the device has, so a refused turn is never drawn")
		}
		turned, _ := panelsReduce(t, asked, `{"type":"simulator.turned","udid":"PHONE","orientation":"landscape-left"}`)
		if turned.Simulator.Orientations["PHONE"] != "landscape-left" {
			t.Error("the device took the turn")
		}
		if stillUp, _ := panelsReduce(t, turned, `{"type":"simulator.booted","udid":"PHONE"}`); stillUp.Simulator.Orientations["PHONE"] != "landscape-left" {
			t.Error("a boot that finds it already up leaves it turned")
		}
		wentDown, _ := panelsReduce(t, turned, `{"type":"simulator.listed","availability":{"status":"available"},"devices":[`+panelsPhone+`,`+panelsPad+`]}`)
		if _, kept := wentDown.Simulator.Orientations["PHONE"]; kept {
			t.Error("a device that went down comes back up held upright")
		}
	})
}

func TestADeviceIsLetGoOnceNoDockShowsIt(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsRun(t, panelsWorkspace(panelsOn(panelsTask("mine"), panelsTask("agent"))), panelsListed, `{"type":"simulator.open","device":"PAD"}`, `{"type":"simulator.open","taskId":"agent","device":"PAD"}`)
		closedOne, transition := panelsReduce(t, state, `{"type":"view.close-dock-panel","panel":"simulator"}`)
		panelsEqual(t, panelsReleased(t, transition), `[]`, "the run's dock still shows it")
		_, transition = panelsReduce(t, panelsRun(t, closedOne, `{"type":"task.select","taskId":"agent"}`), `{"type":"view.close-dock-panel","panel":"simulator"}`)
		panelsEqual(t, panelsReleased(t, transition), `[{"type":"simulator.release","udid":"PAD"}]`, "no dock shows it any more")
		_, switched := panelsReduce(t, panelsRun(t, panelsWorkspace(), panelsListed, `{"type":"simulator.open","device":"PAD"}`), `{"type":"simulator.select","udid":"PHONE"}`)
		panelsEqual(t, panelsReleased(t, switched), `[{"type":"simulator.release","udid":"PAD"}]`, "moving on lets it go")
	})
}
