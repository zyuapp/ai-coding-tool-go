package domain

import (
	"slices"
	"testing"
)

func TestSimulatorInputPositions(t *testing.T) {
	for want, inputs := range map[bool][]string{
		true: {
			`{"kind":"tap","x":0.5,"y":1}`,
			`{"kind":"tap","x":201,"y":437,"points":true}`,
			`{"kind":"swipe","x":0.5,"y":0.8,"toX":0.5,"toY":0.2,"durationMs":300}`,
			`{"kind":"touch","phase":"move","x":0,"y":0}`,
			`{"kind":"touch","phase":"down","x":0.4,"y":0.5,"x2":0.6,"y2":0.5}`,
			`{"kind":"shortcut","usage":4}`,
			`{"kind":"key","usage":4,"down":true}`,
			`{"kind":"rotate","orientation":"landscape-left"}`,
			`{"kind":"tap-element","ref":"e1"}`,
			`{"kind":"paste","text":"hi"}`,
			`{"kind":"pasteboard","phase":"leave"}`,
			`{"kind":"button","button":"lock"}`,
		},
		false: {
			`{"kind":"tap","x":201,"y":437}`,
			`{"kind":"tap","x":0.5,"y":0.5,"points":false}`,
			`{"kind":"swipe","x":0.5,"y":0.8,"toX":0.5,"toY":0.2,"durationMs":60000}`,
			`{"kind":"touch","phase":"hover","x":0,"y":0}`,
			`{"kind":"touch","phase":"down","x":0.4,"y":0.5,"x2":0.6}`,
			`{"kind":"touch","phase":"down","x":0.4,"y":0.5,"x2":1.5,"y2":0.5}`,
			`{"kind":"touch","phase":"down","x":0.4,"y":0.5,"x2":null,"y2":null}`,
			`{"kind":"shortcut","usage":0}`,
			`{"kind":"shortcut","usage":4.5}`,
			`{"kind":"paste","text":""}`,
			`{"kind":"pasteboard","phase":"sync"}`,
			`{"kind":"key","usage":256,"down":true}`,
			`{"kind":"type","text":""}`,
			`{"kind":"button","button":"volume"}`,
			`{"kind":"rotate","orientation":"sideways"}`,
			`{"kind":"warp"}`,
			`[]`,
		},
	} {
		for _, input := range inputs {
			if IsSimulatorInput(featuresDecode(t, input)) != want {
				t.Fatalf("%s should be %v", input, want)
			}
		}
	}
}

func TestHidUsage(t *testing.T) {
	for code, want := range map[string]int{"KeyA": 4, "KeyZ": 29, "Digit1": 30, "Digit0": 39, "Enter": 40, "ShiftLeft": 225, "Home": 74, "Numpad1": 89, "Numpad9": 97, "Numpad0": 98} {
		if got, ok := HidUsage(code); !ok || got != want {
			t.Fatalf("%s = %d, want %d", code, got, want)
		}
	}
	if _, ok := HidUsage("F13"); ok {
		t.Fatal("F13 has no usage")
	}
}

func TestDefaultSimulator(t *testing.T) {
	phone := &SimulatorDevice{UDID: "a", Name: "iPhone 17", Runtime: "iOS 26.4", State: SimulatorDeviceStateShutdown}
	pad := &SimulatorDevice{UDID: "b", Name: "iPad (A16)", Runtime: "iOS 26.4", State: SimulatorDeviceStateShutdown}
	booted := *pad
	booted.State = SimulatorDeviceStateBooted
	if DefaultSimulator([]*SimulatorDevice{pad, phone}, nil).UDID != "a" || DefaultSimulator([]*SimulatorDevice{phone, &booted}, nil).UDID != "b" || DefaultSimulator(nil, nil) != nil {
		t.Fatal("the default device is one already running, else the first iPhone")
	}
}

func TestUntiltedAndTurned(t *testing.T) {
	for _, check := range []struct {
		u, v        float64
		orientation SimulatorOrientation
		x, y        float64
	}{
		{0.25, 0.75, "portrait", 0.25, 0.75},
		{0, 0.5, "landscape-left", 0.5, 0},
		{1, 0, "landscape-left", 1, 1},
		{1, 0.5, "landscape-right", 0.5, 0},
		{0, 1, "landscape-right", 1, 1},
		{0.25, 0.25, "portrait-upside-down", 0.75, 0.75},
	} {
		if x, y := Untilted(check.u, check.v, check.orientation); x != check.x || y != check.y {
			t.Fatalf("%v = %v,%v", check, x, y)
		}
	}
	if Turned("portrait", -1) != "landscape-left" || Turned("landscape-left", -1) != "portrait-upside-down" || Turned("portrait", 1) != "landscape-right" || Turned("landscape-left", 1) != "portrait" {
		t.Fatal("turned")
	}
	turns := []int{}
	for _, orientation := range SimulatorOrientations {
		turns = append(turns, ScreenTurns(orientation))
	}
	if !slices.Equal(turns, []int{0, 3, 1, 2}) {
		t.Fatal(turns)
	}
}

func simulatorGroupIDs(groups []*SimulatorGroup) [][]string {
	ids := [][]string{}
	for _, group := range groups {
		row := []string{group.Label}
		for _, device := range group.Devices {
			row = append(row, device.UDID)
		}
		ids = append(ids, row)
	}
	return ids
}

func TestSimulatorGroups(t *testing.T) {
	devices := []*SimulatorDevice{
		{UDID: "pro", Name: "iPhone 17 Pro", Runtime: "iOS 26.4", State: SimulatorDeviceStateBooted},
		{UDID: "17", Name: "iPhone 17", Runtime: "iOS 26.4", State: SimulatorDeviceStateShutdown},
		{UDID: "pad", Name: "iPad Air 11-inch (M4)", Runtime: "iOS 26.4", State: SimulatorDeviceStateShutdown},
		{UDID: "old", Name: "iPhone 16", Runtime: "iOS 18.5", State: SimulatorDeviceStateBooting},
	}
	featuresExpectJSON(t, simulatorGroupIDs(SimulatorGroups(devices, "")), `[["Running","pro","old"],["iOS 26.4","17","pad"]]`)
	featuresExpectJSON(t, simulatorGroupIDs(SimulatorGroups(devices, "iphone 17")), `[["iOS 26.4","17"],["Running","pro"]]`)
	featuresExpectJSON(t, simulatorGroupIDs(SimulatorGroups(devices, "18.5")), `[["Running","old"]]`)
	featuresExpectJSON(t, simulatorGroupIDs(SimulatorGroups(devices, "pixel")), `[]`)
	pad, gone := "pad", "gone"
	if DefaultSimulator(devices, &pad).UDID != "pad" || DefaultSimulator(devices, &gone).UDID != "pro" {
		t.Fatal("the device last picked wins over one already running")
	}
}
