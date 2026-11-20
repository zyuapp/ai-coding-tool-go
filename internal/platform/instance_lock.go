package platform

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// serveLock is the lock `aic serve` holds on a data folder: the id of the process serving from it.
	serveLock = "serve.lock"
	// appLock is Chromium's single-instance lock, a link to `<host>-<pid>` a crash leaves behind. The
	// Electron app and this one take the same lock, so each recognises the other's hold on a profile.
	appLock       = "SingletonLock"
	appSocket     = "SingletonSocket"
	appCookie     = "SingletonCookie"
	startToken    = "START"
	ackToken      = "ACK"
	shutdownToken = "SHUTDOWN"
	// maxSingletonMessage bounds what a later launch may hand over, as Electron's process singleton does.
	maxSingletonMessage = 32 * 1024 * 1024
	// singletonTimeout is how long either side waits on the other, as Chromium's process singleton does.
	singletonTimeout = 20 * time.Second
)

func hostname() string {
	name, _ := os.Hostname()
	return name
}

func holderOf(lock string) int {
	data, err := os.ReadFile(lock)
	if err != nil {
		return 0
	}
	holder, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || holder <= 0 {
		return 0
	}
	return holder
}

// ServingProcess is the process serving from this folder, or 0 when none is; a lock a dead one left counts for nothing.
func ServingProcess(userData string) int {
	holder := holderOf(filepath.Join(userData, serveLock))
	if holder != 0 && holder != os.Getpid() && Alive(holder) {
		return holder
	}
	return 0
}

// claim takes a lock whole and in one step: a hard link to a file already holding the process id,
// which fails if one stands.
func claim(lock string) (bool, error) {
	staging := fmt.Sprintf("%s.%d", lock, os.Getpid())
	if err := os.WriteFile(staging, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return false, err
	}
	defer os.Remove(staging)
	if err := os.Link(staging, lock); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// localHolder is the pid a `<host>-<pid>` lock names when this host made it.
func localHolder(target string) (int, bool) {
	dash := strings.LastIndex(target, "-")
	if dash < 0 || target[:dash] != hostname() {
		return 0, false
	}
	pid, err := strconv.Atoi(target[dash+1:])
	if err != nil {
		return 0, false
	}
	return pid, true
}

// DesktopOpen reports whether the desktop app has this folder open. A lock naming a dead process here
// is one a crash left.
func DesktopOpen(userData string) bool {
	lock := filepath.Join(userData, appLock)
	target, err := os.Readlink(lock)
	if err != nil {
		_, statErr := os.Lstat(lock)
		return statErr == nil
	}
	pid, ok := localHolder(target)
	if !ok {
		return true
	}
	return Alive(pid)
}

// takeTurn takes the turn to clear a dead server's lock: a link naming the starter that has it, made in one step.
func takeTurn(turn string) (bool, error) {
	if err := os.Symlink(strconv.Itoa(os.Getpid()), turn); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// turnAbandoned reports whether the starter holding the turn is dead. Its turn is never taken over:
// nothing can be, safely, without an OS lock.
func turnAbandoned(turn string) bool {
	target, err := os.Readlink(turn)
	if err != nil {
		return false
	}
	holder, err := strconv.Atoi(target)
	return err == nil && holder > 0 && !Alive(holder)
}

// reclaim clears a dead server's lock and takes it, one starter at a time, so a starter that arrives
// while another has the turn neither clears that one's fresh lock nor waits.
func reclaim(userData, lock string) (bool, error) {
	turn := lock + ".reclaim"
	taken, err := takeTurn(turn)
	if err != nil || !taken {
		return false, err
	}
	defer os.Remove(turn)
	if ServingProcess(userData) != 0 {
		return false, nil
	}
	_ = os.Remove(lock)
	return claim(lock)
}

// ClaimServeLock takes the folder for a server, since a second server, or the desktop app, on the
// same data would write over it. The returned release gives the folder back, and only while this
// process still holds it.
func ClaimServeLock(userData string) (release func(), err error) {
	if DesktopOpen(userData) {
		return nil, errors.New("The desktop app is open on this data folder. Quit it before serving, or serve from another machine.")
	}
	lock := filepath.Join(userData, serveLock)
	claimed, err := claim(lock)
	if err != nil {
		return nil, err
	}
	if !claimed && ServingProcess(userData) == 0 {
		if claimed, err = reclaim(userData, lock); err != nil {
			return nil, err
		}
	}
	if !claimed {
		if holder := ServingProcess(userData); holder != 0 {
			return nil, fmt.Errorf("AI Coding Tool is already serving from this folder (process %d).", holder)
		}
		turn := lock + ".reclaim"
		if turnAbandoned(turn) {
			return nil, fmt.Errorf("A server starting from this folder died before it finished. Remove %s and try again.", turn)
		}
		return nil, errors.New("Another AI Coding Tool is starting to serve from this folder.")
	}
	return func() {
		if holderOf(lock) == os.Getpid() {
			_ = os.Remove(lock)
		}
	}, nil
}

// Launch is what a later launch handed to the one holding the profile: its working folder, its
// arguments with the program first, and any additional data it sent.
type Launch struct {
	Dir            string
	Argv           []string
	AdditionalData []byte
}

// ErrProfileInUse is returned when another process holds the profile and could not be handed the launch.
var ErrProfileInUse = errors.New("profile in use")

// DesktopLock is the profile's single-instance lock while this process holds it.
type DesktopLock struct {
	lock, socketLink, cookieLink, socketDir string
	listener                                *net.UnixListener
	ours                                    string
	closeOnce                               sync.Once
}

// ClaimDesktopLock claims the profile for this window, or hands `launch` to the process that already
// holds it, Electron or this app, and answers forwarded. It speaks Chromium's process-singleton
// protocol, so either app may hold the profile while the other is launched.
func ClaimDesktopLock(userData string, launch Launch) (lock *DesktopLock, forwarded bool, err error) {
	if err := os.MkdirAll(userData, 0o755); err != nil {
		return nil, false, err
	}
	lockPath := filepath.Join(userData, appLock)
	ours := fmt.Sprintf("%s-%d", hostname(), os.Getpid())
	deadline := time.Now().Add(singletonTimeout)
	var unreachableSince time.Time
	for {
		if err := os.Symlink(ours, lockPath); err == nil {
			lock, err := listenDesktopLock(userData, ours)
			if err != nil {
				_ = os.Remove(lockPath)
				return nil, false, err
			}
			return lock, false, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return nil, false, err
		}

		reply, notifyErr := notifyDesktop(userData, launch)
		if notifyErr == nil && reply == ackToken {
			return nil, true, nil
		}
		if notifyErr != nil {
			target, readErr := os.Readlink(lockPath)
			if errors.Is(readErr, fs.ErrNotExist) {
				continue
			}
			if readErr != nil || strings.LastIndex(target, "-") <= 0 {
				_ = os.Remove(lockPath)
				continue
			}
			pid, local := localHolder(target)
			if !local {
				return nil, false, ErrProfileInUse
			}
			if !Alive(pid) || pid == os.Getpid() {
				removeStaleSingleton(userData)
				continue
			}
			// A holder that is starting has its lock before its socket; one that never listens is not
			// the app at all, but a process that took a crashed one's pid.
			if errors.Is(notifyErr, errUnreachable) {
				if unreachableSince.IsZero() {
					unreachableSince = time.Now()
				} else if time.Since(unreachableSince) > unreachableGrace {
					removeStaleSingleton(userData)
					continue
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, false, ErrProfileInUse
		}
		time.Sleep(retryInterval)
	}
}

const (
	unreachableGrace = 3 * time.Second
	retryInterval    = 250 * time.Millisecond
)

var errUnreachable = errors.New("singleton socket unreachable")

// removeStaleSingleton clears what a crashed holder left.
func removeStaleSingleton(userData string) {
	_ = os.Remove(filepath.Join(userData, appLock))
	_ = os.Remove(filepath.Join(userData, appSocket))
	_ = os.Remove(filepath.Join(userData, appCookie))
}

func listenDesktopLock(userData, ours string) (*DesktopLock, error) {
	socketDir, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, err
	}
	socket := filepath.Join(socketDir, appSocket)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		_ = os.RemoveAll(socketDir)
		return nil, err
	}
	listener.SetUnlinkOnClose(true)
	var random [8]byte
	_, _ = rand.Read(random[:])
	cookie := strconv.FormatUint(binary.LittleEndian.Uint64(random[:]), 10)
	lock := &DesktopLock{
		lock:       filepath.Join(userData, appLock),
		socketLink: filepath.Join(userData, appSocket),
		cookieLink: filepath.Join(userData, appCookie),
		socketDir:  socketDir,
		listener:   listener,
		ours:       ours,
	}
	_ = os.Remove(lock.socketLink)
	_ = os.Remove(lock.cookieLink)
	if err := errors.Join(
		os.Symlink(socket, lock.socketLink),
		os.Symlink(cookie, lock.cookieLink),
		os.Symlink(cookie, filepath.Join(socketDir, appCookie)),
	); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(socketDir)
		return nil, err
	}
	return lock, nil
}

// Listen hands every later launch to `receive`, on a goroutine of its own, until Close.
func (l *DesktopLock) Listen(receive func(Launch)) {
	go func() {
		for {
			connection, err := l.listener.AcceptUnix()
			if err != nil {
				return
			}
			go serveLaunch(connection, receive)
		}
	}()
}

func serveLaunch(connection *net.UnixConn, receive func(Launch)) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(singletonTimeout))
	message, err := io.ReadAll(io.LimitReader(connection, maxSingletonMessage+1))
	if err != nil {
		return
	}
	if len(message) > maxSingletonMessage {
		_, _ = connection.Write([]byte(ackToken))
		return
	}
	launch, ok := ParseSingletonMessage(message)
	if !ok {
		return
	}
	receive(launch)
	_, _ = connection.Write([]byte(ackToken))
}

// Close gives the profile back: the lock and its links go while they are still this process's.
func (l *DesktopLock) Close() {
	l.closeOnce.Do(func() {
		_ = l.listener.Close()
		if target, err := os.Readlink(l.lock); err == nil && target == l.ours {
			_ = os.Remove(l.lock)
			if target, err := os.Readlink(l.socketLink); err == nil && strings.HasPrefix(target, l.socketDir+string(filepath.Separator)) {
				_ = os.Remove(l.socketLink)
				_ = os.Remove(l.cookieLink)
			}
		}
		_ = os.RemoveAll(l.socketDir)
	})
}

// SingletonMessage is a launch as Electron's process singleton sends it:
// `START\0<dir>\0<argc>\0<argv...>[\0<size>\0<additional data>]`.
func SingletonMessage(launch Launch) []byte {
	var message bytes.Buffer
	message.WriteString(startToken)
	message.WriteByte(0)
	message.WriteString(launch.Dir)
	message.WriteByte(0)
	message.WriteString(strconv.Itoa(len(launch.Argv)))
	for _, argument := range launch.Argv {
		message.WriteByte(0)
		message.WriteString(argument)
	}
	if len(launch.AdditionalData) > 0 {
		message.WriteByte(0)
		message.WriteString(strconv.Itoa(len(launch.AdditionalData)))
		message.WriteByte(0)
		message.Write(launch.AdditionalData)
	}
	return message.Bytes()
}

// ParseSingletonMessage reads a launch Electron's process singleton sent, or Chromium's own form
// without the argument count.
func ParseSingletonMessage(message []byte) (Launch, bool) {
	if len(message) < len(startToken)+4 {
		return Launch{}, false
	}
	tokens := strings.Split(string(message), "\x00")
	if len(tokens) < 3 || tokens[0] != startToken {
		return Launch{}, false
	}
	launch := Launch{Dir: tokens[1]}
	rest := tokens[2:]
	count, err := strconv.ParseUint(rest[0], 10, 64)
	if err != nil {
		launch.Argv = rest
		return launch, true
	}
	if count > uint64(len(rest)-1) {
		return Launch{}, false
	}
	launch.Argv = rest[1 : 1+count]
	if remaining := rest[1+count:]; len(remaining) >= 2 {
		size, err := strconv.ParseUint(remaining[0], 10, 64)
		payload := strings.Join(remaining[1:], "\x00")
		if err != nil || size > uint64(len(payload)) {
			return Launch{}, false
		}
		launch.AdditionalData = []byte(payload[:size])
	}
	return launch, true
}

// notifyDesktop hands a launch to the process holding the profile and returns its reply.
func notifyDesktop(userData string, launch Launch) (string, error) {
	socketLink := filepath.Join(userData, appSocket)
	target, err := os.Readlink(socketLink)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %w", errUnreachable, err)
		}
		target = socketLink
	} else {
		cookie, err := os.Readlink(filepath.Join(userData, appCookie))
		if err != nil || cookie == "" {
			return "", fmt.Errorf("%w: no cookie", errUnreachable)
		}
		if remote, err := os.Readlink(filepath.Join(filepath.Dir(target), appCookie)); err != nil || remote != cookie {
			return "", fmt.Errorf("%w: cookie mismatch", errUnreachable)
		}
	}
	connection, err := net.DialTimeout("unix", target, time.Second)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errUnreachable, err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(singletonTimeout))
	if _, err := connection.Write(SingletonMessage(launch)); err != nil {
		return "", err
	}
	if unix, ok := connection.(*net.UnixConn); ok {
		_ = unix.CloseWrite()
	}
	reply := make([]byte, len(shutdownToken))
	read, err := io.ReadFull(connection, reply)
	if read == 0 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return "", err
	}
	return string(reply[:read]), nil
}
