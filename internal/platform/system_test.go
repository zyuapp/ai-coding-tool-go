package platform

import (
	"context"
	"math"
	"os"
	"testing"
)

func TestAvailableMacMemoryCountsReclaimablePages(t *testing.T) {
	output := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:  10.\nPages active: 5.\nPages inactive: 2.\nPages speculative: 1.\n"
	if got := AvailableMacMemory(output); got != 13*16384 {
		t.Fatalf("got %v", got)
	}
	if !math.IsInf(AvailableMacMemory("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 10.\n"), 1) {
		t.Fatal("a partial reading became a number")
	}
	if !math.IsInf(availableLinuxMemory("MemTotal: 100 kB\n"), 1) || availableLinuxMemory("MemTotal: 100 kB\nMemAvailable:   50 kB\n") != 50*1024 {
		t.Fatal("meminfo misread")
	}
	if got := AvailableMemory(context.Background()); got <= 0 || math.IsInf(got, 1) {
		t.Fatalf("this machine's memory reads %v", got)
	}
}

func TestProcessSamplesReadPsOutput(t *testing.T) {
	samples := ParseProcessSamples("  412     1  20480 Sat Nov 14 17:06:25 2026\nbad line\n    1     0  100 Sat Nov 14 17:06:25 2026\n  77  412 1 Sat Nov  4 07:06:25 2026")
	if len(samples) != 2 || samples[0] != (ProcessSample{PID: 412, Parent: 1, Bytes: 20480 * 1024, Born: "Sat Nov 14 17:06:25 2026"}) || samples[1].Born != "Sat Nov 4 07:06:25 2026" {
		t.Fatalf("got %#v", samples)
	}
	live, err := ReadProcessSamples(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range live {
		if sample.PID == os.Getpid() {
			return
		}
	}
	t.Fatal("this process is not listed")
}

func env(values map[string]string) Env {
	return func(key string) string { return values[key] }
}

func TestLinuxDisplayCapabilities(t *testing.T) {
	cases := []struct {
		values  map[string]string
		display LinuxDisplayServer
		use     string
		capture string
	}{
		{map[string]string{"DISPLAY": ":0"}, DisplayX11, "available", "available"},
		{map[string]string{"DISPLAY": ":0", "WAYLAND_DISPLAY": "w"}, DisplayXWayland, "available", "unsupported"},
		{map[string]string{"WAYLAND_DISPLAY": "w"}, DisplayWayland, "unsupported", "unsupported"},
		{map[string]string{"WAYLAND_DISPLAY": "w", "CUA_DRIVER_RS_ENABLE_WAYLAND": "1"}, DisplayWayland, "available", "unsupported"},
		{map[string]string{"XDG_SESSION_TYPE": "wayland"}, DisplayNone, "unsupported", "unsupported"},
		{map[string]string{"DISPLAY": ":1", "XDG_SESSION_TYPE": "wayland"}, DisplayX11, "available", "unsupported"},
		{map[string]string{"WAYLAND_DISPLAY": "w", "HYPRLAND_INSTANCE_SIGNATURE": "h", "DISPLAY": ":0"}, DisplayXWayland, "available", "available"},
	}
	for _, c := range cases {
		e := env(c.values)
		if got := LinuxDisplay(e); got != c.display {
			t.Errorf("%v: display %v", c.values, got)
		}
		if got := linuxComputerUse(e); got.Status != c.use || got.Display != c.display {
			t.Errorf("%v: computer use %#v", c.values, got)
		}
		if got := linuxWindowCapture(e); got.Status != c.capture {
			t.Errorf("%v: capture %#v", c.values, got)
		}
	}
	if LinuxWindowCaptureBackend(env(map[string]string{"WAYLAND_DISPLAY": "w", "XDG_CURRENT_DESKTOP": "Hyprland"})) != "" {
		t.Error("a desktop label alone is not a compositor connection")
	}
}
