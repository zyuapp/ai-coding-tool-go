package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialSimulatorState() SimulatorSliceState {
	return SimulatorSliceState{SimulatorDevice: nil, Simulator: domain.NoSimulators}
}
