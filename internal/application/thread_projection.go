package application

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Enough of a message to recognise what happened without carrying a whole transcript.
const messageTextLimit = 2_000

const defaultTranscriptMessages = 30

// The projects a thread listing can name, given as `project` (nil for the caller's own). The error's
// text is what the caller is told.
func ResolveScope(state *WorkspaceState, callerThreadID string, project *string) (contracts.ProjectScope, error) {
	if project != nil && *project == "all" {
		return contracts.ProjectScopeAll{}, nil
	}
	if project == nil || *project == "current" {
		if caller := threadsFind(state.Threads, callerThreadID); caller != nil && caller.ProjectID != nil && *caller.ProjectID != "" {
			return contracts.ProjectScopeProject{ProjectID: *caller.ProjectID}, nil
		}
		return contracts.ProjectScopeProjectless{}, nil
	}
	match, err := domain.FindProject(state.Projects, *project)
	if err != nil {
		return nil, err
	}
	return contracts.ProjectScopeProject{ProjectID: match.ID}, nil
}

// Nil when no thread is named by threadID.
func ThreadWaitResult(state *WorkspaceState, threadID string, timedOut bool) *contracts.ThreadWaitResult {
	thread := FindThread(state, threadID)
	if thread == nil {
		return nil
	}
	var reply *string
	for index := len(thread.Messages) - 1; index >= 0; index-- {
		if message := thread.Messages[index]; message.Kind == domain.ConversationMessageKindAssistant {
			text := message.Text
			reply = &text
			break
		}
	}
	return &contracts.ThreadWaitResult{Thread: *ThreadSummary(state, thread, nil), TimedOut: timedOut, Reply: reply}
}

type projectionIndexResult struct {
	projects  map[string]*domain.Project
	worktrees map[string]*domain.Worktree
}

// Shared lookups for a whole thread listing. First wins, matching the array searches they replace.
func projectionIndex(state *WorkspaceState) *projectionIndexResult {
	index := &projectionIndexResult{projects: map[string]*domain.Project{}, worktrees: map[string]*domain.Worktree{}}
	for _, project := range state.Projects {
		if _, ok := index.projects[project.ID]; !ok {
			index.projects[project.ID] = project
		}
	}
	for _, worktree := range state.Worktrees {
		if _, ok := index.worktrees[worktree.ID]; !ok {
			index.worktrees[worktree.ID] = worktree
		}
	}
	return index
}

// `index` nil looks projects and checkouts up in the state; `attachments` nil counts them.
func projectThreadSummary(state *WorkspaceState, thread *domain.Thread, activity float64, index *projectionIndexResult, attachments *int) *contracts.ThreadSummary {
	var project *domain.Project
	var worktree *domain.Worktree
	if index != nil {
		if thread.ProjectID != nil && *thread.ProjectID != "" {
			project = index.projects[*thread.ProjectID]
		}
		if thread.WorktreeID != nil && *thread.WorktreeID != "" {
			worktree = index.worktrees[*thread.WorktreeID]
		}
	} else {
		project = ProjectFor(state, thread)
		worktree = WorktreeFor(state, thread)
	}
	summary := &contracts.ThreadSummary{
		ID:             thread.ID,
		Title:          thread.Title,
		Archived:       thread.ArchivedAt != nil,
		CreatedAt:      domain.ThreadCreatedAt(thread),
		LastActivityAt: activity,
	}
	if thread.Role != nil && *thread.Role != "" {
		summary.Role = thread.Role
	}
	if thread.ProjectID != nil && *thread.ProjectID != "" {
		summary.ProjectID = thread.ProjectID
	}
	if project != nil {
		root := project.Root
		summary.ProjectRoot = &root
	}
	if worktree != nil {
		id, root := worktree.ID, worktree.Root
		summary.WorktreeID, summary.WorktreeRoot = &id, &root
	}
	if ThreadActivityThreadActivity(state).Working.Has(thread.ID) {
		summary.Status = contracts.ThreadSummaryStatusRunning
	} else {
		summary.Status = contracts.ThreadSummaryStatus(RunStatus(state, &thread.ID))
	}
	if thread.LimitPause != nil {
		resets := thread.LimitPause.ResetsAt
		summary.PausedUntil = &resets
	}
	if thread.HistorySummary != nil {
		summary.MessageCount = thread.HistorySummary.MessageCount
	} else {
		summary.MessageCount = float64(len(thread.Messages))
	}
	if attachments != nil {
		summary.AttachmentCount = float64(*attachments)
	} else {
		summary.AttachmentCount = float64(countAttachments(thread))
	}
	return summary
}

// `index` may be nil.
func ThreadSummary(state *WorkspaceState, thread *domain.Thread, index *projectionIndexResult) *contracts.ThreadSummary {
	return projectThreadSummary(state, thread, domain.ThreadLastOutputAt(thread), index, nil)
}

// Newest activity first, so a limit keeps the threads worth looking at.
func ThreadSummaries(state *WorkspaceState, filter contracts.ThreadFilter, at float64) []*contracts.ThreadSummary {
	search := ""
	if filter.Search != nil {
		search = strings.ToLower(threadsTrim(*filter.Search))
	}
	forked := SideChatIds(state)
	archived := filter.Archived != nil && *filter.Archived
	wantsAttachments := filter.Attachments != nil && *filter.Attachments
	type match struct {
		thread      *domain.Thread
		activity    float64
		attachments *int
	}
	var matching []match
	for _, thread := range state.Threads {
		if forked.Has(thread.ID) || !inScope(thread, filter.Scope) || (thread.ArchivedAt != nil) != archived {
			continue
		}
		activity := domain.ThreadLastOutputAt(thread)
		if filter.IdleForMs != nil && at-activity < *filter.IdleForMs {
			continue
		}
		if search != "" && !matches(thread, search, filter.Stored) {
			continue
		}
		var attachments *int
		if wantsAttachments {
			count := countAttachments(thread)
			if count == 0 {
				continue
			}
			attachments = &count
		}
		matching = append(matching, match{thread: thread, activity: activity, attachments: attachments})
	}
	if len(matching) == 0 {
		return []*contracts.ThreadSummary{}
	}
	if filter.Limit == nil {
		index := projectionIndex(state)
		summaries := make([]*contracts.ThreadSummary, len(matching))
		for at, item := range matching {
			summaries[at] = projectThreadSummary(state, item.thread, item.activity, index, item.attachments)
		}
		slices.SortStableFunc(summaries, func(left, right *contracts.ThreadSummary) int {
			return cmp.Compare(right.LastActivityAt, left.LastActivityAt)
		})
		return summaries
	}
	limit := math.Max(0, *filter.Limit)
	if limit == 0 {
		return []*contracts.ThreadSummary{}
	}
	slices.SortStableFunc(matching, func(left, right match) int { return cmp.Compare(right.activity, left.activity) })
	retained := matching[:int(math.Min(limit, float64(len(matching))))]
	index := projectionIndex(state)
	summaries := make([]*contracts.ThreadSummary, len(retained))
	for at, item := range retained {
		summaries[at] = projectThreadSummary(state, item.thread, item.activity, index, item.attachments)
	}
	return summaries
}

// The threads a draft may name, keyed by the draft rather than the thread, since a draft can be
// older than the thread it starts. Every project is offered, since a query searches them all;
// `inScope` is what keeps an unqualified `@` to the project the draft is being written in.
func ThreadHandleOptions(state *WorkspaceState, draftKey string) []*domain.ThreadHandleOption {
	forked := SideChatIds(state)
	index := projectionIndex(state)
	working := ThreadActivityThreadActivity(state).Working
	// A draft belonging to no thread yet is being written wherever the sidebar is pointed.
	projectID := state.DraftProjectID
	if caller := threadsFind(state.Threads, draftKey); caller != nil {
		projectID = caller.ProjectID
	}
	options := []*domain.ThreadHandleOption{}
	for _, thread := range state.Threads {
		if thread.ID == draftKey || forked.Has(thread.ID) || thread.ArchivedAt != nil {
			continue
		}
		option := &domain.ThreadHandleOption{
			ID:             thread.ID,
			Title:          thread.Title,
			InScope:        threadsSameString(thread.ProjectID, projectID),
			Running:        working.Has(thread.ID),
			LastActivityAt: domain.ThreadLastOutputAt(thread),
		}
		if thread.ProjectID != nil && *thread.ProjectID != "" {
			if project := index.projects[*thread.ProjectID]; project != nil {
				name := domain.ProjectName(project)
				option.Project = &name
			}
		}
		options = append(options, option)
	}
	return domain.ThreadHandles(options)
}

// How closely threads match a reference, closest first: no weaker match ever stands in a closer one's way.
const matchRanks = 5

// The threads a reference could mean, and how closely (the rank): the one whose id it is, else the
// live threads it is the title of, else the live ones whose id it begins, then the same two among
// archived threads, which a live thread always outranks. More than one is a reference too loose to act on.
func ThreadMatches(state *WorkspaceState, reference string) (int, []*domain.Thread) {
	wanted := strings.ToLower(threadsTrim(reference))
	if wanted == "" {
		return matchRanks, []*domain.Thread{}
	}
	ranked := make([][]*domain.Thread, matchRanks)
	for _, thread := range state.Threads {
		id := strings.ToLower(thread.ID)
		if id == wanted {
			return 0, []*domain.Thread{thread}
		}
		archived := 0
		if thread.ArchivedAt != nil {
			archived = 2
		}
		if strings.ToLower(threadsTrim(thread.Title)) == wanted {
			ranked[1+archived] = append(ranked[1+archived], thread)
		} else if strings.HasPrefix(id, wanted) {
			ranked[2+archived] = append(ranked[2+archived], thread)
		}
	}
	for rank, threads := range ranked {
		if len(threads) > 0 {
			return rank, threads
		}
	}
	return matchRanks, []*domain.Thread{}
}

// The thread a reference names: its id, or the one thread it most closely is the title or an id prefix of.
func FindThread(state *WorkspaceState, reference string) *domain.Thread {
	if _, threads := ThreadMatches(state, reference); len(threads) == 1 {
		return threads[0]
	}
	return nil
}

// Where a message came from, in the words every surface shows, or nil.
func LabelOf(origin domain.MessageOrigin, detail *string) *string {
	if label, ok := domain.MessageLabel(origin, detail); ok && label != "" {
		return &label
	}
	return nil
}

// Text cut to `limit` UTF-16 units with an ellipsis, never between the halves of a surrogate pair, which would not survive JSON.
func ClipText(text string, limit int) string {
	if threadsUTF16Len(text) <= limit {
		return text
	}
	units := utf16.Encode([]rune(text))
	end := limit
	if limit > 0 && units[limit-1] >= 0xD800 && units[limit-1] <= 0xDBFF {
		end = limit - 1
	}
	if end < 0 {
		end = max(0, len(units)+end)
	}
	return string(utf16.Decode(units[:end])) + "…"
}

// Up to `limit` messages ending before index `end` (nil: the newest), each text cut to `textLimit`
// (0 or less: messageTextLimit). Nil when no thread is named by threadID.
func ThreadTranscript(state *WorkspaceState, threadID string, limit, textLimit int, end *int) *contracts.ThreadTranscript {
	thread := FindThread(state, threadID)
	if thread == nil {
		return nil
	}
	if textLimit <= 0 {
		textLimit = messageTextLimit
	}
	stop := len(thread.Messages)
	if end != nil {
		stop = min(*end, stop)
	}
	start := max(0, stop-max(0, limit))
	var kept []*domain.ConversationMessage
	if start < stop {
		kept = thread.Messages[start:stop]
	}
	messages := make([]*contracts.ThreadMessage, len(kept))
	for index, message := range kept {
		projected := &contracts.ThreadMessage{Kind: message.Kind, Text: ClipText(message.Text, textLimit), At: message.At}
		if message.Kind == domain.ConversationMessageKindUser {
			projected.Origin = LabelOf(message.Origin, message.Detail)
		}
		messages[index] = projected
	}
	return &contracts.ThreadTranscript{Thread: *ThreadSummary(state, thread, nil), Messages: messages, Omitted: float64(stop - len(kept))}
}

func inScope(thread *domain.Thread, scope contracts.ProjectScope) bool {
	switch scope := scope.(type) {
	case contracts.ProjectScopeAll:
		return true
	case contracts.ProjectScopeProjectless:
		return thread.ProjectID == nil
	case contracts.ProjectScopeProject:
		return thread.ProjectID != nil && *thread.ProjectID == scope.ProjectID
	}
	return false
}

func carriesAttachment(message *domain.ConversationMessage) bool {
	return len(message.Attachments) > 0
}

func countAttachments(thread *domain.Thread) int {
	if thread.HistorySummary != nil {
		return int(thread.HistorySummary.AttachmentCount)
	}
	count := 0
	for _, message := range thread.Messages {
		if carriesAttachment(message) {
			count++
		}
	}
	return count
}

func matches(thread *domain.Thread, search string, stored jsonx.Set[string]) bool {
	if strings.Contains(strings.ToLower(thread.Title), search) {
		return true
	}
	if thread.HistorySummary != nil {
		return stored.Has(thread.ID)
	}
	return slices.ContainsFunc(thread.Messages, func(message *domain.ConversationMessage) bool {
		return strings.Contains(strings.ToLower(message.Text), search)
	})
}
