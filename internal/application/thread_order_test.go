package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func threadsIDs(threads []*domain.Thread) []string {
	ids := make([]string, len(threads))
	for index, thread := range threads {
		ids[index] = thread.ID
	}
	return ids
}

func threadsOrdered(threads []*domain.Thread) []string {
	var live []*domain.Thread
	for _, thread := range threads {
		if thread.ArchivedAt == nil {
			live = append(live, thread)
		}
	}
	return threadsIDs(OrderThreads(live))
}

func thSorted(index float64) func(*domain.Thread) {
	return func(t *domain.Thread) { t.SortIndex = new(index) }
}
func thUpdated(at float64) func(*domain.Thread) { return func(t *domain.Thread) { t.UpdatedAt = at } }
func thProject(id string) func(*domain.Thread) {
	return func(t *domain.Thread) { t.ProjectID = new(id) }
}
func thCreated(at float64) func(*domain.Thread) {
	return func(t *domain.Thread) { t.CreatedAt = new(at) }
}
func thOutcome(value domain.ThreadOutcome) func(*domain.Thread) {
	return func(t *domain.Thread) { t.Outcome = new(value) }
}
func thRole(role domain.ThreadRole) func(*domain.Thread) {
	return func(t *domain.Thread) { t.Role = new(role) }
}
func thUnder(lead string) func(*domain.Thread) {
	return func(t *domain.Thread) { t.ParentID = new(lead) }
}
func thArchived(at float64) func(*domain.Thread) {
	return func(t *domain.Thread) { t.ArchivedAt = new(at) }
}

func threadsSame(t *testing.T, got, want []string, what string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

func TestSortIndexOutranksRecency(t *testing.T) {
	threads := []*domain.Thread{
		coreTask("a", thSorted(1), thUpdated(10)),
		coreTask("b", thSorted(0), thUpdated(5)),
		coreTask("older", thUpdated(2)),
		coreTask("newer", thUpdated(9)),
	}
	threadsSame(t, threadsIDs(OrderThreads(threads)), []string{"b", "a", "newer", "older"}, "order")
}

func TestBackfillFreezesLoadedOrder(t *testing.T) {
	settled := []*domain.Thread{coreTask("a", thSorted(0)), coreTask("b", thSorted(1))}
	if !jsonx.SameSlice(OrderThreads(settled), settled) || !jsonx.SameSlice(BackfillSortIndex(settled), settled) {
		t.Fatal("a settled list was copied")
	}
	mixed := BackfillSortIndex([]*domain.Thread{coreTask("a", thSorted(0)), coreTask("b", thUpdated(9)), coreTask("c", thUpdated(3))})
	for index, id := range []string{"a", "b", "c"} {
		if mixed[index].ID != id || *mixed[index].SortIndex != float64(index) {
			t.Fatalf("backfilled: %s", coreJSON(t, mixed))
		}
	}
	if NextSortIndex(mixed) != -1 || NextSortIndex(nil) != -1 || NextSortIndex([]*domain.Thread{coreTask("a", thSorted(-3)), coreTask("b", thSorted(4))}) != -4 {
		t.Fatal("next sort index")
	}
}

func TestDropLandsAtItsSlot(t *testing.T) {
	threads := []*domain.Thread{coreTask("a", thSorted(0)), coreTask("b", thSorted(1)), coreTask("c", thSorted(2))}
	threadsSame(t, threadsOrdered(MoveThread(threads, "c", domain.ThreadDropTarget{Index: 0})), []string{"c", "a", "b"}, "to the top")
	threadsSame(t, threadsOrdered(MoveThread(threads, "a", domain.ThreadDropTarget{Index: 1})), []string{"b", "a", "c"}, "one down")
	threadsSame(t, threadsOrdered(MoveThread(threads, "a", domain.ThreadDropTarget{Index: 9})), []string{"b", "c", "a"}, "past the end")
	for _, thread := range MoveThread(threads, "a", domain.ThreadDropTarget{Index: 0}) {
		if thread.UpdatedAt != 1 {
			t.Fatal("a drop touched recency")
		}
	}
}

func TestDropIntoAnotherProjectReparents(t *testing.T) {
	threads := []*domain.Thread{coreTask("a", thSorted(0), thProject("one")), coreTask("b", thSorted(1), thProject("two")), coreTask("loose", thSorted(2))}
	reparented := MoveThread(threads, "loose", domain.ThreadDropTarget{ProjectID: new("two"), Index: 0})
	if *threadsFind(reparented, "loose").ProjectID != "two" {
		t.Fatal("not reparented")
	}
	threadsSame(t, threadsOrdered(reparented), []string{"a", "loose", "b"}, "reparented")
	if detached := MoveThread(reparented, "loose", domain.ThreadDropTarget{Index: 0}); threadsFind(detached, "loose").ProjectID != nil {
		t.Fatal("not detached")
	}
	empty := MoveThread([]*domain.Thread{coreTask("a", thSorted(0), thProject("one")), coreTask("loose", thSorted(1))}, "loose", domain.ThreadDropTarget{ProjectID: new("empty"), Index: 0})
	if *threadsFind(empty, "loose").ProjectID != "empty" {
		t.Fatal("did not join an empty project")
	}
	threadsSame(t, threadsOrdered(empty), []string{"loose", "a"}, "empty project")
}

func TestPinnedThreadsStayInTheirFolder(t *testing.T) {
	threads := []*domain.Thread{coreTask("a", thSorted(0)), coreTask("gone", thSorted(1), thArchived(5))}
	if !jsonx.SameSlice(MoveThread(threads, "missing", domain.ThreadDropTarget{}), threads) || !jsonx.SameSlice(MoveThread(threads, "gone", domain.ThreadDropTarget{}), threads) {
		t.Fatal("unknown or archived thread moved")
	}
	inWorktree := func(t *domain.Thread) { t.WorktreeID = new("wt1") }
	checkout := []*domain.Thread{
		coreTask("loose", thProject("project-1"), thSorted(0)),
		coreTask("first", thProject("project-1"), inWorktree, thSorted(1)),
		coreTask("second", thProject("project-1"), inWorktree, thSorted(2)),
	}
	threadsSame(t, threadsOrdered(MoveThread(checkout, "second", domain.ThreadDropTarget{ProjectID: new("project-1"), Index: 0})), []string{"second", "loose", "first"}, "reorder in a checkout")
	if !jsonx.SameSlice(MoveThread(checkout, "first", domain.ThreadDropTarget{ProjectID: new("project-2")}), checkout) || !jsonx.SameSlice(MoveThread(checkout, "first", domain.ThreadDropTarget{}), checkout) {
		t.Fatal("a checkout thread left its project")
	}
	led := []*domain.Thread{
		coreTask("lead", thProject("project-1"), thRole(domain.ThreadRoleCoordinator), thSorted(0)),
		coreTask("member", thProject("project-1"), thUnder("lead"), thSorted(1)),
		coreTask("loose", thProject("project-1"), thSorted(2)),
	}
	if !jsonx.SameSlice(MoveThread(led, "member", domain.ThreadDropTarget{ProjectID: new("project-2")}), led) || !jsonx.SameSlice(MoveThread(led, "member", domain.ThreadDropTarget{ProjectID: new("project-1")}), led) {
		t.Fatal("a member moved away from its coordinator")
	}
	freed := slices.Clone(led)
	lead := *led[0]
	lead.ArchivedAt = new(5.0)
	freed[0] = &lead
	if moved := MoveThread(freed, "member", domain.ThreadDropTarget{ProjectID: new("project-2")}); *threadsFind(moved, "member").ProjectID != "project-2" {
		t.Fatal("a freed member could not move")
	}
}

func TestThreadMoveBlock(t *testing.T) {
	laptop := &ThreadHost{Name: "laptop"}
	inWorktree := func(t *domain.Thread) { t.WorktreeID = new("wt1") }
	for _, item := range []struct {
		thread      *domain.Thread
		host        *ThreadHost
		coordinated bool
		want        string
	}{
		{coreTask("free"), nil, false, ""},
		{coreTask("away"), laptop, false, "Stays in its folder on laptop"},
		{coreTask("checkout", inWorktree), laptop, true, "Stays in its folder on laptop"},
		{coreTask("checkout", inWorktree), nil, true, "Stays in its worktree's folder"},
		{coreTask("member"), nil, true, "Stays with its coordinator"},
	} {
		if got, _ := ThreadMoveBlock(item.thread, item.host, item.coordinated); got != item.want {
			t.Errorf("%s: %q", item.thread.ID, got)
		}
	}
}

func TestActivitySectionsPartitionThreads(t *testing.T) {
	threads := []*domain.Thread{
		coreTask("quiet", thCreated(1)),
		coreTask("busy", thCreated(2)),
		coreTask("asked", thCreated(3)),
		coreTask("settled", thCreated(5), thOutcome(domain.ThreadOutcomeFinished)),
		coreTask("newest", thCreated(9)),
		coreTask("both", thCreated(4), thOutcome(domain.ThreadOutcomeFinished)),
		coreTask("asked-again", thCreated(6), thOutcome(domain.ThreadOutcomeFailed)),
	}
	sections := ThreadOrderActivitySections(threads, jsonx.SetOf("busy", "both", "asked", "asked-again"), jsonx.SetOf("asked", "asked-again"))
	threadsSame(t, threadsIDs(sections[ActivitySectionsKeyPriority]), []string{"asked-again", "settled", "asked"}, "priority")
	threadsSame(t, threadsIDs(sections[ActivitySectionsKeyRunning]), []string{"both", "busy"}, "running")
	threadsSame(t, threadsIDs(sections[ActivitySectionsKeyThreads]), []string{"newest", "quiet"}, "threads")

	ties := []*domain.Thread{
		coreTask("priority-first", thOutcome(domain.ThreadOutcomeFinished)),
		coreTask("running", thSorted(0)),
		coreTask("thread-first"),
		coreTask("priority-second", thOutcome(domain.ThreadOutcomeFailed)),
		coreTask("thread-second"),
	}
	tied := ThreadOrderActivitySections(ties, jsonx.SetOf("running"), jsonx.Set[string]{})
	threadsSame(t, threadsIDs(tied[ActivitySectionsKeyPriority]), []string{"priority-first", "priority-second"}, "tied priority keeps input order")
	threadsSame(t, threadsIDs(tied[ActivitySectionsKeyThreads]), []string{"thread-first", "thread-second"}, "tied threads keep input order")
}

func TestReviewersStayInThreadsUntilApprovalIsNeeded(t *testing.T) {
	for _, engine := range []domain.AgentEngine{domain.AgentEngineClaude, domain.AgentEngineCodex} {
		on := func(t *domain.Thread) { t.Engine = engine }
		reviewer := thRole(domain.ThreadRoleReviewer)
		threads := []*domain.Thread{
			coreTask("working", on, reviewer),
			coreTask("finished", on, reviewer, thOutcome(domain.ThreadOutcomeFinished), func(t *domain.Thread) { t.OutcomeUnread = new(true) }),
			coreTask("failed", on, reviewer, thOutcome(domain.ThreadOutcomeFailed)),
			coreTask("findings", on, reviewer, func(t *domain.Thread) {
				t.Findings = []*domain.AutomationFinding{{ID: "finding", Headline: "Needs a fix", At: 1}}
			}),
			coreTask("approval", on, reviewer),
			coreTask("implementer", on, thRole(domain.ThreadRoleImplementer)),
		}
		busy := jsonx.SetOf("working", "approval", "implementer")
		sections := ThreadOrderActivitySections(threads, busy, jsonx.SetOf("approval"))
		threadsSame(t, threadsIDs(sections[ActivitySectionsKeyThreads]), []string{"working", "finished", "failed", "findings"}, "reviewers")
		threadsSame(t, threadsIDs(sections[ActivitySectionsKeyPriority]), []string{"approval"}, "approval")
		threadsSame(t, threadsIDs(sections[ActivitySectionsKeyRunning]), []string{"implementer"}, "running")
	}
}

func TestRecentInIsTheProjectsOwn(t *testing.T) {
	threads := []*domain.Thread{
		coreTask("old", thProject("p"), thUpdated(1)),
		coreTask("new", thProject("p"), thUpdated(9)),
		coreTask("lead", thProject("p"), thRole(domain.ThreadRoleCoordinator), thUpdated(5)),
		coreTask("member", thProject("p"), thUnder("lead"), thUpdated(8)),
		coreTask("other", thProject("q"), thUpdated(7)),
		coreTask("chat", thUpdated(6)),
		coreTask("gone", thProject("p"), thUpdated(10), thArchived(11)),
	}
	threadsSame(t, threadsIDs(RecentIn(threads, new("p"), 2)), []string{"new", "lead"}, "project p")
	threadsSame(t, threadsIDs(RecentIn(threads, nil, 4)), []string{"chat"}, "no project")
}
