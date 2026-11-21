package application

func InitialIdleCompactionState() IdleCompactionState {
	return IdleCompactionState{IdleCompactions: map[string]*IdleCompaction{}}
}
