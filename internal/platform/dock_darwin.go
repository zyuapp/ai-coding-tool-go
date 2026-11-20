package platform

import (
	"strconv"
	"sync"
)

var dock = struct {
	sync.Mutex
	markAllRead func()
	hasUnread   bool
}{}

func dockMarkAllRead() {
	dock.Lock()
	markAllRead := dock.markAllRead
	dock.Unlock()
	if markAllRead != nil {
		go markAllRead()
	}
}

// InstallDockMenu gives the Dock icon its menu, which clears what its badge counts. Call it once the
// app's delegate exists; it waits for one otherwise.
func InstallDockMenu(onMarkAllRead func()) {
	dock.Lock()
	dock.markAllRead = onMarkAllRead
	dock.Unlock()
	installCocoaDockMenu()
}

// SetUnreadBadge shows the unread count on the Dock icon, and none at zero.
func SetUnreadBadge(count int) {
	label := ""
	if count > 0 {
		label = strconv.Itoa(count)
	}
	setCocoaDockBadge(label)
	dock.Lock()
	changed := (count > 0) != dock.hasUnread
	dock.hasUnread = count > 0
	dock.Unlock()
	if changed {
		setCocoaDockUnread(count > 0)
	}
}
