package application

func InitialBrowserState() BrowserState {
	return BrowserState{BrowserOrigins: []string{}, BrowserImport: NoBrowserImport, BrowserApproval: nil}
}
