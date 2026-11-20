package host

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// directoryRequests holds, per RuntimeDesktop, the token of the newest directory listing asked of it.
var directoryRequests sync.Map

// environmentMu guards every EnvironmentRefreshes map.
var environmentMu sync.Mutex

// refreshEnvironment runs one Git scan per checkout. A tick during a slow scan replaces the one follow-up still needed.
func refreshEnvironment(ctx context.Context, first EnvironmentRefreshEffect, host EffectHost) error {
	refreshes := host.EnvironmentRefreshes.Current
	environmentMu.Lock()
	if _, running := refreshes[first.WorkspaceID]; running {
		refreshes[first.WorkspaceID] = &first
		environmentMu.Unlock()
		return nil
	}
	refreshes[first.WorkspaceID] = nil
	environmentMu.Unlock()
	defer func() {
		environmentMu.Lock()
		delete(refreshes, first.WorkspaceID)
		environmentMu.Unlock()
	}()
	effect := &first
	for effect != nil {
		result, err := host.Desktop.ChangedFiles(ctx, effect.WorkspaceID)
		if err == nil {
			err = host.Dispatch(ctx, application.DiffEventEnvironmentUpdated{WorkspaceID: effect.WorkspaceID, TaskID: nonEmpty(effect.TaskID), RunID: nonEmpty(effect.RunID), Result: result})
		}
		if err != nil {
			if err := host.Dispatch(ctx, application.DiffEventEnvironmentUpdated{WorkspaceID: effect.WorkspaceID, Result: contracts.BranchesResultError{Message: errorMessage(err)}}); err != nil {
				return err
			}
		}
		environmentMu.Lock()
		effect = refreshes[first.WorkspaceID]
		refreshes[first.WorkspaceID] = nil
		environmentMu.Unlock()
	}
	return nil
}

// identity keys a map by v where v can be a key, and by its type otherwise.
func identity(v any) any {
	if v == nil || reflect.TypeOf(v).Comparable() {
		return v
	}
	return reflect.TypeOf(v)
}

// nonEmpty is value unless it holds the empty string, which JavaScript's truthiness leaves out.
func nonEmpty(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	return value
}

// ProjectEffects act on the project's folder, its checkouts, and what Git says about them.
var ProjectEffects = EffectHandlers{
	"add-project": handle(func(ctx context.Context, effect application.WorkspaceEffectAddProject, host EffectHost) error {
		workspace, err := host.Desktop.RegisterProject(ctx, effect.Root)
		if err == nil {
			added := application.ProjectAddEventProjectAdded{Workspace: *workspace, Request: effect.Request}
			if effect.Start != nil && *effect.Start {
				added.Start = new(true)
			}
			err = host.Dispatch(ctx, added)
		}
		if err != nil {
			return host.Dispatch(ctx, application.ProjectAddEventProjectAddFinished{Request: effect.Request, Error: new(errorMessage(err))})
		}
		return nil
	}),
	"project-add.pick": handle(func(ctx context.Context, effect application.WorkspaceEffectProjectAddPick, host EffectHost) error {
		workspace, err := host.Desktop.OpenFolder(ctx)
		if err == nil {
			picked := application.ProjectAddEventProjectPathPicked{Request: effect.Request}
			if workspace != nil {
				picked.Root = &workspace.Root
			}
			err = host.Dispatch(ctx, picked)
		}
		if err != nil {
			return host.Dispatch(ctx, application.ProjectAddEventProjectPathPicked{Request: effect.Request, Error: new(errorMessage(err))})
		}
		return nil
	}),
	"project-add.directories": handle(func(ctx context.Context, effect application.WorkspaceEffectProjectAddDirectories, host EffectHost) error {
		pending := new(struct{ _ byte })
		key := identity(host.Desktop)
		directoryRequests.Store(key, pending)
		time.Sleep(180 * time.Millisecond)
		if current, _ := directoryRequests.Load(key); current != pending {
			return nil
		}
		defer directoryRequests.CompareAndDelete(key, pending)
		directories, err := host.Desktop.Directories(ctx, effect.Prefix, &effect.ComputerID)
		if err == nil {
			err = host.Dispatch(ctx, application.ProjectAddEventProjectDirectories{Request: effect.Request, Directories: directories})
		}
		if err != nil {
			return host.Dispatch(ctx, application.ProjectAddEventProjectDirectories{Request: effect.Request, Directories: []string{}, Error: new(errorMessage(err))})
		}
		return nil
	}),
	"pick-project": handle(func(ctx context.Context, _ application.WorkspaceEffectPickProject, host EffectHost) error {
		workspace, err := host.Desktop.OpenFolder(ctx)
		if err == nil && workspace != nil {
			err = host.Dispatch(ctx, application.ProjectEventProjectOpened{Workspace: *workspace})
		}
		return ReportFailure(ctx, host, err)
	}),
	"register-project": handle(func(ctx context.Context, effect application.WorkspaceEffectRegisterProject, host EffectHost) error {
		workspace, err := host.Desktop.RegisterProject(ctx, effect.Root)
		if err == nil {
			err = host.Dispatch(ctx, application.ProjectEventProjectRegistered{ProjectID: effect.ProjectID, Workspace: *workspace})
		}
		if err != nil {
			return host.Dispatch(ctx, application.ProjectEventProjectRegisterFailed{ProjectID: effect.ProjectID, Message: errorMessage(err)})
		}
		return nil
	}),
	"create-worktree": handle(func(ctx context.Context, effect application.WorkspaceEffectCreateWorktree, host EffectHost) error {
		moving := effect.Move != nil && *effect.Move
		worktree, err := host.Desktop.CreateWorktree(ctx, contracts.CreateWorktreeRequest{ProjectRoot: effect.ProjectRoot, CarryChanges: !moving})
		if err == nil {
			if effect.Name != nil && *effect.Name != "" {
				worktree.Name = effect.Name
			}
			err = host.Dispatch(ctx, application.WorktreeEventWorktreeCreated{TaskID: effect.TaskID, Worktree: *worktree, Move: effect.Move, ProjectID: effect.ProjectID, Then: nonEmpty(effect.Then)})
		}
		if err != nil {
			return host.Dispatch(ctx, application.WorktreeEventWorktreeFailed{TaskID: effect.TaskID, Message: "Could not create the worktree: " + errorMessage(err)})
		}
		return nil
	}),
	"release-worktree": handle(func(ctx context.Context, effect application.WorkspaceEffectReleaseWorktree, host EffectHost) error {
		snapshot, err := host.Desktop.ReleaseWorktree(ctx, contracts.ReleaseWorktreeRequest{
			WorktreeID: effect.WorktreeID,
			Root:       effect.Root,
			TaskID:     &effect.TaskID,
			Title:      effect.Title,
			Release:    domain.WorktreeReleaseReturnedToLocal,
		})
		if err == nil {
			err = host.Dispatch(ctx, application.WorktreeEventWorktreeReleased{TaskID: effect.TaskID, Snapshot: *snapshot})
		}
		if err != nil {
			return host.Dispatch(ctx, application.WorktreeEventWorktreeReleaseFailed{TaskID: effect.TaskID, Message: errorMessage(err)})
		}
		return nil
	}),
	"list-worktrees": handle(func(ctx context.Context, _ application.WorkspaceEffectListWorktrees, host EffectHost) error {
		worktrees, err := host.Desktop.ListManagedWorktrees(ctx)
		if err == nil {
			err = host.Dispatch(ctx, application.WorktreeEventWorktreesLoaded{Worktrees: worktrees})
		}
		if err != nil {
			return host.Dispatch(ctx, application.WorktreeEventWorktreesFailed{Message: errorMessage(err)})
		}
		return nil
	}),
	"reveal-worktree": handle(func(ctx context.Context, effect application.WorkspaceEffectRevealWorktree, host EffectHost) error {
		if err := host.Desktop.RevealWorktree(ctx, effect.Root); err != nil {
			return host.Dispatch(ctx, application.WorktreeEventWorktreesFailed{Message: errorMessage(err)})
		}
		return nil
	}),
	"delete-worktree": handle(func(ctx context.Context, effect application.WorkspaceEffectDeleteWorktree, host EffectHost) error {
		snapshot, err := host.Desktop.ReleaseWorktree(ctx, contracts.ReleaseWorktreeRequest{
			WorktreeID:  effect.WorktreeID,
			Root:        effect.Root,
			TaskID:      nil,
			Title:       effect.Title,
			Release:     domain.WorktreeReleaseDeleted,
			MissingOnly: effect.MissingOnly,
		})
		if err == nil {
			err = host.Dispatch(ctx, application.WorktreeEventWorktreeDeleted{WorktreeID: effect.WorktreeID, Root: effect.Root, Snapshot: *snapshot, MissingOnly: effect.MissingOnly})
		}
		if err != nil {
			return host.Dispatch(ctx, application.WorktreeEventWorktreesFailed{Root: &effect.Root, Message: errorMessage(err)})
		}
		return nil
	}),
	"refresh-environment": handle(refreshEnvironment),
	// A checkout GitHub cannot be asked about keeps the answer it has; the next ask tries again.
	"read-pull-request": handle(func(ctx context.Context, effect application.WorkspaceEffectReadPullRequest, host EffectHost) error {
		answer, err := host.Desktop.PullRequest(ctx, effect.WorkspaceID)
		if err != nil {
			return nil
		}
		event := application.PullRequestEvent{WorkspaceID: effect.WorkspaceID, Branch: effect.Branch, Read: effect.Read, Answer: answer}
		if effect.Members != nil && *effect.Members {
			event.Members = new(true)
		}
		_ = host.Dispatch(ctx, event)
		return nil
	}),
	"read-diff": handle(func(ctx context.Context, effect application.WorkspaceEffectReadDiff, host EffectHost) error {
		result, err := host.Desktop.DiffSummary(ctx, effect.WorkspaceID, effect.Range, &effect.IgnoreWhitespace)
		if err == nil {
			err = host.Dispatch(ctx, application.DiffEventDiffLoaded{Owner: effect.Owner, WorkspaceID: effect.WorkspaceID, Range: effect.Range, Result: result})
		}
		if err != nil {
			return host.Dispatch(ctx, application.DiffEventDiffLoaded{
				Owner:       effect.Owner,
				WorkspaceID: effect.WorkspaceID,
				Range:       effect.Range,
				Result:      contracts.BranchesResultError{Message: errorMessage(err)},
			})
		}
		return nil
	}),
	"checkout-branch": handle(func(ctx context.Context, effect application.WorkspaceEffectCheckoutBranch, host EffectHost) error {
		var message *string
		err := func() error {
			if effect.Create != nil && *effect.Create {
				if err := host.Desktop.CreateBranch(ctx, effect.WorkspaceID, effect.Branch); err != nil {
					return err
				}
			}
			return host.Desktop.CheckoutBranch(ctx, effect.WorkspaceID, effect.Branch)
		}()
		if err != nil {
			message = new(errorMessage(err))
		}
		return host.Dispatch(ctx, application.WorktreeEventCheckoutFinished{Message: message})
	}),
}

var ProjectHost = HostModule{
	Effects: ProjectEffects,
	Subscribe: func(host SubscriptionHost, _ RunReports) func() {
		return host.Desktop.OnOpenProject(func(workspace domain.WorkspaceRecord) {
			host.send(application.ProjectEventProjectOpened{Workspace: workspace})
		})
	},
}
