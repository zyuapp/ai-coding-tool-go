package services

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/workspace"
)

// convert reads a value as another type with the same JSON shape: the workspace package's own
// records as the generated ones.
func convert[T any](value any) (T, error) {
	var converted T
	data, err := jsonx.Marshal(value)
	if err != nil {
		return converted, err
	}
	err = jsonx.Unmarshal(data, &converted)
	return converted, err
}

// Workspaces is the folders the app works in, their checkouts, and what Git and GitHub say about
// them, as the runtime's desktop and the computer reads ask.
type Workspaces struct {
	Service   *workspace.WorkspaceService
	Worktrees *workspace.WorktreeService
	Git       *workspace.Git
}

func (w *Workspaces) Resolve(_ context.Context, workspaceID string) (domain.WorkspaceResolution, error) {
	resolution, err := w.Service.Resolve(workspaceID)
	if err != nil {
		return nil, err
	}
	return convert[domain.WorkspaceResolution](resolution)
}

func (w *Workspaces) record(resolution workspace.WorkspaceResolution, err error) (*domain.WorkspaceRecord, error) {
	if err != nil {
		return nil, err
	}
	return &domain.WorkspaceRecord{ID: resolution.Workspace.ID, Kind: domain.WorkspaceKind(resolution.Workspace.Kind), Root: resolution.Workspace.Root}, nil
}

func (w *Workspaces) RegisterProject(_ context.Context, root string) (*domain.WorkspaceRecord, error) {
	return w.record(w.Service.RegisterProject(root))
}

func (w *Workspaces) Projectless(context.Context) (*domain.WorkspaceRecord, error) {
	return w.record(w.Service.GetProjectless())
}

func (w *Workspaces) ProjectFolder(_ context.Context, root string, worktreesRoots []string) (string, error) {
	return workspace.ProjectFolder(root, worktreesRoots)
}

func (w *Workspaces) ChangedFiles(ctx context.Context, workspaceID string) (contracts.ChangedFilesResult, error) {
	return convert[contracts.ChangedFilesResult](w.Git.ChangedFiles(ctx, workspaceID, w.Service))
}

func localRange(diffRange domain.DiffRange) (workspace.DiffRange, error) {
	return convert[workspace.DiffRange](diffRange)
}

func (w *Workspaces) DiffSummary(ctx context.Context, workspaceID string, diffRange domain.DiffRange, ignoreWhitespace bool) (contracts.DiffSummaryResult, error) {
	local, err := localRange(diffRange)
	if err != nil {
		return nil, err
	}
	return convert[contracts.DiffSummaryResult](w.Git.DiffSummary(ctx, workspaceID, local, w.Service, ignoreWhitespace))
}

func (w *Workspaces) DiffPatch(ctx context.Context, workspaceID string, diffRange domain.DiffRange, path string, previousPath *string, ignoreWhitespace bool) (contracts.DiffPatchResult, error) {
	local, err := localRange(diffRange)
	if err != nil {
		return nil, err
	}
	return convert[contracts.DiffPatchResult](w.Git.DiffPatch(ctx, workspaceID, local, path, w.Service, previousPath, ignoreWhitespace))
}

func (w *Workspaces) PullRequestFor(ctx context.Context, root string) (domain.PullRequestAnswer, error) {
	return convert[domain.PullRequestAnswer](w.Git.PullRequestFor(ctx, root))
}

func (w *Workspaces) CheckoutBranch(ctx context.Context, root, branch string) error {
	return w.Git.CheckoutBranch(ctx, root, branch)
}

func (w *Workspaces) CreateBranch(ctx context.Context, root, branch string) error {
	return w.Git.CreateBranch(ctx, root, branch)
}

func (w *Workspaces) ListBranches(ctx context.Context, root string) (*contracts.BranchesResultAvailable, error) {
	branches, err := w.Git.ListBranches(ctx, root)
	if err != nil {
		return nil, err
	}
	return &contracts.BranchesResultAvailable{Branches: branches.Branches, Remotes: branches.Remotes, Current: branches.Current}, nil
}

func (w *Workspaces) KnownCommits(ctx context.Context, root string, hashes []string) ([]string, error) {
	return w.Git.KnownCommits(ctx, root, hashes)
}

func (w *Workspaces) Create(ctx context.Context, request contracts.CreateWorktreeRequest) (*contracts.CreatedWorktree, error) {
	local, err := convert[workspace.CreateWorktreeRequest](request)
	if err != nil {
		return nil, err
	}
	created, err := w.Worktrees.Create(ctx, local)
	if err != nil {
		return nil, err
	}
	return convert[*contracts.CreatedWorktree](created)
}

func (w *Workspaces) List(ctx context.Context) ([]*domain.ManagedWorktree, error) {
	listed, err := w.Worktrees.List(ctx)
	if err != nil {
		return nil, err
	}
	return convert[[]*domain.ManagedWorktree](listed)
}

func (w *Workspaces) Release(ctx context.Context, request contracts.ReleaseWorktreeRequest) (*contracts.WorktreeSnapshotResult, error) {
	local, err := convert[workspace.ReleaseWorktreeRequest](request)
	if err != nil {
		return nil, err
	}
	snapshot, err := w.Worktrees.Release(ctx, local)
	if err != nil {
		return nil, err
	}
	return convert[*contracts.WorktreeSnapshotResult](snapshot)
}

// OwnedPath is the app-owned checkout root names, refusing anything else.
func (w *Workspaces) OwnedPath(root string) (string, error) { return w.Worktrees.OwnedPath(root) }
