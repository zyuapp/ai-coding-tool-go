package domain

import (
	"reflect"
	"slices"
	"testing"
)

func TestEditorCandidatesCarryTheLine(t *testing.T) {
	line := 119.0
	candidates := EditorCandidates(PlatformDarwin, "/checkout/src/app.mts", &line)
	byID := func(id string) []string {
		index := slices.IndexFunc(candidates, func(c *Candidate) bool { return c.ID == id })
		if index < 0 {
			t.Fatalf("no %s", id)
		}
		return candidates[index].Args
	}
	if !reflect.DeepEqual(byID("vscode"), []string{"-g", "/checkout/src/app.mts:119"}) ||
		!reflect.DeepEqual(byID("zed"), []string{"/checkout/src/app.mts:119"}) ||
		!reflect.DeepEqual(byID("jetbrains"), []string{"--line", "119", "/checkout/src/app.mts"}) {
		t.Fatal(candidates)
	}
	for _, candidate := range EditorCandidates(PlatformDarwin, "/checkout/src/app.mts", nil) {
		if !reflect.DeepEqual(candidate.Args, []string{"/checkout/src/app.mts"}) {
			t.Fatal(candidate.Command)
		}
	}
}

func TestEditorCandidatesPerPlatform(t *testing.T) {
	commands := func(platform Platform) []string {
		out := []string{}
		for _, candidate := range EditorCandidates(platform, "/f.ts", nil) {
			out = append(out, candidate.Command)
		}
		return out
	}
	darwin := commands(PlatformDarwin)
	cursor := slices.Index(darwin, "/Applications/Cursor.app/Contents/Resources/app/bin/cursor")
	if cursor < 0 || cursor > slices.Index(darwin, "cursor") || slices.Index(darwin, "cursor") > slices.Index(darwin, "code") {
		t.Fatal(darwin)
	}
	if !reflect.DeepEqual(commands(PlatformWin32), []string{"cursor.cmd", "code.cmd", "windsurf.cmd", "subl.exe", "idea.bat", "webstorm.bat"}) {
		t.Fatal(commands(PlatformWin32))
	}
	if !slices.Contains(commands(PlatformLinux), "zed") || len(commands("freebsd")) != 0 {
		t.Fatal("linux or unknown platform")
	}
}

func TestEditorLaunchAndFloor(t *testing.T) {
	line := 4.0
	if got := EditorLaunch(PlatformDarwin, "code", "/f.ts", &line); !reflect.DeepEqual(got, &Launch{Command: "code", Args: []string{"-g", "/f.ts:4"}}) {
		t.Fatal(got)
	}
	if EditorLaunch(PlatformDarwin, "code.cmd", "/f.ts", &line) != nil || EditorLaunch(PlatformDarwin, "quicktime", "/f.ts", nil) != nil {
		t.Fatal("foreign launcher")
	}
	if got := TextHandlerLaunch(PlatformDarwin, "/f.mts"); !reflect.DeepEqual(got, &Launch{Command: "open", Args: []string{"-t", "/f.mts"}}) {
		t.Fatal(got)
	}
	if got := TextHandlerLaunch(PlatformWin32, `C:\f.mts`); !reflect.DeepEqual(got, &Launch{Command: "notepad.exe", Args: []string{`C:\f.mts`}}) {
		t.Fatal(got)
	}
	if TextHandlerLaunch(PlatformLinux, "/f.mts") != nil {
		t.Fatal("linux floor")
	}
}

func TestOpensInBrowser(t *testing.T) {
	line := 12.0
	if !OpensInBrowser("/tmp/mockup.html", nil) || !OpensInBrowser("/tmp/MOCKUP.HTM", nil) || OpensInBrowser("/tmp/mockup.html", &line) || OpensInBrowser("/checkout/src/app.mts", nil) {
		t.Fatal("opensInBrowser")
	}
}
