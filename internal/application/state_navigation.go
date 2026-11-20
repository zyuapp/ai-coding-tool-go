package application

func InitialNavigationState() NavigationState {
	return NavigationState{LastFolder: nil, CurrentID: nil, History: []string{}, HistoryIndex: -1}
}
