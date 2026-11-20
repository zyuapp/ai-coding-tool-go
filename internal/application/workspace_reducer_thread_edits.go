package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The threads as one input leaves them. Only the functions here make one. TS types it by the thread
// fields a slice may patch (ThreadsEdit<Facet>); Go has one type for every facet.
type ThreadsEdit struct {
	View ThreadsState
}

type threadsChange = ThreadsEdit

func (e ThreadsEdit) EditView() any { return e.View }

// Changes to a copy of one thread, made in place on that copy. Clearing a field is setting it to its
// zero value (TS: a field given as undefined). TS limits the fields to the slice's facet and
// `updatedAt`; Go does not check that.
type ThreadPatch func(next *domain.Thread)

// What WithdrawnIn reads of the run it withdraws (TS: Pick<ActiveRun, "messagesBefore" | "before" | "reportedIssues">).
type WithdrawnRun struct {
	MessagesBefore float64
	Before         ThreadMark
	ReportedIssues []string
}

func ThreadsOf(state *WorkspaceState) ThreadsEdit {
	return ThreadsEdit{View: ThreadsState{Threads: state.Threads}}
}

// One thread with what the patch says. The list and that thread are new even when the patch changes nothing.
func EditThread(edit ThreadsEdit, id string, patch func(thread *domain.Thread) ThreadPatch) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread { return patched(thread, patch(thread)) })
}

// Every thread the patch answers for, each one it answers nil for left as it was. A list none changed is kept.
func EditThreads(edit ThreadsEdit, patch func(thread *domain.Thread) ThreadPatch) ThreadsEdit {
	var threads []*domain.Thread
	for index, thread := range edit.View.Threads {
		fields := patch(thread)
		if fields == nil {
			continue
		}
		if threads == nil {
			threads = slices.Clone(edit.View.Threads)
		}
		threads[index] = patched(thread, fields)
	}
	if threads == nil {
		return edit
	}
	return threadsEditOf(threads)
}

func threadsEditOf(threads []*domain.Thread) ThreadsEdit {
	return ThreadsEdit{View: ThreadsState{Threads: threads}}
}

// A new thread, at the end of the list.
func AddedIn(edit ThreadsEdit, thread *domain.Thread) ThreadsEdit {
	return threadsEditOf(append(slices.Clip(edit.View.Threads), thread))
}

// Threads filed away: their verdicts, and what their runs found, set down as domain.Dismissed sets them.
func DismissedIn(edit ThreadsEdit, ids jsonx.Set[string]) ThreadsEdit {
	threads := domain.Dismissed(edit.View.Threads, ids)
	if jsonx.SameSlice(threads, edit.View.Threads) {
		return edit
	}
	return threadsEditOf(threads)
}

// Threads under a coordinator whose work settled at `at`, filed away under Resolved.
func ResolvedIn(edit ThreadsEdit, ids jsonx.Set[string], at float64) ThreadsEdit {
	threads := make([]*domain.Thread, len(edit.View.Threads))
	for index, thread := range edit.View.Threads {
		if ids.Has(thread.ID) {
			next := *thread
			resolvedAt := at
			next.ResolvedAt = &resolvedAt
			thread = &next
		}
		threads[index] = thread
	}
	return DismissedIn(threadsEditOf(threads), ids)
}

// A thread written to while filed away, back among the others.
func RestoredIn(edit ThreadsEdit, id string) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		next := *thread
		next.ArchivedAt = nil
		return &next
	})
}

// A snoozed thread written to, which ends its snooze.
func UnsnoozedIn(edit ThreadsEdit, id string) ThreadsEdit {
	return mapped(edit, id, domain.WithoutSnooze)
}

// A thread waiting out a usage limit written to at `at`, which moves it to the front of the line.
func NudgedIn(edit ThreadsEdit, id string, at float64) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		if thread.LimitPause == nil {
			return thread
		}
		next := *thread
		pause := *thread.LimitPause
		nudgedAt := at
		pause.NudgedAt = &nudgedAt
		next.LimitPause = &pause
		return &next
	})
}

// A thread's resource interruption, retained until an explicit resume. A nil stop takes it off.
func ResourceStopIn(edit ThreadsEdit, id string, stop *domain.ResourceStop) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		next := *thread
		next.ResourceStop = stop
		return &next
	})
}

// A thread out of its usage limit's line.
func UnpausedIn(edit ThreadsEdit, id string) ThreadsEdit {
	return mapped(edit, id, domain.WithoutLimitPause)
}

// A thread whose turn a run of `provenance` opens, as OpenedTurn has it, unless it is open already. Only Origin, Quiet and Operation are read.
func TurnOpenedIn(edit ThreadsEdit, id string, provenance RunProvenance) ThreadsEdit {
	read := RunProvenance{Origin: provenance.Origin, Quiet: provenance.Quiet, Operation: provenance.Operation}
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		if thread.RunOpen != nil && *thread.RunOpen {
			return thread
		}
		return OpenedTurn(thread, read)
	})
}

// A decision whose answer never reached its thread, open to answer again.
func AnswerReopenedIn(edit ThreadsEdit, id, decisionID string) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread { return domain.WithoutAnswer(thread, decisionID) })
}

// Notes left for a coordinator to hear with its next wake.
func NotedIn(edit ThreadsEdit, leadID string, notes ...*domain.CoordinationNote) ThreadsEdit {
	return mapped(edit, leadID, func(lead *domain.Thread) *domain.Thread {
		for _, note := range notes {
			lead = domain.WithCoordinationNote(lead, note)
		}
		return lead
	})
}

// What a coordinator holds of its news kept for a later wake, so none of it wakes it now.
func NewsQuietedIn(edit ThreadsEdit, leadID string) ThreadsEdit {
	quiet := func(note *domain.CoordinationNote) bool { return note.Quiet != nil && *note.Quiet }
	lead := threadsFind(edit.View.Threads, leadID)
	if lead == nil || !slices.ContainsFunc(lead.CoordinationNotes, func(note *domain.CoordinationNote) bool { return !quiet(note) }) {
		return edit
	}
	return mapped(edit, leadID, func(thread *domain.Thread) *domain.Thread {
		next := *thread
		notes := make([]*domain.CoordinationNote, len(thread.CoordinationNotes))
		for index, note := range thread.CoordinationNotes {
			if !quiet(note) {
				quieted := *note
				quieted.Quiet = new(true)
				note = &quieted
			}
			notes[index] = note
		}
		next.CoordinationNotes = notes
		return &next
	})
}

// A thread whose turn ended with `status`, as EndedTurn has it.
func TurnEndedIn(edit ThreadsEdit, id string, status domain.RunStatus) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread { return EndedTurn(thread, status) })
}

// The verdict a settled turn leaves its thread, unread unless the user was looking at it. A thread filed away is left none.
func VerdictIn(edit ThreadsEdit, id string, outcome domain.ThreadOutcome, unread bool) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		if thread.ArchivedAt != nil {
			return thread
		}
		next := *thread
		verdict := outcome
		next.Outcome = &verdict
		if unread {
			next.OutcomeUnread = new(true)
		}
		return &next
	})
}

// A tick's run withdrawn as if it never ran, or the filed-away issues it no longer reports closed, or both.
func WithdrawnIn(edit ThreadsEdit, id string, run WithdrawnRun, withdraws, closesIssues bool) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		withdrawn := thread
		if withdraws {
			withdrawn = WithdrawRun(thread, run.MessagesBefore, run.Before)
		}
		if closesIssues {
			return domain.WithClosedIssues(withdrawn, run.ReportedIssues)
		}
		return withdrawn
	})
}

// How the last turn ended, its verdict and its report put back, as RestoredTestimony has it.
func TestimonyRestoredIn(edit ThreadsEdit, id string, before ThreadMark) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread { return RestoredTestimony(thread, before) })
}

// A usage limit one thread's run reached, which every waiting thread of its engine waits for too.
func EngineLimitIn(edit ThreadsEdit, id string, limit domain.UsageLimit) ThreadsEdit {
	thread := threadsFind(edit.View.Threads, id)
	if thread == nil {
		return edit
	}
	threads := domain.WithEngineLimit(edit.View.Threads, thread.Engine, limit)
	if jsonx.SameSlice(threads, edit.View.Threads) {
		return edit
	}
	return threadsEditOf(threads)
}

// A thread in its usage limit's line. One that carries on by itself has its verdict and its end taken off.
func LimitPausedIn(edit ThreadsEdit, id string, pause domain.LimitPause, carriesOn bool) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		next := *thread
		paused := pause
		next.LimitPause = &paused
		if carriesOn {
			next.Outcome, next.OutcomeUnread, next.RunEndedAs = nil, nil, nil
		}
		return &next
	})
}

// What a run found, said in the thread and kept with its findings, unless the thread already carries it.
func FindingIn(edit ThreadsEdit, id string, report domain.FindingReport, at float64, seen bool) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread { return domain.WithFinding(thread, report, at, seen) })
}

// Threads whose work finished, keyed by id, each ranked and marked as its FinishedVerdict has it.
func FinishedIn(edit ThreadsEdit, verdicts map[string]*FinishedVerdict) ThreadsEdit {
	if len(verdicts) == 0 {
		return edit
	}
	threads := make([]*domain.Thread, len(edit.View.Threads))
	for index, thread := range edit.View.Threads {
		if verdict := verdicts[thread.ID]; verdict != nil {
			next := *thread
			finished := domain.ThreadOutcomeFinished
			next.Outcome, next.RunEndedAs = &finished, &finished
			if verdict.RankedAt != nil {
				next.RankedAt = *verdict.RankedAt
			}
			if verdict.RunEndedAt != nil {
				next.RunEndedAt = verdict.RunEndedAt
			}
			if verdict.OutcomeUnread != nil {
				next.OutcomeUnread = verdict.OutcomeUnread
			}
			thread = &next
		}
		threads[index] = thread
	}
	return threadsEditOf(threads)
}

// A resolved thread its failed turn left stuck, open again.
func ReopenedIn(edit ThreadsEdit, id string) ThreadsEdit {
	return mapped(edit, id, domain.ReopenedIfStuck)
}

// A thread the user has seen: its verdict and what its runs found are read. A list with nothing unread is kept.
func ReadIn(edit ThreadsEdit, id string) ThreadsEdit {
	read := domain.ReadAttention(edit.View.Threads, id)
	if jsonx.SameSlice(read, edit.View.Threads) {
		return edit
	}
	return threadsEditOf(read)
}

// ReadIn for a row, which a coordinator speaks for with every thread under it, as ReadRow has it.
func RowReadIn(edit ThreadsEdit, id *string) ThreadsEdit {
	read := ReadRow(edit.View.Threads, id)
	if jsonx.SameSlice(read, edit.View.Threads) {
		return edit
	}
	return threadsEditOf(read)
}

// Threads gone for good, out of the list.
func RemovedIn(edit ThreadsEdit, ids jsonx.Set[string]) ThreadsEdit {
	threads := make([]*domain.Thread, 0, len(edit.View.Threads))
	for _, thread := range edit.View.Threads {
		if !ids.Has(thread.ID) {
			threads = append(threads, thread)
		}
	}
	return threadsEditOf(threads)
}

// A thread's conversation, loaded from the store where only its summary was held.
func HistoryLoadedIn(edit ThreadsEdit, id string, messages []*domain.ConversationMessage) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		next := *thread
		next.HistorySummary = nil
		next.Messages = messages
		return &next
	})
}

// A project's threads once the project is gone: none of them is in it, and each is filed away at `at` unless it was already.
func ProjectRemovedIn(edit ThreadsEdit, projectID string, at func() float64) ThreadsEdit {
	threads := make([]*domain.Thread, len(edit.View.Threads))
	for index, thread := range edit.View.Threads {
		if thread.ProjectID != nil && *thread.ProjectID == projectID {
			next := *thread
			next.ProjectID = nil
			if thread.ArchivedAt == nil {
				archivedAt := at()
				next.ArchivedAt = &archivedAt
			}
			thread = &next
		}
		threads[index] = thread
	}
	return threadsEditOf(threads)
}

// Every thread's verdict, and what its runs found, read. A list with nothing unread is kept.
func AllReadIn(edit ThreadsEdit) ThreadsEdit {
	threads := domain.WithAllRead(edit.View.Threads)
	if jsonx.SameSlice(threads, edit.View.Threads) {
		return edit
	}
	return threadsEditOf(threads)
}

// A thread whose verdict reached the user through the thread that waited on it.
func VerdictCollectedIn(edit ThreadsEdit, id string) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		next := *thread
		next.OutcomeUnread = nil
		return &next
	})
}

// A thread playing `role`, or none, at `at`. A coordinator starts its threads on what it runs on itself;
// a thread that stops being one forgets that choice, the threads it suggested and its mute.
func RoleSetIn(edit ThreadsEdit, id string, role *domain.ThreadRole, catalog domain.ModelCatalog, at func() float64) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		item := *thread
		item.Role, item.ThreadModel, item.Proposals, item.Muted = nil, nil, nil, nil
		if role != nil && *role == domain.ThreadRoleCoordinator {
			next := *domain.AsCoordinator(&catalog, &item)
			next.UpdatedAt = at()
			return &next
		}
		if role != nil && *role != "" {
			played := *role
			item.Role = &played
		}
		item.UpdatedAt = at()
		return &item
	})
}

// A copy of `source` with id `id`, straight under it in the list, as ForkedThreads makes it.
func ForkedIn(edit ThreadsEdit, source *domain.Thread, id string, at float64, title *string) ThreadsEdit {
	threads, _ := ForkedThreads(edit.View.Threads, source, id, at, title)
	return threadsEditOf(threads)
}

// A thread dropped where the sidebar's drag put it. A drop that changes nothing keeps the list.
func MovedInListIn(edit ThreadsEdit, id string, target domain.ThreadDropTarget) ThreadsEdit {
	threads := MoveThread(edit.View.Threads, id, target)
	if jsonx.SameSlice(threads, edit.View.Threads) {
		return edit
	}
	return threadsEditOf(threads)
}

// A message said in a thread at `at`.
func SaidIn(edit ThreadsEdit, id string, message *domain.ConversationMessage, at float64) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		next := *thread
		next.Messages = append(slices.Clip(thread.Messages), message)
		next.UpdatedAt = at
		return &next
	})
}

// A thread moved to another checkout, or to the project's own with none: it says so, its changes are
// counted afresh, and a session it had is forked on its next run.
func RelocatedIn(edit ThreadsEdit, id string, worktreeID *string, note *domain.ConversationMessage, at func() float64) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		moved := *thread
		moved.WorktreeID, moved.WorktreeEnteredAt = nil, nil
		moved.Messages = append(slices.Clip(thread.Messages), note)
		moved.LastChangeSnapshot = domain.ChangeSnapshot{Files: []string{}, CapturedAt: at()}
		moved.UpdatedAt = at()
		if worktreeID != nil && *worktreeID != "" {
			moved.WorktreeID = worktreeID
		}
		if thread.Continuation != nil {
			moved.InheritedContinuation = new(true)
		}
		return &moved
	})
}

// A thread in the checkout made for it, saying so.
func EnteredCheckoutIn(edit ThreadsEdit, id, worktreeID string, note *domain.ConversationMessage, at float64) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		next := *thread
		next.WorktreeID = &worktreeID
		next.Messages = append(slices.Clip(thread.Messages), note)
		next.UpdatedAt = at
		return &next
	})
}

// A thread walked out of its checkout, local again and saying so.
func LeftCheckoutIn(edit ThreadsEdit, id string, note *domain.ConversationMessage, at float64) ThreadsEdit {
	return mapped(edit, id, func(thread *domain.Thread) *domain.Thread {
		next := *thread
		next.WorktreeID, next.WorktreeEnteredAt = nil, nil
		next.Messages = append(slices.Clip(thread.Messages), note)
		next.UpdatedAt = at
		return &next
	})
}

// A checkout that is gone: every thread in it is local again, hears `note`, and forks a session it had on its next run.
func CheckoutGoneIn(edit ThreadsEdit, worktreeID string, note func() *domain.ConversationMessage, at func() float64) ThreadsEdit {
	threads := make([]*domain.Thread, len(edit.View.Threads))
	for index, thread := range edit.View.Threads {
		if thread.WorktreeID != nil && *thread.WorktreeID == worktreeID {
			local := *thread
			local.WorktreeID, local.WorktreeEnteredAt = nil, nil
			if thread.Continuation != nil {
				local.InheritedContinuation = new(true)
			}
			local.Messages = append(slices.Clip(thread.Messages), note())
			local.UpdatedAt = at()
			thread = &local
		}
		threads[index] = thread
	}
	return threadsEditOf(threads)
}

// The list with `update` made to one thread. The list is new even when no thread has that id.
func mapped(edit ThreadsEdit, id string, update func(thread *domain.Thread) *domain.Thread) ThreadsEdit {
	threads := make([]*domain.Thread, len(edit.View.Threads))
	for index, thread := range edit.View.Threads {
		if thread.ID == id {
			thread = update(thread)
		}
		threads[index] = thread
	}
	return threadsEditOf(threads)
}

func patched(thread *domain.Thread, patch ThreadPatch) *domain.Thread {
	next := *thread
	patch(&next)
	return &next
}
