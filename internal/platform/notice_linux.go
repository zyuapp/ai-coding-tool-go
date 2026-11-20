package platform

import (
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	notificationsName = "org.freedesktop.Notifications"
	notificationsPath = "/org/freedesktop/Notifications"
)

var linuxNotices = struct {
	sync.Mutex
	connection *dbus.Conn
	markup     bool
	ids        map[uint32]string
	failed     bool
}{ids: map[uint32]string{}}

// noticeConnection is the session bus, watched for clicks, once a notification server answers on it.
func noticeConnection() *dbus.Conn {
	linuxNotices.Lock()
	defer linuxNotices.Unlock()
	if linuxNotices.connection != nil || linuxNotices.failed {
		return linuxNotices.connection
	}
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		linuxNotices.failed = true
		return nil
	}
	var capabilities []string
	if err := connection.Object(notificationsName, notificationsPath).Call(notificationsName+".GetCapabilities", 0).Store(&capabilities); err != nil {
		connection.Close()
		linuxNotices.failed = true
		return nil
	}
	for _, capability := range capabilities {
		if capability == "body-markup" {
			linuxNotices.markup = true
		}
	}
	_ = connection.AddMatchSignal(dbus.WithMatchInterface(notificationsName), dbus.WithMatchObjectPath(notificationsPath))
	signals := make(chan *dbus.Signal, 16)
	connection.Signal(signals)
	go watchNoticeSignals(signals)
	linuxNotices.connection = connection
	return connection
}

func watchNoticeSignals(signals chan *dbus.Signal) {
	for signal := range signals {
		if len(signal.Body) < 1 {
			continue
		}
		id, ok := signal.Body[0].(uint32)
		if !ok {
			continue
		}
		linuxNotices.Lock()
		identifier := linuxNotices.ids[id]
		delete(linuxNotices.ids, id)
		linuxNotices.Unlock()
		if signal.Name == notificationsName+".ActionInvoked" && len(signal.Body) > 1 && signal.Body[1] == "default" && identifier != "" {
			noticeClicked(identifier)
		}
	}
}

func noticesSupported() bool {
	return noticeConnection() != nil
}

var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func postNotice(identifier, title, body string) {
	connection := noticeConnection()
	if connection == nil {
		return
	}
	linuxNotices.Lock()
	if linuxNotices.markup {
		body = markupEscaper.Replace(body)
	}
	linuxNotices.Unlock()
	hints := map[string]dbus.Variant{"suppress-sound": dbus.MakeVariant(true)}
	if desktop := desktopEntry(); desktop != "" {
		hints["desktop-entry"] = dbus.MakeVariant(desktop)
	}
	var id uint32
	call := connection.Object(notificationsName, notificationsPath).Call(notificationsName+".Notify", 0,
		AppName, uint32(0), "", title, body, []string{"default", "View"}, hints, int32(-1))
	if call.Store(&id) != nil {
		return
	}
	linuxNotices.Lock()
	if len(linuxNotices.ids) >= maxPendingNotices {
		clear(linuxNotices.ids)
	}
	linuxNotices.ids[id] = identifier
	linuxNotices.Unlock()
}

// desktopEntry is the installed app's desktop id, which lets the notification server show its icon.
func desktopEntry() string {
	if Packaged() {
		return "aicodingtool"
	}
	return ""
}

// SystemIdleState is whether the screen saver has locked the session, or how long nobody has touched
// it, as far as the session's services tell. A session that tells nothing counts as active.
func SystemIdleState(thresholdSeconds int) IdleState {
	connection, err := dbus.SessionBus()
	if err != nil {
		return IdleActive
	}
	var active bool
	saver := connection.Object("org.freedesktop.ScreenSaver", "/org/freedesktop/ScreenSaver")
	if saver.Call("org.freedesktop.ScreenSaver.GetActive", 0).Store(&active) == nil && active {
		return IdleLocked
	}
	var idleMilliseconds uint64
	if connection.Object("org.gnome.Mutter.IdleMonitor", "/org/gnome/Mutter/IdleMonitor/Core").Call("org.gnome.Mutter.IdleMonitor.GetIdletime", 0).Store(&idleMilliseconds) == nil {
		if time.Duration(idleMilliseconds)*time.Millisecond >= time.Duration(thresholdSeconds)*time.Second {
			return IdleIdle
		}
		return IdleActive
	}
	var idleSeconds uint32
	if saver.Call("org.freedesktop.ScreenSaver.GetSessionIdleTime", 0).Store(&idleSeconds) == nil && int(idleSeconds) >= thresholdSeconds {
		return IdleIdle
	}
	return IdleActive
}
