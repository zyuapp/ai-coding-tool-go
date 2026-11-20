package application

import (
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// SimulatorInputs are the input types ReduceSimulator takes.
var SimulatorInputs = []string{
	"simulator.open", "simulator.refresh", "simulator.select", "simulator.input",
	"simulator.listed", "simulator.booted", "simulator.failed", "simulator.refused", "simulator.turned", "simulator.idle",
}

// shownDevices is the devices some dock's Simulator panel shows.
func shownDevices(docks map[string]*ThreadDock) jsonx.Set[string] {
	shown := jsonx.Set[string]{}
	for _, dock := range docks {
		if dock.SimulatorID != nil && *dock.SimulatorID != "" && slices.Contains(dock.Panels, SimulatorPanel) {
			shown[*dock.SimulatorID] = struct{}{}
		}
	}
	return shown
}

// ReconcileSimulators: a device no dock shows any more is let go, however its panel closed or moved on.
func ReconcileSimulators(previous *WorkspaceState, transition WorkspaceTransition) WorkspaceTransition {
	if jsonx.Same(previous.Docks, transition.State.Docks) {
		return transition
	}
	after := shownDevices(transition.State.Docks)
	var released []WorkspaceEffect
	for _, udid := range shownDevices(previous.Docks).Sorted() {
		if !after.Has(udid) {
			released = append(released, WorkspaceEffectSimulatorRelease{UDID: udid})
		}
	}
	if len(released) == 0 {
		return transition
	}
	transition.Effects = slices.Concat(transition.Effects, released)
	return transition
}

const SimulatorUnsupported = "The simulator needs Xcode on a Mac."
const SimulatorNotOpen = "Open the simulator first."
const SimulatorStarting = "The simulator is still starting."

// simulatorWork is what an input has made of the simulator and the docks so far, and the work it
// asks for (TS: simulator.ts Work).
type simulatorWork struct {
	Sim     SimulatorSliceState
	Docks   DocksEdit
	Effects []WorkspaceEffect
}

func simulatorWorkOf(state *WorkspaceState) simulatorWork {
	return simulatorWork{Sim: SimulatorSliceState{Simulator: state.Simulator, SimulatorDevice: state.SimulatorDevice}, Docks: DocksOf(state), Effects: []WorkspaceEffect{}}
}

// simulatorDone is the answer a piece of work comes to, naming only what it changed. `result` may be nil.
func simulatorDone(state *WorkspaceState, work simulatorWork, result contracts.WorkspaceCommandResult) SliceTransition {
	sim, docks := work.Sim, work.Docks
	ownChanged := !sameValue(reflect.ValueOf(sim.Simulator), reflect.ValueOf(state.Simulator)) || !sameOptionalString(sim.SimulatorDevice, state.SimulatorDevice)
	docksChanged := !jsonx.Same(docks.View.Docks, state.Docks) || docks.View.DockFocus != state.DockFocus
	transition := SliceTransition{Effects: work.Effects, Result: result}
	if ownChanged {
		transition.Patch = func(next *WorkspaceState) {
			next.Simulator = sim.Simulator
			next.SimulatorDevice = sim.SimulatorDevice
		}
	}
	if docksChanged {
		transition.Edits = []OwnerEdit{docks}
	}
	return transition
}

// withSimulator is `sim` with `patch` made to a copy of its simulator.
func withSimulator(sim SimulatorSliceState, patch func(next *domain.SimulatorState)) SimulatorSliceState {
	patch(&sim.Simulator)
	return sim
}

// without is `record` without `udid`, the same map when it has none.
func without[Value any](record map[string]Value, udid string) map[string]Value {
	if _, ok := record[udid]; !ok {
		return record
	}
	rest := maps.Clone(record)
	delete(rest, udid)
	return rest
}

// remember: the device picked or opened by name is the one a dock with none shows from then on,
// across launches.
func remember(state *WorkspaceState, work simulatorWork, udid string) simulatorWork {
	if work.Sim.SimulatorDevice != nil && *work.Sim.SimulatorDevice == udid {
		return work
	}
	sim := work.Sim
	sim.SimulatorDevice = new(udid)
	after := Afterward(state, SliceTransition{
		Patch: func(next *WorkspaceState) {
			next.Simulator = sim.Simulator
			next.SimulatorDevice = sim.SimulatorDevice
		},
		Edits: []OwnerEdit{work.Docks},
	})
	work.Sim = sim
	work.Effects = slices.Concat(work.Effects, PersistView(after))
	return work
}

func fallbackDevice(sim SimulatorSliceState) *domain.SimulatorDevice {
	return domain.DefaultSimulator(sim.Simulator.Devices, sim.SimulatorDevice)
}

// refused is a refusal shown in the panel rather than the workspace banner, and answered to whoever asked.
func refused(message string) SliceTransition {
	return SliceTransition{Result: contracts.WorkspaceCommandResultFalse{Message: message}}
}

func device(sim SimulatorSliceState, udid string) *domain.SimulatorDevice {
	for _, item := range sim.Simulator.Devices {
		if item.UDID == udid {
			return item
		}
	}
	return nil
}

// named is a device by its UDID, else by its name, newest runtime first.
func named(sim SimulatorSliceState, name string) *domain.SimulatorDevice {
	if found := device(sim, name); found != nil {
		return found
	}
	wanted := strings.ToLower(runsJSTrim(name))
	for _, item := range sim.Simulator.Devices {
		if strings.ToLower(item.UDID) == wanted || strings.ToLower(item.Name) == wanted {
			return item
		}
	}
	return nil
}

// boot: booting is idempotent on the Mac's side, so it is only skipped for a device already up or on its way.
func boot(work simulatorWork, udid string) simulatorWork {
	sim := work.Sim
	if known := device(sim, udid); (known != nil && known.State == domain.SimulatorDeviceStateBooted) || slices.Contains(sim.Simulator.Booting, udid) {
		return work
	}
	work.Sim = withSimulator(sim, func(next *domain.SimulatorState) {
		next.Booting = append(slices.Clip(next.Booting), udid)
		next.Errors = without(next.Errors, udid)
	})
	work.Effects = slices.Concat(work.Effects, []WorkspaceEffect{WorkspaceEffectSimulatorBoot{UDID: udid, FollowUp: new(true)}})
	return work
}

// openIn shows the panel in a dock on the device it names (nil for none). Only the user's own open
// brings the panel to the front.
func openIn(work simulatorWork, owner string, udid *string, byUser bool) simulatorWork {
	dock := DocksDock(work.Docks.View, owner)
	panels := dock.Panels
	if !slices.Contains(panels, SimulatorPanel) {
		panels = append(slices.Clip(panels), SimulatorPanel)
	}
	placed := EditDock(work.Docks, owner, func(dock *ThreadDock) {
		dock.Panels = panels
		dock.SimulatorID = udid
	})
	shown, focusEffects := placed, []WorkspaceEffect{}
	if byUser {
		shown, focusEffects = FocusDockTabIn(ShowDockTabIn(placed, owner, SimulatorPanel), owner, SimulatorPanel)
	}
	work.Docks = shown
	work.Effects = slices.Concat(work.Effects, []WorkspaceEffect{WorkspaceEffectSimulatorList{}}, focusEffects)
	if udid != nil && *udid != "" {
		return boot(work, *udid)
	}
	return work
}

// adoptDefaults: a dock showing the panel with nothing picked yet takes the default device once
// there is a list to pick from.
func adoptDefaults(work simulatorWork) simulatorWork {
	fallback := fallbackDevice(work.Sim)
	if fallback == nil {
		return work
	}
	next := work
	docks := work.Docks.View.Docks
	for _, owner := range slices.Sorted(maps.Keys(docks)) {
		dock := docks[owner]
		if !slices.Contains(dock.Panels, SimulatorPanel) {
			continue
		}
		if dock.SimulatorID != nil && *dock.SimulatorID != "" && device(next.Sim, *dock.SimulatorID) != nil {
			continue
		}
		next.Docks = EditDock(next.Docks, owner, func(dock *ThreadDock) { dock.SimulatorID = new(fallback.UDID) })
		next = boot(next, fallback.UDID)
	}
	return next
}

func turn(sim SimulatorSliceState, udid string, orientation domain.SimulatorOrientation) SimulatorSliceState {
	if held, ok := sim.Simulator.Orientations[udid]; ok && held == orientation {
		return sim
	}
	return withSimulator(sim, func(next *domain.SimulatorState) {
		orientations := maps.Clone(next.Orientations)
		if orientations == nil {
			orientations = map[string]domain.SimulatorOrientation{}
		}
		orientations[udid] = orientation
		next.Orientations = orientations
	})
}

func patchDevice(sim SimulatorSliceState, udid string, patch func(next *domain.SimulatorDevice)) []*domain.SimulatorDevice {
	devices := make([]*domain.SimulatorDevice, len(sim.Simulator.Devices))
	for index, item := range sim.Simulator.Devices {
		if item.UDID == udid {
			patched := *item
			patch(&patched)
			devices[index] = &patched
		} else {
			devices[index] = item
		}
	}
	return devices
}

// ReduceSimulator takes an input whose type is one of SimulatorInputs.
func ReduceSimulator(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	work := simulatorWorkOf(state)
	sim := work.Sim
	simPatch := func(next SimulatorSliceState) OwnPatch {
		return func(state *WorkspaceState) {
			state.Simulator = next.Simulator
			state.SimulatorDevice = next.SimulatorDevice
		}
	}
	switch input := input.(type) {
	case contracts.SimulatorCommandSimulatorOpen:
		if unsupported, ok := sim.Simulator.Availability.(domain.SimulatorAvailabilityUnsupported); ok {
			return refused(unsupported.Reason)
		}
		owner := DockOwner(state, input.TaskID)
		var picked *domain.SimulatorDevice
		if input.Device != nil {
			picked = named(sim, *input.Device)
			if picked == nil {
				return refused("No simulator is called " + *input.Device + ".")
			}
		}
		var udid *string
		switch {
		case picked != nil:
			udid = new(picked.UDID)
		case DockFor(state, owner).SimulatorID != nil:
			udid = DockFor(state, owner).SimulatorID
		default:
			if fallback := fallbackDevice(sim); fallback != nil {
				udid = new(fallback.UDID)
			}
		}
		opened := openIn(work, owner, udid, input.TaskID == nil)
		if picked != nil {
			opened = remember(state, opened, picked.UDID)
		}
		return simulatorDone(state, opened, nil)

	case contracts.SimulatorCommandSimulatorRefresh:
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectSimulatorList{}}}

	case contracts.SimulatorCommandSimulatorSelect:
		if device(sim, input.UDID) == nil {
			return SliceTransition{}
		}
		owner := DockOwner(state, nil)
		udid := input.UDID
		placed := work
		placed.Docks = EditDock(work.Docks, owner, func(dock *ThreadDock) { dock.SimulatorID = new(udid) })
		return simulatorDone(state, remember(state, boot(placed, udid), udid), nil)

	case contracts.SimulatorCommandSimulatorInput:
		owner := DockOwner(state, input.TaskID)
		udid := input.UDID
		if udid == nil {
			udid = DockFor(state, owner).SimulatorID
		}
		if udid == nil || *udid == "" {
			return refused(SimulatorNotOpen)
		}
		// A person's pointer over a device still starting goes nowhere; a run is told why.
		if target := device(sim, *udid); target != nil && target.State != domain.SimulatorDeviceStateBooted {
			if input.TaskID != nil && *input.TaskID != "" {
				return refused(SimulatorStarting)
			}
			return SliceTransition{}
		}
		if input.TaskID == nil {
			return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectSimulatorInput{UDID: *udid, Input: input.Input}}}
		}
		seq := 1.0
		if sim.Simulator.Driver != nil {
			seq = sim.Simulator.Driver.Seq + 1
		}
		driver := &domain.SimulatorStateDriver{TaskID: *input.TaskID, UDID: *udid, Seq: seq}
		return SliceTransition{
			Patch:   simPatch(withSimulator(sim, func(next *domain.SimulatorState) { next.Driver = driver })),
			Effects: []WorkspaceEffect{WorkspaceEffectSimulatorInput{UDID: *udid, Input: input.Input, TaskID: input.TaskID, Seq: new(seq)}},
		}

	case SimulatorEventSimulatorListed:
		booted := func(udid string) bool {
			return slices.ContainsFunc(input.Devices, func(item *domain.SimulatorDevice) bool {
				return item.UDID == udid && item.State == domain.SimulatorDeviceStateBooted
			})
		}
		booting := []string{}
		for _, udid := range sim.Simulator.Booting {
			if slices.ContainsFunc(input.Devices, func(item *domain.SimulatorDevice) bool {
				return item.UDID == udid && item.State != domain.SimulatorDeviceStateBooted
			}) {
				booting = append(booting, udid)
			}
		}
		// A device that went down comes back up held upright.
		orientations := map[string]domain.SimulatorOrientation{}
		for udid, orientation := range sim.Simulator.Orientations {
			if booted(udid) {
				orientations[udid] = orientation
			}
		}
		listed := work
		listed.Sim = withSimulator(sim, func(next *domain.SimulatorState) {
			next.Availability = input.Availability
			next.Devices = input.Devices
			next.Booting = booting
			next.Orientations = orientations
		})
		return simulatorDone(state, adoptDefaults(listed), nil)

	case SimulatorEventSimulatorBooted:
		return SliceTransition{Patch: simPatch(withSimulator(sim, func(next *domain.SimulatorState) {
			next.Devices = patchDevice(sim, input.UDID, func(item *domain.SimulatorDevice) { item.State = domain.SimulatorDeviceStateBooted })
			if known := device(sim, input.UDID); known == nil || known.State != domain.SimulatorDeviceStateBooted {
				next.Orientations = without(sim.Simulator.Orientations, input.UDID)
			}
			next.Booting = slices.DeleteFunc(slices.Clone(sim.Simulator.Booting), func(udid string) bool { return udid == input.UDID })
			next.Errors = without(sim.Simulator.Errors, input.UDID)
		}))}

	case SimulatorEventSimulatorFailed:
		return SliceTransition{
			Patch: simPatch(withSimulator(sim, func(next *domain.SimulatorState) {
				next.Booting = slices.DeleteFunc(slices.Clone(sim.Simulator.Booting), func(udid string) bool { return udid == input.UDID })
				errors := maps.Clone(sim.Simulator.Errors)
				if errors == nil {
					errors = map[string]string{}
				}
				errors[input.UDID] = input.Message
				next.Errors = errors
			})),
			Result: contracts.WorkspaceCommandResultFalse{Message: input.Message},
		}

	// A gesture the device would not take: answered to whoever sent it, and never drawn over the panel.
	case SimulatorEventSimulatorRefused:
		return refused(input.Message)

	case SimulatorEventSimulatorTurned:
		turned := work
		turned.Sim = turn(sim, input.UDID, input.Orientation)
		return simulatorDone(state, turned, nil)

	case SimulatorEventSimulatorIdle:
		if sim.Simulator.Driver != nil && sim.Simulator.Driver.Seq == input.Seq {
			return SliceTransition{Patch: simPatch(withSimulator(sim, func(next *domain.SimulatorState) { next.Driver = nil }))}
		}
		return SliceTransition{}
	}
	return SliceTransition{}
}
