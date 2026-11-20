package platform

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunKeepsTheOutputOfAFailedProcess(t *testing.T) {
	_, _, err := Run(context.Background(), "sh", []string{"-c", "printf out; printf err >&2; exit 3"}, RunOptions{})
	var failure *RunError
	if !errors.As(err, &failure) || failure.Code != 3 || string(failure.Stdout) != "out" || string(failure.Stderr) != "err" {
		t.Fatalf("got %#v", err)
	}
	if failure.Error() != "Command failed: sh -c printf out; printf err >&2; exit 3\nerr" {
		t.Fatalf("message %q", failure.Error())
	}
}

func TestRunRefusesOutputPastTheBuffer(t *testing.T) {
	_, _, err := Run(context.Background(), "sh", []string{"-c", "yes | head -c 5000"}, RunOptions{MaxBuffer: 1000})
	var failure *RunError
	if !errors.As(err, &failure) || !failure.MaxBuffer || failure.Error() != "stdout maxBuffer length exceeded" {
		t.Fatalf("got %v", err)
	}
}

func TestRunStopsAProcessPastItsTimeout(t *testing.T) {
	started := time.Now()
	_, _, err := Run(context.Background(), "sleep", []string{"5"}, RunOptions{Timeout: 100 * time.Millisecond})
	var failure *RunError
	if !errors.As(err, &failure) || !failure.Killed || SignalName(failure.Signal) != "SIGTERM" {
		t.Fatalf("got %#v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("waited past the timeout")
	}
}

func TestRunWritesInputAndReportsAMissingProgram(t *testing.T) {
	stdout, _, err := Run(context.Background(), "cat", nil, RunOptions{Input: []byte("hello")})
	if err != nil || string(stdout) != "hello" {
		t.Fatalf("got %q %v", stdout, err)
	}
	_, _, err = Run(context.Background(), "act-no-such-program", nil, RunOptions{})
	var failure *RunError
	if !errors.As(err, &failure) || !failure.NotFound || failure.Error() != "spawn act-no-such-program ENOENT" {
		t.Fatalf("got %v", err)
	}
}

func TestOwnGroupCancellationReachesGrandchildren(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	command := OwnGroup(Command(ctx, "sh", "-c", "sleep 60 & echo $!; wait"))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	read, _ := stdout.Read(buffer)
	var grandchild int
	for _, digit := range buffer[:read] {
		if digit >= '0' && digit <= '9' {
			grandchild = grandchild*10 + int(digit-'0')
		}
	}
	cancel()
	_ = command.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for Alive(grandchild) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if Alive(grandchild) {
		t.Fatalf("grandchild %d outlived its group", grandchild)
	}
}
