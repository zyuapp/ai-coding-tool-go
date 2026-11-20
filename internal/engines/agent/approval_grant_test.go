package agent

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestPolicyAndChannelDecideWhatRunsWithoutApproval(t *testing.T) {
	for _, reach := range []ToolReach{ReachApp, ReachComputerUse, ReachWorkspace} {
		equal(t, GrantsTool(reach, nil), reach == ReachApp, "no run", string(reach))
	}
	for _, policy := range []domain.ExecutionPolicy{"confirm", "plan", "allow-edits", "autonomous", "bypass"} {
		for _, channel := range []contracts.RunChannel{"main", "side"} {
			scope := &ApprovalScope{Policy: policy, Channel: channel}
			equal(t, GrantsTool(ReachApp, scope), true)
			equal(t, GrantsTool(ReachComputerUse, scope), policy == "bypass" || (policy == "autonomous" && channel == "main"), string(policy), string(channel))
			equal(t, GrantsTool(ReachWorkspace, scope), policy == "bypass", string(policy), string(channel))
		}
	}
}

func TestToolReachReadsServerGrants(t *testing.T) {
	held := ToolServers()
	defer SetToolServers(held)
	SetToolServers([]ToolServer{
		{Name: AutomationServerName, Grants: GrantsAll, Definitions: []ToolDefinitionInfo{{Name: "schedule"}}},
		{Name: BrowserServerName, Grants: GrantsReadOnly, Definitions: []ToolDefinitionInfo{{Name: "browser_read", ReadOnly: true}, {Name: "browser_open"}}},
		{Name: ThreadServerName, Grants: GrantsNone, Definitions: []ToolDefinitionInfo{{Name: "read_thread", ReadOnly: true}}},
	})
	equal(t, ToolReachOf("mcp__aicodingtool-automation__schedule"), ReachApp)
	equal(t, ToolReachOf("mcp__aicodingtool-browser__browser_read"), ReachApp)
	equal(t, ToolReachOf("mcp__aicodingtool-browser__browser_open"), ReachWorkspace)
	equal(t, ToolReachOf("mcp__aicodingtool-threads__read_thread"), ReachWorkspace)
	equal(t, ToolReachOf("mcp__cua-driver__click"), ReachComputerUse)
	equal(t, ToolReachOf("Bash"), ReachWorkspace)
}

func TestSideChatWithholdsSchedulingAndCheckoutMoves(t *testing.T) {
	equal(t, WithheldTools(contracts.RunChannelMain), []string{"AskUserQuestion", "EnterWorktree", "ExitWorktree"})
	equal(t, WithheldTools(contracts.RunChannelSide)[3:], []string{
		"mcp__aicodingtool-automation__schedule",
		"mcp__aicodingtool-automation__update",
		"mcp__aicodingtool-automation__stop",
		"mcp__aicodingtool-automation__notify",
		"mcp__aicodingtool-automation__nothing_to_report",
		"mcp__aicodingtool-threads__move_to_worktree",
	})
	tools := []BoundTool{{Name: "schedule"}, {Name: "status"}}
	equal(t, len(OfferedOn(contracts.RunChannelSide, AutomationServerName, tools)), 1)
	equal(t, len(OfferedOn(contracts.RunChannelMain, AutomationServerName, tools)), 2)
}

func TestRunBriefGatesOnOfferedToolsAndChannel(t *testing.T) {
	held := ToolServers()
	defer SetToolServers(held)
	SetToolServers([]ToolServer{{
		Name: ThreadServerName,
		Serve: func(run *ProviderRunInput) ([]BoundTool, bool) {
			return []BoundTool{{Name: "read_thread"}, {Name: "move_to_worktree"}}, run.Threads != nil
		},
		Brief: func(offers Offers, _ BriefDialect, _ BriefRun) []string {
			if offers("move_to_worktree") {
				return []string{"threads+move"}
			}
			if offers("") {
				return []string{"threads"}
			}
			return nil
		},
	}})
	none := RunBriefFor(testInput(nil), BriefDialect{})
	equal(t, none.Instructions, []string{linkInstructions})
	main := RunBriefFor(testInput(func(input *ProviderRunInput) { input.Threads = scopedThreads{} }), BriefDialect{})
	equal(t, main.Instructions, []string{linkInstructions, "threads+move", workflowInstructions})
	side := RunBriefFor(testInput(func(input *ProviderRunInput) {
		input.Threads = scopedThreads{}
		input.Channel = contracts.RunChannelSide
		input.ComputerUse = domain.ComputerUseRunConfigSetupRequired{}
	}), BriefDialect{})
	equal(t, side.Instructions, []string{computerUseInstructions, linkInstructions, "threads", workflowInstructions, SideChatInstructions})
	equal(t, IdentityKey(main.Identity) == IdentityKey(side.Identity), false)
	equal(t, IdentityKey(main.Identity), IdentityKey(RunBriefFor(testInput(func(input *ProviderRunInput) { input.Threads = scopedThreads{} }), BriefDialect{}).Identity))
}
