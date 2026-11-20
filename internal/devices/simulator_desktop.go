package devices

import (
	"context"
	"errors"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// SimulatorDesktop is the simulator panel's devices, driven from this process.
func SimulatorDesktop(host *SimulatorHost) contracts.SimulatorPortRuntime { return host }

// needsDesktop is the refusal for what only the desktop app can do, on a host that draws nothing.
func needsDesktop(what string) error { return errors.New(what + " needs the desktop app.") }

// HeadlessSimulatorDesktop is the simulator on a host without a window.
type HeadlessSimulatorDesktop struct{}

var _ contracts.SimulatorPortRuntime = HeadlessSimulatorDesktop{}

func (HeadlessSimulatorDesktop) ListSimulators(context.Context) (*contracts.DesktopAPIListSimulatorsResult, error) {
	return &contracts.DesktopAPIListSimulatorsResult{Availability: domain.SimulatorAvailabilityUnsupported{Reason: "The simulator needs the desktop app."}, Devices: []*domain.SimulatorDevice{}}, nil
}

func (HeadlessSimulatorDesktop) BootSimulator(context.Context, string) error {
	return needsDesktop("The simulator")
}

func (HeadlessSimulatorDesktop) ShutdownSimulator(context.Context, string) error { return nil }

func (HeadlessSimulatorDesktop) SimulatorInput(context.Context, string, domain.SimulatorInput) error {
	return needsDesktop("The simulator")
}

func (HeadlessSimulatorDesktop) DescribeSimulator(context.Context, string, string) (*domain.SimulatorSnapshot, error) {
	return nil, needsDesktop("The simulator")
}

func (HeadlessSimulatorDesktop) CaptureSimulator(context.Context, string) (*domain.SimulatorShot, error) {
	return nil, needsDesktop("The simulator")
}
