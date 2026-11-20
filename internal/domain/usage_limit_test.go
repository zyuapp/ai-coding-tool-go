package domain

import (
	"reflect"
	"testing"
)

func limitThread(id string, pause LimitPause, role *ThreadRole) *Thread {
	thread := &Thread{ThreadCore: ThreadCore{ID: id, Engine: "claude", Role: role}}
	thread.LimitPause = &pause
	return thread
}

func TestLimitLineOrder(t *testing.T) {
	session := UsageLimit{ResetsAt: 100, Window: UsageLimitWindowSession}
	threads := []*Thread{
		limitThread("old", LimitPause{UsageLimit: session, PausedAt: 1}, nil),
		limitThread("flow", LimitPause{UsageLimit: session, PausedAt: 0, Workflow: new(true)}, nil),
		limitThread("reviewer", LimitPause{UsageLimit: session, PausedAt: 5}, new(ThreadRoleReviewer)),
		limitThread("nudged", LimitPause{UsageLimit: session, PausedAt: 9, NudgedAt: new(10.0)}, nil),
		limitThread("weekly", LimitPause{UsageLimit: UsageLimit{ResetsAt: 100, Window: UsageLimitWindowWeekly}, PausedAt: 0}, nil),
	}
	lines := LimitLines(threads)
	order := []string{}
	for _, thread := range lines[0].Threads {
		order = append(order, thread.ID)
	}
	if len(lines) != 1 || !reflect.DeepEqual(order, []string{"nudged", "reviewer", "old", "flow"}) {
		t.Fatal(order)
	}
	if positions := LinePositions(threads); positions["old"] != 3 || positions["weekly"] != 0 {
		t.Fatal(positions)
	}
	held := WithEngineLimit(threads, "claude", UsageLimit{ResetsAt: 200, Window: UsageLimitWindowWeekly})
	if held[0].LimitPause.ResetsAt != 200 || held[0].LimitPause.Window != UsageLimitWindowWeekly || threads[0].LimitPause.ResetsAt != 100 {
		t.Fatal("an account limit holds every waiting thread")
	}
	if WithEngineLimit(threads, "claude", UsageLimit{ResetsAt: 50, Window: UsageLimitWindowSession})[0] != threads[0] {
		t.Fatal("an earlier reset changes nothing")
	}
}

func TestPauseSummary(t *testing.T) {
	pause := &LimitPause{UsageLimit: UsageLimit{ResetsAt: 10, Window: UsageLimitWindowSession}}
	if got := PauseSummary(pause, new(2.0), 20); got != "Resuming · 2nd" {
		t.Error(got)
	}
	pause.Window = UsageLimitWindowWeekly
	if got := PauseSummary(pause, nil, 20); got != "Weekly limit reset" {
		t.Error(got)
	}
	for position, want := range map[int]string{1: "1st", 11: "11th", 22: "22nd", 113: "113th"} {
		if got := ordinal(position); got != want {
			t.Error(got)
		}
	}
}
