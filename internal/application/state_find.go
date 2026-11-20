package application

func InitialFindBarState() FindBarState {
	return FindBarState{Find: nil, FindResults: nil, Jump: nil, Zen: false}
}
