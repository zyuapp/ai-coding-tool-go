package platform

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func dump(body string) string {
	return "a start-up file said this\n" + environmentMark + body + environmentMark + "\ngoodbye\n"
}

func TestSearchPathIsReadBetweenTheMarks(t *testing.T) {
	if got := SearchPathFromEnvironment(dump("HOME=/Users/dev\nPATH=/opt/homebrew/bin:/usr/bin\nSHELL=/bin/zsh\n")); got != "/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("got %q", got)
	}
	for name, output := range map[string]string{
		"no marks":           "",
		"opening mark only":  "noise" + environmentMark + "HOME=/Users/dev\n",
		"no PATH":            dump("HOME=/Users/dev\n"),
		"empty PATH":         dump("PATH=\n"),
		"PATH outside marks": "PATH=/should/not/count\n" + environmentMark + "HOME=/Users/dev\n" + environmentMark,
	} {
		if got := SearchPathFromEnvironment(output); got != "" {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

func TestMergingKeepsTheFirstMention(t *testing.T) {
	if got := MergeSearchPaths("/opt/homebrew/bin:/usr/bin", "/usr/bin:/bin", "", ":/sbin:"); got != "/opt/homebrew/bin:/usr/bin:/bin:/sbin" {
		t.Fatalf("got %q", got)
	}
}

func folders() []string { return strings.Split(SearchPath(), ":") }

func TestLoginShellPathIsAdoptedAndTheStartingOneKept(t *testing.T) {
	folder := t.TempDir()
	holdSearchPath(t, folder)
	t.Setenv("SHELL", "/bin/sh")
	AdoptLoginShellPath(context.Background())
	got := folders()
	if !slices.Contains(got, folder) {
		t.Fatalf("%q lost from %q", folder, got)
	}
	if len(slices.Compact(slices.Sorted(slices.Values(got)))) != len(got) {
		t.Fatalf("a folder is listed twice: %q", got)
	}
}

func TestAShellThatCannotRunLeavesTheStartingPath(t *testing.T) {
	bin := t.TempDir()
	holdSearchPath(t, bin)
	t.Setenv("SHELL", filepath.Join(bin, "no-such-shell"))
	AdoptLoginShellPath(context.Background())
	if !slices.Contains(folders(), bin) {
		t.Fatalf("lost %q", bin)
	}
}

func TestStandardToolFoldersAreAddedOnlyWhenPresent(t *testing.T) {
	home := t.TempDir()
	local := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", filepath.Join(home, "no-such-shell"))
	holdSearchPath(t, "/usr/bin")
	AdoptLoginShellPath(context.Background())
	got := folders()
	if !slices.Contains(got, local) {
		t.Fatalf("missing %q in %q", local, got)
	}
	for _, folder := range got {
		if strings.HasPrefix(folder, home) && folder != local {
			t.Fatalf("absent folder %q added", folder)
		}
	}
}

func TestLinuxLoginShellHasItsOwnSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	folder := t.TempDir()
	shell := filepath.Join(folder, "probe")
	writeExecutable(t, shell, "#!/bin/sh\nread pid comm state ppid pgrp session tty rest < /proc/$$/stat\nprintf '"+environmentMark+"PATH=p%s:s%s:t%s\\n"+environmentMark+"' \"$pid\" \"$session\" \"$tty\"\n")
	holdSearchPath(t, "/usr/bin")
	t.Setenv("SHELL", shell)
	AdoptLoginShellPath(context.Background())
	parts := folders()
	if parts[1][1:] != parts[0][1:] {
		t.Fatalf("not a session leader: %q", parts)
	}
	if parts[2] != "t0" {
		t.Fatalf("has a terminal: %q", parts)
	}
}

func TestACommandIsFoundOnAndSeesTheHeldSearchPath(t *testing.T) {
	folder := t.TempDir()
	writeExecutable(t, filepath.Join(folder, "act-search-path-probe"), "#!/bin/sh\nprintf '%s|%s' \"$PATH\" \"$ACT_PROBE\"\n")
	holdSearchPath(t, MergeSearchPaths(folder, "/usr/bin:/bin"))
	SetChildEnv("ACT_PROBE", "child")
	defer func() {
		childEnv.Lock()
		delete(childEnv.values, "ACT_PROBE")
		childEnv.keys = slices.DeleteFunc(childEnv.keys, func(key string) bool { return key == "ACT_PROBE" })
		childEnv.Unlock()
	}()
	output, err := Command(context.Background(), "act-search-path-probe").Output()
	if err != nil {
		t.Fatal(err)
	}
	path, variable, _ := strings.Cut(string(output), "|")
	if !strings.HasPrefix(path, folder) || variable != "child" {
		t.Fatalf("child saw %q", output)
	}
	if strings.Contains(os.Getenv("PATH"), folder) || os.Getenv("ACT_PROBE") != "" {
		t.Fatal("this process's own environment changed")
	}
	if _, err := Command(context.Background(), "act-no-such-program").Output(); err == nil {
		t.Fatal("a missing program started")
	}
}
