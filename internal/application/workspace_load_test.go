package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const loadNow = 1_790_000_000_000

func loadTask(id string, edits ...func(*domain.Thread)) *domain.Thread {
	return coreTask(id, append([]func(*domain.Thread){func(thread *domain.Thread) {
		thread.ProjectID = new("project-1")
		thread.RankedAt, thread.UpdatedAt = loadNow, loadNow
	}}, edits...)...)
}

func loadData(t *testing.T, lastFolder *string, threads ...*domain.Thread) *domain.ThreadStoreData {
	t.Helper()
	tasks := []*domain.StoredThread{}
	for _, thread := range threads {
		tasks = append(tasks, coreStored(t, thread))
	}
	return &domain.ThreadStoreData{Tasks: tasks, Projects: []*domain.Project{{ID: "project-1", Root: "/project"}}, Worktrees: []*domain.Worktree{}, LastFolder: lastFolder}
}

func TestStoreAnswerOpensOnItsFirstThread(t *testing.T) {
	corePorted(t, func() {
		corePin(t, loadNow)
		state := StateFromData(loadData(t, nil, loadTask("first"), loadTask("second")), nil, nil, nil)
		if deref(state.CurrentID) != "first" || !slices.Equal(state.History, []string{"first"}) || deref(state.DraftProjectID) != "project-1" {
			t.Fatalf("opened on %v %v %v", deref(state.CurrentID), state.History, deref(state.DraftProjectID))
		}
	})
}

func TestReviewersModeNeverBecomesTheDraft(t *testing.T) {
	corePorted(t, func() {
		corePin(t, loadNow)
		reviewer := loadTask("review", func(thread *domain.Thread) {
			thread.Role = new(domain.ThreadRoleReviewer)
			thread.ExecutionPolicy = domain.ExecutionPolicyBypass
		})
		older := loadTask("older", func(thread *domain.Thread) { thread.ExecutionPolicy = domain.ExecutionPolicyPlan })
		state := StateFromData(loadData(t, nil, reviewer, older), nil, nil, nil)
		if deref(state.CurrentID) != "review" {
			t.Fatal("the app still opens on the newest thread")
		}
		if state.DraftPolicy != domain.ExecutionPolicyPlan {
			t.Fatalf("draft policy %s", state.DraftPolicy)
		}
		if got := StateFromData(loadData(t, nil, reviewer), nil, nil, nil).DraftPolicy; got != domain.ExecutionPolicyConfirm {
			t.Fatalf("a store holding only a reviewer keeps %s", got)
		}
	})
}

func TestDraftModeHeldToTheDraftsEngine(t *testing.T) {
	corePorted(t, func() {
		corePin(t, loadNow)
		reviewer := loadTask("review", func(thread *domain.Thread) {
			thread.Engine = domain.AgentEngineOpencode
			thread.Model = new(domain.AgentModel("openrouter/z-ai/glm-5.3-flash"))
			thread.ExecutionPolicy = domain.ExecutionPolicyBypass
			thread.Role = new(domain.ThreadRoleReviewer)
		})
		older := loadTask("older", func(thread *domain.Thread) { thread.ExecutionPolicy = domain.ExecutionPolicyAutonomous })
		state := StateFromData(loadData(t, nil, reviewer, older), nil, nil, nil)
		if state.DraftEngine != domain.AgentEngineOpencode || state.DraftModel != "openrouter/z-ai/glm-5.3-flash" || state.DraftPolicy != domain.ExecutionPolicyAllowEdits {
			t.Fatalf("draft %s %s %s", state.DraftEngine, state.DraftModel, state.DraftPolicy)
		}
	})
}

func TestDraftEffortAndModelHeldToTheFirstThreadsEngine(t *testing.T) {
	corePorted(t, func() {
		corePin(t, loadNow)
		luna := loadTask("luna", func(thread *domain.Thread) {
			thread.Engine = domain.AgentEngineCodex
			thread.Model = new(domain.AgentModel("gpt-6-luna"))
			thread.Effort = new(domain.AgentEffort("ultra"))
			thread.FastMode = new(true)
		})
		state := StateFromData(loadData(t, nil, luna), nil, nil, nil)
		if state.DraftEffort != "max" || !state.DraftFastMode {
			t.Fatalf("draft effort %s fast %v", state.DraftEffort, state.DraftFastMode)
		}
		opencode := loadTask("open", func(thread *domain.Thread) {
			thread.Engine = domain.AgentEngineOpencode
			thread.Effort = new(domain.AgentEffort("max"))
		})
		unnamed := StateFromData(loadData(t, nil, opencode), nil, nil, nil)
		if unnamed.DraftEngine != domain.AgentEngineClaude || unnamed.DraftModel != "opus" {
			t.Fatalf("unnamed draft %s %s", unnamed.DraftEngine, unnamed.DraftModel)
		}
		status := &domain.EngineStatus{Opencode: &domain.EngineReadiness{Access: domain.EngineAccessReady, Catalog: []*domain.ModelSpec{{
			ID: "openrouter/z-ai/glm-5.3-flash", Label: "GLM", Description: "", ContextWindow: 8_000,
			Efforts: []*domain.EffortSpec{{ID: "low", Label: "Low"}},
		}}}}
		named := StateFromData(loadData(t, nil, opencode), nil, status, nil)
		if named.DraftEngine != domain.AgentEngineOpencode || named.DraftModel != "openrouter/z-ai/glm-5.3-flash" || named.DraftEffort != "low" {
			t.Fatalf("named draft %s %s %s", named.DraftEngine, named.DraftModel, named.DraftEffort)
		}
	})
}

func TestLastFolderNoProjectHoldsComesBackAsAProject(t *testing.T) {
	corePorted(t, func() {
		corePin(t, loadNow)
		state := StateFromData(loadData(t, new("/older")), nil, nil, nil)
		ids := []string{}
		for _, project := range state.Projects {
			ids = append(ids, project.ID)
		}
		if !slices.Equal(ids, []string{"project-1", domain.LegacyProjectID("/older")}) || deref(state.DraftProjectID) != domain.LegacyProjectID("/older") || state.CurrentID != nil {
			t.Fatalf("projects %v draft %v current %v", ids, deref(state.DraftProjectID), state.CurrentID)
		}
	})
}

func TestUntouchedSessionTakesTheStoresThread(t *testing.T) {
	corePorted(t, func() {
		corePin(t, loadNow)
		loaded := WithStoreData(EmptyWorkspaceState(nil), loadData(t, nil, loadTask("first")), nil)
		if deref(loaded.CurrentID) != "first" || deref(loaded.DraftProjectID) != "project-1" {
			t.Fatalf("loaded on %v %v", deref(loaded.CurrentID), deref(loaded.DraftProjectID))
		}
	})
}

func TestTypedSessionKeepsItsPlaceWhenTheStoreAnswers(t *testing.T) {
	corePorted(t, func() {
		corePin(t, loadNow)
		typed := coreWorkspace(func(state *WorkspaceState) { state.Prompts = map[string]string{"draft": "hello"} })
		loaded := WithStoreData(typed, loadData(t, nil, loadTask("first")), nil)
		if len(loaded.Threads) != 1 || loaded.Threads[0].ID != "first" || loaded.CurrentID != nil || loaded.DraftProjectID != nil || loaded.Prompts["draft"] != "hello" {
			t.Fatalf("typed session moved: %v %v", loaded.CurrentID, loaded.DraftProjectID)
		}
	})
}

func stateCheckout(id, root string) *domain.Worktree {
	return &domain.Worktree{ID: id, ProjectID: "project-1", Root: root, WorkspaceID: "workspace-" + id, BaseCommit: "abcdef1", CreatedAt: 1, LastUsedAt: 2}
}

func TestLoadKeepsOnlyClaimedSessionCheckouts(t *testing.T) {
	corePorted(t, func() {
		in := func(worktree string) func(*domain.Thread) {
			return func(thread *domain.Thread) {
				thread.ProjectID = new("project-1")
				thread.WorktreeID = &worktree
			}
		}
		state := coreWorkspace(func(state *WorkspaceState) {
			state.Threads = []*domain.Thread{coreTask("held", in("live")), coreTask("stale", in("unclaimed"))}
			state.Worktrees = []*domain.Worktree{stateCheckout("stored", "/session-stored"), stateCheckout("live", "/session-live"), stateCheckout("unclaimed", "/session-unclaimed")}
			state.CurrentID = new("held")
			state.ActiveRuns = map[string]ActiveRun{"held": coreActive("held", "run-1")}
		})
		stored := stateCheckout("stored", "/stored")
		data := &domain.ThreadStoreData{
			Tasks:     []*domain.StoredThread{coreStored(t, coreTask("from-store", in("stored")))},
			Projects:  []*domain.Project{{ID: "project-1", Root: "/project"}},
			Worktrees: []*domain.Worktree{stored},
		}
		loaded := WithStoreData(state, data, nil)
		ids := []string{}
		for _, thread := range loaded.Threads {
			ids = append(ids, thread.ID)
		}
		if !slices.Equal(ids, []string{"from-store", "held"}) {
			t.Fatalf("threads %v", ids)
		}
		coreEqualJSON(t, loaded.Worktrees, []*domain.Worktree{stored, stateCheckout("live", "/session-live")}, "worktrees")
	})
}
