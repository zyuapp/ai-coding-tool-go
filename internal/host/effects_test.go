package host

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// fakeDesktop answers what a test sets and panics on the rest, through the embedded nil interface.
type fakeDesktop struct {
	RuntimeDesktop
	sendToComputer func(id string, inputs []application.WorkspaceInput) (contracts.WorkspaceCommandResult, error)
	directories    func(prefix string, computerID *string) ([]string, error)
	preserve       func(files []string, root, messageID string) error
	changedFiles   func(workspaceID string) (contracts.ChangedFilesResult, error)
}

func (f *fakeDesktop) SendToComputer(_ context.Context, id string, inputs []application.WorkspaceInput) (contracts.WorkspaceCommandResult, error) {
	return f.sendToComputer(id, inputs)
}

func (f *fakeDesktop) Directories(_ context.Context, prefix string, computerID *string) ([]string, error) {
	return f.directories(prefix, computerID)
}

func (f *fakeDesktop) PreserveMessageImages(_ context.Context, files []string, root, messageID string) error {
	return f.preserve(files, root, messageID)
}

func (f *fakeDesktop) ChangedFiles(_ context.Context, workspaceID string) (contracts.ChangedFilesResult, error) {
	return f.changedFiles(workspaceID)
}

// raised collects what an effect dispatched.
type raised struct {
	mu     sync.Mutex
	inputs []application.WorkspaceInput
}

func (r *raised) dispatch(_ context.Context, input application.WorkspaceInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, input)
	return nil
}

func (r *raised) json(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := jsonx.Marshal(r.inputs)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func runEffect(t *testing.T, effect application.WorkspaceEffect, host EffectHost) {
	t.Helper()
	if err := RunWorkspaceEffect(context.Background(), effect, host); err != nil {
		t.Fatal(err)
	}
}

func TestAForwardTheLineRefusedIsAnErrorOnceAndHousekeepingIsNone(t *testing.T) {
	inputs := &raised{}
	host := EffectHost{Dispatch: inputs.dispatch, Desktop: &fakeDesktop{sendToComputer: func(string, []application.WorkspaceInput) (contracts.WorkspaceCommandResult, error) {
		return nil, errors.New("That computer cannot be reached right now.")
	}}}
	runEffect(t, application.WorkspaceEffectComputerForward{ID: "linux", Inputs: []application.WorkspaceInput{contracts.ViewCommandViewSetFocused{Focused: true}}}, host)
	runEffect(t, application.WorkspaceEffectComputerForward{ID: "linux", Inputs: []application.WorkspaceInput{contracts.PlanUsageCommandUsageRead{}}}, host)
	if got := inputs.json(t); got != "[]" {
		t.Fatalf("housekeeping raised %s", got)
	}
	runEffect(t, application.WorkspaceEffectComputerForward{ID: "linux", Inputs: []application.WorkspaceInput{contracts.ViewCommandViewSetFocused{Focused: true}, contracts.TaskCommandTaskSelect{TaskID: "remote-thread"}}}, host)
	if got := inputs.json(t); got != `[{"type":"action.failed","message":"That computer cannot be reached right now."}]` {
		t.Fatalf("raised %s", got)
	}
}

func TestAStartThatComputerRefusedSaysItStartedNothing(t *testing.T) {
	inputs := &raised{}
	effect := application.WorkspaceEffectComputerForward{ID: "linux", Inputs: []application.WorkspaceInput{contracts.TaskCommandTaskSend{Text: new("Port it")}}, Proposal: &application.ComputerEffectComputerForwardProposal{TaskID: "lead", ProposalID: "p1"}}
	answering := func(send func(string, []application.WorkspaceInput) (contracts.WorkspaceCommandResult, error)) EffectHost {
		return EffectHost{Dispatch: inputs.dispatch, Desktop: &fakeDesktop{sendToComputer: send}}
	}
	runEffect(t, effect, answering(func(string, []application.WorkspaceInput) (contracts.WorkspaceCommandResult, error) {
		return contracts.WorkspaceCommandResultFalse{Message: "No such folder."}, nil
	}))
	runEffect(t, effect, answering(func(string, []application.WorkspaceInput) (contracts.WorkspaceCommandResult, error) {
		return nil, errors.New("That computer cannot be reached right now.")
	}))
	want := `[{"type":"proposal.settled","taskId":"lead","proposalId":"p1","message":"No such folder.","refused":true},` +
		`{"type":"proposal.settled","taskId":"lead","proposalId":"p1","message":"That computer cannot be reached right now."}]`
	if got := inputs.json(t); got != want {
		t.Fatalf("raised %s", got)
	}
}

func TestAProjectRemovalThatComputerRefusedIsShownOnTheProjectsPage(t *testing.T) {
	inputs := &raised{}
	host := EffectHost{Dispatch: inputs.dispatch, Desktop: &fakeDesktop{sendToComputer: func(string, []application.WorkspaceInput) (contracts.WorkspaceCommandResult, error) {
		return contracts.WorkspaceCommandResultFalse{Message: "Stop the running tasks before removing this project."}, nil
	}}}
	runEffect(t, application.WorkspaceEffectComputerForward{ID: "linux", Inputs: []application.WorkspaceInput{contracts.ProjectCommandProjectRemove{ProjectID: "ml"}}}, host)
	if got := inputs.json(t); got != `[{"type":"action.failed","message":"Stop the running tasks before removing this project.","page":"projects"}]` {
		t.Fatalf("raised %s", got)
	}
}

func TestPathQueriesDebounceBeforeReadingDisk(t *testing.T) {
	var mu sync.Mutex
	var calls [][2]string
	host := EffectHost{Dispatch: (&raised{}).dispatch, Desktop: &fakeDesktop{directories: func(prefix string, computerID *string) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, [2]string{prefix, *computerID})
		return []string{}, nil
	}}}
	var wait sync.WaitGroup
	for _, effect := range []application.WorkspaceEffectProjectAddDirectories{{ComputerID: "linux", Prefix: "/w", Request: 1}, {ComputerID: "this", Prefix: "/work", Request: 2}} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			runEffect(t, effect, host)
		}()
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	early := len(calls)
	mu.Unlock()
	if early != 0 {
		t.Fatal("read disk before the pause")
	}
	wait.Wait()
	if !slices.Equal(calls, [][2]string{{"/work", "this"}}) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestAMessagesLocalImagesArePreservedUnderIt(t *testing.T) {
	ported(t, func() {
		var preserved [][]any
		host := EffectHost{Desktop: &fakeDesktop{preserve: func(files []string, root, messageID string) error {
			preserved = append(preserved, []any{files, root, messageID})
			return nil
		}}}
		runEffect(t, application.WorkspaceEffectPreserveMessageImages{Text: "See [Shot](/tmp/shot.png).", Root: "/repo", MessageID: "answer"}, host)
		runEffect(t, application.WorkspaceEffectPreserveMessageImages{Text: "No images here.", Root: "/repo", MessageID: "plain"}, host)
		if raw, _ := jsonx.Marshal(preserved); string(raw) != `[[["/tmp/shot.png"],"/repo","answer"]]` {
			t.Fatalf("preserved = %s", raw)
		}
	})
}

func TestATickDuringASlowScanLeavesOneFollowUp(t *testing.T) {
	inputs := &raised{}
	release := make(chan struct{})
	var mu sync.Mutex
	scans := 0
	host := EffectHost{
		Dispatch:             inputs.dispatch,
		EnvironmentRefreshes: EnvironmentRefreshes{Current: map[string]*EnvironmentRefreshEffect{}},
		Desktop: &fakeDesktop{changedFiles: func(string) (contracts.ChangedFilesResult, error) {
			mu.Lock()
			scans++
			first := scans == 1
			mu.Unlock()
			if first {
				<-release
			}
			return contracts.ChangedFilesResultAvailable{Files: []string{}}, nil
		}},
	}
	done := make(chan struct{})
	go func() {
		runEffect(t, application.WorkspaceEffectRefreshEnvironment{WorkspaceID: "w"}, host)
		close(done)
	}()
	eventually(t, "the first scan", func() bool { mu.Lock(); defer mu.Unlock(); return scans == 1 })
	runEffect(t, application.WorkspaceEffectRefreshEnvironment{WorkspaceID: "w", TaskID: new("a")}, host)
	runEffect(t, application.WorkspaceEffectRefreshEnvironment{WorkspaceID: "w", TaskID: new("b")}, host)
	close(release)
	<-done
	if scans != 2 {
		t.Fatalf("scans = %d", scans)
	}
	if len(host.EnvironmentRefreshes.Current) != 0 {
		t.Fatal("the checkout is still marked as scanning")
	}
	inputs.mu.Lock()
	defer inputs.mu.Unlock()
	last := inputs.inputs[len(inputs.inputs)-1].(application.DiffEventEnvironmentUpdated)
	if last.TaskID == nil || *last.TaskID != "b" {
		t.Fatal("the follow-up is not the newest tick")
	}
}

// resolveDesktop is a ResolveDesktop answering from funcs.
type resolveDesktop struct {
	createBranch   func(workspaceID, branch string) error
	checkoutBranch func(workspaceID, branch string) error
	openFolder     func() (*domain.WorkspaceRecord, error)
}

func (d resolveDesktop) OpenFolder(context.Context) (*domain.WorkspaceRecord, error) {
	return d.openFolder()
}

func (resolveDesktop) ProjectlessWorkspace(context.Context) (*domain.WorkspaceRecord, error) {
	return &domain.WorkspaceRecord{ID: "projectless", Kind: domain.WorkspaceKindProjectless, Root: "/tmp"}, nil
}

func (d resolveDesktop) CreateBranch(_ context.Context, workspaceID, branch string) error {
	return d.createBranch(workspaceID, branch)
}

func (d resolveDesktop) CheckoutBranch(_ context.Context, workspaceID, branch string) error {
	return d.checkoutBranch(workspaceID, branch)
}

func (resolveDesktop) CreateWorktree(context.Context, contracts.CreateWorktreeRequest) (*contracts.CreatedWorktree, error) {
	return nil, errors.New("the worktree already exists")
}

func TestResolvingARunHandsBackTheNamedWorkspace(t *testing.T) {
	refuse := func(string, string) error { return errors.New("nothing should be done") }
	worktree := &domain.WorkspaceRecord{ID: "worktree-1", Kind: domain.WorkspaceKindWorktree, Root: "/worktrees/repo-wt1"}
	resolved := ResolveRunWorkspace(context.Background(), application.WorkspaceEffectResolveRunWorkspace{PendingID: "pending-1", Workspace: worktree}, resolveDesktop{createBranch: refuse, checkoutBranch: refuse})
	if raw, _ := jsonx.Marshal(resolved); string(raw) != `{"type":"run.resolved","pendingId":"pending-1","workspace":{"id":"worktree-1","kind":"worktree","root":"/worktrees/repo-wt1"}}` {
		t.Fatalf("resolved = %s", raw)
	}
}

func TestResolvingARunReportsWhatTheBranchCouldNotDo(t *testing.T) {
	var calls [][3]string
	desktop := resolveDesktop{
		createBranch: func(workspaceID, branch string) error {
			calls = append(calls, [3]string{"create", workspaceID, branch})
			return nil
		},
		checkoutBranch: func(string, string) error { return errors.New("Your local changes would be overwritten.") },
	}
	failed := ResolveRunWorkspace(context.Background(), application.WorkspaceEffectResolveRunWorkspace{
		PendingID:    "pending-2",
		Workspace:    &domain.WorkspaceRecord{ID: "workspace-a", Kind: domain.WorkspaceKindProject, Root: "/repo"},
		CreateBranch: &application.WorkspaceEffectResolveRunWorkspaceCreateBranch{WorkspaceID: "workspace-a", Branch: "feature-x"},
		Checkout:     &application.WorkspaceEffectResolveRunWorkspaceCheckout{WorkspaceID: "workspace-a", Branch: "feature-x"},
	}, desktop)
	if !slices.Equal(calls, [][3]string{{"create", "workspace-a", "feature-x"}}) {
		t.Fatalf("calls = %v", calls)
	}
	if raw, _ := jsonx.Marshal(failed); string(raw) != `{"type":"run.unresolved","pendingId":"pending-2","message":"Could not check out feature-x: Your local changes would be overwritten."}` {
		t.Fatalf("failed = %s", raw)
	}
}

func TestResolvingThroughThePickerInsistsOnTheSameFolder(t *testing.T) {
	desktop := resolveDesktop{openFolder: func() (*domain.WorkspaceRecord, error) {
		return &domain.WorkspaceRecord{ID: "workspace-b", Kind: domain.WorkspaceKindProject, Root: "/elsewhere"}, nil
	}}
	wrong, ok := ResolveRunWorkspace(context.Background(), application.WorkspaceEffectResolveRunWorkspace{PendingID: "pending-3", Picker: true, Root: new("/repo")}, desktop).(application.WorkspaceEventRunUnresolved)
	if !ok || wrong.Message != application.WorkspaceErrors["sameProject"] {
		t.Fatalf("wrong = %+v", wrong)
	}
}
