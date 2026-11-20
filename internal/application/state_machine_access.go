package application

func InitialCliState() CliSliceState {
	return CliSliceState{Cli: NoCli}
}

func InitialPlanUsageState() PlanUsageSliceState {
	return PlanUsageSliceState{PlanUsage: NoPlanUsage}
}

func InitialComputerUseState() ComputerUseSliceState {
	return ComputerUseSliceState{ComputerUsePermissions: NoComputerUseAccess, ComputerUseSetup: false}
}
