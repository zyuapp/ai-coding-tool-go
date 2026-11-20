package platform

import (
	"github.com/godbus/dbus/v5"
)

const (
	login1Path    = "/org/freedesktop/login1"
	login1Manager = "org.freedesktop.login1.Manager"
)

// WatchWake calls woke each time the machine resumes from sleep, as logind announces it. Without a
// system bus nothing is watched.
func WatchWake(woke func()) (stop func()) {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return func() {}
	}
	if err := connection.AddMatchSignal(dbus.WithMatchObjectPath(login1Path), dbus.WithMatchInterface(login1Manager), dbus.WithMatchMember("PrepareForSleep")); err != nil {
		connection.Close()
		return func() {}
	}
	signals := make(chan *dbus.Signal, 4)
	connection.Signal(signals)
	go func() {
		for signal := range signals {
			// PrepareForSleep(false) is the machine coming back.
			if signal.Name == login1Manager+".PrepareForSleep" && len(signal.Body) == 1 && signal.Body[0] == false {
				woke()
			}
		}
	}()
	return func() { connection.Close() }
}
