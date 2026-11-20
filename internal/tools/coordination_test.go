package tools

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type fakeCoordination struct {
	changes  []contracts.ThreadRequestMemoryChange
	proposed [][]*domain.ProposalRequest
	decided  []domain.DecisionRequest
	remember func(contracts.ThreadRequestMemoryChange) (MemoryNote, error)
	propose  func([]*domain.ProposalRequest) (MemoryNote, error)
}

func (f *fakeCoordination) Report(context.Context, domain.CoordinationState, string) (contracts.FindingResult, error) {
	return contracts.FindingResult{Recorded: true}, nil
}

func (f *fakeCoordination) Decide(_ context.Context, request domain.DecisionRequest) (contracts.FindingResult, error) {
	f.decided = append(f.decided, request)
	return contracts.FindingResult{Recorded: true}, nil
}

func (f *fakeCoordination) Remember(_ context.Context, change contracts.ThreadRequestMemoryChange) (MemoryNote, error) {
	f.changes = append(f.changes, change)
	if f.remember != nil {
		return f.remember(change)
	}
	return MemoryNote{Note: "Saved as [n1]. You and every thread you start from now on read it."}, nil
}

func (f *fakeCoordination) Propose(_ context.Context, proposals []*domain.ProposalRequest) (MemoryNote, error) {
	f.proposed = append(f.proposed, proposals)
	if f.propose != nil {
		return f.propose(proposals)
	}
	return MemoryNote{Note: fmt.Sprintf("Suggested %d threads.", len(proposals))}, nil
}

var coordinatorShapes = shapesOf(coordinatorCoordinationTools)

func TestACoordinatorRemembersForgetsAndSetsItsPurpose(t *testing.T) {
	bridge := &fakeCoordination{}
	tools := CoordinationToolsFor(bridge, domain.CoordinationRoleCoordinator)
	saved := callBound(t, tools, "remember", `{"text":"Propose threads before starting them"}`)
	callBound(t, tools, "forget", `{"noteId":"n3"}`)
	callBound(t, tools, "set_purpose", `{"purpose":"Ship the v2 reader"}`)
	callBound(t, tools, "set_purpose", `{"purpose":null}`)
	assertJSON(t, bridge.changes, `[{"kind":"remember","text":"Propose threads before starting them"},{"kind":"forget","noteId":"n3"},{"kind":"purpose","purpose":"Ship the v2 reader"},{"kind":"purpose","purpose":null}]`)
	if textOf(saved) != "Saved as [n1]. You and every thread you start from now on read it." || saved.IsError {
		t.Fatalf("%+v", saved)
	}
}

func TestAMemoryChangeThatCannotBeMadeComesBackInItsOwnWords(t *testing.T) {
	bridge := &fakeCoordination{remember: func(contracts.ThreadRequestMemoryChange) (MemoryNote, error) {
		return MemoryNote{}, errors.New("Memory holds at most 30 notes. Forget one first.")
	}}
	refused := callBound(t, CoordinationToolsFor(bridge, domain.CoordinationRoleCoordinator), "remember", `{"text":"One more"}`)
	if !refused.IsError || textOf(refused) != "Memory holds at most 30 notes. Forget one first." {
		t.Fatalf("%+v", refused)
	}
}

func TestTheToolsHoldNotesAndPurposesToTheWindowsLimits(t *testing.T) {
	for _, c := range []struct {
		tool, args string
		want       bool
	}{
		{"remember", `{"text":"` + repeat("x", 501) + `"}`, false},
		{"remember", `{"text":"` + repeat("x", 500) + `"}`, true},
		{"set_purpose", `{"purpose":"` + repeat("x", 201) + `"}`, false},
		{"set_purpose", `{"purpose":null}`, true},
		{"remember", `{"text":" \n "}`, false},
	} {
		if _, ok := parses(t, nil, coordinatorShapes, c.tool, c.args); ok != c.want {
			t.Errorf("%s %.40s: took %v", c.tool, c.args, ok)
		}
	}
	parsed, _ := parses(t, nil, coordinatorShapes, "remember", `{"text":" Ask before merging\n"}`)
	assertJSON(t, map[string]any(parsed), `{"text":"Ask before merging"}`)
}

func TestADecisionRefusesBlanksAndSharedLabelsAndLeavesBlankContextOut(t *testing.T) {
	bridge := &fakeCoordination{}
	tools := CoordinationToolsFor(bridge, domain.CoordinationRoleMember)
	shapes := shapesOf(memberCoordinationTools)
	for _, args := range []string{
		`{"question":"  ","options":[{"label":"Keep it"}]}`,
		`{"question":"Drop the cache?","options":[{"label":"Keep it"},{"label":" "}]}`,
		`{"question":"Drop the cache?","options":[{"label":"Keep it"},{"label":"keep it ","description":"For now"}]}`,
	} {
		if _, ok := parses(t, nil, shapes, "raise_decision", args); ok {
			t.Errorf("took %s", args)
		}
	}
	callBound(t, tools, "raise_decision", `{"question":"Drop the cache?","context":" ","options":[{"label":"Drop it","description":"\n","recommended":true},{"label":"Keep it"}]}`)
	assertJSON(t, bridge.decided, `[{"question":"Drop the cache?","options":[{"label":"Drop it","recommended":true},{"label":"Keep it"}]}]`)
}

func TestAThreadsReportHoldsASummaryTheWindowCanSave(t *testing.T) {
	shapes := shapesOf(memberCoordinationTools)
	for _, c := range []struct {
		args string
		want bool
	}{
		{`{"state":"done","summary":"PR #42"}`, true},
		{`{"state":"done","summary":"` + repeat("x", 501) + `"}`, false},
		{`{"state":"done","summary":" \n"}`, false},
	} {
		if _, ok := parses(t, nil, shapes, "report_status", c.args); ok != c.want {
			t.Errorf("%.40s: took %v", c.args, ok)
		}
	}
	if got := names(CoordinationToolsFor(&fakeCoordination{}, domain.CoordinationRoleMember)); fmt.Sprint(got) != "[report_status raise_decision]" {
		t.Fatal(got)
	}
}

func TestACoordinatorsSuggestionsCarryStartThreadsFieldsAndATitle(t *testing.T) {
	bridge := &fakeCoordination{}
	answer := runDefinition(t, coordinatorCoordinationTools, CoordinationBridge(bridge), "propose_threads", `{"threads":[
		{"title":"Fix the login flake","prompt":"Make the login test pass reliably.","intent":"fix the flaky login test","doneWhen":"it passes 50 runs","delivers":"pull-request","worktree":true,"model":"gpt-6-luna","effort":"low","role":"implementer"},
		{"title":"Survey the docs","prompt":"List what the docs miss.","intent":"find gaps in the docs","doneWhen":"a list exists","project":"docs","worktreeId":"wt-1","worktree":true}
	]}`)
	assertJSON(t, bridge.proposed, `[[
		{"title":"Fix the login flake","prompt":"Make the login test pass reliably.","brief":{"intent":"fix the flaky login test","doneWhen":"it passes 50 runs","delivers":"pull-request"},"worktree":true,"model":"gpt-6-luna","effort":"low","role":"implementer"},
		{"title":"Survey the docs","prompt":"List what the docs miss.","project":"docs","worktreeId":"wt-1"}
	]]`)
	if textOf(answer) != "Suggested 2 threads." {
		t.Fatal(textOf(answer))
	}
}

func TestASuggestionsOptionalFieldSentBlankIsLeftOut(t *testing.T) {
	bridge := &fakeCoordination{}
	callBound(t, CoordinationToolsFor(bridge, domain.CoordinationRoleCoordinator), "propose_threads", `{"threads":[
		{"title":"Fix the login flake","prompt":"Make the login test pass reliably.","intent":"fix the flaky login test","doneWhen":"it passes 50 runs","delivers":"pull-request","project":" ","worktreeId":"  ","computer":"\n"},
		{"title":"Survey the docs","prompt":"List what the docs miss.","intent":" ","doneWhen":"it passes 50 runs","delivers":"pull-request"}
	]}`)
	assertJSON(t, bridge.proposed, `[[
		{"title":"Fix the login flake","prompt":"Make the login test pass reliably.","brief":{"intent":"fix the flaky login test","doneWhen":"it passes 50 runs","delivers":"pull-request"}},
		{"title":"Survey the docs","prompt":"List what the docs miss."}
	]]`)
}

func TestSuggestionsThatCannotBeKeptComeBackInTheirOwnWords(t *testing.T) {
	bridge := &fakeCoordination{propose: func([]*domain.ProposalRequest) (MemoryNote, error) {
		return MemoryNote{}, errors.New(`Not suggested "Docs": A coordinator hands every thread it starts a brief.`)
	}}
	refused := callBound(t, CoordinationToolsFor(bridge, domain.CoordinationRoleCoordinator), "propose_threads", `{"threads":[{"title":"Docs","prompt":"Write them."}]}`)
	if !refused.IsError || textOf(refused) != `Not suggested "Docs": A coordinator hands every thread it starts a brief.` {
		t.Fatalf("%+v", refused)
	}
	item := func(extra string) string { return `{"title":"Docs","prompt":"Write them."` + extra + `}` }
	thirteen := item("")
	for range 12 {
		thirteen += "," + item("")
	}
	for _, c := range []struct {
		args string
		want bool
	}{
		{`{"threads":[` + item("") + `]}`, true},
		{`{"threads":[]}`, false},
		{`{"threads":[` + thirteen + `]}`, false},
		{`{"threads":[{"title":"` + repeat("x", 121) + `","prompt":"Write them."}]}`, false},
		{`{"threads":[` + item(`,"project":"`+repeat("x", 4097)+`"`) + `]}`, false},
		{`{"threads":[` + item(`,"worktreeId":"`+repeat("x", 257)+`"`) + `]}`, false},
		{`{"threads":[` + item(`,"computer":"`+repeat("x", 257)+`"`) + `]}`, false},
		{`{"threads":[` + item(`,"project":"`+repeat("x", 4096)+`","worktreeId":"`+repeat("x", 256)+`","computer":"`+repeat("x", 256)+`"`) + `]}`, true},
		{`{"threads":[{"prompt":"Write them."}]}`, false},
		{`{"threads":[{"title":"  ","prompt":"Write them."}]}`, false},
		{`{"threads":[{"title":"Docs","prompt":" \n"}]}`, false},
	} {
		if _, ok := parses(t, nil, coordinatorShapes, "propose_threads", c.args); ok != c.want {
			t.Errorf("%.80s: took %v", c.args, ok)
		}
	}
}

func TestCoordinationInstructionsFollowTheRole(t *testing.T) {
	dialect := BriefDialect{CoordinatorReads: "You read with Read.", OwnCheckoutCommits: ptr("Commit with git.")}
	if got := CoordinationInstructions("", "/w", nil, dialect); got != nil {
		t.Fatal(got)
	}
	coordinator := CoordinationInstructions(domain.CoordinationRoleCoordinator, "/w", nil, dialect)
	assertMatch(t, coordinator[0], `^You are a coordinator in AI Coding Tool\.`)
	assertMatch(t, coordinator[0], `Never change anything yourself, however small\. You read with Read\. Read what you need`)
	if got := CoordinationInstructions(domain.CoordinationRoleMember, "/w", nil, dialect); len(got) != 1 || got[0] != MemberInstructions {
		t.Fatal(got)
	}
	own := CoordinationInstructions(domain.CoordinationRoleMember, "/wt/app", ptr("/code/app"), dialect)
	if len(own) != 3 || own[1] != "You work in your own checkout, /wt/app. The project's main checkout, /code/app, is the user's: never change anything there. Where your brief or prompt names that folder, it means the same files in your own checkout: make every change and commit here." || own[2] != "Commit with git." {
		t.Fatal(own)
	}
}
