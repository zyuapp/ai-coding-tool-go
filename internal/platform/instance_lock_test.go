package platform

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAServeLockStandsWhileItsProcessLives(t *testing.T) {
	userData := t.TempDir()
	if ServingProcess(userData) != 0 {
		t.Fatal("nobody serves yet")
	}
	server := bystander(t)
	os.WriteFile(filepath.Join(userData, "serve.lock"), []byte(strconv.Itoa(server.Process.Pid)), 0o644)
	if ServingProcess(userData) != server.Process.Pid {
		t.Fatal("the living server is not seen")
	}
	if _, err := ClaimServeLock(userData); err == nil || err.Error() != fmt.Sprintf("AI Coding Tool is already serving from this folder (process %d).", server.Process.Pid) {
		t.Fatalf("got %v", err)
	}
	ended(t, server)
	if ServingProcess(userData) != 0 {
		t.Fatal("a dead server's lock counts")
	}
	release, err := ClaimServeLock(userData)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(filepath.Join(userData, "serve.lock")); !os.IsNotExist(err) {
		t.Fatal("release left the lock")
	}
}

func TestADeadServersLockIsClearedOneStarterAtATime(t *testing.T) {
	userData := t.TempDir()
	lock := filepath.Join(userData, "serve.lock")
	os.WriteFile(lock, []byte("999999"), 0o644)
	turn := lock + ".reclaim"
	starter := bystander(t)
	os.Symlink(strconv.Itoa(starter.Process.Pid), turn)
	if _, err := ClaimServeLock(userData); err == nil || !strings.Contains(err.Error(), "Another AI Coding Tool is starting to serve") {
		t.Fatalf("got %v", err)
	}
	if target, _ := os.Readlink(turn); target != strconv.Itoa(starter.Process.Pid) {
		t.Fatal("a living starter lost its turn")
	}
	ended(t, starter)
	if _, err := ClaimServeLock(userData); err == nil || err.Error() != "A server starting from this folder died before it finished. Remove "+turn+" and try again." {
		t.Fatalf("got %v", err)
	}
	os.Remove(turn)
	release, err := ClaimServeLock(userData)
	if err != nil {
		t.Fatal(err)
	}
	if holderOf(lock) != os.Getpid() {
		t.Fatal("not claimed")
	}
	if _, err := os.Lstat(turn); !os.IsNotExist(err) {
		t.Fatal("turn left behind")
	}
	release()
}

func TestAServerLetsGoOfItsOwnLockOnly(t *testing.T) {
	userData := t.TempDir()
	release, err := ClaimServeLock(userData)
	if err != nil {
		t.Fatal(err)
	}
	other := bystander(t)
	os.WriteFile(filepath.Join(userData, "serve.lock"), []byte(strconv.Itoa(other.Process.Pid)), 0o644)
	release()
	if ServingProcess(userData) != other.Process.Pid {
		t.Fatal("another server's lock was removed")
	}
}

func TestTheDesktopLockKeepsAServerOffOnlyWhileTheAppRuns(t *testing.T) {
	userData := t.TempDir()
	if DesktopOpen(userData) {
		t.Fatal("open with no lock")
	}
	app := bystander(t)
	lock := filepath.Join(userData, "SingletonLock")
	os.Symlink(fmt.Sprintf("%s-%d", hostname(), app.Process.Pid), lock)
	if !DesktopOpen(userData) {
		t.Fatal("living app not seen")
	}
	if _, err := ClaimServeLock(userData); err == nil || !strings.Contains(err.Error(), "desktop app is open on this data folder") {
		t.Fatalf("got %v", err)
	}
	ended(t, app)
	if DesktopOpen(userData) {
		t.Fatal("a crashed app's lock counts")
	}
	release, err := ClaimServeLock(userData)
	if err != nil {
		t.Fatal(err)
	}
	release()
	os.Remove(lock)
	os.Symlink(fmt.Sprintf("elsewhere-%d", os.Getpid()), lock)
	if !DesktopOpen(userData) {
		t.Fatal("a lock held from another machine cannot be checked, so it stands")
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "aic-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestASecondLaunchHandsItsArgumentsToTheFirst(t *testing.T) {
	userData := shortTempDir(t)
	lock, forwarded, err := ClaimDesktopLock(userData, Launch{})
	if err != nil || forwarded {
		t.Fatalf("first claim: %v %v", forwarded, err)
	}
	if !DesktopOpen(userData) {
		t.Fatal("lock not seen")
	}
	received := make(chan Launch, 1)
	lock.Listen(func(launch Launch) { received <- launch })

	_, forwarded, err = ClaimDesktopLock(userData, Launch{Dir: "/work", Argv: []string{"aic", "/repo", "with space"}})
	if err != nil || !forwarded {
		t.Fatalf("second claim: %v %v", forwarded, err)
	}
	select {
	case launch := <-received:
		if launch.Dir != "/work" || !slices.Equal(launch.Argv, []string{"aic", "/repo", "with space"}) {
			t.Fatalf("got %#v", launch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing received")
	}
	lock.Close()
	if DesktopOpen(userData) {
		t.Fatal("lock left after close")
	}
	for _, name := range []string{"SingletonSocket", "SingletonCookie"} {
		if _, err := os.Lstat(filepath.Join(userData, name)); !os.IsNotExist(err) {
			t.Fatalf("%s left after close", name)
		}
	}
}

func TestACrashedWindowsLockCountsForNothing(t *testing.T) {
	userData := shortTempDir(t)
	os.Symlink(hostname()+"-999999", filepath.Join(userData, "SingletonLock"))
	os.Symlink("/tmp/gone/SingletonSocket", filepath.Join(userData, "SingletonSocket"))
	if DesktopOpen(userData) {
		t.Fatal("crashed lock counts")
	}
	lock, forwarded, err := ClaimDesktopLock(userData, Launch{})
	if err != nil || forwarded {
		t.Fatalf("got %v %v", forwarded, err)
	}
	lock.Close()
}

func TestALockHeldFromAnotherMachineIsNotTaken(t *testing.T) {
	userData := shortTempDir(t)
	os.Symlink("elsewhere-1234", filepath.Join(userData, "SingletonLock"))
	if _, _, err := ClaimDesktopLock(userData, Launch{}); err != ErrProfileInUse {
		t.Fatalf("got %v", err)
	}
}

// electronHolder stands in for the Electron app holding the profile, laid out as Chromium lays it out.
func electronHolder(t *testing.T, userData string, reply string) <-chan []byte {
	t.Helper()
	socketDir := shortTempDir(t)
	socket := filepath.Join(socketDir, "SingletonSocket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	os.Symlink(fmt.Sprintf("%s-%d", hostname(), os.Getppid()), filepath.Join(userData, "SingletonLock"))
	os.Symlink(socket, filepath.Join(userData, "SingletonSocket"))
	os.Symlink("12345", filepath.Join(userData, "SingletonCookie"))
	os.Symlink("12345", filepath.Join(socketDir, "SingletonCookie"))
	messages := make(chan []byte, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		var message []byte
		buffer := make([]byte, 4096)
		for {
			read, err := connection.Read(buffer)
			message = append(message, buffer[:read]...)
			if err != nil {
				break
			}
		}
		messages <- message
		connection.Write([]byte(reply))
	}()
	return messages
}

func TestALaunchIsHandedToTheElectronApp(t *testing.T) {
	userData := shortTempDir(t)
	messages := electronHolder(t, userData, "ACK")
	_, forwarded, err := ClaimDesktopLock(userData, Launch{Dir: "/work", Argv: []string{"/bin/app", "aicodingtool://open?path=%2Frepo"}})
	if err != nil || !forwarded {
		t.Fatalf("got %v %v", forwarded, err)
	}
	if got := string(<-messages); got != "START\x00/work\x002\x00/bin/app\x00aicodingtool://open?path=%2Frepo" {
		t.Fatalf("sent %q", got)
	}
}

func TestSingletonMessagesReadEveryForm(t *testing.T) {
	launch := Launch{Dir: "/w", Argv: []string{"a", "", "c"}, AdditionalData: []byte("x\x00y")}
	parsed, ok := ParseSingletonMessage(SingletonMessage(launch))
	if !ok || parsed.Dir != "/w" || !slices.Equal(parsed.Argv, launch.Argv) || string(parsed.AdditionalData) != "x\x00y" {
		t.Fatalf("got %#v", parsed)
	}
	chromium, ok := ParseSingletonMessage([]byte("START\x00/w\x00/bin/app\x00--flag"))
	if !ok || !slices.Equal(chromium.Argv, []string{"/bin/app", "--flag"}) {
		t.Fatalf("got %#v", chromium)
	}
	for _, bad := range []string{"", "STOP\x00/w\x001\x00a", "START\x00/w\x009\x00a", "START\x00/w\x001\x00a\x0099\x00short"} {
		if _, ok := ParseSingletonMessage([]byte(bad)); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}
