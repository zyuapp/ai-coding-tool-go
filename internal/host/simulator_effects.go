package host

import (
	"context"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// drivingMs is how long a run counts as driving a device after its last gesture.
const drivingMs = 4_000

// SimulatorEffects drive the simulator panel's devices: reading them, booting one, and the gestures sent to it.
var SimulatorEffects = EffectHandlers{
	"simulator.list": handle(func(ctx context.Context, _ application.WorkspaceEffectSimulatorList, host EffectHost) error {
		listed, err := host.Desktop.ListSimulators(ctx)
		if err == nil {
			err = host.Dispatch(ctx, application.SimulatorEventSimulatorListed{Availability: listed.Availability, Devices: listed.Devices})
		}
		if err != nil {
			return host.Dispatch(ctx, application.SimulatorEventSimulatorListed{
				Availability: domain.SimulatorAvailabilityUnsupported{Reason: errorMessage(err)},
				Devices:      []*domain.SimulatorDevice{},
			})
		}
		return nil
	}),
	"simulator.boot": handle(func(ctx context.Context, effect application.WorkspaceEffectSimulatorBoot, host EffectHost) error {
		err := host.Desktop.BootSimulator(ctx, effect.UDID)
		if err == nil {
			err = host.Dispatch(ctx, application.SimulatorEventSimulatorBooted{UDID: effect.UDID})
		}
		if err != nil {
			return host.Dispatch(ctx, application.SimulatorEventSimulatorFailed{UDID: effect.UDID, Message: errorMessage(err)})
		}
		return nil
	}),
	"simulator.release": handle(func(ctx context.Context, effect application.WorkspaceEffectSimulatorRelease, host EffectHost) error {
		_ = host.Desktop.ShutdownSimulator(ctx, effect.UDID)
		return host.Dispatch(ctx, contracts.SimulatorCommandSimulatorRefresh{})
	}),
	"simulator.input": handle(func(ctx context.Context, effect application.WorkspaceEffectSimulatorInput, host EffectHost) error {
		if seq := effect.Seq; seq != nil {
			defer time.AfterFunc(drivingMs*time.Millisecond, func() {
				_ = host.Dispatch(background, application.SimulatorEventSimulatorIdle{Seq: *seq})
			})
		}
		err := host.Desktop.SimulatorInput(ctx, effect.UDID, effect.Input)
		if rotate, ok := effect.Input.(domain.SimulatorInputRotate); err == nil && ok {
			err = host.Dispatch(ctx, application.SimulatorEventSimulatorTurned{UDID: effect.UDID, Orientation: rotate.Orientation})
		}
		if err != nil {
			return host.Dispatch(ctx, application.SimulatorEventSimulatorRefused{Message: errorMessage(err)})
		}
		return nil
	}),
}

var SimulatorHost = HostModule{
	Effects: SimulatorEffects,
	Subscribe: func(host SubscriptionHost, _ RunReports) func() {
		host.send(contracts.SimulatorCommandSimulatorRefresh{})
		return nil
	},
}
