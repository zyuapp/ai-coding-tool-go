package domain

import (
	"testing"
	"time"
)

func planWindow(resetsAt string, utilization float64) *UsageWindow {
	return &UsageWindow{ID: "codex:primary", Label: "Current session", Utilization: &utilization, ResetsAt: &resetsAt}
}

func planMs(text string) float64 {
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		panic(err)
	}
	return float64(at.UnixMilli())
}

func TestDismissedUsageWarningOutlastsResetDrift(t *testing.T) {
	dismissed := WithUsageWarningDismissed(nil, UsageWarningKey("codex", planWindow("2026-11-19T11:42:01.000Z", 85)))
	drifted := UsageWarningKey("codex", planWindow("2026-11-19T11:42:18.000Z", 85))
	if !UsageWarningDismissed(dismissed, drifted, planMs("2026-11-19T10:00:00Z")) {
		t.Error("drifted reset still dismissed")
	}
	if UsageWarningDismissed(dismissed, drifted, planMs("2026-11-19T11:42:02Z")) {
		t.Error("comes back once the window resets")
	}
	if UsageWarningDismissed(dismissed, UsageWarningKey("codex", planWindow("2026-11-19T11:42:18.000Z", 100)), planMs("2026-11-19T10:00:00Z")) {
		t.Error("reaching the limit is a new warning")
	}
	if key := UsageWarningKey("claude", planWindow("2026-11-21T21:59:59.581923+00:00", 91)); key != "claude:codex:primary:near@1795298399581" {
		t.Errorf("key %s", key)
	}
}

func TestFormatReset(t *testing.T) {
	now := planMs("2026-11-19T10:00:00Z")
	if got, _ := FormatReset(new("2026-11-19T15:05:00Z"), now, "UTC", true); got != "Resets 3:05pm (UTC)" {
		t.Error(got)
	}
	if got, _ := FormatReset(new("2026-11-21T00:30:00Z"), now, "America/New_York", false); got != "Resets Nov 20 at 7:30pm" {
		t.Error(got)
	}
	if _, ok := FormatReset(new("soon"), now, "UTC", true); ok {
		t.Error("an unreadable reset")
	}
}

func TestWindowsForAModel(t *testing.T) {
	shared, own, other := &UsageWindow{ID: "shared"}, &UsageWindow{ID: "opus", Model: new("Claude Opus 4")}, &UsageWindow{ID: "sonnet", Model: new("sonnet")}
	windows := WindowsFor(PlanUsageAvailable{Windows: []*UsageWindow{shared, own, other}}, "Opus")
	if len(windows) != 2 || windows[0] != shared || windows[1] != own {
		t.Fatal(windows)
	}
	if len(WindowsFor(nil, "opus")) != 0 {
		t.Fatal("no usage")
	}
}
