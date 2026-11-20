package host

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type peerQuery struct {
	id    string
	query contracts.ComputerThreadQuery
}

// peers is a desktop whose paired computers answer from funcs.
type peers struct {
	mu      sync.Mutex
	queries []peerQuery
	answer  func(id string, query contracts.ComputerThreadQuery) (jsontext.Value, error)
}

func (p *peers) QueryComputerThreads(_ context.Context, id string, query contracts.ComputerThreadQuery) (jsontext.Value, error) {
	p.mu.Lock()
	p.queries = append(p.queries, peerQuery{id, query})
	p.mu.Unlock()
	return p.answer(id, query)
}

func answering(state func() *application.WorkspaceState, prepare PrepareThreadRequest) func(string, contracts.ComputerThreadQuery) (jsontext.Value, error) {
	return func(_ string, query contracts.ComputerThreadQuery) (jsontext.Value, error) {
		result, err := QueryLocalThreads(context.Background(), state, prepare, query)
		if err != nil {
			return nil, err
		}
		return jsonx.Marshal(result)
	}
}

func preparesNothing(context.Context, contracts.ThreadRequest) (*PreparedThreadRequest, error) {
	return nil, nil
}

type readFixture struct {
	state    *application.WorkspaceState
	remote   *application.WorkspaceState
	computer *application.PairedComputer
	desktop  *peers
	host     ReadHost
}

func newReadFixture() *readFixture {
	remote := workspace(func(state *application.WorkspaceState) {
		state.Projects = []*domain.Project{{ID: "remote-project", Root: "/linux/app"}}
		state.Threads = []*domain.Thread{task("remote-id", func(thread *domain.Thread) {
			thread.ProjectID, thread.Title, thread.UpdatedAt = new("remote-project"), "Remote work", 20
			thread.Messages = []*domain.ConversationMessage{{ID: "message", Kind: domain.ConversationMessageKindAssistant, Text: "Experiment results", At: 20}}
		})}
	})
	computer := &application.PairedComputer{ComputerLink: domain.ComputerLink{ID: "linux", Name: "Linux", Host: "linux.test", PairedAt: 1, Status: domain.ComputerStatusConnected}, State: remote}
	state := workspace(func(state *application.WorkspaceState) {
		state.Threads = []*domain.Thread{task("caller", func(thread *domain.Thread) { thread.UpdatedAt = 10 })}
		state.Computers.Filter = "this"
		state.Computers.Paired = []*application.PairedComputer{computer}
	})
	f := &readFixture{state: state, remote: remote, computer: computer}
	f.desktop = &peers{answer: answering(func() *application.WorkspaceState { return f.remote }, preparesNothing)}
	f.host = ReadHost{State: func() *application.WorkspaceState { return f.state }, Desktop: f.desktop}
	return f
}

func ids(rows []*contracts.ThreadSummary) []string {
	out := []string{}
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func rejects(t *testing.T, err error, pattern string) {
	t.Helper()
	if err == nil || !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("err = %v, want /%s/", err, pattern)
	}
}

func list(t *testing.T, f *readFixture, query contracts.ThreadListQuery) []*contracts.ThreadSummary {
	t.Helper()
	rows, err := ListAcrossComputers(context.Background(), f.host, "caller", query, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func read(f *readFixture, reference string, limit *float64, selected string) (*contracts.ThreadTranscript, error) {
	return ReadAcrossComputers(context.Background(), f.host, reference, limit, selected)
}

func TestAKnownRemoteIDReadsOnlyItsOwnersTranscript(t *testing.T) {
	f := newReadFixture()
	transcript, err := read(f, "remote-id", new(1.0), "all")
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Messages[0].Text != "Experiment results" || *transcript.Thread.Computer != (contracts.ThreadSummaryComputer{ID: "linux", Name: "Linux"}) {
		t.Fatalf("transcript = %+v", transcript)
	}
	if raw, _ := jsonx.Marshal(f.desktop.queries[0].query); len(f.desktop.queries) != 1 || string(raw) != `{"kind":"thread-read","threadId":"remote-id","limit":1}` {
		t.Fatalf("queries = %s", raw)
	}
	if _, err := read(f, "caller", nil, "all"); err != nil || len(f.desktop.queries) != 1 {
		t.Fatal("a local read queried a peer")
	}
}

func TestListDefaultsStayLocalAndAllComputersMergeByActivity(t *testing.T) {
	f := newReadFixture()
	if got := ids(list(t, f, contracts.ThreadListQuery{})); strings.Join(got, ",") != "caller" {
		t.Fatal(got)
	}
	rows := list(t, f, contracts.ThreadListQuery{Computer: new("all"), Limit: new(1.0)})
	if strings.Join(ids(rows), ",") != "remote-id" || rows[0].Computer.ID != "linux" || len(f.desktop.queries) != 0 {
		t.Fatalf("rows = %v", ids(rows))
	}
	if rows := list(t, f, contracts.ThreadListQuery{Computer: new("Linux"), Project: new("app")}); rows[0].ID != "remote-id" {
		t.Fatal(ids(rows))
	}
	if got := ids(list(t, f, contracts.ThreadListQuery{Computer: new("all"), Project: new("app")})); strings.Join(got, ",") != "remote-id" {
		t.Fatal(got)
	}
	if got := list(t, f, contracts.ThreadListQuery{Computer: new("all"), Limit: new(0.0)}); len(got) != 0 {
		t.Fatal(ids(got))
	}
	f.state.Computers.Paired = nil
	if got := ids(list(t, f, contracts.ThreadListQuery{Computer: new("all"), Project: new("current")})); strings.Join(got, ",") != "caller" {
		t.Fatal(got)
	}
}

func TestOfflineHostsListFromCachedRowsButReadsAndSearchesFail(t *testing.T) {
	f := newReadFixture()
	f.computer.Status = domain.ComputerStatusOffline
	rows := list(t, f, contracts.ThreadListQuery{Computer: new("all")})
	if row := rows[0]; row.ID != "remote-id" || !row.Computer.Offline {
		t.Fatal(ids(rows))
	}
	_, err := read(f, "remote-id", nil, "all")
	rejects(t, err, "Linux is offline")
	_, err = ListAcrossComputers(context.Background(), f.host, "caller", contracts.ThreadListQuery{Computer: new("linux"), Search: new("results")}, nil)
	rejects(t, err, "Linux is offline")
	_, err = read(f, "missing", nil, "all")
	rejects(t, err, "Linux is not connected")
	if len(f.desktop.queries) != 0 {
		t.Fatal("an offline host was queried")
	}
}

func TestMatchingReferencesRequireAComputer(t *testing.T) {
	f := newReadFixture()
	f.state.Threads[0].Title = "Remote work"
	_, err := read(f, "Remote work", nil, "all")
	rejects(t, err, "More than one computer")
	if transcript, err := read(f, "Remote work", new(1.0), "linux"); err != nil || transcript.Thread.ID != "remote-id" {
		t.Fatal(err)
	}
	if transcript, err := read(f, "Remote work", new(1.0), "this"); err != nil || transcript.Thread.ID != "caller" {
		t.Fatal(err)
	}
	f.state.Threads[0].Title = "remote-id"
	if transcript, err := read(f, "remote-id", nil, "all"); err != nil || transcript.Thread.ID != "remote-id" {
		t.Fatal(err)
	}
	_, err = read(f, "caller", new(1.0), "linux")
	rejects(t, err, "No thread")
	_, err = read(f, "remote-id", new(1.0), "unknown")
	rejects(t, err, "No paired computer")
}

func TestReadsDiagnoseMissingSnapshotsAndAmbiguousLabels(t *testing.T) {
	f := newReadFixture()
	twin := *f.computer
	twin.ID = "another-linux"
	f.state.Computers.Paired = append(f.state.Computers.Paired, &twin)
	_, err := read(f, "remote-id", new(1.0), "Linux")
	rejects(t, err, "More than one computer is named")
	f.state.Computers.Paired = f.state.Computers.Paired[:1]
	f.computer.State = nil
	_, err = read(f, "remote-id", nil, "all")
	rejects(t, err, "Linux is not connected")
	_, err = ListAcrossComputers(context.Background(), f.host, "caller", contracts.ThreadListQuery{Computer: new("linux")}, nil)
	rejects(t, err, "has not sent its thread list yet")
}

func TestRemoteResponsesAreValidated(t *testing.T) {
	f := newReadFixture()
	transcript, _ := jsonx.Marshal(application.ThreadTranscript(f.remote, "remote-id", 30, 0, nil))
	for _, result := range []string{`null`, `{}`, strings.Replace(string(transcript), `"text":"Experiment results"`, `"text":42`, 1), strings.Replace(string(transcript), `"id":"remote-id"`, `"id":"another"`, 1)} {
		f.desktop.answer = func(string, contracts.ComputerThreadQuery) (jsontext.Value, error) {
			return jsontext.Value(result), nil
		}
		_, err := read(f, "remote-id", nil, "all")
		rejects(t, err, "Invalid thread transcript")
	}
	f.desktop.answer = func(string, contracts.ComputerThreadQuery) (jsontext.Value, error) {
		return jsontext.Value(`[{"id":"bad"}]`), nil
	}
	_, err := ListAcrossComputers(context.Background(), f.host, "caller", contracts.ThreadListQuery{Computer: new("linux"), Search: new("results")}, nil)
	rejects(t, err, "Invalid thread list")
	f.desktop.answer = func(string, contracts.ComputerThreadQuery) (jsontext.Value, error) {
		return nil, errors.New("This feature is unavailable on that computer. Updating AI Coding Tool there may help.")
	}
	_, err = read(f, "remote-id", nil, "all")
	rejects(t, err, "Updating AI Coding Tool")
}

// historyFixture is a runtime history over a state the reducer changes.
type historyFixture struct {
	mu      sync.Mutex
	state   *application.WorkspaceState
	loads   []string
	history *RuntimeHistory
}

func newHistoryFixture(initial *application.WorkspaceState) *historyFixture {
	h := &historyFixture{state: initial}
	h.history = createRuntimeHistory(historyHost{
		state: h.current,
		search: func(_ context.Context, search string, ids []string) ([]string, error) {
			if strings.Contains("needle in disk history", search) {
				return ids, nil
			}
			return []string{}, nil
		},
		load: func(_ context.Context, id string) ([]*domain.ConversationMessage, error) {
			h.mu.Lock()
			h.loads = append(h.loads, id)
			h.mu.Unlock()
			return []*domain.ConversationMessage{{ID: "saved", Kind: domain.ConversationMessageKindAssistant, Text: "needle in disk history", At: 1}}, nil
		},
		dispatch: func(_ context.Context, input application.WorkspaceInput) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			next := application.Reduce(h.state, input)
			h.state = &next.State
			return nil
		},
		persistence: &PersistenceQueue{},
	})
	return h
}

func (h *historyFixture) current() *application.WorkspaceState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

func unloaded(thread *domain.Thread) *domain.Thread {
	next := *thread
	next.Messages = []*domain.ConversationMessage{}
	next.HistorySummary = &domain.StoredThreadHistorySummary{MessageCount: 1}
	return &next
}

func TestRemoteSearchesReadTheOwnersDiskWithoutLoadingIt(t *testing.T) {
	f := newReadFixture()
	f.remote.Threads = []*domain.Thread{unloaded(f.remote.Threads[0])}
	owner := newHistoryFixture(f.remote)
	f.desktop.answer = answering(owner.current, owner.history.PrepareThreadRequest)
	rows, err := ListAcrossComputers(context.Background(), f.host, "caller", contracts.ThreadListQuery{Computer: new("linux"), Search: new("needle")}, nil)
	if err != nil || len(rows) == 0 || rows[0].ID != "remote-id" {
		t.Fatalf("rows = %v, %v", ids(rows), err)
	}
	if len(owner.loads) != 0 || owner.current().Threads[0].HistorySummary == nil {
		t.Fatal("a search loaded the history")
	}
	if rows, _ := ListAcrossComputers(context.Background(), f.host, "caller", contracts.ThreadListQuery{Computer: new("linux"), Search: new("haystack")}, nil); len(rows) != 0 {
		t.Fatal(ids(rows))
	}
	transcript, err := read(f, "remote-id", nil, "all")
	if err != nil || transcript.Messages[0].Text != "needle in disk history" || strings.Join(owner.loads, ",") != "remote-id" {
		t.Fatalf("transcript = %+v, loads = %v, %v", transcript, owner.loads, err)
	}
	caller := newHistoryFixture(workspace(func(state *application.WorkspaceState) {
		thread := unloaded(f.state.Threads[0])
		thread.Title = "remote-id"
		state.Threads = []*domain.Thread{thread}
		state.Computers = f.state.Computers
	}))
	if _, err := caller.history.PrepareThreadRequest(context.Background(), contracts.ThreadRequestRead{RequestID: "read", TaskID: "caller", ThreadID: "remote-id"}); err != nil {
		t.Fatal(err)
	}
	if len(caller.loads) != 0 {
		t.Fatal("an exact remote ID hydrated a local title match")
	}
}

func TestPairedQueriesStayLocal(t *testing.T) {
	f := newReadFixture()
	holder := newHistoryFixture(f.state)
	_, err := QueryLocalThreads(context.Background(), holder.current, holder.history.PrepareThreadRequest, contracts.ComputerThreadQueryThreadRead{ThreadID: "remote-id"})
	rejects(t, err, "No thread")
	rows, err := QueryLocalThreads(context.Background(), holder.current, holder.history.PrepareThreadRequest, contracts.ComputerThreadQueryThreadList{})
	if err != nil || strings.Join(ids(rows.([]*contracts.ThreadSummary)), ",") != "caller" {
		t.Fatalf("rows = %v, %v", rows, err)
	}
}
