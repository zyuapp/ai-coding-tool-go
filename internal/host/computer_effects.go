package host

import (
	"context"
	"errors"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// forward carries inputs to a paired computer. What the other computer refused comes back as this
// window's own error, since it is the one that asked. A draft a send carried is let go of only once
// that computer has taken it.
func forward(ctx context.Context, effect application.WorkspaceEffectComputerForward, host EffectHost) error {
	err := func() error {
		result, err := host.Desktop.SendToComputer(ctx, effect.ID, effect.Inputs)
		if err != nil {
			return err
		}
		refused, isRefusal := result.(contracts.WorkspaceCommandResultFalse)
		// That computer refusing a start says it started nothing, which an answer lost on the way cannot.
		if isRefusal && effect.Proposal != nil {
			return host.Dispatch(ctx, application.WorkspaceEventProposalSettled{TaskID: effect.Proposal.TaskID, ProposalID: effect.Proposal.ProposalID, Message: &refused.Message, Refused: new(true)})
		}
		if isRefusal {
			return errors.New(refused.Message)
		}
		taskID := resultTaskID(result)
		if effect.Proposal != nil {
			settled := application.WorkspaceEventProposalSettled{TaskID: effect.Proposal.TaskID, ProposalID: effect.Proposal.ProposalID}
			if taskID == nil {
				settled.Message = new("That computer did not say which thread it started.")
			} else {
				settled.ThreadID = taskID
			}
			return host.Dispatch(ctx, settled)
		}
		if taskID != nil {
			if err := host.Dispatch(ctx, application.ComputerEventComputersAnswered{TaskID: *taskID}); err != nil {
				return err
			}
		}
		if effect.ProjectAddRequest != nil {
			if err := host.Dispatch(ctx, application.ProjectAddEventProjectAddFinished{Request: effect.ProjectAddRequest}); err != nil {
				return err
			}
		}
		if effect.Draft != nil {
			return host.Dispatch(ctx, application.ComputerEventComputersForwarded{Draft: *effect.Draft})
		}
		return nil
	}()
	if err == nil {
		return nil
	}
	// Whether anyone here is looking is told anew when the thread is next selected, and a computer's
	// limits and its threads' pull requests are asked anew on the next refresh, so a line that dropped
	// any of them is no error, and neither is housekeeping retried once the line is back.
	if effect.Silent != nil && *effect.Silent {
		return nil
	}
	housekeeping := true
	for _, input := range effect.Inputs {
		switch application.InputType(input) {
		case "view.set-focused", "usage.read", "pull-request.read-member":
		default:
			housekeeping = false
		}
	}
	if housekeeping {
		return nil
	}
	message := errorMessage(err)
	if effect.Proposal != nil {
		return host.Dispatch(ctx, application.WorkspaceEventProposalSettled{TaskID: effect.Proposal.TaskID, ProposalID: effect.Proposal.ProposalID, Message: &message})
	}
	if effect.ProjectAddRequest != nil {
		return host.Dispatch(ctx, application.ProjectAddEventProjectAddFinished{Request: effect.ProjectAddRequest, Error: &message})
	}
	if effect.Draft == nil || effect.Draft.Attachments == nil {
		event := contracts.ActionFailedEvent{Message: message}
		// A project is removed from Settings, so that computer's refusal is shown on the Projects page.
		for _, input := range effect.Inputs {
			if application.InputType(input) == "project.remove" {
				event.Page = new(domain.SettingsSectionProjects)
				break
			}
		}
		return host.Dispatch(ctx, event)
	}
	failed := application.AttachmentEventAttachmentsFailed{Message: message}
	if composer := effect.Draft.Attachments.Key; composer != "" {
		failed.TaskID = &composer
	}
	return host.Dispatch(ctx, failed)
}

// resultTaskID is the thread an accepted result names, or nil.
func resultTaskID(result contracts.WorkspaceCommandResult) *string {
	if accepted, ok := result.(contracts.WorkspaceCommandResultTrue); ok {
		return accepted.TaskID
	}
	return nil
}

// ComputerEffects reach other computers running this app: finding them, pairing, and what is carried to one of them.
var ComputerEffects = EffectHandlers{
	"computer.discover": handle(func(ctx context.Context, _ application.WorkspaceEffectComputerDiscover, host EffectHost) error {
		found, err := host.Desktop.DiscoverComputers(ctx)
		if err == nil {
			err = host.Dispatch(ctx, application.ComputerEventComputersFound{Found: found})
		}
		if err != nil {
			return host.Dispatch(ctx, application.ComputerEventComputersSearchFailed{Message: errorMessage(err)})
		}
		return nil
	}),
	"computer.pair": handle(func(ctx context.Context, effect application.WorkspaceEffectComputerPair, host EffectHost) error {
		if err := host.Desktop.PairComputer(ctx, effect.Host, effect.Name, effect.Code); err != nil {
			return host.Dispatch(ctx, application.ComputerEventComputersPairFailed{Message: errorMessage(err)})
		}
		return nil
	}),
	"computer.forget": reporting(func(ctx context.Context, effect application.WorkspaceEffectComputerForget, desktop RuntimeDesktop) error {
		return desktop.ForgetComputer(ctx, effect.ID)
	}),
	"computer.reconnect": reporting(func(ctx context.Context, effect application.WorkspaceEffectComputerReconnect, desktop RuntimeDesktop) error {
		return desktop.ReconnectComputer(ctx, effect.ID)
	}),
	"computer.rename": reporting(func(ctx context.Context, effect application.WorkspaceEffectComputerRename, desktop RuntimeDesktop) error {
		return desktop.RenameComputer(ctx, effect.Name)
	}),
	"computer.label": reporting(func(ctx context.Context, effect application.WorkspaceEffectComputerLabel, desktop RuntimeDesktop) error {
		return desktop.LabelComputer(ctx, effect.ID, effect.Name)
	}),
	"computer.forward": handle(forward),
}

var ComputerHost = HostModule{
	Effects: ComputerEffects,
	Subscribe: func(host SubscriptionHost, _ RunReports) func() {
		desktop := host.Desktop
		stops := []func(){
			desktop.OnComputersChanged(func(name string, links []*domain.ComputerLink) {
				host.send(application.ComputerEventComputersChanged{Name: name, Links: links})
			}),
			desktop.OnComputerState(func(id string, state application.WorkspaceState) {
				host.send(application.ComputerEventComputerState{ID: id, State: state})
			}),
			desktop.OnComputerNotice(func(id string, notice contracts.ThreadNotice) {
				host.send(application.ComputerEventComputerNotice{ID: id, Notice: notice})
			}),
		}
		return func() {
			for _, stop := range stops {
				stop()
			}
		}
	},
}
