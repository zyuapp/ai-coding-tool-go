package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var NoPlanUsage = PlanUsageState{Read: 0, Reports: PlanUsageStateReports{}, Reading: []domain.AgentEngine{}, Dismissed: []string{}}

// PlanLimitsComputerUsage is each paired computer's limits as the Usage page draws them.
func PlanLimitsComputerUsage(paired []*PairedComputer) []*ComputerUsage {
	usages := make([]*ComputerUsage, 0, len(paired))
	for _, computer := range paired {
		var usage *PlanUsageState
		if computer.State != nil {
			usage = &computer.State.PlanUsage
		}
		usages = append(usages, &ComputerUsage{
			ID:        computer.ID,
			Name:      computer.Name,
			Status:    computer.Status,
			Usage:     usage,
			Supported: contracts.SupportsComputerCommand(computer.Capabilities, contracts.PlanUsageCommandUsageRead{}),
		})
	}
	return usages
}

func SameComputerUsage(before, next []*ComputerUsage) bool {
	if len(before) != len(next) {
		return false
	}
	for index, item := range before {
		other := next[index]
		if item.ID != other.ID || item.Name != other.Name || item.Status != other.Status || item.Usage != other.Usage || item.Supported != other.Supported {
			return false
		}
	}
	return true
}
