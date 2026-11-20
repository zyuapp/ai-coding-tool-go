package application

import (
	"errors"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The rules a new thread starts by, whoever asks for it: what it runs on, whom it works under, and
// where it starts. One function holds them, so a thread a run starts and one started any other way
// for a coordinator cannot drift apart.

// A thread under a coordinator on another computer cannot ask that coordinator's user to allow a start, so it starts threads only here.
const RemoteLedHere = "This thread works under a coordinator on another computer, so the threads it starts run on this computer."

// A coordinator does no work itself, so a thread starts one only in its project's checkout.
const CoordinatorWorktreeStart = "A coordinator stays in its project's checkout, since it changes nothing itself. Start it without worktree or worktreeId; the threads it starts take worktree: true."

const BriefRequired = "A coordinator hands every thread it starts a brief: pass intent (the user's own words), doneWhen, and delivers."

// What a thread `caller` asks for starts on. A thread that will work under a coordinator, because the
// coordinator asked or one of its threads did, runs on what the coordinator's threads run on and never
// on a model they may not; any other inherits the caller's own model and effort. A thread under a
// coordinator on another computer starts its threads under that coordinator too, on its own model,
// since what the coordinator's threads run on is that computer's to know. `asked` is the computer the
// request named: null for this one, absent when it named none.
func ThreadStartsNewThreadSend(state *WorkspaceState, caller *domain.Thread, command NewThreadSend, asked jsonx.Opt[*PairedComputer]) NewThreadStart {
	refused := func(message string) NewThreadStart { return NewThreadStart{Error: &message} }
	lead := caller
	if !domain.IsCoordinator(caller) {
		lead = domain.CoordinatorOf(state.Threads, caller)
	}
	var remote *domain.HeldLead
	if lead == nil {
		remote = domain.RemoteLeadOf(caller)
	}
	catalog := CatalogOf(state)
	var inherited *domain.ThreadModelChoice
	inheritedModel := false
	if lead != nil {
		inherited = domain.ThreadModelOf(&catalog, lead)
		inheritedModel = inherited != nil
	} else {
		engine := caller.Engine
		inherited = &domain.ThreadModelChoice{Engine: &engine, Effort: caller.Effort}
		inherited.Model, inheritedModel = domain.ThreadModel(&catalog, caller)
	}
	// An engine named for a model, or named with none, means its default one, which an engine that lists no models has none of; any other name means the model it reads as.
	var asEngine *domain.AgentEngine
	if command.Model == nil {
		asEngine = command.Engine
	} else if domain.IsAgentEngine(*command.Model) {
		engine := domain.AgentEngine(*command.Model)
		asEngine = &engine
	}
	var engine domain.AgentEngine
	var model domain.AgentModel
	hasModel := false
	switch {
	case asEngine != nil && *asEngine != "":
		if defaulted, ok := domain.DefaultModelIn(&catalog, *asEngine); ok {
			engine, model, hasModel = *asEngine, defaulted, true
		}
	case command.Model != nil && asEngine == nil:
		named := domain.AgentEngine("")
		if command.Engine != nil {
			named = *command.Engine
		}
		resolved, err := domain.ResolveModel(&catalog, *command.Model, state.FavoriteModels, named)
		if err != nil {
			return refused(err.Error())
		}
		engine, model, hasModel = resolved.Engine, resolved.Model, true
	case inherited != nil:
		if inherited.Engine != nil {
			engine = *inherited.Engine
		}
		model, hasModel = inherited.Model, inheritedModel
	}
	if !hasModel {
		if asEngine != nil && *asEngine != "" {
			return refused(domain.EngineLabel(string(*asEngine)) + " has no models to start a thread on. Connect a provider first.")
		}
		owner := caller
		if lead != nil {
			owner = lead
		}
		return refused("Pass a model: " + domain.EngineLabel(string(owner.Engine)) + " has yet to name the one this thread runs on.")
	}
	if engine == "" || !domain.EngineHasModel(&catalog, engine, model) {
		return refused(domain.UnknownModel(model, ""))
	}
	// A thread started as a coordinator takes only the efforts a coordinator does.
	var role domain.CoordinationRole
	if command.Role != nil && *command.Role == domain.ThreadRoleCoordinator {
		role = domain.CoordinationRoleCoordinator
	}
	if role != "" && (command.Worktree != nil && *command.Worktree || command.WorktreeID != nil) {
		return refused(CoordinatorWorktreeStart)
	}
	if command.Effort != nil && *command.Effort != "" && !domain.ModelHasEffort(&catalog, engine, model, *command.Effort, role) {
		suffix := ""
		if role != "" {
			suffix = " for a coordinator"
		}
		return refused("The " + model + " model does not support " + string(*command.Effort) + " effort" + suffix + ".")
	}
	// An inherited effort the new model does not take drops to the nearest one it does.
	var effort domain.AgentEffort
	if command.Effort != nil {
		effort = *command.Effort
	} else {
		wanted := domain.DefaultEffortFor(string(engine))
		if inherited != nil && inherited.Effort != nil {
			wanted = *inherited.Effort
		}
		effort = domain.EffortForModel(&catalog, engine, model, wanted, role)
	}
	// A brief is what a coordinator hands the threads it starts, so one any other thread passes is dropped.
	chosen := command
	chosen.Brief = nil
	if lead == caller && command.Brief != nil {
		chosen.Brief = command.Brief
	}
	chosen.Engine, chosen.Model, chosen.Effort = &engine, &model, nil
	if domain.ModelTakesEffort(&catalog, engine, model) {
		chosen.Effort = &effort
	}
	// A coordinator's new thread starts from the brief it was handed.
	if lead == caller && chosen.Brief == nil {
		return refused(BriefRequired)
	}

	// A new thread asked for on a paired computer, or named into a checkout or project there, is that computer's to start.
	placedOn, _ := ComputerOfPlace(state, chosen.Project, chosen.WorktreeID)
	if placedOn != nil && !asked.IsAbsent() {
		if wanted, ok := asked.Get(); !ok || wanted == nil || wanted.ID != placedOn.ID {
			return refused("That project or worktree is on " + placedOn.Name + ". Pass computer \"" + placedOn.Name + "\" to start the thread there.")
		}
	}
	elsewhere := placedOn
	if wanted, ok := asked.Get(); ok && wanted != nil {
		elsewhere = wanted
	}
	if elsewhere != nil && remote != nil {
		return refused(RemoteLedHere)
	}
	if elsewhere != nil {
		return NewThreadStart{Elsewhere: elsewhere, Command: &chosen, Lead: lead}
	}

	// With no place named it starts where the caller lives: its project, and its worktree when it has one, unless it is a coordinator.
	if chosen.Project == nil && caller.ProjectID != nil && *caller.ProjectID != "" {
		project := *caller.ProjectID
		chosen.Project = &project
	}
	if role == "" && chosen.Worktree == nil && chosen.WorktreeID == nil && caller.WorktreeID != nil && *caller.WorktreeID != "" {
		worktreeID := *caller.WorktreeID
		chosen.WorktreeID = &worktreeID
	}
	// It works under the lead, and its first message names the thread that sent it. The suggestion the caller was is the caller's alone.
	if lead != nil {
		leadID := lead.ID
		chosen.CoordinatorID = &leadID
	} else if remote != nil {
		chosen.Lead = &domain.RemoteLead{RemoteSender: domain.RemoteSender{ThreadID: remote.ThreadID, Title: remote.Title, Computer: remote.Computer}}
	}
	from := caller.ID
	chosen.From = &from
	return NewThreadStart{Command: &chosen}
}

// A thread `caller` asks its coordinator's user for, checked now by the rules its start will be, and
// placed in the project and checkout that start resolves to, or why it cannot be. A coordinator
// suggests threads here or on `asked`, a paired computer; a thread under it asks only for one on a
// paired computer, which the user allows there. Only a model and effort that were named are kept, so
// starting it later runs on what the coordinator's threads run on by then. One asked for without a
// title (an empty Title) is labelled by its prompt and starts unnamed, so its name is written as any
// other start's is. The error's text is what the caller is told.
func ThreadProposal(state *WorkspaceState, caller *domain.Thread, request domain.ProposalRequest, at float64, asked jsonx.Opt[*PairedComputer]) (*domain.ThreadProposal, error) {
	prompt := request.Prompt
	start := ThreadStartsNewThreadSend(state, caller, NewThreadSend{
		Text: &prompt, Brief: request.Brief, Project: request.Project, Worktree: request.Worktree, WorktreeID: request.WorktreeID,
		Engine: request.Engine, Model: request.Model, Effort: request.Effort, Role: request.Role,
	}, asked)
	if start.Error != nil {
		return nil, errors.New(*start.Error)
	}
	place, brief, from, err := proposalPlace(state, caller, start)
	if err != nil {
		return nil, err
	}
	proposal := &domain.ThreadProposal{ID: NewID(), Prompt: prompt, Place: place, Brief: brief, From: from, ProposedAt: at}
	if title := threadsTrim(request.Title); title != "" {
		named := true
		proposal.Title, proposal.Named = title, &named
	} else {
		proposal.Title = domain.ClampTitle(prompt)
	}
	// A model named by its name is kept as the model and engine it meant when suggested; an engine stays an engine, for its default when started.
	switch {
	case request.Model != nil && *request.Model != "" && !domain.IsAgentEngine(*request.Model) && start.Command.Engine != nil && *start.Command.Engine != "" && start.Command.Model != nil && *start.Command.Model != "":
		engine, model := string(*start.Command.Engine), *start.Command.Model
		proposal.Engine, proposal.Model = &engine, &model
	case request.Model != nil && *request.Model != "":
		proposal.Model = request.Model
	case request.Engine != nil && *request.Engine != "":
		engine := string(*request.Engine)
		proposal.Engine = &engine
	}
	if request.Effort != nil && *request.Effort != "" {
		proposal.Effort = request.Effort
	}
	if request.Role != nil && *request.Role != "" {
		proposal.Role = request.Role
	}
	return proposal, nil
}

// Where a suggestion starts, the brief it carries, and, for one a thread under the coordinator asked
// for on another computer, which thread that was. A place here is checked as the send will check it.
// `start` is never a refusal.
func proposalPlace(state *WorkspaceState, caller *domain.Thread, start NewThreadStart) (place domain.ProposalPlace, brief *domain.ThreadBrief, from *string, err error) {
	command := start.Command
	brief = command.Brief
	if start.Elsewhere != nil {
		if start.Lead == nil {
			return place, nil, nil, errors.New("Only a coordinator and the threads under it ask for threads on another computer.")
		}
		place, err = elsewherePlace(state, caller, start.Elsewhere, command)
		if err != nil {
			return domain.ProposalPlace{}, nil, nil, err
		}
		if caller.ID != start.Lead.ID {
			id := caller.ID
			from = &id
		}
		return place, brief, from, nil
	}
	if command.Brief == nil {
		return place, nil, nil, errors.New(BriefRequired)
	}
	project, worktree, err := NamedPlace(state, command.Project, command.WorktreeID)
	if err != nil {
		return place, nil, nil, err
	}
	switch {
	case project != nil && project.ID != "":
		id := project.ID
		place.Project = &id
	case project == nil && worktree != nil && worktree.ProjectID != "":
		id := worktree.ProjectID
		place.Project = &id
	}
	// Whether it gets a checkout of its own is said either way, so starting it never moves into one the coordinator entered since.
	if worktree != nil {
		id := worktree.ID
		place.WorktreeID = &id
	} else {
		own := command.Worktree != nil && *command.Worktree
		place.Worktree = &own
	}
	return place, brief, nil, nil
}

// Where on a paired computer a thread asked for there starts: the project there its start names, or
// the one matching the asking thread's own, and whether it gets a worktree of its own there. A
// checkout that computer already has is its own to name, so asking for one is refused.
func elsewherePlace(state *WorkspaceState, caller *domain.Thread, computer *PairedComputer, command *NewThreadSend) (domain.ProposalPlace, error) {
	if command.WorktreeID != nil {
		return domain.ProposalPlace{}, errors.New("A worktree already on " + computer.Name + " cannot be named. Pass worktree: true for a new one there.")
	}
	found, err := ProjectOnComputer(computer, command.Project, ProjectFor(state, caller))
	if err != nil {
		return domain.ProposalPlace{}, err
	}
	project := found.ID
	own := command.Worktree != nil && *command.Worktree
	return domain.ProposalPlace{Computer: &domain.ProposalPlaceComputer{ID: computer.ID, Name: computer.Name}, Project: &project, Worktree: &own}, nil
}

// The send a suggestion asks for, as the lead would have started it, in the place it was suggested
// for, and named what the suggestion was called when that was given rather than read from its prompt.
// A refusal for one on an engine this build does not know.
func ProposalSend(proposal *domain.ThreadProposal) (*NewThreadSend, error) {
	place, engine := proposal.Place, proposal.Engine
	if engine != nil && !domain.IsAgentEngine(*engine) {
		return nil, errors.New("This version can't start threads on " + *engine + ". Update to start it here.")
	}
	prompt := proposal.Prompt
	send := &NewThreadSend{Text: &prompt, Brief: proposal.Brief, Project: place.Project}
	if proposal.Named != nil && *proposal.Named {
		title := proposal.Title
		send.Title = &title
	}
	if place.WorktreeID != nil && *place.WorktreeID != "" {
		send.WorktreeID = place.WorktreeID
	} else if place.Worktree != nil {
		send.Worktree = place.Worktree
	}
	if proposal.Model != nil && *proposal.Model != "" {
		send.Model = proposal.Model
	}
	if engine != nil && *engine != "" {
		agentEngine := domain.AgentEngine(*engine)
		send.Engine = &agentEngine
	}
	if proposal.Effort != nil && *proposal.Effort != "" {
		send.Effort = proposal.Effort
	}
	if proposal.Role != nil && *proposal.Role != "" {
		send.Role = proposal.Role
	}
	return send, nil
}
