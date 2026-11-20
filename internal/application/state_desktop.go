package application

func InitialDesktopState() DesktopState {
	return DesktopState{InstalledApps: nil, Focused: true, ViewingImage: nil, ViewingGallery: []string{}}
}
