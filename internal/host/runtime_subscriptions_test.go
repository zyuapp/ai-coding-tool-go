package host

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// listeningDesktop answers what every host module asks for at the start, and keeps its listeners.
type listeningDesktop struct {
	RuntimeDesktop
	mu            sync.Mutex
	threadAsks    []func(request contracts.ThreadRequest)
	threadAnswers []contracts.ThreadResponse
	agentEvents   []func(event contracts.AgentEvent)
}

func off() {}

func (d *listeningDesktop) OnAgentEvent(listener func(contracts.AgentEvent)) func() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.agentEvents = append(d.agentEvents, listener)
	return off
}

func (d *listeningDesktop) OnThreadRequest(listener func(contracts.ThreadRequest)) func() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.threadAsks = append(d.threadAsks, listener)
	return off
}

func (d *listeningDesktop) AnswerThreadRequest(response contracts.ThreadResponse) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.threadAnswers = append(d.threadAnswers, response)
}

func (d *listeningDesktop) answers() []contracts.ThreadResponse {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]contracts.ThreadResponse(nil), d.threadAnswers...)
}

func (d *listeningDesktop) askThreads(request contracts.ThreadRequest) {
	d.mu.Lock()
	listeners := d.threadAsks
	d.mu.Unlock()
	for _, listener := range listeners {
		listener(request)
	}
}

func (d *listeningDesktop) ListAutomations(context.Context) ([]*domain.AutomationView, error) {
	return []*domain.AutomationView{}, nil
}
func (d *listeningDesktop) OnAutomationsChanged(func([]*domain.AutomationView)) func() { return off }
func (d *listeningDesktop) OnAutomationFire(func(contracts.AutomationFire)) func()     { return off }
func (d *listeningDesktop) AppUpdate(context.Context) (domain.AppUpdate, error) {
	return nil, context.Canceled
}
func (d *listeningDesktop) AppBuild(context.Context) (*domain.AppBuild, error) {
	return nil, context.Canceled
}
func (d *listeningDesktop) LaunchedUpdate(context.Context) (*string, error) { return nil, nil }
func (d *listeningDesktop) OnAppUpdate(func(domain.AppUpdate)) func()       { return off }
func (d *listeningDesktop) MobileState(context.Context) (*domain.MobileServerState, error) {
	state := domain.EmptyMobileServerState()
	return &state, nil
}
func (d *listeningDesktop) OnMobileState(func(domain.MobileServerState)) func()          { return off }
func (d *listeningDesktop) OnMobileRequest(func(contracts.MobileRequest)) func()         { return off }
func (d *listeningDesktop) OnDictationModel(func(contracts.DictationModelStatus)) func() { return off }
func (d *listeningDesktop) OnBrowserEvent(func(contracts.BrowserPageEvent)) func()       { return off }
func (d *listeningDesktop) OnTerminalEvent(func(domain.TerminalUpdate)) func()           { return off }
func (d *listeningDesktop) OnBrowserFind(func(contracts.BrowserFindEvent)) func()        { return off }
func (d *listeningDesktop) OnOpenProject(func(domain.WorkspaceRecord)) func()            { return off }
func (d *listeningDesktop) OnComputersChanged(func(string, []*domain.ComputerLink)) func() {
	return off
}
func (d *listeningDesktop) OnComputerState(func(string, application.WorkspaceState)) func() {
	return off
}
func (d *listeningDesktop) OnComputerNotice(func(string, contracts.ThreadNotice)) func() { return off }

func TestALocalToolSearchAnswersFromStoredHistoryWithoutLoadingIt(t *testing.T) {
	owner := newHistoryFixture(workspace(func(state *application.WorkspaceState) {
		state.Threads = []*domain.Thread{task("caller"), unloaded(task("cold", func(thread *domain.Thread) { thread.Title = "Old work" }))}
	}))
	desktop := &listeningDesktop{}
	settled := settledFuture[contracts.WorkspaceCommandResult](acceptedResult, nil)
	stop, _ := SubscribeWorkspaceRuntime(SubscriptionHost{
		State:                owner.current,
		Dispatch:             func(context.Context, application.WorkspaceInput) error { return nil },
		Execute:              func(application.WorkspaceInput) application.WorkspaceExecution { return execution(settled, settled) },
		Waiters:              &ThreadWaiters{},
		PrepareThreadRequest: owner.history.PrepareThreadRequest,
		Desktop:              desktop,
	})
	defer stop()
	desktop.askThreads(contracts.ThreadRequestList{RequestID: "search", TaskID: "caller", Search: new("needle")})
	eventually(t, "the answer", func() bool { return len(desktop.answers()) == 1 })
	answered, ok := desktop.answers()[0].(contracts.ThreadResponseTrue)
	if !ok {
		t.Fatalf("refused: %+v", desktop.answers()[0])
	}
	var rows []*contracts.ThreadSummary
	if err := jsonx.Unmarshal(answered.Result, &rows); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids(rows), ",") != "cold" || len(owner.loads) != 0 || findThread(owner.current().Threads, "cold").HistorySummary == nil {
		t.Fatalf("rows = %v, loads = %v", ids(rows), owner.loads)
	}
}

func TestARunsReportsAreFoldedInAFrameAtATime(t *testing.T) {
	desktop := &listeningDesktop{}
	var mu sync.Mutex
	var batches [][]contracts.AgentEvent
	settled := settledFuture[contracts.WorkspaceCommandResult](acceptedResult, nil)
	frames := make(chan func(), 4)
	stop, flush := SubscribeWorkspaceRuntime(SubscriptionHost{
		State:    func() *application.WorkspaceState { return workspace() },
		Dispatch: func(context.Context, application.WorkspaceInput) error { return nil },
		Execute: func(input application.WorkspaceInput) application.WorkspaceExecution {
			if batch, ok := input.(application.WorkspaceEventAgentEvents); ok {
				mu.Lock()
				batches = append(batches, batch.Events)
				mu.Unlock()
			}
			return execution(settled, settled)
		},
		Waiters:              &ThreadWaiters{},
		PrepareThreadRequest: preparesNothing,
		Desktop:              desktop,
		Frame: func(flush func()) func() {
			frames <- flush
			return func() {}
		},
	})
	defer stop()
	report := func(text string) {
		for _, listener := range desktop.agentEvents {
			listener(contracts.RunEventAssistantDelta{RunEventBase: contracts.RunEventBase{TaskID: "t", RunID: "r"}, Text: text})
		}
	}
	report("a")
	report("b")
	(<-frames)()
	report("c")
	flush()
	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 2 || len(batches[0]) != 2 || len(batches[1]) != 1 {
		t.Fatalf("batches = %v", batches)
	}
}
