package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestProjects(t *testing.T) {
	projects := []*Project{{ID: "p1", Root: "/work/app/"}, {ID: "p2", Root: "/other/app", Name: new("Site")}, {ID: "p3", Root: "/x/tools"}}
	if found, err := FindProject(projects, " /work/app "); err != nil || found.ID != "p1" {
		t.Fatal(found, err)
	}
	if found, err := FindProject(projects, "site"); err != nil || found.ID != "p2" {
		t.Fatal(found, err)
	}
	if _, err := FindProject(projects, "app"); err == nil || err.Error() != `More than one open project is named "app": /work/app/, /other/app. Name the folder path instead.` {
		t.Fatal(err)
	}
	if _, err := FindProject(nil, "app"); err == nil || err.Error() != `No project matches "app". Open projects: none.` {
		t.Fatal(err)
	}
	if LegacyProjectID("/a b/ü//") != "legacy-project-%2Fa%20b%2F%C3%BC" {
		t.Fatal(LegacyProjectID("/a b/ü//"))
	}
	if !IsProject(decoded(t, `{"id":"p","root":"/r","sortIndex":1,"unlisted":true}`)) || IsProject(decoded(t, `{"id":"p","root":"/r","name":" "}`)) || IsProject(decoded(t, `{"id":"p","root":"/r","unlisted":false}`)) {
		t.Fatal("isProject")
	}
}

func TestThreadTitles(t *testing.T) {
	if ClampTitle("  short  ") != "short" {
		t.Fatal("trim")
	}
	long := strings.Repeat("a", 60)
	if got := ClampTitle(long); got != strings.Repeat("a", 49)+"…" {
		t.Fatal(got)
	}
	if got := ForkTitle("Fix it (fork)", []string{"Fix it (fork)"}); got != "Fix it (fork 2)" {
		t.Fatal(got)
	}
	if got := ForkTitle(long, nil); got != strings.Repeat("a", 44)+"… (fork)" {
		t.Fatal(got)
	}
	if got := ForkTitle(" (fork 3)", nil); got != "(fork 3) (fork)" {
		t.Fatal(got)
	}
	thread := coordThread("t", func(thread *Thread) {
		thread.UpdatedAt = 9
		thread.Messages = []*ConversationMessage{{At: 3}, {At: 7, Withdrawn: new(true)}}
	})
	if ThreadCreatedAt(thread) != 3 || ThreadLastOutputAt(thread) != 3 {
		t.Fatal(ThreadCreatedAt(thread), ThreadLastOutputAt(thread))
	}
	thread.RunEndedAt = new(8.0)
	thread.HistorySummary = &StoredThreadHistorySummary{FirstMessageAt: new(1.0), LastAudibleAt: new(5.0)}
	if ThreadCreatedAt(thread) != 1 || ThreadLastOutputAt(thread) != 8 {
		t.Fatal(ThreadCreatedAt(thread), ThreadLastOutputAt(thread))
	}
}

func TestRetention(t *testing.T) {
	if got := ArchiveConsequence(true, true); got != "Its run stops and it leaves the list. Restore it from Settings › Archived threads on your computer within 5 days." {
		t.Fatal(got)
	}
	if got := ArchiveConsequence(false, false); got != "It leaves the sidebar. Restore it from Settings › Archived threads within 5 days." {
		t.Fatal(got)
	}
	old := &StoredThread{ID: "old", ArchivedAt: new(0.0)}
	fresh := &StoredThread{ID: "fresh", ArchivedAt: new(1.0)}
	live := &StoredThread{ID: "live"}
	kept := []*StoredThread{fresh, live}
	if got := RetainedThreads(kept, ArchiveRetentionMs); &got[0] != &kept[0] {
		t.Fatal("nothing dropped keeps the list")
	}
	got := RetainedThreads([]*StoredThread{live, old, fresh}, ArchiveRetentionMs)
	if len(got) != 2 || got[0] != live || got[1] != fresh {
		t.Fatal(got)
	}
}

func TestResourcesAndUsage(t *testing.T) {
	if !IsResourceStop(decoded(t, `{"at":0,"bytes":5,"cleanup":"complete"}`)) || IsResourceStop(decoded(t, `{"at":-1,"bytes":5,"cleanup":"complete"}`)) || IsResourceStop(decoded(t, `{"at":1,"bytes":5,"cleanup":"done"}`)) {
		t.Fatal("isResourceStop")
	}
	const gib = 1024 * 1024 * 1024
	total := 16.0 * gib
	if verdict, ok := UnsafeMemory([]*MemorySample{{At: 0, Bytes: 64 * miB, Available: 200 * miB}}, total); !ok || verdict != MemoryVerdictCritical {
		t.Fatal("critical")
	}
	sustained := []*MemorySample{}
	for i := range 4 {
		sustained = append(sustained, &MemorySample{At: float64(i) * 5_000, Bytes: 3 * gib, Available: 512 * miB})
	}
	if verdict, ok := UnsafeMemory(sustained, total); !ok || verdict != MemoryVerdictPressure {
		t.Fatal("pressure")
	}
	if _, ok := UnsafeMemory(sustained[:3], total); ok {
		t.Fatal("too few samples")
	}
	if !IsThreadUsage(decoded(t, `{"opus":12,"openai/gpt-9":0}`)) || IsThreadUsage(decoded(t, `{"opus":1.5}`)) || IsThreadUsage(decoded(t, `{"two words":1}`)) || IsThreadUsage(decoded(t, `[]`)) {
		t.Fatal("isThreadUsage")
	}
	if !reflect.DeepEqual(RememberProject([]string{"a", "b", "c", "d", "e"}, "c"), []string{"c", "a", "b", "d", "e"}) {
		t.Fatal("rememberProject")
	}
}
