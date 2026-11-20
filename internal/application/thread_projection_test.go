package application

import (
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const (
	projectionHour = 60 * 60 * 1000.0
	projectionNow  = 1_800_000_000_000.0
)

func projectionMessage(text string, at float64, kind domain.ConversationMessageKind) *domain.ConversationMessage {
	return &domain.ConversationMessage{ID: text, Kind: kind, Text: text, At: at}
}

func projectionTask(id string, edits ...func(*domain.Thread)) *domain.Thread {
	base := func(t *domain.Thread) {
		t.LastChangeSnapshot.CapturedAt = projectionNow
		t.CreatedAt = new(projectionNow - 10*projectionHour)
		t.UpdatedAt = projectionNow
	}
	return coreTask(id, append([]func(*domain.Thread){base}, edits...)...)
}

func projectionSaying(messages ...*domain.ConversationMessage) func(*domain.Thread) {
	return func(t *domain.Thread) { t.Messages = messages }
}

func projectionWorkspace(threads []*domain.Thread, edits ...func(*WorkspaceState)) *WorkspaceState {
	return coreWorkspace(append([]func(*WorkspaceState){func(s *WorkspaceState) {
		s.Threads = threads
		s.Projects = []*domain.Project{{ID: "project-app", Root: "/code/app"}, {ID: "project-site", Root: "/code/site"}}
	}}, edits...)...)
}

func projectionIDs(summaries []*contracts.ThreadSummary) []string {
	ids := make([]string, len(summaries))
	for index, summary := range summaries {
		ids[index] = summary.ID
	}
	return ids
}

func TestThreadScopeIsTheCallersProject(t *testing.T) {
	state := projectionWorkspace([]*domain.Thread{projectionTask("in-app", thProject("project-app")), projectionTask("loose")})
	scope := func(caller string, project *string) contracts.ProjectScope {
		t.Helper()
		resolved, err := ResolveScope(state, caller, project)
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	coreEqualJSON(t, scope("in-app", nil), `{"kind":"project","projectId":"project-app"}`, "current")
	coreEqualJSON(t, scope("loose", nil), `{"kind":"projectless"}`, "projectless")
	coreEqualJSON(t, scope("in-app", new("all")), `{"kind":"all"}`, "all")
	coreEqualJSON(t, scope("loose", new("/code/site")), `{"kind":"project","projectId":"project-site"}`, "by path")
	if _, err := ResolveScope(state, "loose", new("/code/missing")); err == nil || !strings.Contains(err.Error(), "No project matches") {
		t.Fatalf("missing: %v", err)
	}
	threadsSame(t, projectionIDs(ThreadSummaries(state, contracts.ThreadFilter{Scope: contracts.ProjectScopeProjectless{}}, projectionNow)), []string{"loose"}, "projectless")
	all := projectionIDs(ThreadSummaries(state, contracts.ThreadFilter{Scope: contracts.ProjectScopeAll{}}, projectionNow))
	slices.Sort(all)
	threadsSame(t, all, []string{"in-app", "loose"}, "all")
}

func TestIdlenessCountsRealActivity(t *testing.T) {
	user := domain.ConversationMessageKindUser
	state := projectionWorkspace([]*domain.Thread{
		projectionTask("chatting", projectionSaying(projectionMessage("hello", projectionNow-projectionHour, user))),
		projectionTask("stale", projectionSaying(projectionMessage("hello", projectionNow-6*projectionHour, user)), func(t *domain.Thread) { t.RunEndedAt = new(projectionNow - 5*projectionHour) }),
		projectionTask("untouched", func(t *domain.Thread) { t.CreatedAt = new(projectionNow - 9*projectionHour) }),
	})
	idle := ThreadSummaries(state, contracts.ThreadFilter{Scope: contracts.ProjectScopeAll{}, IdleForMs: new(3 * projectionHour)}, projectionNow)
	threadsSame(t, projectionIDs(idle), []string{"stale", "untouched"}, "idle")
	if idle[0].LastActivityAt != projectionNow-5*projectionHour {
		t.Fatalf("activity %v", idle[0].LastActivityAt)
	}
}

func TestArchivedAndSearchedSummaries(t *testing.T) {
	user := domain.ConversationMessageKindUser
	state := projectionWorkspace([]*domain.Thread{
		projectionTask("live", projectionSaying(projectionMessage("fix the header", projectionNow-projectionHour, user))),
		projectionTask("filed", thArchived(projectionNow-projectionHour), projectionSaying(projectionMessage("fix the header", projectionNow-2*projectionHour, user))),
	})
	ids := func(filter contracts.ThreadFilter) []string {
		filter.Scope = contracts.ProjectScopeAll{}
		return projectionIDs(ThreadSummaries(state, filter, projectionNow))
	}
	threadsSame(t, ids(contracts.ThreadFilter{}), []string{"live"}, "live")
	threadsSame(t, ids(contracts.ThreadFilter{Archived: new(true)}), []string{"filed"}, "archived")
	threadsSame(t, ids(contracts.ThreadFilter{Search: new("HEADER")}), []string{"live"}, "search")
	threadsSame(t, ids(contracts.ThreadFilter{Search: new("footer")}), []string{}, "no match")
	threadsSame(t, ids(contracts.ThreadFilter{Limit: new(0.0)}), []string{}, "limit 0")

	unloaded := func(t *domain.Thread) { t.HistorySummary = &domain.StoredThreadHistorySummary{MessageCount: 1} }
	stored := projectionWorkspace([]*domain.Thread{
		projectionTask("loaded", projectionSaying(projectionMessage("fix the header", projectionNow-projectionHour, user))),
		projectionTask("stored", unloaded),
		projectionTask("unmatched", unloaded),
		projectionTask("header title", unloaded),
	})
	sortedIDs := func(filter contracts.ThreadFilter) []string {
		filter.Scope = contracts.ProjectScopeAll{}
		found := projectionIDs(ThreadSummaries(stored, filter, projectionNow))
		slices.Sort(found)
		return found
	}
	threadsSame(t, sortedIDs(contracts.ThreadFilter{Search: new("header")}), []string{"header title", "loaded"}, "unloaded by title")
	threadsSame(t, sortedIDs(contracts.ThreadFilter{Search: new("header"), Stored: jsonx.SetOf("stored", "loaded")}), []string{"header title", "loaded", "stored"}, "stored matches")
	threadsSame(t, sortedIDs(contracts.ThreadFilter{Search: new("footer"), Stored: jsonx.SetOf("loaded")}), []string{}, "loaded answers from memory")
	threadsSame(t, sortedIDs(contracts.ThreadFilter{Search: new("header"), Stored: jsonx.SetOf("stored"), Limit: new(5.0)}), []string{"header title", "loaded", "stored"}, "limited")
}

func TestAttachmentsFilterAndCount(t *testing.T) {
	user := domain.ConversationMessageKindUser
	shot := projectionMessage("look at this", projectionNow-projectionHour, user)
	shot.Attachments = []string{"/tmp/shot-1.png", "/tmp/shot-2.png"}
	last := projectionMessage("one more", projectionNow, user)
	last.Attachments = []string{"/tmp/shot-3.png"}
	state := projectionWorkspace([]*domain.Thread{
		projectionTask("illustrated", projectionSaying(shot, projectionMessage("and this", projectionNow-projectionHour/2, user), last)),
		projectionTask("wordy", projectionSaying(projectionMessage("no pictures here", projectionNow-2*projectionHour, user))),
	})
	threadsSame(t, projectionIDs(ThreadSummaries(state, contracts.ThreadFilter{Scope: contracts.ProjectScopeAll{}, Attachments: new(true)}, projectionNow)), []string{"illustrated"}, "filtered")
	all := ThreadSummaries(state, contracts.ThreadFilter{Scope: contracts.ProjectScopeAll{}}, projectionNow)
	threadsSame(t, projectionIDs(all), []string{"illustrated", "wordy"}, "unfiltered")
	if all[0].AttachmentCount != 2 || all[1].AttachmentCount != 0 {
		t.Fatalf("counts %v %v", all[0].AttachmentCount, all[1].AttachmentCount)
	}

	illustrated := func(id string, at float64, text string) *domain.Thread {
		message := projectionMessage(text, at, user)
		message.Attachments = []string{"/tmp/" + id + ".png"}
		return projectionTask(id, projectionSaying(message))
	}
	ties := projectionWorkspace([]*domain.Thread{
		illustrated("newest", projectionNow, "keep"),
		illustrated("tie-first", projectionNow-projectionHour, "keep"),
		illustrated("tie-second", projectionNow-projectionHour, "keep"),
		illustrated("wrong-search", projectionNow+projectionHour, "discard"),
		projectionTask("no-image", projectionSaying(projectionMessage("keep", projectionNow+projectionHour, user))),
	})
	filter := contracts.ThreadFilter{Scope: contracts.ProjectScopeAll{}, Search: new("keep"), Attachments: new(true)}
	unlimited := ThreadSummaries(ties, filter, projectionNow)
	filter.Limit = new(2.0)
	limited := ThreadSummaries(ties, filter, projectionNow)
	threadsSame(t, projectionIDs(unlimited), []string{"newest", "tie-first", "tie-second"}, "unlimited")
	coreEqualJSON(t, limited, unlimited[:2], "limited")
}

func TestTranscriptKeepsTheNewestMessages(t *testing.T) {
	user := domain.ConversationMessageKindUser
	var messages []*domain.ConversationMessage
	for index := range 5 {
		messages = append(messages, projectionMessage("turn "+string(rune('0'+index)), projectionNow-float64(5-index)*projectionHour, user))
	}
	state := projectionWorkspace([]*domain.Thread{projectionTask("long", thProject("project-app"), projectionSaying(messages...))})
	transcript := ThreadTranscript(state, "long", 2, 0, nil)
	if transcript == nil || len(transcript.Messages) != 2 || transcript.Messages[0].Text != "turn 3" || transcript.Messages[1].Text != "turn 4" || transcript.Omitted != 3 {
		t.Fatalf("transcript: %s", coreJSON(t, transcript))
	}
	if *transcript.Thread.ProjectRoot != "/code/app" || transcript.Thread.MessageCount != 5 || ThreadTranscript(state, "missing", defaultTranscriptMessages, 0, nil) != nil {
		t.Fatal("summary")
	}
	noisy := projectionWorkspace([]*domain.Thread{projectionTask("noisy", projectionSaying(projectionMessage(strings.Repeat("x", 5000), projectionNow, user)))})
	only := ThreadTranscript(noisy, "noisy", defaultTranscriptMessages, 0, nil).Messages[0]
	if threadsUTF16Len(only.Text) != 2001 || !strings.HasSuffix(only.Text, "…") {
		t.Fatalf("clipped to %d", threadsUTF16Len(only.Text))
	}
}

func TestSummaryRunningWhileWorkIsGoing(t *testing.T) {
	state := projectionWorkspace([]*domain.Thread{projectionTask("running"), projectionTask("resolving"), projectionTask("queued"), projectionTask("done")}, func(s *WorkspaceState) {
		s.ActiveRuns = map[string]ActiveRun{"running": coreActive("running", "run-1")}
		s.PendingRuns = map[string]*PendingRun{"pending-1": {ID: "pending-1", RunID: "run-2", Origin: PendingRunOriginComposer, TaskID: new("resolving"), Text: "go", Prompt: "go", Attachments: []string{}}}
		s.QueuedMessages = map[string][]*QueuedMessage{"queued": {{HeldMessage: domain.HeldMessage{ID: "message-1", Text: "next", Prompt: "next", Attachments: []string{}}}}}
	})
	var running []string
	for _, summary := range ThreadSummaries(state, contracts.ThreadFilter{Scope: contracts.ProjectScopeAll{}}, projectionNow) {
		if summary.Status == contracts.ThreadSummaryStatusRunning {
			running = append(running, summary.ID)
		}
	}
	slices.Sort(running)
	threadsSame(t, running, []string{"queued", "resolving", "running"}, "running")
}

func TestWaitReportsTheLastReply(t *testing.T) {
	state := projectionWorkspace([]*domain.Thread{projectionTask("answered", projectionSaying(
		projectionMessage("do it", projectionNow-projectionHour, domain.ConversationMessageKindUser),
		projectionMessage("still working", projectionNow-3*projectionHour/4, domain.ConversationMessageKindAssistant),
		projectionMessage("done", projectionNow-projectionHour/2, domain.ConversationMessageKindAssistant),
		projectionMessage("Bash", projectionNow, domain.ConversationMessageKindTool),
	))})
	waited := ThreadWaitResult(state, "answered", false)
	if waited == nil || *waited.Reply != "done" || waited.TimedOut || waited.Thread.Status != contracts.ThreadSummaryStatusIdle {
		t.Fatalf("waited: %s", coreJSON(t, waited))
	}
	if ThreadWaitResult(state, "missing", false) != nil || ThreadWaitResult(projectionWorkspace([]*domain.Thread{projectionTask("silent")}), "silent", true).Reply != nil {
		t.Fatal("missing or silent")
	}
}

func TestWorktreeThreadReportsItsCheckout(t *testing.T) {
	worktree := &domain.Worktree{ID: "wt1", ProjectID: "project-app", Root: "/worktrees/app-wt1", WorkspaceID: "worktree-1", BaseCommit: "abcdef1", CreatedAt: 1, LastUsedAt: 1}
	state := projectionWorkspace([]*domain.Thread{
		projectionTask("task-a", thProject("project-app"), func(t *domain.Thread) { t.WorktreeID = new("wt1") }),
		projectionTask("task-b", thProject("project-app")),
	}, func(s *WorkspaceState) { s.Worktrees = []*domain.Worktree{worktree} })
	summaries := ThreadSummaries(state, contracts.ThreadFilter{Scope: contracts.ProjectScopeAll{}}, projectionNow)
	slices.SortFunc(summaries, func(left, right *contracts.ThreadSummary) int { return strings.Compare(left.ID, right.ID) })
	if *summaries[0].WorktreeRoot != "/worktrees/app-wt1" || *summaries[0].ProjectRoot != "/code/app" || summaries[1].WorktreeRoot != nil {
		t.Fatalf("summaries: %s", coreJSON(t, summaries))
	}
}

func TestHandleOptionsMarkTheDraftsProject(t *testing.T) {
	titled := func(title string) func(*domain.Thread) { return func(t *domain.Thread) { t.Title = title } }
	state := projectionWorkspace([]*domain.Thread{
		projectionTask("in-app", thProject("project-app"), titled("Raise the dock")),
		projectionTask("in-site", thProject("project-site"), titled("Raise the panel")),
		projectionTask("caller", thProject("project-app")),
		projectionTask("gone", thProject("project-app"), thArchived(projectionNow)),
	})
	options := ThreadHandleOptions(state, "caller")
	if len(options) != 2 || options[0].ID != "in-app" || options[0].Handle != "raise-the-dock" || !options[0].InScope ||
		options[1].ID != "in-site" || options[1].Handle != "site/raise-the-panel" || options[1].InScope {
		t.Fatalf("options: %s", coreJSON(t, options))
	}
	draft := projectionWorkspace([]*domain.Thread{projectionTask("in-app", thProject("project-app")), projectionTask("in-site", thProject("project-site"))}, func(s *WorkspaceState) {
		s.DraftProjectID = new("project-site")
	})
	drafted := ThreadHandleOptions(draft, "draft:project-site")
	if drafted[0].ID != "in-app" || drafted[0].InScope || drafted[1].ID != "in-site" || !drafted[1].InScope {
		t.Fatalf("draft scope: %s", coreJSON(t, drafted))
	}
}

func TestThreadReferences(t *testing.T) {
	ended := func(at float64) func(*domain.Thread) { return func(t *domain.Thread) { t.RunEndedAt = new(at) } }
	titled := func(title string) func(*domain.Thread) { return func(t *domain.Thread) { t.Title = title } }
	state := projectionWorkspace([]*domain.Thread{
		projectionTask("t-9f2c00", titled("Sink the mode choices"), ended(projectionNow-projectionHour)),
		projectionTask("t-3a1100", titled("Sink the mode choices"), ended(projectionNow)),
		projectionTask("t-3a2200", titled("Mode picker"), ended(projectionNow)),
	})
	idOf := func(reference string) string {
		if found := FindThread(state, reference); found != nil {
			return found.ID
		}
		return ""
	}
	if idOf("t-9f2c00") != "t-9f2c00" || idOf("t-9f2c") != "t-9f2c00" || idOf("mode picker") != "t-3a2200" || idOf("Sink the mode choices") != "" || idOf("t-3a") != "" || idOf("nothing at all") != "" || idOf("  ") != "" {
		t.Fatal("references")
	}
	_, both := ThreadMatches(state, "sink the mode choices")
	threadsSame(t, threadsIDs(both), []string{"t-9f2c00", "t-3a1100"}, "title matches")

	ranked := projectionWorkspace([]*domain.Thread{
		projectionTask("topic-newest-prefix", titled("Something else")),
		projectionTask("title-first", titled("Topic")),
		projectionTask("title-second", titled(" topic "), thArchived(projectionNow)),
		projectionTask("prefix-first", titled("prefix-second")),
		projectionTask("prefix-second"),
	})
	for reference, want := range map[string]string{"topic": "title-first", "title-second": "title-second", "prefix-second": "prefix-second", "prefix-": ""} {
		found := FindThread(ranked, reference)
		if found == nil && want != "" || found != nil && found.ID != want {
			t.Errorf("%q found %v", reference, found)
		}
	}
	live := projectionWorkspace([]*domain.Thread{projectionTask("old", titled("abc"), thArchived(projectionNow)), projectionTask("abc123", titled("Live"))})
	if FindThread(live, "abc").ID != "abc123" {
		t.Fatal("a live prefix loses to an archived title")
	}
	archivedOnly := projectionWorkspace([]*domain.Thread{projectionTask("old", titled("abc"), thArchived(projectionNow)), projectionTask("abd", titled("Other"), thArchived(projectionNow))})
	if FindThread(archivedOnly, "abc").ID != "old" {
		t.Fatal("among archived threads a title outranks a prefix")
	}
}

func TestClipTextKeepsSurrogatePairsWhole(t *testing.T) {
	text := strings.Repeat("a", 4) + "😀tail"
	for limit, want := range map[int]string{5: "aaaa…", 6: "aaaa😀…", 4: "aaaa…"} {
		if got := ClipText(text, limit); got != want {
			t.Errorf("ClipText(%d) = %q", limit, got)
		}
	}
	if ClipText("short", 10) != "short" {
		t.Fatal("short")
	}
}
