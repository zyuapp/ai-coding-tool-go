package codex

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

func policyShape(policy CodexPolicy) map[string]any {
	return map[string]any{"approvalPolicy": decodeAny(encodedOf(policy.ApprovalPolicy)), "sandbox": string(policy.Sandbox), "approvalsReviewer": string(policy.ApprovalsReviewer)}
}

func TestEveryExecutionPolicyMapsOntoCodexApprovalsSandboxAndReviewer(t *testing.T) {
	assertJSON(t, policyShape(Policy(domain.ExecutionPolicyConfirm, nil)), `{"approvalPolicy":"untrusted","sandbox":"read-only","approvalsReviewer":"user"}`)
	assertJSON(t, policyShape(Policy(domain.ExecutionPolicyPlan, nil)), policyShape(Policy(domain.ExecutionPolicyConfirm, nil)))
	assertJSON(t, policyShape(Policy(domain.ExecutionPolicyAllowEdits, nil)), `{"approvalPolicy":"on-request","sandbox":"workspace-write","approvalsReviewer":"user"}`)
	assertJSON(t, policyShape(Policy(domain.ExecutionPolicyAutonomous, nil)), `{"approvalPolicy":"on-request","sandbox":"workspace-write","approvalsReviewer":"auto_review"}`)
	assertJSON(t, policyShape(Policy(domain.ExecutionPolicyBypass, nil)), `{"approvalPolicy":"never","sandbox":"danger-full-access","approvalsReviewer":"user"}`)
	member := domain.CoordinationRoleMember
	assertJSON(t, policyShape(Policy(domain.ExecutionPolicyAutonomous, &member)), `{"approvalPolicy":"on-request","sandbox":"workspace-write","approvalsReviewer":"auto_review"}`, "in Auto, Codex's own reviewer decides a member's escalations")
	coordinator := domain.CoordinationRoleCoordinator
	assertJSON(t, policyShape(Policy(domain.ExecutionPolicyAutonomous, &coordinator)), `{"approvalPolicy":{"granular":{"sandbox_approval":false,"rules":false,"skill_approval":false,"request_permissions":false,"mcp_elicitations":true}},"sandbox":"read-only","approvalsReviewer":"auto_review"}`)
	assertJSON(t, policyShape(Policy(domain.ExecutionPolicyAllowEdits, &coordinator)), `{"approvalPolicy":"on-request","sandbox":"read-only","approvalsReviewer":"user"}`)
}

func TestARunKeptOutOfACheckoutGivesUpTheTemporaryFolderThatHoldsIt(t *testing.T) {
	temporary := &TemporaryFolders{SlashTmp: []string{"/tmp", "/private/tmp"}, Tmpdir: []string{"/var/folders/xy/T"}}
	open := `{"type":"workspaceWrite","writableRoots":[],"networkAccess":false,"excludeTmpdirEnvVar":false,"excludeSlashTmp":false}`
	assertJSON(t, SandboxPolicy(protocol.SandboxWorkspaceWrite, nil, temporary), open)
	assertJSON(t, SandboxPolicy(protocol.SandboxWorkspaceWrite, ptr("/home/me/repo"), temporary), open, "a checkout outside both is not writable to begin with")
	assertJSON(t, SandboxPolicy(protocol.SandboxWorkspaceWrite, ptr("/private/tmp/qa/repo"), temporary), `{"type":"workspaceWrite","writableRoots":[],"networkAccess":false,"excludeTmpdirEnvVar":false,"excludeSlashTmp":true}`)
	assertJSON(t, SandboxPolicy(protocol.SandboxWorkspaceWrite, ptr("/var/folders/xy/T/repo"), temporary), `{"type":"workspaceWrite","writableRoots":[],"networkAccess":false,"excludeTmpdirEnvVar":true,"excludeSlashTmp":false}`)
	assertJSON(t, SandboxPolicy(protocol.SandboxWorkspaceWrite, ptr("/tmpfiles/repo"), temporary), open, "a sibling that only shares the name's start is not inside")
	assertJSON(t, SandboxPolicy(protocol.SandboxReadOnly, ptr("/tmp/qa/repo"), temporary), `{"type":"readOnly","networkAccess":false}`)
	assertJSON(t, SandboxPolicy(protocol.SandboxDangerFullAccess, ptr("/tmp/qa/repo"), temporary), `{"type":"dangerFullAccess"}`)
	assertJSON(t, SandboxPolicy(protocol.SandboxWorkspaceWrite, ptr("/repo"), &TemporaryFolders{SlashTmp: []string{"/tmp"}}), open, "a member's sandbox never opens the project's git data")
}
