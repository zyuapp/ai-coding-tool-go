package application

import (
	"reflect"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"sync"
)

// Claim is the input types one reducer answers, as that reducer lists them.
type Claim struct {
	Types   []string
	Reducer func(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition
}

// feature is a reducer that changes only fields of one slice's state and reaches every other owner through that owner's edits.
func feature[In any](reducer func(state *WorkspaceState, input In) SliceTransition) func(state *WorkspaceState, input In) WorkspaceTransition {
	return Owned(reducer)
}

// claim is a feature answering types. In is the input type the reducer takes; every input of types must be one.
func claim[In any](types []string, reducer func(state *WorkspaceState, input In) SliceTransition) Claim {
	owned := Owned(reducer)
	return Claim{Types: types, Reducer: func(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition {
		return owned(state, any(input).(In))
	}}
}

// composition is a pass that fills in or reconciles every owner's state at once, which no feature is.
func composition(types []string, reducer func(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition) Claim {
	return Claim{Types: types, Reducer: reducer}
}

// unowned is a feature whose reducer still changes the whole state, until its changes are split into its owners' edits.
func unowned(types []string, reducer func(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition) Claim {
	return Claim{Types: types, Reducer: reducer}
}

// Claims is every claimed input type, each claimed by exactly one reducer, in the TypeScript order.
var Claims []Claim

var (
	remote    func(state *WorkspaceState, input RemoteInput) WorkspaceTransition
	engine    func(state *WorkspaceState, input EngineInput) WorkspaceTransition
	computers func(state *WorkspaceState, input ComputerInput) WorkspaceTransition
)

// reducers is Claims by input type.
var reducers map[string]func(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition

// stepClaim is claim for a reducer that answers as one owner's step.
func stepClaim(types []string, reducer func(state *WorkspaceState, input WorkspaceInput) Step) Claim {
	return claim(types, func(state *WorkspaceState, input WorkspaceInput) SliceTransition {
		return SliceTransition(reducer(state, input))
	})
}

func init() {
	Claims = []Claim{
		claim(WorktreeMenuInputs, ReduceWorktreeMenu),
		stepClaim(ThreadCommandInputs, ReduceThreadCommands),
		claim(CoordinationInputs, ReduceCoordination),
		claim(ProposalInputs, ReduceProposals),
		claim(WorktreeInputs, ReduceWorktrees),
		claim(WorktreeManagementInputs, ReduceWorktreeManagement),
		claim(AttachmentInputs, ReduceComposerAttachments),
		stepClaim(SendInputs, func(state *WorkspaceState, input WorkspaceInput) Step {
			return ReduceSending(state, input, CoordinationSend{}, SendSaid{})
		}),
		claim(ProjectAddInputs, ReduceProjectAdd),
		claim(ProjectInputs, ReduceProjectCommands),
		// The line itself moves in reconcileLimitPauses, which every input passes through.
		claim([]string{"limits.elapsed"}, func(*WorkspaceState, WorkspaceInput) SliceTransition { return SliceTransition{} }),
		// Compactions start in ReconcileIdleCompaction, which every input passes through.
		claim([]string{"idle-compaction.due"}, func(*WorkspaceState, WorkspaceInput) SliceTransition { return SliceTransition{} }),
		claim([]string{"limit.resume"}, func(state *WorkspaceState, input contracts.RunControlCommandLimitResume) SliceTransition {
			return SliceTransition(Resumed(state, input.TaskID))
		}),
		claim([]string{"limit.cancel"}, func(state *WorkspaceState, input contracts.RunControlCommandLimitCancel) SliceTransition {
			return SliceTransition(PauseCancelled(state, input.TaskID))
		}),
		claim([]string{"run.unresolved"}, func(state *WorkspaceState, input WorkspaceEventRunUnresolved) SliceTransition {
			return SliceTransition(ReduceRunUnresolved(state, input))
		}),
		unowned(RunInputs, ReduceRuns),
		stepClaim(RunControlInputs, ReduceRunControl),
		claim(AutomationInputs, ReduceAutomations),
		claim(SideChatInputs, ReduceSideChats),
		claim(PullRequestInputs, ReducePullRequests),
		claim(ComputerUseInputs, ReduceComputerUse),
		claim(PlanUsageInputs, ReducePlanUsage),
		claim(CliInputs, ReduceCli),
		claim(DiffInputs, ReduceDiffs),
		claim(StoreInputs, ReduceStore),
		composition(StoreLoadInputs, ReduceStoreLoad),
		claim(ComposerInputs, ReduceComposer),
		claim([]string{"view.dismiss-action-error"}, func(state *WorkspaceState, _ WorkspaceInput) SliceTransition { return ReduceErrorDismissed(state) }),
		claim([]string{"view.dismiss-hidden-tasks"}, func(state *WorkspaceState, _ WorkspaceInput) SliceTransition {
			return ReduceHiddenThreadsDismissed(state)
		}),
		claim([]string{"view.set-model-favorite"}, ReduceModelFavorite),
		claim([]string{"view.remember-project"}, ReduceRememberProject),
		claim([]string{"view.dismiss-toast", "view.hold-toasts"}, ReduceToasts),
		claim(AppUpdateInputs, ReduceAppUpdate),
		claim(SettingsInputs, ReduceSettings),
		stepClaim(DockInputs, ReduceDock),
		claim(BrowserInputs, ReduceBrowser),
		claim(BrowserImportInputs, ReduceBrowserImport),
		claim(SimulatorInputs, ReduceSimulator),
		claim(DictationInputs, ReduceDictation),
		claim(DesktopInputs, ReduceDesktop),
		claim(TerminalInputs, ReduceTerminal),
		claim(ViewInputs, ReduceView),
	}
	remote = feature(ReduceRemote)
	engine = feature(ReduceEngine)
	computers = feature(ReduceComputers)
	reducers = map[string]func(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition{}
	for _, claimed := range Claims {
		for _, inputType := range claimed.Types {
			if _, taken := reducers[inputType]; taken {
				panic("application: input " + inputType + " is claimed twice")
			}
			reducers[inputType] = claimed.Reducer
		}
	}
}

// Apply runs every input Reduce has not already unpacked through the reducer that claims it.
func Apply(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition {
	if remoteInput, ok := input.(RemoteInput); ok && IsRemoteInput(input) {
		return remote(state, remoteInput)
	}
	if computerInput, ok := input.(ComputerInput); ok && IsComputerInput(input) {
		return computers(state, computerInput)
	}
	if engineInput, ok := input.(EngineInput); ok && IsEngineInput(input) {
		return engine(state, engineInput)
	}
	reducer, ok := reducers[InputType(input)]
	if !ok {
		panic("application: no reducer claims input " + InputType(input))
	}
	return reducer(state, input)
}

var inputTypes sync.Map // reflect.Type -> string

// InputType is an input's TypeScript `type`: the literal its Type tag holds.
func InputType(input WorkspaceInput) string {
	member := reflect.TypeOf(input)
	if held, ok := inputTypes.Load(member); ok {
		return held.(string)
	}
	value := reflect.ValueOf(input)
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	field := value.FieldByName("Type")
	var tag interface{ Value() any }
	if field.IsValid() {
		tag, _ = field.Interface().(interface{ Value() any })
	}
	if tag == nil {
		panic("application: input " + member.String() + " has no Type tag")
	}
	name, _ := tag.Value().(string)
	inputTypes.Store(member, name)
	return name
}
