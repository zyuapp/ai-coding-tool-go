package host

import (
	"context"
	"fmt"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func message(err error) string { return err.Error() }

// step names what was being attempted on the way out, since Git says what went wrong but not that.
func step[T any](attempt string, work func() (T, error)) (T, error) {
	value, err := work()
	if err != nil {
		var zero T
		return zero, fmt.Errorf("Could not %s: %s", attempt, message(err))
	}
	return value, nil
}

// ResolveRunWorkspace turns a resolution request into the event that answers it. A workspace the
// reducer already named is handed straight back, so nothing here ever decides what kind of checkout
// a run happens in.
func ResolveRunWorkspace(ctx context.Context, effect application.WorkspaceEffectResolveRunWorkspace, desktop ResolveDesktop) application.WorkspaceEvent {
	pendingID := effect.PendingID
	event, err := func() (application.WorkspaceEvent, error) {
		// A branch named but not yet in the repository has to exist before anything starts from it.
		if creating := effect.CreateBranch; creating != nil {
			if _, err := step("create the branch "+creating.Branch, func() (struct{}, error) {
				return struct{}{}, desktop.CreateBranch(ctx, creating.WorkspaceID, creating.Branch)
			}); err != nil {
				return nil, err
			}
		}
		// A branch a thread starts from moves the project checkout, so it happens before the run.
		if checkout := effect.Checkout; checkout != nil {
			if _, err := step("check out "+checkout.Branch, func() (struct{}, error) {
				return struct{}{}, desktop.CheckoutBranch(ctx, checkout.WorkspaceID, checkout.Branch)
			}); err != nil {
				return nil, err
			}
		}
		if making := effect.CreateWorktree; making != nil {
			worktree, err := step("create the worktree", func() (*contracts.CreatedWorktree, error) {
				return desktop.CreateWorktree(ctx, contracts.CreateWorktreeRequest(*making))
			})
			if err != nil {
				return nil, err
			}
			return application.WorkspaceEventRunResolved{
				PendingID: pendingID,
				Workspace: domain.WorkspaceRecord{ID: worktree.WorkspaceID, Kind: domain.WorkspaceKindWorktree, Root: worktree.Root},
				Worktree:  worktree,
			}, nil
		}
		if effect.Workspace != nil {
			return application.WorkspaceEventRunResolved{PendingID: pendingID, Workspace: *effect.Workspace}, nil
		}
		if !effect.Picker {
			workspace, err := desktop.ProjectlessWorkspace(ctx)
			if err != nil {
				return nil, err
			}
			return application.WorkspaceEventRunResolved{PendingID: pendingID, Workspace: *workspace}, nil
		}
		selected, err := desktop.OpenFolder(ctx)
		if err != nil {
			return nil, err
		}
		if selected == nil {
			return application.WorkspaceEventRunUnresolved{PendingID: pendingID, Message: application.WorkspaceErrors["reopenProject"]}, nil
		}
		if effect.Root == nil || selected.Root != *effect.Root {
			return application.WorkspaceEventRunUnresolved{PendingID: pendingID, Message: application.WorkspaceErrors["sameProject"]}, nil
		}
		return application.WorkspaceEventRunResolved{PendingID: pendingID, Workspace: *selected}, nil
	}()
	if err != nil {
		return application.WorkspaceEventRunUnresolved{PendingID: pendingID, Message: message(err)}
	}
	return event
}
