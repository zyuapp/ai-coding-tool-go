package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

type capture struct {
	args  []string
	input string
	cwd   string
}

// Stands in for the binary: records the call and writes what the last message would have been.
func fakeExec(lastMessage *string, held *capture) CodexExec {
	return func(_ context.Context, args []string, input, cwd string) error {
		if held != nil {
			held.args, held.input, held.cwd = args, input, cwd
		}
		output := args[slices.Index(args, "--output-last-message")+1]
		if lastMessage != nil {
			return os.WriteFile(output, []byte(*lastMessage), 0o644)
		}
		return nil
	}
}

func suggest(t *testing.T, text string, attachments []string, exec CodexExec) *string {
	t.Helper()
	title, err := SuggestCodexTitle(context.Background(), text, attachments, exec)
	must(t, err)
	return title
}

func TestNamingAThreadOnCodexRunsOneReadOnlyEphemeralExecHeldToATitleSchema(t *testing.T) {
	held := &capture{}
	title := suggest(t, "Inspect the app and tell me what breaks", nil, fakeExec(ptr(`{"title":"App breakage review."}`), held))
	if title == nil || *title != "App breakage review" {
		t.Fatalf("title = %v", title)
	}
	args := held.args
	assertJSON(t, args[:4], []string{"exec", "--ephemeral", "--ignore-user-config", "--skip-git-repo-check"})
	at := func(flag string) string { return args[slices.Index(args, flag)+1] }
	if at("-s") != "read-only" || at("-m") != "gpt-6-luna" || at("-c") != `model_reasoning_effort="low"` || args[len(args)-1] != "-" || slices.Contains(args, "-i") {
		t.Fatalf("args = %v", args)
	}
	if !regexp.MustCompile(`^You title chat threads\.`).MatchString(held.input) || !regexp.MustCompile(`<message>\nInspect the app and tell me what breaks\n</message>`).MatchString(held.input) {
		t.Fatalf("input = %q", held.input)
	}
	if _, err := os.Stat(at("--output-schema")); err == nil {
		t.Fatal("the schema and the answer are cleaned up with the run")
	}
	if held.cwd != filepath.Dir(at("--output-schema")) {
		t.Fatal("the run happens in its own scratch folder")
	}
}

func TestALongMessageIsCutBeforeItIsSentAndALongAnswerIsCutToATitle(t *testing.T) {
	held := &capture{}
	long := make([]byte, 5_000)
	for index := range long {
		long[index] = 'x'
	}
	answer := `{"title":"` + string(long[:80]) + `"}`
	title := suggest(t, string(long), nil, fakeExec(&answer, held))
	if title == nil || len(*title) >= 80 || len(held.input) >= 2_500 {
		t.Fatalf("title = %v, input %d long", title, len(held.input))
	}
}

func TestNothingUsableComesBackAsNoTitle(t *testing.T) {
	for _, c := range []struct {
		text   string
		answer *string
		exec   CodexExec
	}{
		{"hi", ptr(`{"title":"   "}`), nil},
		{"Reply Ready and await my first request", ptr(`{"title":"Ready Reply"}`), nil},
		{"Add multiply to math.js", ptr(`{"title":"Thread naming request"}`), nil},
		{"hi", ptr("not json"), nil},
		{"hi", ptr(`{"name":"x"}`), nil},
		{"hi", nil, nil},
		{"hi", nil, func(context.Context, []string, string, string) error { return errors.New("spawn ENOENT") }},
		{"   ", ptr(`{"title":"x"}`), nil},
	} {
		exec := c.exec
		if exec == nil {
			exec = fakeExec(c.answer, nil)
		}
		if title := suggest(t, c.text, nil, exec); title != nil {
			t.Errorf("%q answered %v gave %q", c.text, c.answer, *title)
		}
	}
}

func TestScreenshotsRideAlongAsImagesSkippingMissingAndOversizedOnes(t *testing.T) {
	directory := t.TempDir()
	shot := filepath.Join(directory, "shot.png")
	writeText(t, shot, "png")
	oversized := filepath.Join(directory, "big.png")
	must(t, os.WriteFile(oversized, make([]byte, 1024*1024+1), 0o644))
	held := &capture{}
	title := suggest(t, "", []string{shot, filepath.Join(directory, "missing.png"), oversized}, fakeExec(ptr(`{"title":"Sidebar overlap"}`), held))
	if title == nil || *title != "Sidebar overlap" {
		t.Fatalf("title = %v", title)
	}
	var images []string
	for index, arg := range held.args {
		if index > 0 && held.args[index-1] == "-i" {
			images = append(images, arg)
		}
	}
	assertJSON(t, images, []string{shot})
	if !regexp.MustCompile(`Write the title of the thread that starts with these screenshots\.`).MatchString(held.input) {
		t.Fatalf("input = %q", held.input)
	}
}
