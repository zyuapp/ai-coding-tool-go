package host

import "testing"

func TestLayoutDefaultsUseTheWindowWidthWhileStoredChoicesWin(t *testing.T) {
	storage := newMemoryStorage(nil)
	narrow := LoadViewPreferences(storage, new(1240.0))
	if !narrow.SidebarOpen || narrow.SessionPanelOpen {
		t.Fatalf("narrow = %+v", narrow)
	}
	wide := LoadViewPreferences(storage, new(1600.0))
	if !wide.SidebarOpen || !wide.SessionPanelOpen {
		t.Fatalf("wide = %+v", wide)
	}
	headless := LoadViewPreferences(storage, nil)
	if !headless.SidebarOpen || headless.SessionPanelOpen {
		t.Fatalf("without a window = %+v", headless)
	}
	wide.SidebarOpen, wide.SessionPanelOpen = false, false
	SaveViewPreferences(storage, wide)
	stored := LoadViewPreferences(storage, new(1600.0))
	if stored.SidebarOpen || stored.SessionPanelOpen {
		t.Fatalf("stored = %+v", stored)
	}
}
