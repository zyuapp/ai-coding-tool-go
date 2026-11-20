package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

func TestEveryInputTypeIsClaimedOnce(t *testing.T) {
	owners := map[string]int{}
	for index, claimed := range Claims {
		for _, inputType := range claimed.Types {
			if _, taken := owners[inputType]; taken {
				t.Fatalf("%s is claimed twice", inputType)
			}
			owners[inputType] = index
		}
	}
}

func TestInputReachesTheReducerThatClaimsIt(t *testing.T) {
	corePorted(t, func() {
		state := EmptyWorkspaceState(nil)
		coreEqualJSON(t, Apply(state, contracts.CliCommandCliRead{}), Owned(ReduceCli)(state, contracts.CliCommandCliRead{}), "cli.read")
		closing := contracts.SideChatCommandSideChatClose{ChatID: "c1"}
		coreEqualJSON(t, Apply(state, closing), Owned(ReduceSideChats)(state, closing), "side-chat.close")
	})
}

func TestLiftedLimitLeavesTheStateToTheReconcilers(t *testing.T) {
	state := EmptyWorkspaceState(nil)
	transition := Apply(state, WorkspaceEventLimitsElapsed{At: 1})
	if !sameState(&transition.State, state) || len(transition.Effects) != 0 {
		t.Fatal("limits.elapsed changed the state")
	}
}
