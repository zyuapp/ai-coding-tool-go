package tools

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var simulatorDevice = domain.SimulatorDevice{UDID: "PHONE", Name: "iPhone 17", Runtime: "iOS 26.4", State: "booted"}

var simulatorScreen = contracts.SimulatorReadResultSnapshot{
	Snapshot: domain.SimulatorSnapshot{UDID: "PHONE", Device: "iPhone 17", Application: ptr("Settings"), Width: 402, Height: 874,
		Elements: []*domain.SimulatorElement{{Ref: "3.1", Role: "Button", Label: ptr("General"), Frame: [4]float64{16, 380, 370, 52}, Depth: 1}}},
	Shot: &domain.SimulatorShot{Path: "/tmp/s.png", Width: 1206, Height: 2622},
}

type fakeSimulator struct {
	writes []contracts.SimulatorWrite
	reads  []contracts.SimulatorRead
	status contracts.SimulatorReadResult
}

func (f *fakeSimulator) Command(_ context.Context, write contracts.SimulatorWrite) error {
	f.writes = append(f.writes, write)
	return nil
}

func (f *fakeSimulator) Read(_ context.Context, read contracts.SimulatorRead) (contracts.SimulatorReadResult, error) {
	f.reads = append(f.reads, read)
	if _, status := read.(contracts.SimulatorReadStatus); status {
		if f.status != nil {
			return f.status, nil
		}
		return contracts.SimulatorReadResultStatus{Device: simulatorDevice, Booted: true}, nil
	}
	return simulatorScreen, nil
}

func simulatorCall(t *testing.T, bridge SimulatorBridge, name, args string) ToolResult {
	t.Helper()
	return callBound(t, SimulatorToolsFor(bridge, false), name, args)
}

func init() { simulatorSettle = time.Millisecond }

func TestOpeningWaitsForTheDeviceAndAnswersWithItsUDID(t *testing.T) {
	bridge := &fakeSimulator{}
	text := textOf(simulatorCall(t, bridge, "simulator_open", `{"device":"iPhone 17"}`))
	assertJSON(t, bridge.writes, `[{"type":"simulator.open","device":"iPhone 17"}]`)
	assertJSON(t, bridge.reads, `[{"op":"status","timeoutMs":240000}]`)
	if text != "iPhone 17 · iOS 26.4 is running. UDID: PHONE. Read the screen with simulator_read." {
		t.Fatal(text)
	}
	failed := simulatorCall(t, &fakeSimulator{status: contracts.SimulatorReadResultStatus{Device: simulatorDevice, Error: ptr("Unable to boot")}}, "simulator_open", `{}`)
	if !failed.IsError || textOf(failed) != "Simulator error: iPhone 17 · iOS 26.4 could not start: Unable to boot" {
		t.Fatalf("%+v", failed)
	}
}

func TestAReadListsElementsByRefWithTheirFrames(t *testing.T) {
	text := textOf(simulatorCall(t, &fakeSimulator{}, "simulator_read", `{}`))
	if text != "Screenshot saved to /tmp/s.png (1206x2622 pixels). Open that file to see the screen.\n\nSettings on iPhone 17 — screen 402x874 points\n  [3.1] Button \"General\" (16,380 370x52)" {
		t.Fatal(text)
	}
}

func TestATapNamesARefOrAPoint(t *testing.T) {
	bridge := &fakeSimulator{}
	simulatorCall(t, bridge, "simulator_tap", `{"ref":"3.1"}`)
	simulatorCall(t, bridge, "simulator_tap", `{"x":201,"y":406}`)
	assertJSON(t, bridge.writes, `[{"type":"simulator.input","input":{"kind":"tap-element","ref":"3.1"}},{"type":"simulator.input","input":{"kind":"tap","x":201,"y":406,"points":true}}]`)
	assertJSON(t, bridge.reads[len(bridge.reads)-1], `{"op":"snapshot","screenshot":false}`)
	if !simulatorCall(t, bridge, "simulator_tap", `{"x":3}`).IsError {
		t.Fatal("a tap with only x was taken")
	}
}

func TestTypingCanPressReturnAndACoordinatorOnlyReads(t *testing.T) {
	bridge := &fakeSimulator{}
	simulatorCall(t, bridge, "simulator_type", `{"text":"hello","submit":true}`)
	assertJSON(t, bridge.writes, `[{"type":"simulator.input","input":{"kind":"type","text":"hello\n"}}]`)
	if got := fmt.Sprint(names(SimulatorToolsFor(bridge, true))); got != "[simulator_read]" {
		t.Fatal(got)
	}
}

func TestRotatingNamesHowToHoldTheDevice(t *testing.T) {
	bridge := &fakeSimulator{}
	simulatorCall(t, bridge, "simulator_rotate", `{"orientation":"landscape-right"}`)
	assertJSON(t, bridge.writes, `[{"type":"simulator.input","input":{"kind":"rotate","orientation":"landscape-right"}}]`)
	assertJSON(t, bridge.reads[len(bridge.reads)-1], `{"op":"snapshot","screenshot":false}`)
}
