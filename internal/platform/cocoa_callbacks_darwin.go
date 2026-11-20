package platform

import "C"

//export platformNoticeClicked
func platformNoticeClicked(identifier *C.char) {
	noticeClicked(C.GoString(identifier))
}

//export platformDockMarkAllRead
func platformDockMarkAllRead() {
	dockMarkAllRead()
}

//export platformScreenLock
func platformScreenLock(locked C.int) {
	screenLockChanged(locked != 0)
}
