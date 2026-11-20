package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const (
	threadNow = 1_800_000_000_000.0
	hourMs    = 60 * 60 * 1000.0
)

func summary(edit func(*contracts.ThreadSummary)) *contracts.ThreadSummary {
	thread := &contracts.ThreadSummary{
		ID: "task-1", Title: "Rework the sidebar", ProjectID: ptr("project-app"), ProjectRoot: ptr("/code/app"),
		Status: contracts.ThreadSummaryStatusIdle, CreatedAt: threadNow - 5*hourMs, LastActivityAt: threadNow - 4*hourMs, MessageCount: 12,
	}
	if edit != nil {
		edit(thread)
	}
	return thread
}

type threadCall struct {
	op       string
	value    any
	computer *string
}

type fakeThreads struct {
	calls   []threadCall
	list    func(contracts.ThreadListQuery) ([]*contracts.ThreadSummary, error)
	read    func(string, *float64) (contracts.ThreadTranscript, error)
	wait    func(string, float64) (contracts.ThreadWaitResult, error)
	command func(contracts.ExternalCommand, *string) (contracts.ThreadCommandResult, error)
}

func (f *fakeThreads) List(_ context.Context, query contracts.ThreadListQuery) ([]*contracts.ThreadSummary, error) {
	f.calls = append(f.calls, threadCall{op: "list", value: query})
	if f.list != nil {
		return f.list(query)
	}
	return []*contracts.ThreadSummary{summary(nil)}, nil
}

func (f *fakeThreads) Read(_ context.Context, threadID string, limit *float64, computer *string) (contracts.ThreadTranscript, error) {
	f.calls = append(f.calls, threadCall{op: "read", value: []any{threadID, limit}, computer: computer})
	if f.read != nil {
		return f.read(threadID, limit)
	}
	return contracts.ThreadTranscript{
		Thread:   *summary(func(s *contracts.ThreadSummary) { s.ID = threadID }),
		Messages: []*contracts.ThreadMessage{{Kind: domain.ConversationMessageKindUser, Text: "how do we do it", At: threadNow}},
		Omitted:  4,
	}, nil
}

func (f *fakeThreads) Wait(_ context.Context, threadID string, timeoutMs float64) (contracts.ThreadWaitResult, error) {
	f.calls = append(f.calls, threadCall{op: "wait", value: []any{threadID, timeoutMs}})
	if f.wait != nil {
		return f.wait(threadID, timeoutMs)
	}
	return contracts.ThreadWaitResult{Thread: *summary(func(s *contracts.ThreadSummary) { s.ID = threadID }), Reply: ptr("on branch main")}, nil
}

func (f *fakeThreads) Command(_ context.Context, command contracts.ExternalCommand, computer *string) (contracts.ThreadCommandResult, error) {
	f.calls = append(f.calls, threadCall{op: "command", value: command, computer: computer})
	if f.command != nil {
		return f.command(command, computer)
	}
	taskID := "task-new"
	switch command := command.(type) {
	case contracts.TaskCommandTaskSend:
		if command.TaskID != nil {
			taskID = *command.TaskID
		}
	case contracts.TaskCommandTaskArchive:
		taskID = command.TaskID
	case contracts.TaskCommandTaskSetRole:
		taskID = *command.TaskID
	case contracts.TaskCommandTaskMoveWorktree:
		if command.TaskID != nil {
			taskID = *command.TaskID
		}
	case contracts.RunControlCommandRunCancel:
		taskID = *command.TaskID
	}
	return contracts.ThreadCommandResult{Thread: summary(func(s *contracts.ThreadSummary) { s.ID = taskID })}, nil
}

func (f *fakeThreads) last() threadCall { return f.calls[len(f.calls)-1] }

func runThread(t *testing.T, bridge ThreadBridge, name, args string) ToolResult {
	t.Helper()
	return runDefinition(t, ThreadTools, ThreadToolContext{Bridge: bridge, Now: func() float64 { return threadNow }}, name, args)
}

func TestListingThreadsTurnsMinutesIntoAQueryAndReportsEachThreadOnOneLine(t *testing.T) {
	bridge := &fakeThreads{}
	listed := runThread(t, bridge, "list_threads", `{"project":"all","idleMinutes":180}`)
	assertJSON(t, bridge.last().value, `{"project":"all","idleForMs":10800000,"limit":20}`)
	assertMatch(t, textOf(listed), `Rework the sidebar \[task-1\] · /code/app · idle · 12 messages · idle 4h`)

	runThread(t, bridge, "list_threads", `{"archived":true,"search":"sidebar","limit":5}`)
	assertJSON(t, bridge.last().value, `{"archived":true,"search":"sidebar","limit":5}`)

	empty := runThread(t, &fakeThreads{list: func(contracts.ThreadListQuery) ([]*contracts.ThreadSummary, error) { return nil, nil }}, "list_threads", `{}`)
	assertMatch(t, textOf(empty), `No thread matches`)
}

func TestThreadsWithImagesAreAskedForByNameAndSayHowManyMessagesCarryOne(t *testing.T) {
	bridge := &fakeThreads{list: func(contracts.ThreadListQuery) ([]*contracts.ThreadSummary, error) {
		return []*contracts.ThreadSummary{summary(func(s *contracts.ThreadSummary) { s.AttachmentCount = 3 })}, nil
	}}
	listed := runThread(t, bridge, "list_threads", `{"hasImages":true}`)
	assertJSON(t, bridge.last().value, `{"attachments":true,"limit":20}`)
	assertMatch(t, textOf(listed), `12 messages · 3 with images · idle 4h`)

	plain := runThread(t, &fakeThreads{}, "list_threads", `{}`)
	assertNoMatch(t, textOf(plain), `with images`)
}

func TestReadingAThreadSaysWhatItLeftOut(t *testing.T) {
	bridge := &fakeThreads{}
	read := runThread(t, bridge, "read_thread", `{"threadId":"task-7","limit":2}`)
	assertJSON(t, bridge.last().value, `["task-7",2]`)
	assertMatch(t, textOf(read), `\(4 earlier messages not shown\)`)
	assertMatch(t, textOf(read), `\[user\] how do we do it`)
}

func TestThreadToolsCarryComputerSelectorsAndLabelRemoteResults(t *testing.T) {
	remote := summary(func(s *contracts.ThreadSummary) {
		s.Computer = &contracts.ThreadSummaryComputer{ID: "linux", Name: "Linux", Offline: true}
	})
	bridge := &fakeThreads{
		list: func(contracts.ThreadListQuery) ([]*contracts.ThreadSummary, error) {
			return []*contracts.ThreadSummary{remote}, nil
		},
		read: func(string, *float64) (contracts.ThreadTranscript, error) {
			return contracts.ThreadTranscript{Thread: *remote, Messages: []*contracts.ThreadMessage{{Kind: "user", Text: "Stop the stack", Origin: ptr("From Monday · lead"), At: 1}}}, nil
		},
	}
	listing := runThread(t, bridge, "list_threads", `{"computer":"all","limit":2}`)
	assertJSON(t, bridge.last().value, `{"computer":"all","limit":2}`)
	assertMatch(t, textOf(listing), `computer Linux \[linux\] \(offline, cached\)`)
	reading := runThread(t, bridge, "read_thread", `{"threadId":"task-1","computer":"linux","limit":1}`)
	if call := bridge.last(); call.computer == nil || *call.computer != "linux" {
		t.Fatalf("computer not carried: %+v", call)
	}
	assertMatch(t, textOf(reading), `computer Linux`)
	assertMatch(t, textOf(reading), `\[user · From Monday · lead\] Stop the stack`)
}

func TestStartingMessagingArchivingAndStoppingGoThroughTheCommandSurface(t *testing.T) {
	bridge := &fakeThreads{}
	started := runThread(t, bridge, "start_thread", `{"prompt":"Implement item 1","project":"app"}`)
	assertJSON(t, bridge.last().value, `{"type":"task.send","text":"Implement item 1","project":"app"}`)
	assertMatch(t, textOf(started), `^Started Rework the sidebar \[task-new\]`)

	runThread(t, bridge, "start_thread", `{"prompt":"Take the other half","worktree":true,"worktreeId":"wt1","model":"sonnet","effort":"max"}`)
	assertJSON(t, bridge.last().value, `{"type":"task.send","text":"Take the other half","worktreeId":"wt1","model":"sonnet","effort":"max"}`)

	runThread(t, bridge, "message_thread", `{"threadId":"task-2","text":"also update the README"}`)
	assertJSON(t, bridge.last().value, `{"type":"task.send","taskId":"task-2","text":"also update the README","steer":true}`)

	runThread(t, bridge, "archive_thread", `{"threadId":"task-3"}`)
	assertJSON(t, bridge.last().value, `{"type":"task.archive","taskId":"task-3"}`)

	runThread(t, bridge, "stop_thread", `{"threadId":"task-4"}`)
	assertJSON(t, bridge.last().value, `{"type":"run.cancel","taskId":"task-4"}`)
}

func TestAThreadCanBeStartedUnderANameByAnyThreadAndAnyCoordinator(t *testing.T) {
	bridge := &fakeThreads{}
	runThread(t, bridge, "start_thread", `{"prompt":"Add multiply(a, b) to src/math.js","title":"Add multiply to math.js"}`)
	assertJSON(t, bridge.last().value, `{"type":"task.send","text":"Add multiply(a, b) to src/math.js","title":"Add multiply to math.js"}`)

	for _, definitions := range [][]Definition[ThreadToolContext]{ThreadTools, coordinatorThreadTools, memberThreadTools} {
		shapes := shapesOf(definitions)
		check := func(args string, want bool) {
			if _, ok := parses(t, nil, shapes, "start_thread", args); ok != want {
				t.Errorf("%s: took %v, want %v", args, ok, want)
			}
		}
		check(`{"prompt":"Fix it","title":"Fix login"}`, true)
		check(`{"prompt":"Fix it"}`, true)
		check(`{"prompt":"Fix it","title":"  "}`, false)
		check(`{"prompt":"Fix it","title":"`+repeat("x", 121)+`"}`, false)
		check(`{"prompt":"  \n "}`, false)
	}
}

func TestAnOptionalStartFieldSentBlankIsLeftOut(t *testing.T) {
	bridge := &fakeThreads{}
	tools := ThreadToolsFor(bridge, func() float64 { return threadNow }, domain.CoordinationRoleCoordinator)
	callBound(t, tools, "start_thread", `{"prompt":"Fix it","project":" ","worktreeId":"\n","computer":"  ","intent":" ","doneWhen":" it passes ","delivers":"commit"}`)
	assertJSON(t, bridge.last().value, `{"type":"task.send","text":"Fix it"}`)
	if bridge.last().computer != nil {
		t.Fatalf("a blank computer starts it on this one: %q", *bridge.last().computer)
	}
}

func TestABlankMessageIsRefusedRatherThanAnsweredAsSent(t *testing.T) {
	shapes := shapesOf(ThreadTools)
	if _, ok := parses(t, nil, shapes, "message_thread", `{"threadId":"task-2","text":"  \n "}`); ok {
		t.Fatal("a blank message was taken")
	}
	parsed, ok := parses(t, nil, shapes, "message_thread", `{"threadId":"task-2","text":" also update the README\n"}`)
	if !ok {
		t.Fatal("refused")
	}
	assertJSON(t, map[string]any(parsed), `{"threadId":"task-2","text":"also update the README"}`)
}

func TestDeletingAWorktreeNamesItByIDAndReportsWhatBecameOfItsLooseWork(t *testing.T) {
	bridge := &fakeThreads{command: func(contracts.ExternalCommand, *string) (contracts.ThreadCommandResult, error) {
		return contracts.ThreadCommandResult{Notice: ptr("Deleted /worktrees/repo-wt1. Recover it with git show refs/aicodingtool/wt1.")}, nil
	}}
	deleted := runThread(t, bridge, "delete_worktree", `{"worktreeId":"wt1"}`)
	assertJSON(t, bridge.last().value, `{"type":"worktree.delete","worktreeId":"wt1"}`)
	if textOf(deleted) != "Deleted /worktrees/repo-wt1. Recover it with git show refs/aicodingtool/wt1." {
		t.Fatal(textOf(deleted))
	}
}

func TestMovingNamesTheThreadAndTheCheckout(t *testing.T) {
	bridge := &fakeThreads{}
	moved := runThread(t, bridge, "move_to_worktree", `{"threadId":"task-2","worktreeId":"wt1"}`)
	assertJSON(t, bridge.last().value, `{"type":"task.move-worktree","taskId":"task-2","destination":{"kind":"worktree","id":"wt1"}}`)
	assertMatch(t, textOf(moved), `^Moved Rework the sidebar \[task-2\]`)

	runThread(t, bridge, "move_to_worktree", `{"threadId":"task-2","worktreeId":null}`)
	assertJSON(t, bridge.last().value, `{"type":"task.move-worktree","taskId":"task-2","destination":{"kind":"local"}}`)

	runThread(t, bridge, "move_to_worktree", `{"threadId":"task-2","worktreeId":"new"}`)
	assertJSON(t, bridge.last().value, `{"type":"task.move-worktree","taskId":"task-2","destination":{"kind":"new"}}`)

	own := &fakeThreads{command: func(contracts.ExternalCommand, *string) (contracts.ThreadCommandResult, error) {
		return contracts.ThreadCommandResult{Thread: summary(nil), Notice: ptr("This thread moves to /worktrees/app-wt1 when this turn ends.")}, nil
	}}
	waiting := runThread(t, own, "move_to_worktree", `{"worktreeId":"wt1"}`)
	assertJSON(t, own.last().value, `{"type":"task.move-worktree","destination":{"kind":"worktree","id":"wt1"}}`)
	if textOf(waiting) != "This thread moves to /worktrees/app-wt1 when this turn ends." {
		t.Fatal(textOf(waiting))
	}

	runThread(t, own, "move_to_worktree", `{"threadId":null,"worktreeId":"new","then":"Fix the login bug"}`)
	assertJSON(t, own.last().value, `{"type":"task.move-worktree","destination":{"kind":"new"},"then":"Fix the login bug"}`)
	runThread(t, own, "move_to_worktree", `{"worktreeId":"new","then":null}`)
	assertJSON(t, own.last().value, `{"type":"task.move-worktree","destination":{"kind":"new"}}`)
}

func TestAThreadsRoleIsSetWhenItStartsChangedLaterOrTakenOff(t *testing.T) {
	bridge := &fakeThreads{}
	runThread(t, bridge, "start_thread", `{"prompt":"Review the diff","role":"reviewer"}`)
	assertJSON(t, bridge.last().value, `{"type":"task.send","text":"Review the diff","role":"reviewer"}`)

	given := runThread(t, bridge, "set_thread_role", `{"threadId":"task-2","role":"coordinator"}`)
	assertJSON(t, bridge.last().value, `{"type":"task.set-role","taskId":"task-2","role":"coordinator"}`)
	assertMatch(t, textOf(given), `^Set the role of `)

	cleared := runThread(t, bridge, "set_thread_role", `{"threadId":"task-2","role":null}`)
	assertJSON(t, bridge.last().value, `{"type":"task.set-role","taskId":"task-2","role":null}`)
	assertMatch(t, textOf(cleared), `^Took the role off `)
}

func TestWaitingReportsWhatTheThreadSaidOrThatItIsStillGoing(t *testing.T) {
	bridge := &fakeThreads{}
	finished := runThread(t, bridge, "wait_for_thread", `{"threadId":"task-5"}`)
	assertJSON(t, bridge.last().value, `["task-5",300000]`)
	assertMatch(t, textOf(finished), `^Finished: Rework the sidebar \[task-5\]`)
	assertMatch(t, textOf(finished), `on branch main`)

	runThread(t, bridge, "wait_for_thread", `{"threadId":"task-5","timeoutSeconds":4000}`)
	assertJSON(t, bridge.last().value, `["task-5",900000]`)

	patient := &fakeThreads{wait: func(string, float64) (contracts.ThreadWaitResult, error) {
		return contracts.ThreadWaitResult{Thread: *summary(nil), TimedOut: true}, nil
	}}
	running := runThread(t, patient, "wait_for_thread", `{"threadId":"task-1","timeoutSeconds":30}`)
	assertMatch(t, textOf(running), `^Still working after 30s: `)
}

func TestARefusedRequestComesBackAsAToolErrorTheModelCanCorrect(t *testing.T) {
	bridge := &fakeThreads{read: func(string, *float64) (contracts.ThreadTranscript, error) {
		return contracts.ThreadTranscript{}, errors.New("No thread has the ID ghost.")
	}}
	failed := runThread(t, bridge, "read_thread", `{"threadId":"ghost"}`)
	if !failed.IsError {
		t.Fatal("not an error")
	}
	assertMatch(t, textOf(failed), `Thread error: No thread has the ID ghost`)
}

func TestIdleTimesReadAsMinutesHoursAndDays(t *testing.T) {
	for _, c := range []struct {
		ms   float64
		want string
	}{{0, "0m"}, {59 * minuteMs, "59m"}, {60 * minuteMs, "1h"}, {61 * minuteMs, "1h 1m"}, {47*hourMs + 59*minuteMs, "47h 59m"}, {48 * hourMs, "2d"}} {
		if got := elapsed(c.ms); got != c.want {
			t.Errorf("elapsed(%v) = %q, want %q", c.ms, got, c.want)
		}
	}
	paused := describeThread(summary(func(s *contracts.ThreadSummary) {
		s.PausedUntil = ptr(threadNow)
		s.WorktreeRoot = ptr("/wt/app")
		s.WorktreeID = ptr("wt1")
		s.Role = ptr(domain.ThreadRoleReviewer)
		s.Archived = true
	}), threadNow)
	if paused != "Rework the sidebar [task-1] · role reviewer · /wt/app · worktree wt1 · idle · paused by its usage limit until 2027-01-15T08:00:00.000Z · 12 messages · idle 4h · archived" {
		t.Fatal(paused)
	}
}

func TestACoordinatorNeitherWaitsNorMovesAndLedStartsNameTheirDefaults(t *testing.T) {
	coordinator := names(ThreadToolsFor(&fakeThreads{}, nil, domain.CoordinationRoleCoordinator))
	for _, name := range coordinator {
		if name == "wait_for_thread" || name == "move_to_worktree" {
			t.Fatalf("a coordinator is offered %s", name)
		}
	}
	member := ThreadToolsFor(&fakeThreads{}, nil, domain.CoordinationRoleMember)
	if len(member) != len(ThreadTools) {
		t.Fatalf("a member has %d tools", len(member))
	}
	model, _ := memberThreadTools[3].Input.Get("model")
	assertMatch(t, model.description, `Omit to run it on the model your coordinator's threads run on\.$`)
	computer, _ := CoordinatorStartInput.Get("computer")
	if computer.description != "A paired computer to run it on. It starts there once the user allows it." {
		t.Fatal(computer.description)
	}
}

func repeat(text string, n int) string {
	out := make([]byte, 0, len(text)*n)
	for range n {
		out = append(out, text...)
	}
	return string(out)
}
