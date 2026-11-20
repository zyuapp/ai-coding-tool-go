package tools

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func automationView(edit func(*domain.AutomationView)) *domain.AutomationView {
	view := &domain.AutomationView{
		Automation: domain.Automation{ID: "automation-1", TaskID: "task-1", Prompt: "check whether the PR is approved", Schedule: "* * * * *", CreatedAt: 1, UpdatedAt: 1, RunCount: 3},
		NextRunAt:  ptr(1_700_000_060_000.0),
	}
	if edit != nil {
		edit(view)
	}
	return view
}

type fakeAutomations struct {
	calls  []any
	read   func() (*domain.AutomationView, error)
	list   func() ([]*domain.AutomationView, error)
	save   func(contracts.AutomationRequestSaveDraft) (*domain.AutomationView, error)
	remove func() (bool, error)
}

func (f *fakeAutomations) Read(context.Context) (*domain.AutomationView, error) {
	f.calls = append(f.calls, "read")
	if f.read != nil {
		return f.read()
	}
	return automationView(nil), nil
}

func (f *fakeAutomations) List(context.Context) ([]*domain.AutomationView, error) {
	f.calls = append(f.calls, "list")
	if f.list != nil {
		return f.list()
	}
	return []*domain.AutomationView{automationView(nil)}, nil
}

func (f *fakeAutomations) Save(_ context.Context, draft contracts.AutomationRequestSaveDraft) (*domain.AutomationView, error) {
	f.calls = append(f.calls, draft)
	if f.save != nil {
		return f.save(draft)
	}
	return automationView(func(v *domain.AutomationView) {
		v.Prompt, v.Schedule, v.SurfaceWhen = draft.Prompt, draft.Schedule, draft.SurfaceWhen
	}), nil
}

func (f *fakeAutomations) Update(_ context.Context, patch domain.AutomationPatch) (*domain.AutomationView, error) {
	f.calls = append(f.calls, patch)
	return automationView(func(v *domain.AutomationView) {
		if patch.Paused != nil {
			v.Paused = *patch.Paused
		}
	}), nil
}

func (f *fakeAutomations) Remove(context.Context) (bool, error) {
	f.calls = append(f.calls, "remove")
	if f.remove != nil {
		return f.remove()
	}
	return true, nil
}

func (f *fakeAutomations) last() any { return f.calls[len(f.calls)-1] }

func runAutomation(t *testing.T, bridge AutomationBridge, name, args string) ToolResult {
	t.Helper()
	return callBound(t, AutomationToolsFor(bridge, ""), name, args)
}

func TestTheAutomationToolsCoverTheWholeLifecycle(t *testing.T) {
	bridge := &fakeAutomations{}
	scheduled := runAutomation(t, bridge, "schedule", `{"prompt":"poll the PR","schedule":"*/5 * * * *","policy":"autonomous"}`)
	assertMatch(t, textOf(scheduled), `Automation scheduled`)
	assertJSON(t, bridge.last(), `{"prompt":"poll the PR","schedule":"*/5 * * * *","policy":"autonomous"}`)

	updated := runAutomation(t, bridge, "update", `{"paused":true}`)
	assertMatch(t, textOf(updated), `Automation updated`)
	assertJSON(t, bridge.last(), `{"paused":true}`)

	status := textOf(runAutomation(t, bridge, "status", `{}`))
	if status != "schedule: * * * * *\npaused: false\nruns so far: 3\nnext run: 2023-11-14T22:14:20.000Z\nlast run: never\nprompt: check whether the PR is approved" {
		t.Fatal(status)
	}
	assertMatch(t, textOf(runAutomation(t, bridge, "stop", `{}`)), `stopped and removed`)
}

func TestTheToolsReportAMissingAutomationInsteadOfFailingTheRun(t *testing.T) {
	bridge := &fakeAutomations{
		read:   func() (*domain.AutomationView, error) { return nil, nil },
		remove: func() (bool, error) { return false, nil },
		list:   func() ([]*domain.AutomationView, error) { return nil, nil },
	}
	assertMatch(t, textOf(runAutomation(t, bridge, "status", `{}`)), `no automation`)
	assertMatch(t, textOf(runAutomation(t, bridge, "stop", `{}`)), `no automation`)
	assertMatch(t, textOf(runAutomation(t, bridge, "list_all", `{}`)), `No automations exist`)
	listed := textOf(runAutomation(t, &fakeAutomations{}, "list_all", `{}`))
	assertMatch(t, listed, `^task task-1\nschedule: `)
}

func TestARejectedScheduleComesBackAsAToolError(t *testing.T) {
	bridge := &fakeAutomations{save: func(contracts.AutomationRequestSaveDraft) (*domain.AutomationView, error) {
		return nil, errors.New("Automations run at most once a minute.")
	}}
	result := runAutomation(t, bridge, "schedule", `{"prompt":"poll","schedule":"* * * * * *"}`)
	if !result.IsError || textOf(result) != "Automation error: Automations run at most once a minute." {
		t.Fatalf("%+v", result)
	}
}

type fakeFindings struct {
	calls   []any
	notify  func(contracts.FindingReport) (contracts.FindingResult, error)
	nothing func(string) (contracts.FindingResult, error)
}

func (f *fakeFindings) Notify(_ context.Context, report contracts.FindingReport) (contracts.FindingResult, error) {
	f.calls = append(f.calls, report)
	return f.notify(report)
}

func (f *fakeFindings) NothingToReport(_ context.Context, checked string) (contracts.FindingResult, error) {
	f.calls = append(f.calls, checked)
	return f.nothing(checked)
}

func TestTheTwoReportingToolsNameNothingAboutTheRun(t *testing.T) {
	bridge := &fakeFindings{
		notify: func(contracts.FindingReport) (contracts.FindingResult, error) {
			return contracts.FindingResult{Recorded: true, Note: "Raised. This thread now carries 1 unread finding."}, nil
		},
		nothing: func(string) (contracts.FindingResult, error) {
			return contracts.FindingResult{Recorded: true, Note: "Noted. This run settles without reaching the user."}, nil
		},
	}
	tools := FindingToolsFor(bridge)
	raised := callBound(t, tools, "notify", `{"headline":"5xx on checkout","detail":"12 in an hour","key":"checkout"}`)
	assertMatch(t, textOf(raised), `1 unread finding`)
	assertJSON(t, bridge.calls[len(bridge.calls)-1], `{"headline":"5xx on checkout","detail":"12 in an hour","key":"checkout"}`)
	silent := callBound(t, tools, "nothing_to_report", `{"checked":"the alert feed"}`)
	assertMatch(t, textOf(silent), `settles without reaching the user`)
	if bridge.calls[len(bridge.calls)-1] != "the alert feed" {
		t.Fatal(bridge.calls)
	}
}

func TestAReportTheWindowCannotKeepIsAToolError(t *testing.T) {
	bridge := &fakeFindings{notify: func(contracts.FindingReport) (contracts.FindingResult, error) {
		return contracts.FindingResult{}, errors.New("This thread is already carrying 10 unread findings")
	}}
	result := callBound(t, FindingToolsFor(bridge), "notify", `{"headline":"One too many"}`)
	if !result.IsError || textOf(result) != "Automation error: This thread is already carrying 10 unread findings This run found something either way, so it must not call nothing_to_report." {
		t.Fatalf("%+v", result)
	}
}

func TestTheReportingToolsAreOfferedOnlyWhereAFindingCouldBeRead(t *testing.T) {
	if got := fmt.Sprint(names(AutomationToolsFor(&fakeAutomations{}, ""))); got != "[schedule status update stop list_all]" {
		t.Fatal(got)
	}
	if got := fmt.Sprint(names(FindingToolsFor(&fakeFindings{}))); got != "[notify nothing_to_report]" {
		t.Fatal(got)
	}
	if got := fmt.Sprint(names(AutomationToolsFor(&fakeAutomations{}, domain.CoordinationRoleCoordinator))); got != "[status list_all]" {
		t.Fatal(got)
	}
	run := &Run{Automations: &fakeAutomations{}}
	tools, _ := AutomationServer.Serve(run)
	if len(tools) != 5 {
		t.Fatal(names(tools))
	}
	run.Findings = &fakeFindings{}
	tools, _ = AutomationServer.Serve(run)
	if len(tools) != 7 {
		t.Fatal(names(tools))
	}
}

func TestAScheduleSaysForItselfWhenItIsWorthSurfacing(t *testing.T) {
	bridge := &fakeAutomations{}
	runAutomation(t, bridge, "schedule", `{"prompt":"poll Datadog","schedule":"*/30 * * * *","surfaceWhen":"an error was caused by the user's own code."}`)
	if draft := bridge.last().(contracts.AutomationRequestSaveDraft); *draft.SurfaceWhen != "an error was caused by the user's own code." {
		t.Fatal(*draft.SurfaceWhen)
	}
	runAutomation(t, bridge, "update", `{"surfaceWhen":""}`)
	assertJSON(t, bridge.last(), `{"surfaceWhen":""}`)
	assertNoMatch(t, textOf(runAutomation(t, bridge, "status", `{}`)), `surfaces when`)
	quiet := &fakeAutomations{read: func() (*domain.AutomationView, error) {
		return automationView(func(v *domain.AutomationView) {
			v.SurfaceWhen = ptr("there is an error.")
			v.EndsWhen = ptr("the PR is approved.")
			v.Timezone = ptr("Europe/Paris")
			v.LastRunAt = ptr(1_700_000_000_000.0)
			v.LastStatus = ptr(domain.AutomationRunStatusSucceeded)
			v.Policy = ptr(domain.ExecutionPolicyAutonomous)
			v.Paused = true
		}), nil
	}}
	status := textOf(runAutomation(t, quiet, "status", `{}`))
	if status != "schedule: * * * * * (Europe/Paris)\npaused: true\nruns so far: 3\nnext run: 2023-11-14T22:14:20.000Z\nsurfaces when: there is an error.\nends when: the PR is approved.\nlast run: 2023-11-14T22:13:20.000Z (succeeded)\npolicy: autonomous\nprompt: check whether the PR is approved" {
		t.Fatal(status)
	}
}
