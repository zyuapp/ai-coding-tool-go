package application

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func coordMatch(t *testing.T, text, pattern, why string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(text) {
		t.Errorf("%s: %q does not match %s", why, text, pattern)
	}
}

func coordNoMatch(t *testing.T, text, pattern, why string) {
	t.Helper()
	if regexp.MustCompile(pattern).MatchString(text) {
		t.Errorf("%s: %q matches %s", why, text, pattern)
	}
}

func TestRosterListsWhatNeedsAttentionAndCountsTheRest(t *testing.T) {
	coordPorted(t, func() {
		threads := []*domain.Thread{
			coordLead("lead"),
			coordTask("asking", coordUnder("lead"), coordTitled("Asking"), func(thread *domain.Thread) {
				thread.Decisions = []*domain.Decision{{ID: "d1", Question: "Keep it?", Options: []*domain.DecisionOption{}, RaisedAt: 1}}
			}),
			coordTask("busy", coordUnder("lead"), coordTitled("Busy")),
			coordTask("review", coordUnder("lead"), coordTitled("Review"), func(thread *domain.Thread) {
				thread.Brief = new(coordBrief)
				thread.PullRequest = &domain.PullRequestLink{Number: 212, URL: "https://github.com/o/r/pull/212", Title: new("Fix it"), State: new(domain.PullRequestStateOpen), PullRequestNeeds: domain.PullRequestNeeds{Checks: new(domain.PullRequestChecksPassing)}}
			}),
			coordTask("failing", coordUnder("lead"), coordTitled("Failing"), func(thread *domain.Thread) {
				thread.Brief = new(coordBrief)
				thread.PullRequest = &domain.PullRequestLink{Number: 213, URL: "https://github.com/o/r/pull/213", Title: new("Fix that"), State: new(domain.PullRequestStateOpen), PullRequestNeeds: domain.PullRequestNeeds{Checks: new(domain.PullRequestChecksFailing)}}
			}),
		}
		for index := range 13 {
			threads = append(threads, coordTask(fmt.Sprintf("done-%d", index), coordUnder("lead"), coordTitled(fmt.Sprintf("Done %d", index)), func(thread *domain.Thread) {
				thread.Report = &domain.CoordinationReport{State: domain.CoordinationStateDone, Summary: "Shipped", At: 1}
				thread.RunEndedAt = new(float64(100 + index))
			}))
		}
		resolved := func(index int) *domain.Thread {
			return coordTask(fmt.Sprintf("resolved-%d", index), coordUnder("lead"), coordTitled(fmt.Sprintf("Resolved %d", index)), func(thread *domain.Thread) { thread.ResolvedAt = new(1.0) })
		}
		threads = append(threads, resolved(0), resolved(1))
		state := coordWorkspace(coordHolding(threads...), coordRunning(coordRun("busy", "run-b")))
		context := CoordinationContext(state, state.Threads[0], CoordinationContextOptions{})
		coordMatch(t, context, `"Asking" \[asking\] · idle · waiting on the user to decide: Keep it\?`, "asking")
		coordMatch(t, context, `"Busy" \[busy\] · working`, "busy")
		coordMatch(t, context, `"Review" \[review\] · idle · ready for review: #212 · Checks passing`, "review")
		coordMatch(t, context, `"Failing" \[failing\] · idle · its pull request needs work: #213 · Checks failing`, "failing")
		coordMatch(t, context, `"Done 12" \[done-12\]`, "newest done")
		coordMatch(t, context, `"Done 3" \[done-3\]`, "the ten most recently active done threads are listed")
		coordNoMatch(t, context, `"Done 2" \[done-2\]`, "older done")
		coordNoMatch(t, context, `Resolved 0`, "resolved")
		coordMatch(t, context, `3 more finished and 2 resolved threads; read_thread reaches them\.`, "counted")
		one := coordWorkspace(coordHolding(coordLead("lead"), resolved(0)))
		coordMatch(t, CoordinationContext(one, one.Threads[0], CoordinationContextOptions{}), `1 resolved thread; read_thread reaches it\.`, "one")
	})
}

func TestAThreadThatEndsWithoutReportingReadsAsDoneInWhatItLastSaid(t *testing.T) {
	coordPorted(t, func() {
		reply := &domain.ConversationMessage{ID: "reply", Kind: domain.ConversationMessageKindAssistant, Text: "\n- Pushed **commit** `a1b2c3d` to [main](https://github.com/o/r/tree/main).\n\nTests pass.", At: 5}
		member := func(id string, edits ...func(*domain.Thread)) *domain.Thread {
			return coordTask(id, append([]func(*domain.Thread){coordUnder("lead"), coordTitled(strings.ToUpper(id[:1]) + id[1:])}, edits...)...)
		}
		said := func(thread *domain.Thread) { thread.Messages = []*domain.ConversationMessage{reply} }
		ended := func(value domain.ThreadOutcome) func(*domain.Thread) {
			return func(thread *domain.Thread) { thread.Outcome, thread.RunEndedAs = new(value), new(value) }
		}
		threads := []*domain.Thread{
			coordLead("lead", coordTitled("Lead")),
			member("unreported", ended(domain.ThreadOutcomeFinished), said, func(thread *domain.Thread) {
				thread.Brief = &domain.ThreadBrief{Intent: "push the login fix", DoneWhen: "it is on main", Delivers: domain.DeliveryCommit}
			}),
			member("silent", ended(domain.ThreadOutcomeFinished)),
			member("progressed", ended(domain.ThreadOutcomeFinished), said, func(thread *domain.Thread) {
				thread.Report = &domain.CoordinationReport{State: domain.CoordinationStateWorking, Summary: "Halfway", At: 1}
			}),
			member("reported", ended(domain.ThreadOutcomeFinished), said, func(thread *domain.Thread) {
				thread.Report = &domain.CoordinationReport{State: domain.CoordinationStateDone, Summary: "Shipped", At: 1}
			}),
			member("asking", ended(domain.ThreadOutcomeFinished), said, func(thread *domain.Thread) {
				thread.Decisions = []*domain.Decision{{ID: "d1", Question: "Keep it?", Options: []*domain.DecisionOption{}, RaisedAt: 1}}
			}),
			member("stopped", ended(domain.ThreadOutcomeStopped), said),
		}
		state := coordWorkspace(coordHolding(threads...))
		view := CoordinationViewCoordinationView(state, threads[0], coordNone, coordNone, nil, nil).(CoordinationViewTrue)
		var got []string
		for _, member := range view.Members {
			got = append(got, fmt.Sprintf("%s %s %s", member.Thread.ID, member.Status, coordValueOr(member.Summary, "<nil>")))
		}
		want := []string{
			"unreported done Pushed commit a1b2c3d to main.",
			"silent done <nil>",
			"progressed done Pushed commit a1b2c3d to main.",
			"reported done Shipped",
			"asking asking Keep it?",
			"stopped idle <nil>",
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("a finished turn is done whichever engine ran it:\n%s", strings.Join(got, "\n"))
		}
		context := CoordinationContext(state, threads[0], CoordinationContextOptions{})
		coordMatch(t, context, `(?m)"Unreported" \[unreported\] · idle · ended its turn without reporting; it last said: Pushed commit a1b2c3d to main\.$`, "unreported")
		coordMatch(t, context, `(?m)"Silent" \[silent\] · idle · ended its turn without reporting$`, "silent")
		coordMatch(t, context, `(?m)"Progressed" \[progressed\] · idle · ended its turn without reporting; it last said: Pushed commit a1b2c3d to main\.$`, "a report that it was working is no final report")
		coordMatch(t, context, `(?m)"Reported" \[reported\] · idle · reported done: Shipped$`, "a thread that reported says so instead")
		coordNoMatch(t, context, `"Asking" [^\n]*without reporting`, "nor is one waiting on the user")
		coordNoMatch(t, context, `"Stopped" [^\n]*without reporting`, "nor one that was stopped")

		busy := []*domain.Thread{coordLead("lead"), member("busy", ended(domain.ThreadOutcomeFinished), func(thread *domain.Thread) {
			thread.Report = &domain.CoordinationReport{State: domain.CoordinationStateWorking, Summary: "Halfway", At: 1}
		})}
		atWork := CoordinationContext(coordWorkspace(coordHolding(busy...), coordRunning(coordRun("busy", "run-busy"))), busy[0], CoordinationContextOptions{})
		coordMatch(t, atWork, `(?m)"Busy" \[busy\] · working · reported working: Halfway$`, "a thread still at work keeps its report that it is working")
	})
}

func TestTheSeedStaysBoundedHoweverLongTheConversation(t *testing.T) {
	coordPorted(t, func() {
		session := func(thread *domain.Thread) {
			thread.Continuation = &domain.Continuation{Provider: "claude", Value: "session"}
			thread.ContinuationStatus = domain.ContinuationStatusAvailable
		}
		messages := make([]*domain.ConversationMessage, 200)
		for index := range messages {
			kind := domain.ConversationMessageKindUser
			if index%2 == 1 {
				kind = domain.ConversationMessageKindAssistant
			}
			messages[index] = &domain.ConversationMessage{ID: fmt.Sprint(index), Kind: kind, Text: fmt.Sprintf("%d %s", index, strings.Repeat("x", 2_000)), At: float64(index)}
		}
		state := coordWorkspace(coordHolding(coordLead("lead", session, func(thread *domain.Thread) { thread.Messages = messages })))
		seed := CoordinationContext(state, state.Threads[0], CoordinationContextOptions{Fresh: true})
		recent := strings.Split(strings.SplitN(seed, "The last messages of your conversation, oldest first:\n", 2)[1], "\n")
		if len(recent) != 12 {
			t.Errorf("only the last 12 messages are read: %d", len(recent))
		}
		coordMatch(t, recent[len(recent)-1], `^You: 199 x+…$`, "newest last")
		for _, line := range recent {
			if coordUTF16Len(line) > len("The user: ")+800 {
				t.Errorf("each message is clipped: %d", coordUTF16Len(line))
			}
		}
		if coordUTF16Len(strings.Join(recent, "\n")) > 10_000 {
			t.Error("the block stays under its total")
		}
		threadOrigin := domain.MessageOriginThread{ThreadID: new("worker"), Title: strings.Repeat("t", 64)}
		fromThread := make([]*domain.ConversationMessage, len(messages))
		for index, message := range messages {
			copied := *message
			copied.Kind, copied.Origin = domain.ConversationMessageKindUser, threadOrigin
			fromThread[index] = &copied
		}
		titled := coordWorkspace(coordHolding(coordLead("lead", session, func(thread *domain.Thread) { thread.Messages = fromThread })))
		long := strings.Split(strings.SplitN(CoordinationContext(titled, titled.Threads[0], CoordinationContextOptions{Fresh: true}), "oldest first:\n", 2)[1], "\n")
		if len(long) >= 12 || coordUTF16Len(strings.Join(long, "\n")) > 10_000 {
			t.Errorf("the oldest go first once the block runs long: %d lines", len(long))
		}
		coordMatch(t, long[len(long)-1], `^From t+ · worker: 199 `, "newest from the thread")
		cold := coordWorkspace(coordHolding(coordLead("lead", session, func(thread *domain.Thread) {
			thread.HistorySummary = &domain.StoredThreadHistorySummary{MessageCount: 200}
		})))
		unread := CoordinationContext(cold, cold.Threads[0], CoordinationContextOptions{Fresh: true})
		coordMatch(t, unread, `^This coordinator continues in a new session`, "a fresh session")
		coordNoMatch(t, unread, `The last messages`, "a conversation not loaded yet is left to read_thread")
	})
}

func TestClippedCutsToUTF16UnitsWithAnEllipsis(t *testing.T) {
	if got := clipped("abcdef", 4); got != "abc…" {
		t.Errorf("got %q", got)
	}
	if got := clipped("abcd", 4); got != "abcd" {
		t.Errorf("a text that fits is kept: %q", got)
	}
	if got := clipped("a😀bcd", 4); got != "a😀…" {
		t.Errorf("an astral character counts two units: %q", got)
	}
}

func TestCoordinationUpdateTellsTheCoordinatorHowMuchToSay(t *testing.T) {
	notes := []*domain.CoordinationNote{{Text: "\"A\" ended its turn."}, {Text: "\"B\" failed."}}
	text, prompt := CoordinationUpdate(notes, 2, nil)
	if text != "\"A\" ended its turn.\n\"B\" failed." {
		t.Errorf("text: %q", text)
	}
	coordMatch(t, prompt, `2 other threads are still working, so tell the user only what cannot wait for them, in a line or two\. Nothing is waiting on the user\. Say nothing about progress\.`, "two working")
	waiting := "A decision is waiting on the user"
	_, done := CoordinationUpdate(notes, 0, &waiting)
	coordMatch(t, done, `None of your threads is working now, so give the user one answer[^.]*\. A decision is waiting on the user\. Say nothing`, "none working")
}
