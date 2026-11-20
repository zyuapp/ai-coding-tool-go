package platform

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"strconv"
	"sync"
	"sync/atomic"
)

// awayAfterSeconds is how long the computer sits untouched before the user counts as away from it.
const awayAfterSeconds = 180

var notices = struct {
	sync.Mutex
	next    atomic.Uint64
	clicked map[string]func()
}{clicked: map[string]func(){}}

// maxPendingNotices bounds the click handlers kept for notices nobody clicked.
const maxPendingNotices = 200

func noticeIdentifier(onClick func()) string {
	identifier := "aicodingtool-notice-" + strconv.FormatUint(notices.next.Add(1), 10)
	if onClick == nil {
		return identifier
	}
	notices.Lock()
	defer notices.Unlock()
	if len(notices.clicked) >= maxPendingNotices {
		clear(notices.clicked)
	}
	notices.clicked[identifier] = onClick
	return identifier
}

func noticeClicked(identifier string) {
	notices.Lock()
	onClick := notices.clicked[identifier]
	delete(notices.clicked, identifier)
	notices.Unlock()
	if onClick != nil {
		go onClick()
	}
}

// Notify says what happened where the user already is, silently, since taking the window would take
// their place. A click on it runs onClick. Does nothing where the desktop has no notices.
func Notify(title, body string, onClick func()) {
	if !noticesSupported() {
		return
	}
	postNotice(noticeIdentifier(onClick), title, body)
}

// ThreadNotice is a thread's notice, as the runtime raises it.
type ThreadNotice = contracts.ThreadNotice

// NoticeHost is what a notice needs from the window it belongs to.
type NoticeHost interface {
	// Focused reports whether a window exists and the user is looking at it.
	Focused() bool
	// HasWindow reports whether a window exists at all.
	HasWindow() bool
	// Reveal brings the window back to the user.
	Reveal()
	// OpenThread shows the thread in the window.
	OpenThread(taskID string)
}

// AnnounceThread takes a thread's notice to where the user is. A window they are already looking at
// says it itself, so nothing is announced then; anywhere else the desktop carries it and the click
// lands on the thread.
func AnnounceThread(host NoticeHost, notice ThreadNotice) {
	if !host.HasWindow() || host.Focused() {
		return
	}
	Notify(notice.Title, notice.Headline, func() {
		host.Reveal()
		if host.HasWindow() {
			host.OpenThread(notice.TaskID)
		}
	})
}

// DesktopAway reports whether the user has stepped away from this computer: it is locked, or nobody
// has touched it for a while.
func DesktopAway() bool {
	return SystemIdleState(awayAfterSeconds) != IdleActive
}
