package devices

import (
	"encoding/binary"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func helperFrame(kind byte, payload []byte) []byte {
	head := make([]byte, 5)
	binary.BigEndian.PutUint32(head, uint32(len(payload)+1))
	head[4] = kind
	return append(head, payload...)
}

func helperBinary(header map[string]any, data []byte) []byte {
	json, _ := jsonx.Marshal(header)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(json)))
	return slices.Concat(length, json, data)
}

func TestFramesAreReadWholeHoweverThePipeSplitsThem(t *testing.T) {
	var messages []SimulatorHelperMessage
	reader := NewSimulatorFrameReader(func(message SimulatorHelperMessage) { messages = append(messages, message) })
	stream := slices.Concat(
		helperFrame(0, []byte(`{"id":1,"ok":true}`)),
		helperFrame(2, helperBinary(map[string]any{"udid": "U", "width": 1206, "height": 2622}, []byte{1, 100, 0, 31})),
		helperFrame(1, helperBinary(map[string]any{"udid": "U", "key": true, "ts": 16_000}, []byte{0, 0, 0, 1, 9})),
	)
	for index := 0; index < len(stream); index += 3 {
		if err := reader.Push(stream[index:min(len(stream), index+3)]); err != nil {
			t.Fatal(err)
		}
	}
	want := []SimulatorHelperMessage{
		{JSON: map[string]any{"id": 1.0, "ok": true}},
		{Frame: domain.SimulatorFrameConfig{UDID: "U", Width: 1206, Height: 2622, Data: []byte{1, 100, 0, 31}}},
		{Frame: domain.SimulatorFrameChunk{UDID: "U", Key: true, Timestamp: 16_000, Data: []byte{0, 0, 0, 1, 9}}},
	}
	if !reflect.DeepEqual(messages, want) {
		t.Fatalf("got %#v", messages)
	}
}

func TestFrameThatDoesNotParseIsDropped(t *testing.T) {
	var messages []SimulatorHelperMessage
	reader := NewSimulatorFrameReader(func(message SimulatorHelperMessage) { messages = append(messages, message) })
	if err := reader.Push(slices.Concat(helperFrame(0, []byte("{nope")), helperFrame(0, []byte(`{"event":"ready"}`)))); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages, []SimulatorHelperMessage{{JSON: map[string]any{"event": "ready"}}}) {
		t.Fatalf("got %#v", messages)
	}
	if err := reader.Push([]byte{0xff, 0xff, 0xff, 0xff, 0}); err == nil || !strings.Contains(err.Error(), "malformed frame") {
		t.Fatalf("got %v", err)
	}
}

func TestDeviceListIsIOSOnlyNewestFirstIPhonesBeforeIPads(t *testing.T) {
	listed, err := ParseDeviceList([]byte(`{
		"devices": {
			"com.apple.CoreSimulator.SimRuntime.iOS-18-5": [{"udid": "old", "name": "iPhone 16", "state": "Shutdown", "isAvailable": true}],
			"com.apple.CoreSimulator.SimRuntime.watchOS-11-0": [{"udid": "watch", "name": "Apple Watch", "state": "Shutdown"}],
			"com.apple.CoreSimulator.SimRuntime.iOS-26-4": [
				{"udid": "pad", "name": "iPad (A16)", "state": "Booted"},
				{"udid": "phone", "name": "iPhone 17", "state": "Booting"},
				{"udid": "gone", "name": "iPhone 15", "state": "Shutdown", "isAvailable": false}
			],
			"com.apple.CoreSimulator.SimRuntime.iOS-26-10": [{"udid": "newest", "name": "iPhone Air", "state": "Creating"}]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []*domain.SimulatorDevice{
		{UDID: "newest", Name: "iPhone Air", Runtime: "iOS 26.10", State: "other"},
		{UDID: "phone", Name: "iPhone 17", Runtime: "iOS 26.4", State: "booting"},
		{UDID: "pad", Name: "iPad (A16)", Runtime: "iOS 26.4", State: "booted"},
		{UDID: "old", Name: "iPhone 16", Runtime: "iOS 18.5", State: "shutdown"},
	}
	if !reflect.DeepEqual(listed, want) {
		got, _ := jsonx.Marshal(listed)
		t.Fatalf("got %s", got)
	}
	if empty, err := ParseDeviceList([]byte("{}")); err != nil || len(empty) != 0 {
		t.Fatalf("empty: %v %v", empty, err)
	}
}

func TestTextAUSKeyboardCannotTypeIsPastedWithItsReturnsStillPressed(t *testing.T) {
	if _, _, pasted := PastedText("plain text\n"); pasted {
		t.Fatal("plain text is typed")
	}
	if paste, returns, pasted := PastedText("Grüße 👋\n"); !pasted || paste != "Grüße 👋" || returns != "\n" {
		t.Fatalf("got %q %q", paste, returns)
	}
	if paste, returns, pasted := PastedText("a\nñ"); !pasted || paste != "a\nñ" || returns != "" {
		t.Fatalf("got %q %q", paste, returns)
	}
}

func TestElementRefsFromAnOlderReadAreRefused(t *testing.T) {
	host := NewSimulatorHost(SimulatorHostOptions{Helper: "/nonexistent"})
	host.snapshots["U"] = &simulatorSnapshot{id: 3, width: 400, height: 800, elements: []*domain.SimulatorElement{{Ref: "3.1", Frame: [4]float64{100, 200, 50, 40}}}}
	x, y, err := host.elementCenter("U", "3.1")
	if err != nil || x != 0.3125 || y != 0.275 {
		t.Fatalf("got %v %v %v", x, y, err)
	}
	if _, _, err := host.elementCenter("U", "2.1"); err == nil || err.Error() != "The ref 2.1 is from an older read of the screen. Read it again, then tap." {
		t.Fatalf("older read: %v", err)
	}
	if _, _, err := host.elementCenter("U", "3.9"); err == nil || err.Error() != "The latest read has no element 3.9." {
		t.Fatalf("missing element: %v", err)
	}
}

func TestHeadlessSimulatorNeedsTheDesktopApp(t *testing.T) {
	var headless contracts.SimulatorPortRuntime = HeadlessSimulatorDesktop{}
	listed, err := headless.ListSimulators(t.Context())
	if err != nil || listed.Availability != (domain.SimulatorAvailabilityUnsupported{Reason: "The simulator needs the desktop app."}) || len(listed.Devices) != 0 {
		t.Fatalf("got %v %v", listed, err)
	}
	if err := headless.BootSimulator(t.Context(), "U"); err == nil || err.Error() != "The simulator needs the desktop app." {
		t.Fatalf("boot: %v", err)
	}
	if err := headless.ShutdownSimulator(t.Context(), "U"); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
