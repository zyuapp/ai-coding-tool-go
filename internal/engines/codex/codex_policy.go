// Package codex runs threads on the Codex app server: sessions, their policy and config, the
// shared and private Codex homes, account and plan reads, and the services the app asks of Codex
// outside a run. Ports src/main/codex (the wire client is in ./protocol).
package codex

import (
	"os"
	"path/filepath"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/workspace"
)

type CodexPolicy struct {
	ApprovalPolicy    protocol.AskForApproval
	Sandbox           protocol.SandboxMode
	ApprovalsReviewer protocol.ApprovalsReviewer
}

// Codex puts MCP tool calls to the reviewer and refuses escalations out of the sandbox without asking.
// Granular has no switch for network review, so a managed network proxy's misses still go to the reviewer.
var toolCallsOnly = protocol.AskForApproval{Granular: &protocol.GranularApproval{McpElicitations: true}}

// Codex has no plan mode, so a plan run is held to what a confirm run may do. A coordinator changes
// nothing itself: its shell is read-only, and every escalation out of that comes to the app, which
// refuses it. In Auto, Codex refuses those itself, so the reviewer that judges every other Auto
// run's tool calls judges the coordinator's app tools too, as Claude's auto mode does.
func Policy(policy domain.ExecutionPolicy, coordinationRole *domain.CoordinationRole) CodexPolicy {
	granted := policyGrants(policy)
	if coordinationRole == nil || *coordinationRole != domain.CoordinationRoleCoordinator {
		return granted
	}
	granted.Sandbox = protocol.SandboxReadOnly
	if policy == domain.ExecutionPolicyAutonomous {
		granted.ApprovalPolicy = toolCallsOnly
	} else {
		granted.ApprovalsReviewer = protocol.ApprovalsReviewerUser
	}
	return granted
}

func policyGrants(policy domain.ExecutionPolicy) CodexPolicy {
	switch policy {
	case domain.ExecutionPolicyAllowEdits:
		return CodexPolicy{protocol.AskForApproval{Mode: protocol.ApprovalOnRequest}, protocol.SandboxWorkspaceWrite, protocol.ApprovalsReviewerUser}
	case domain.ExecutionPolicyAutonomous:
		return CodexPolicy{protocol.AskForApproval{Mode: protocol.ApprovalOnRequest}, protocol.SandboxWorkspaceWrite, protocol.ApprovalsReviewerAutoReview}
	case domain.ExecutionPolicyBypass:
		return CodexPolicy{protocol.AskForApproval{Mode: protocol.ApprovalNever}, protocol.SandboxDangerFullAccess, protocol.ApprovalsReviewerUser}
	default:
		return CodexPolicy{protocol.AskForApproval{Mode: protocol.ApprovalUntrusted}, protocol.SandboxReadOnly, protocol.ApprovalsReviewerUser}
	}
}

// The temporary folders workspace-write lets every command write in beside its own checkout, each as
// written and as it really is.
type TemporaryFolders struct {
	SlashTmp []string
	Tmpdir   []string
}

// The sandbox a turn runs in. Workspace-write lets a command write in /tmp and $TMPDIR as well as its
// own checkout, so a run that must leave kept alone gives up whichever of them holds it. Nil
// temporary reads them from this machine.
func SandboxPolicy(sandbox protocol.SandboxMode, kept *string, temporary *TemporaryFolders) protocol.SandboxPolicy {
	switch sandbox {
	case protocol.SandboxReadOnly:
		return protocol.ReadOnlyPolicy{}
	case protocol.SandboxDangerFullAccess:
		return protocol.DangerFullAccessPolicy{}
	}
	policy := protocol.WorkspaceWritePolicy{WritableRoots: []string{}}
	if kept == nil || *kept == "" {
		return policy
	}
	folders := temporary
	if folders == nil {
		found := temporaryFolders(os.Getenv("TMPDIR"))
		folders = &found
	}
	holdsKept := func(candidates []string) bool {
		for _, folder := range candidates {
			if workspace.IsPathInside(folder, *kept) {
				return true
			}
		}
		return false
	}
	policy.ExcludeSlashTmp = holdsKept(folders.SlashTmp)
	policy.ExcludeTmpdirEnvVar = holdsKept(folders.Tmpdir)
	return policy
}

// A checkout's root is kept resolved, and /tmp is /private/tmp on macOS, so each folder is matched both ways.
func temporaryFolders(tmpdir string) TemporaryFolders {
	folders := TemporaryFolders{SlashTmp: bothWays("/tmp"), Tmpdir: []string{}}
	if tmpdir != "" {
		folders.Tmpdir = bothWays(tmpdir)
	}
	return folders
}

func bothWays(folder string) []string {
	resolved, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return []string{folder}
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return []string{folder}
	}
	return []string{folder, absolute}
}
