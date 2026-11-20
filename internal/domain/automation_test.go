package domain

import (
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const automationRecord = `{"id":"a1","taskId":"t1","prompt":"Check the PR","schedule":"*/5 * * * *","paused":false,"createdAt":1,"updatedAt":2,"runCount":0}`

func automationWith(t *testing.T, base string, key string, value any) map[string]any {
	t.Helper()
	record := featuresDecode(t, base).(map[string]any)
	if value == nil {
		delete(record, key)
	} else {
		record[key] = value
	}
	return record
}

func TestEditedAutomationFields(t *testing.T) {
	saved := &Automation{Schedule: "0 9 * * *", Prompt: "Check the PR"}
	edit := &AutomationEdit{Base: AutomationAutomationFields(saved), Fields: AutomationFields{Schedule: "0 9 * * *", Prompt: "Mine"}}
	theirs := *saved
	theirs.Prompt = "Theirs"
	if EditedAutomationFields(saved, edit).Prompt != "Mine" || EditedAutomationFields(&theirs, edit).Prompt != "Theirs" || EditedAutomationFields(saved, nil).Prompt != "Check the PR" {
		t.Fatal("edits show only while the saved fields they began from still stand")
	}
	if AutomationAutomationFields(nil) != (AutomationFields{}) {
		t.Fatal("no automation")
	}
}

func TestIsAutomation(t *testing.T) {
	if !IsAutomation(featuresDecode(t, automationRecord)) {
		t.Fatal("a record")
	}
	var typed Automation
	if err := jsonx.Unmarshal([]byte(automationRecord), &typed); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		key   string
		value any
		want  bool
	}{
		{"policy", "autonomous", true},
		{"policy", "yolo", false},
		{"surfaceWhen", "", false},
		{"surfaceWhen", strings.Repeat("x", MaxSurfaceWhen+1), false},
		{"lastStatus", "missed", true},
		{"lastStatus", "lost", false},
		{"runCount", 1.5, false},
		{"runCount", -1.0, false},
		{"createdAt", 9007199254740992.0, false},
		{"paused", "no", false},
		{"timezone", nil, true},
		{"id", nil, false},
		{"prompt", strings.Repeat("😀", MaxAutomationPrompt/2+1), false},
	} {
		if IsAutomation(automationWith(t, automationRecord, change.key, change.value)) != change.want {
			t.Fatalf("%s=%v should be %v", change.key, change.value, change.want)
		}
	}
	if IsAutomation(nil) || IsAutomation("x") {
		t.Fatal("not objects")
	}
}

func TestIsAutomationDraftAndPatch(t *testing.T) {
	draft := `{"taskId":"t1","prompt":"p","schedule":"0 9 * * *"}`
	if !IsAutomationDraft(featuresDecode(t, draft)) || IsAutomationDraft(automationWith(t, draft, "surfaceWhen", "")) || IsAutomationDraft(automationWith(t, draft, "paused", 1.0)) || IsAutomationDraft(automationWith(t, draft, "taskId", nil)) {
		t.Fatal("draft")
	}
	if !IsAutomationPatch(featuresDecode(t, `{}`)) || !IsAutomationPatch(featuresDecode(t, `{"surfaceWhen":"","endsWhen":""}`)) || IsAutomationPatch(featuresDecode(t, `{"prompt":""}`)) || IsAutomationPatch(featuresDecode(t, `{"paused":null}`)) {
		t.Fatal("an empty sentence takes the quiet off a patch, but a prompt cannot be empty")
	}
}

func TestAutomationAfterRun(t *testing.T) {
	declines := 2.0
	automation := &Automation{ID: "a1", RunCount: 3, ConsecutiveDeclines: &declines}
	missed := AutomationAfterRun(automation, AutomationRunStatusMissed, 10)
	if missed.RunCount != 3 || missed.LastRunAt != nil || *missed.LastStatus != "missed" || *missed.LastStatusAt != 10 || missed.UpdatedAt != 10 || *missed.ConsecutiveDeclines != 2 {
		t.Fatal(featuresJSON(t, missed))
	}
	skipped := AutomationAfterRun(automation, AutomationRunStatusSkipped, 11)
	if skipped.RunCount != 3 || *skipped.ConsecutiveDeclines != 3 || skipped.LastRunAt != nil {
		t.Fatal(featuresJSON(t, skipped))
	}
	ran := AutomationAfterRun(automation, AutomationRunStatusSucceeded, 12)
	if ran.RunCount != 4 || *ran.LastRunAt != 12 || ran.ConsecutiveDeclines != nil || *ran.LastStatus != "succeeded" {
		t.Fatal(featuresJSON(t, ran))
	}
	if *automation.ConsecutiveDeclines != 2 || automation.LastStatus != nil {
		t.Fatal("the record it came from is left alone")
	}
}

func TestWithWatchedThreads(t *testing.T) {
	ends := "the PR merges"
	busy := jsonx.SetOf("t1")
	routine := []*Automation{{TaskID: "t2"}, {TaskID: "t3", EndsWhen: &ends, Paused: true}, {TaskID: "t1", EndsWhen: &ends}}
	if got := WithWatchedThreads(busy, routine); !jsonx.Same(got, busy) {
		t.Fatal("nothing added returns busy itself")
	}
	watched := WithWatchedThreads(busy, append(routine, &Automation{TaskID: "t4", EndsWhen: &ends}))
	if !watched.Has("t1") || !watched.Has("t4") || len(watched) != 2 || len(busy) != 1 {
		t.Fatal(watched.Sorted())
	}
}

func TestOneShotScheduleAndQuietTicks(t *testing.T) {
	for schedule, want := range map[string]bool{
		"2026-11-20T15:00:00Z":      true,
		"2026-11-20T15:00:00.5Z":    true,
		"2026-11-20T15:00:00+08:00": true,
		"2026-11-20T15:00":          true,
		"2026-11-20":                true,
		"2026-11-20 15:00":          true,
		"2026-11-20 3:00 PM":        true,
		"2026-11-20T24:00":          true,
		"2026-11-20T25:00":          false,
		"2026-13-01":                false,
		"2026-11-20T15":             false,
		"2026-11-20T15:00:00+25:00": false,
		" 2026-11-20T15:00:00Z":     false,
		"12-31-2026":                true,
		"31-12-2026":                false,
		"0 9 * * 1-5":               false,
		"0 0 1-15 * *":              false,
		"*/5 * * * *":               false,
		"0 9 * * MON-FRI":           false,
		"2026":                      false,
		"tomorrow-ish":              false,
	} {
		if IsOneShotSchedule(schedule) != want {
			t.Fatalf("%q one-shot should be %v", schedule, want)
		}
	}
	surface := "a failure"
	if !QuietTick(&Automation{Schedule: "0 9 * * *", SurfaceWhen: &surface}, false) || QuietTick(&Automation{Schedule: "0 9 * * *", SurfaceWhen: &surface}, true) ||
		QuietTick(&Automation{Schedule: "2026-11-20T15:00:00Z", SurfaceWhen: &surface}, false) || QuietTick(&Automation{Schedule: "0 9 * * *"}, false) {
		t.Fatal("quietTick")
	}
	if ScheduleFieldCount("") != 1 || ScheduleFieldCount(" 0  9 * * * ") != 5 {
		t.Fatal("scheduleFieldCount")
	}
}
