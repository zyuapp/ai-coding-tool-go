package application

import (
	"encoding/json/jsontext"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var noticesFromLead = domain.MessageOriginThread{ThreadID: new("lead"), Title: "Release"}

func noticesRun(taskID string, edit func(*ActiveRunRunning)) ActiveRun {
	run := coreActive(taskID, "run-1").(ActiveRunRunning)
	if edit != nil {
		edit(&run)
	}
	return run
}

// noticesWorkspace is one thread with a run in it, and the user somewhere else.
func noticesWorkspace(edits ...func(*WorkspaceState)) *WorkspaceState {
	return coreWorkspace(append([]func(*WorkspaceState){func(s *WorkspaceState) {
		s.Threads = []*domain.Thread{coreTask("task-a")}
		s.ActiveRuns = map[string]ActiveRun{"task-a": noticesRun("task-a", nil)}
		s.CurrentID = nil
		s.Focused = false
		s.Restored = true
	}}, edits...)...)
}

func noticesCoordinated(lead, member func(*domain.Thread), edits ...func(*WorkspaceState)) *WorkspaceState {
	return noticesWorkspace(append([]func(*WorkspaceState){func(s *WorkspaceState) {
		s.Threads = []*domain.Thread{
			coreTask("lead", thRole(domain.ThreadRoleCoordinator), func(t *domain.Thread) { t.Title = "Release" }, lead),
			coreTask("task-a", thUnder("lead"), func(t *domain.Thread) {
				t.Title = "Fix login"
				t.Messages = []*domain.ConversationMessage{domain.CreateSentMessage(domain.SentMessage{Text: "Fix the login test", Origin: noticesFromLead})}
			}, member),
		}
		s.ActiveRuns = map[string]ActiveRun{"lead": noticesRun("lead", nil), "task-a": noticesRun("task-a", nil)}
	}}, edits...)...)
}

func noticesFire(state *WorkspaceState, event contracts.RunEvent) WorkspaceTransition {
	return Reduce(state, WorkspaceEventRunEvent{Event: event})
}

func noticesStatus(taskID string, status domain.RunStatus, message *string) contracts.RunEvent {
	return contracts.RunEventRunStatus{RunEventBase: contracts.RunEventBase{TaskID: taskID, RunID: "run-1", Sequence: 1}, Status: status, Message: message}
}

func noticesApproval() contracts.RunEvent {
	return contracts.RunEventApprovalRequested{
		RunEventBase: contracts.RunEventBase{TaskID: "task-a", RunID: "run-1", Sequence: 1},
		ApprovalID:   "approval-1",
		Intent:       domain.ToolIntent{ToolID: "tool-1", Name: "Bash", Input: jsontext.Value(`{"command":"ls"}`)},
		Title:        "Bash needs approval",
		Description:  "Review this action before it runs.",
	}
}

func noticesSaid(effects []WorkspaceEffect) (headlines, titles []string) {
	headlines, titles = []string{}, []string{}
	for _, effect := range effects {
		if announced, ok := effect.(WorkspaceEffectAnnounceThread); ok {
			headlines = append(headlines, announced.Notice.Headline)
			titles = append(titles, announced.Notice.Title)
		}
	}
	return headlines, titles
}

func TestReplyLineReadsPlainWords(t *testing.T) {
	for reply, want := range map[string]string{
		"## Two of three threads are done\n\nFix login opened #212.":                          "Two of three threads are done",
		"**Status:** two of three threads are _done_, see [PR #212](https://example.com/212)": "Status: two of three threads are done, see PR #212",
		"> - Fix `parse_args` in my_module":                                                   "Fix parse_args in my_module",
		"```ts\nconst ready = true;":                                                          "const ready = true;",
		"---\n\n1. Merged the login fix":                                                      "Merged the login fix",
		"- [x] ~~old~~ new":                                                                   "old new",
	} {
		if got, ok := ReplyLine(reply, posted); !ok || got != want {
			t.Errorf("ReplyLine(%q) = %q", reply, got)
		}
	}
	long := strings.TrimSpace(strings.Repeat("Everything is merged ", 10))
	clipped, _ := ReplyLine(long, posted)
	if threadsUTF16Len(clipped) != 120 || !strings.HasSuffix(clipped, "…") || !strings.HasPrefix(long, strings.TrimSuffix(clipped, "…")) {
		t.Fatalf("clipped: %q", clipped)
	}
	if _, ok := ReplyLine("```\n---\n", posted); ok {
		t.Fatal("a reply of only fences said something")
	}
}

func TestCoordinatorReplyIsAnnounced(t *testing.T) {
	corePorted(t, func() {
		replied := func(messages []*domain.ConversationMessage, messagesBefore float64) WorkspaceTransition {
			state := noticesCoordinated(func(t *domain.Thread) { t.Messages = messages }, func(*domain.Thread) {}, func(s *WorkspaceState) {
				s.ActiveRuns = map[string]ActiveRun{"lead": noticesRun("lead", func(run *ActiveRunRunning) { run.MessagesBefore = messagesBefore })}
			})
			return noticesFire(state, noticesStatus("lead", domain.RunStatusSucceeded, nil))
		}
		assistant := func(text string) *domain.ConversationMessage {
			return domain.CreateConversationMessage(domain.ConversationMessageKindAssistant, text, nil, nil, nil, nil, nil)
		}
		update := replied([]*domain.ConversationMessage{domain.CreateSentMessage(domain.SentMessage{Text: "How are the threads doing?"}), assistant("## Two of three threads are done\n\nFix login opened #212.")}, 0)
		headlines, titles := noticesSaid(update.Effects)
		threadsSame(t, headlines, []string{"Two of three threads are done"}, "headline")
		threadsSame(t, titles, []string{"Release"}, "title")
		silent := replied([]*domain.ConversationMessage{assistant("An earlier answer"), domain.CreateSentMessage(domain.SentMessage{Text: "Carry on"})}, 1)
		quiet, _ := noticesSaid(silent.Effects)
		threadsSame(t, quiet, []string{"The run finished."}, "no reply in the turn")
	})
}

func TestMemberTurnsAnnouncedOnlyWhenTheUsersOwn(t *testing.T) {
	corePorted(t, func() {
		started := noticesCoordinated(func(*domain.Thread) {}, func(*domain.Thread) {})
		said, _ := noticesSaid(noticesFire(started, noticesStatus("task-a", domain.RunStatusSucceeded, nil)).Effects)
		threadsSame(t, said, []string{}, "the coordinator's own turn")
		asked := noticesCoordinated(func(*domain.Thread) {}, func(t *domain.Thread) {
			t.Messages = []*domain.ConversationMessage{
				domain.CreateSentMessage(domain.SentMessage{Text: "Fix it", Origin: noticesFromLead}),
				domain.CreateSentMessage(domain.SentMessage{Text: "Why is it still red?"}),
			}
		})
		headlines, titles := noticesSaid(noticesFire(asked, noticesStatus("task-a", domain.RunStatusSucceeded, nil)).Effects)
		threadsSame(t, headlines, []string{"The run finished."}, "the user's own turn")
		threadsSame(t, titles, []string{"Release · Fix login"}, "named under its coordinator")
		failed, _ := noticesSaid(noticesFire(started, noticesStatus("task-a", domain.RunStatusFailed, new("The tool crashed"))).Effects)
		threadsSame(t, failed, []string{"The tool crashed"}, "failure")
		waiting, _ := noticesSaid(noticesFire(started, noticesApproval()).Effects)
		threadsSame(t, waiting, []string{"Waiting for your permission to use Bash"}, "approval")
	})
}

func TestMutedCoordinatorAnnouncesNothing(t *testing.T) {
	corePorted(t, func() {
		muted := noticesCoordinated(func(t *domain.Thread) { t.Muted = new(true) }, func(t *domain.Thread) {
			t.Messages = []*domain.ConversationMessage{domain.CreateSentMessage(domain.SentMessage{Text: "Why is it red?"})}
		})
		for _, event := range []contracts.RunEvent{noticesStatus("task-a", domain.RunStatusSucceeded, nil), noticesStatus("task-a", domain.RunStatusFailed, new("The tool crashed")), noticesApproval()} {
			if said, _ := noticesSaid(noticesFire(muted, event).Effects); len(said) != 0 {
				t.Fatalf("muted said %v", said)
			}
		}
		alone := *muted
		alone.Threads = []*domain.Thread{muted.Threads[0], coreTask("task-a", func(t *domain.Thread) { t.Title = "Fix login" })}
		_, titles := noticesSaid(noticesFire(&alone, noticesStatus("task-a", domain.RunStatusSucceeded, nil)).Effects)
		threadsSame(t, titles, []string{"Fix login"}, "a thread out of a muted coordinator")
	})
}

func TestRemoteLedThreadLeavesNoticesToItsComputer(t *testing.T) {
	corePorted(t, func() {
		lead := domain.HeldLead{RemoteLead: domain.RemoteLead{RemoteSender: domain.RemoteSender{ThreadID: "lead-there", Title: "Release", Computer: "Laptop"}}}
		there := noticesWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{coreTask("task-a", func(t *domain.Thread) {
				t.Title = "Fix login"
				held := lead
				t.RemoteLead = &held
			})}
		})
		if said, _ := noticesSaid(noticesFire(there, noticesStatus("task-a", domain.RunStatusFailed, new("The tool crashed"))).Effects); len(said) != 0 {
			t.Fatalf("said here: %v", said)
		}
		away := *there
		awayLead := lead
		awayLead.Away = new(true)
		thread := *there.Threads[0]
		thread.RemoteLead = &awayLead
		away.Threads = []*domain.Thread{&thread}
		_, titles := noticesSaid(noticesFire(&away, noticesApproval()).Effects)
		threadsSame(t, titles, []string{"Fix login"}, "standing on its own")
		silent := *there
		silent.Notifications = false
		if got := Announced(&silent, &thread, "x", NoticeReasonFailed); len(got) != 0 {
			t.Fatal("notifications off still announced")
		}
	})
}

func TestNoticeTitleNamesASideChatsSource(t *testing.T) {
	state := noticesWorkspace(func(s *WorkspaceState) {
		s.Threads = []*domain.Thread{coreTask("main", func(t *domain.Thread) { t.Title = "Main" }), coreTask("chat", func(t *domain.Thread) { t.Title = "Chat 1" })}
		s.SideChats = []*SideChat{{ID: "chat", SourceThreadID: "main"}}
		s.Notifications = true
	})
	effects := Announced(state, state.Threads[1], "Done", NoticeReasonFinished)
	_, titles := noticesSaid(effects)
	threadsSame(t, titles, []string{"Main · Chat 1"}, "side chat title")
	if got := WaitingHeadline(MemberFactWaitingOn{Question: new("Ship it?")}); got != "Ship it?" {
		t.Fatalf("question headline %q", got)
	}
}
