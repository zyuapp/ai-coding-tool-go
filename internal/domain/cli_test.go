package domain

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func cliURLFor(root string) string {
	encoded := strings.NewReplacer("+", "-", "/", "_").Replace(base64.StdEncoding.EncodeToString([]byte(root)))
	return "aicodingtool://open?path=" + encoded
}

func TestProjectPathFromURL(t *testing.T) {
	for _, root := range []string{"/Users/me/code/app", "/Users/me/Ünïcode dir"} {
		if got, ok := ProjectPathFromURL(cliURLFor(root)); !ok || got != root {
			t.Errorf("%s: %q %v", root, got, ok)
		}
	}
	for _, url := range []string{"https://example.com/open?path=Lw", "aicodingtool://send?path=Lw", "aicodingtool://open", "aicodingtool://open?path=***", cliURLFor("relative/path"), "not a url"} {
		if got, ok := ProjectPathFromURL(url); ok {
			t.Errorf("%s: %q", url, got)
		}
	}
	if got, ok := ProjectPathFromURL("aicodingtool://open?path=Lw"); !ok || got != "/" {
		t.Errorf("unpadded: %q %v", got, ok)
	}
	if got, ok := ProjectPathFromArgv([]string{"/Applications/AI Coding Tool.app", cliURLFor("/tmp/app")}); !ok || got != "/tmp/app" {
		t.Errorf("argv: %q", got)
	}
	if _, ok := ProjectPathFromArgv([]string{"/Applications/AI Coding Tool.app", "--updated"}); ok {
		t.Error("argv without a URL")
	}
}

func TestCliScriptIsOursAndOpensTheFolder(t *testing.T) {
	script := MacCliScript("/Applications/AI Coding Tool.app/Contents/MacOS/AI Coding Tool")
	if !IsCliScript(script) || !IsCliScript("#!/bin/sh\n# aic-cli v1\n") || IsCliScript("#!/bin/sh\necho hi\n") || !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Fatal("stamp")
	}
	for _, pattern := range []string{`exec open "aicodingtool://open\?path=\$encoded"`, `ELECTRON_RUN_AS_NODE=1 '/Applications/AI Coding Tool.app/Contents/MacOS/AI Coding Tool' -e`} {
		if !regexp.MustCompile(pattern).MatchString(script) {
			t.Error(pattern)
		}
	}
	if !strings.Contains(MacCliScript(""), "this install cannot serve") {
		t.Error("refusal")
	}
}

func TestCliConfiguration(t *testing.T) {
	linux := CliCliConfiguration("linux", "/home/me", "/home/me/Applications/AI-Coding-Tool.AppImage")
	if !reflect.DeepEqual(linux, &CliConfiguration{InstallPath: "/home/me/.local/bin/aic", Script: LinuxCliScript("/home/me/Applications/AI-Coding-Tool.AppImage")}) {
		t.Fatal(linux)
	}
	if got := CliCliConfiguration("linux", "/", ""); got == nil || got.InstallPath != "/.local/bin/aic" {
		t.Fatal(got)
	}
	if !strings.Contains(linux.Script, `exec xdg-open "$url"`) || !strings.Contains(linux.Script, `exec gio open "$url"`) {
		t.Fatal("openers")
	}
	if got := CliCliConfiguration("darwin", "/Users/me", "x"); got == nil || got.InstallPath != "/usr/local/bin/aic" {
		t.Fatal(got)
	}
	if CliCliConfiguration("win32", `C:\Users\me`, "") != nil || CliCliConfiguration("linux", "relative/home", "") != nil {
		t.Fatal("unsupported")
	}
}

func TestLinuxCliScriptOpensThroughTheDesktop(t *testing.T) {
	tools := map[string]string{}
	for _, tool := range []string{"base64", "tr"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skip(tool)
		}
		tools[tool] = path
	}
	cases := []struct {
		name      string
		gio       bool
		gioStatus int
		opener    string
		status    int
	}{
		{"prefers gio", true, 0, "gio", 0},
		{"uses xdg-open without gio", false, 0, "xdg-open", 0},
		{"reports gio failures", true, 42, "gio", 42},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			project := filepath.Join(root, "project ü")
			command := filepath.Join(root, "aic")
			opened := filepath.Join(root, "opened-url")
			cliMust(t, os.Mkdir(bin, 0o755), os.Mkdir(project, 0o755))
			cliMust(t, os.WriteFile(command, []byte(LinuxCliScript("/home/me/Applications/AI-Coding-Tool.AppImage")), 0o755),
				os.WriteFile(filepath.Join(bin, "xdg-open"), []byte("#!/bin/sh\nprintf \"xdg-open\\n%s\\n\" \"$1\" > \"$AIC_TEST_OUTPUT\"\n"), 0o755))
			if c.gio {
				cliMust(t, os.WriteFile(filepath.Join(bin, "gio"), []byte("#!/bin/sh\nprintf 'gio\\n%s\\n%s\\n' \"$1\" \"$2\" > \"$AIC_TEST_OUTPUT\"\nexit "+strconv.Itoa(c.gioStatus)+"\n"), 0o755))
			}
			for tool, path := range tools {
				cliMust(t, os.Symlink(path, filepath.Join(bin, tool)))
			}
			cmd := exec.Command(command, project)
			cmd.Env = append(os.Environ(), "AIC_TEST_OUTPUT="+opened, "PATH="+bin)
			err := cmd.Run()
			status := 0
			if exit, ok := err.(*exec.ExitError); ok {
				status = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if status != c.status {
				t.Fatalf("status %d", status)
			}
			output, err := os.ReadFile(opened)
			cliMust(t, err)
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			want := []string{c.opener, cliURLFor(project)}
			if c.opener == "gio" {
				want = []string{"gio", "open", cliURLFor(project)}
			}
			if !reflect.DeepEqual(lines, want) {
				t.Fatalf("got %q, want %q", lines, want)
			}
		})
	}
}

func TestCliScriptServesAndRestartsAfterAnUpdate(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app '$(false)")
	command := filepath.Join(root, "aic")
	seen := filepath.Join(root, "seen")
	restarted := filepath.Join(root, "restarted")
	cliMust(t, os.WriteFile(app, []byte(strings.Join([]string{
		"#!/bin/sh",
		`printf '%s|' "$ELECTRON_RUN_AS_NODE" "$` + ServeRestartsEnv + `" "$@" >> "` + seen + `"; echo >> "` + seen + `"`,
		`[ -e "` + restarted + `" ] && exit 3`,
		`: > "` + restarted + `"; exit ` + strconv.Itoa(ServeRestartExit),
		"",
	}, "\n")), 0o755), os.WriteFile(command, []byte(LinuxCliScript(app)), 0o755))
	runs := func() [][]string {
		output, err := os.ReadFile(seen)
		cliMust(t, err)
		var runs [][]string
		for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
			fields := strings.Split(line, "|")
			runs = append(runs, append(fields[:3:3], fields[4:]...))
		}
		return runs
	}
	exitCode := func(err error) int {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		cliMust(t, err)
		return 0
	}
	if status := exitCode(exec.Command(command, "serve", "--port", "0").Run()); status != 3 {
		t.Fatalf("serve status %d", status)
	}
	serve := []string{"1", "1", "-e", "--", "serve", "--port", "0", ""}
	if got := runs(); !reflect.DeepEqual(got, [][]string{serve, serve}) {
		t.Fatalf("serve runs %q", got)
	}
	cliMust(t, os.Remove(seen), os.Remove(restarted))
	if status := exitCode(exec.Command(command, "pair", "--dev").Run()); status != ServeRestartExit {
		t.Fatalf("pair status %d", status)
	}
	if got := runs(); !reflect.DeepEqual(got, [][]string{{"1", "", "-e", "--", "pair", "--dev", ""}}) {
		t.Fatalf("pair runs %q", got)
	}
}

func cliMust(t *testing.T, errs ...error) {
	t.Helper()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
