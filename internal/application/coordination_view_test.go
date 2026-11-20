package application

import (
	"reflect"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func coordSpent(usage domain.ThreadUsage) func(*domain.Thread) {
	return func(thread *domain.Thread) { thread.Usage = usage }
}

func coordUsageThreads() []*domain.Thread {
	return []*domain.Thread{
		coordLead("lead", func(thread *domain.Thread) { thread.Model = new("opus") }, coordSpent(domain.ThreadUsage{"opus": 240_000})),
		coordTask("tests", coordUnder("lead"), func(thread *domain.Thread) { thread.Model = new("sonnet") }, coordSpent(domain.ThreadUsage{"sonnet": 1_500_000, "opus": 100_000})),
		coordTask("docs", coordUnder("lead"), func(thread *domain.Thread) {
			thread.Engine, thread.Model = "codex", new("gpt-6.2-sol")
		}, coordSpent(domain.ThreadUsage{"gpt-6.2-sol": 900_000})),
		coordTask("merged", coordUnder("lead"), func(thread *domain.Thread) { thread.ResolvedAt = new(5.0) }, coordSpent(domain.ThreadUsage{"sonnet": 364_220})),
		coordTask("idle", coordUnder("lead")),
		coordTask("archived", coordUnder("lead"), func(thread *domain.Thread) { thread.ArchivedAt = new(9.0) }, coordSpent(domain.ThreadUsage{"opus": 7_000_000})),
		coordTask("elsewhere", coordSpent(domain.ThreadUsage{"opus": 50})),
	}
}

type usageModel struct {
	engine domain.AgentEngine
	model  string
	tokens float64
}

func usageModels(usage *CoordinationUsage) []usageModel {
	var out []usageModel
	for _, entry := range usage.ByModel {
		out = append(out, usageModel{entry.Engine, entry.Model, entry.Tokens})
	}
	return out
}

func TestCoordinationUsageAddsUpTheCoordinatorAndItsThreads(t *testing.T) {
	threads := coordUsageThreads()
	state := coordWorkspace(coordHolding(threads...))
	usage := CoordinationViewCoordinationUsage(CoordinationMembers(state, "lead"), threads[0])
	if usage == nil {
		t.Fatal("no usage")
	}
	if usage.Total != 3_104_220 || usage.Coordinator != 240_000 {
		t.Errorf("archived threads and threads under no coordinator do not count: total %v, coordinator %v", usage.Total, usage.Coordinator)
	}
	want := []usageModel{{"claude", "sonnet", 1_864_220}, {"codex", "gpt-6.2-sol", 900_000}, {"claude", "opus", 340_000}}
	if got := usageModels(usage); !reflect.DeepEqual(got, want) {
		t.Errorf("by model: %v", got)
	}
	type byThread struct {
		id       string
		computer *PairedComputer
		tokens   float64
	}
	var got []byThread
	for _, entry := range usage.ByThread {
		got = append(got, byThread{entry.Thread.ID, entry.Computer, entry.Tokens})
	}
	if !reflect.DeepEqual(got, []byThread{{"tests", nil, 1_600_000}, {"docs", nil, 900_000}, {"merged", nil, 364_220}}) {
		t.Errorf("most first, leaving out a thread that spent nothing: %v", got)
	}
}

func TestCoordinationViewKeepsItsUsageWhileNothingItCountsMoved(t *testing.T) {
	if CoordinationViewCoordinationUsage(nil, coordLead("lead")) != nil {
		t.Error("a coordinator nothing was counted for shows no usage")
	}
	coordPorted(t, func() {
		usageOf := func(view CoordinationView) *CoordinationUsage { return view.(CoordinationViewTrue).Usage }
		empty := coordWorkspace(coordHolding(coordLead("lead")))
		if usageOf(CoordinationViewCoordinationView(empty, empty.Threads[0], coordNone, coordNone, nil, nil)) != nil {
			t.Error("a coordinator's view with nothing counted has no usage")
		}
		before := coordWorkspace(coordHolding(coordUsageThreads()...))
		first := CoordinationViewCoordinationView(before, before.Threads[0], coordNone, coordNone, nil, nil)
		talked := make([]*domain.Thread, len(before.Threads))
		for index, thread := range before.Threads {
			talked[index] = thread
			if thread.ID == "docs" {
				copied := *thread
				copied.Messages = []*domain.ConversationMessage{{ID: "m", Kind: domain.ConversationMessageKindUser, Text: "go on", At: 30}}
				talked[index] = &copied
			}
		}
		unchanged := CoordinationViewCoordinationView(coordWorkspace(coordHolding(talked...)), talked[0], coordNone, coordNone, nil, first)
		if usageOf(unchanged) != usageOf(first) {
			t.Error("a message moves no count")
		}
		spent := make([]*domain.Thread, len(talked))
		for index, thread := range talked {
			spent[index] = thread
			if thread.ID == "docs" {
				copied := *thread
				copied.Usage = domain.ThreadUsage{"gpt-6.2-sol": 900_500}
				spent[index] = &copied
			}
		}
		grown := CoordinationViewCoordinationView(coordWorkspace(coordHolding(spent...)), spent[0], coordNone, coordNone, nil, unchanged)
		if usageOf(grown) == usageOf(first) || usageOf(grown).Total != 3_104_720 {
			t.Errorf("a count that moved makes new usage: %+v", usageOf(grown))
		}
	})
}

func TestCoordinationUsageCountsAPairedComputersModelByTheNameItGoesByHere(t *testing.T) {
	far := coordTask("far", coordFarLead, func(thread *domain.Thread) { thread.Engine = "codex" },
		coordSpent(domain.ThreadUsage{"gpt-7-nova": 500, "gpt-5.6-sol": 200, "gpt-6.2-sol": 100}))
	holder := coordWorkspace(coordHolding(far))
	studio := coordComputer("studio", holder)
	lead := coordUsageThreads()[0]
	usage := CoordinationViewCoordinationUsage([]*CoordinationMember{{Thread: *far, Computer: studio, Holder: holder}}, lead)
	want := []usageModel{{"claude", "opus", 240_000}, {"codex", "gpt-7-nova", 500}, {"codex", "gpt-6.2-sol", 300}}
	if got := usageModels(usage); !reflect.DeepEqual(got, want) {
		t.Errorf("a retired name counts toward its successor: %v", got)
	}
}
