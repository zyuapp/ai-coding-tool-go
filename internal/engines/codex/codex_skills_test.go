package codex

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

func TestTheCodexMenuOffersEnabledSkillsThatSlashInvocationResolves(t *testing.T) {
	var mu sync.Mutex
	imagegenEnabled := true
	skills := func() string {
		mu.Lock()
		defer mu.Unlock()
		enabled := "true"
		if !imagegenEnabled {
			enabled = "false"
		}
		return `{"data":[{"cwd":"/tmp/project","errors":[],"skills":[
			{"name":"imagegen","description":"Generate images.","path":"/Users/me/.codex/skills/.system/imagegen/SKILL.md","scope":"system","enabled":` + enabled + `,"pluginId":null},
			{"name":"slides","description":"Create slides.","path":"/Users/me/.codex/plugins/cache/slides/SKILL.md","scope":"user","enabled":true,"pluginId":"slides@marketplace"},
			{"name":"disabled","description":"Disabled skill.","path":"/Users/me/.agents/skills/disabled/SKILL.md","scope":"user","enabled":false,"pluginId":null}]}]}`
	}
	release := make(chan struct{})
	c := &connector{script: script{"skills/list": func(context.Context, jsontext.Value) (any, error) {
		<-release
		return jsontext.Value(skills()), nil
	}}}
	results := make(chan []*contracts.AvailableCommand, 2)
	for range 2 {
		go func() {
			commands, err := DiscoverCodexCommands(context.Background(), "/tmp/project", c.connect, 0)
			must(t, err)
			results <- commands
		}()
	}
	tick()
	close(release)
	for range 2 {
		assertJSON(t, <-results, `[{"name":"imagegen","description":"Generate images.","argumentHint":""},{"name":"slides","description":"Create slides.","argumentHint":""}]`)
	}
	if len(c.all()) != 1 {
		t.Fatalf("concurrent reads share one server: %d", len(c.all()))
	}
	client := c.all()[0]
	if !client.isClosed() || client.command.Cwd != "/tmp/project" {
		t.Fatal("the server runs in the workspace and is closed")
	}
	if !slices.ContainsFunc(client.command.Args, func(arg string) bool {
		return strings.HasPrefix(arg, "plugins=") && strings.Contains(arg, "browser@openai-bundled")
	}) {
		t.Fatal("the bundled plugins are configured as for a run")
	}
	assertJSON(t, client.methods(), []string{"initialize", "skills/list"})
	assertJSON(t, client.calls("skills/list"), `[{"cwds":["/tmp/project"],"forceReload":true}]`)

	session := NewCodexSkills(client, "/tmp/project")
	<-session.Refresh(true)
	prompt := "/imagegen /slides /disabled"
	assertJSON(t, session.Input(prompt), `[
		{"type":"skill","name":"imagegen","path":"/Users/me/.codex/skills/.system/imagegen/SKILL.md"},
		{"type":"skill","name":"slides","path":"/Users/me/.codex/plugins/cache/slides/SKILL.md"},
		{"type":"text","text":"/imagegen /slides /disabled","text_elements":[]}]`)

	mu.Lock()
	imagegenEnabled = false
	mu.Unlock()
	commands, err := DiscoverCodexCommands(context.Background(), "/tmp/project", c.connect, 0)
	must(t, err)
	assertJSON(t, commands, `[{"name":"slides","description":"Create slides.","argumentHint":""}]`)
	if len(c.all()) != 2 {
		t.Fatal("the next refresh asks Codex again")
	}
}

const appSkillJSON = `{"data":[{"cwd":"/tmp/project","errors":[],"skills":[{"name":"aicodingtool:review-thread","description":"Review this thread's work.","path":"/app/resources/app-plugin/skills/review-thread/SKILL.md","scope":"user","enabled":true,"pluginId":null}]}]}`

func TestTheAppsSkillsAreReadFromItsPluginAndASlashByShortNameSendsTheQualifiedSkill(t *testing.T) {
	SetAppPluginRoot("/app/resources/app-plugin")
	defer SetAppPluginRoot("")
	skills := script{"skills/extraRoots/set": reply(`{}`), "skills/list": reply(appSkillJSON)}
	c := &connector{script: skills}
	commands, err := DiscoverCodexCommands(context.Background(), "/tmp/project", c.connect, 0)
	must(t, err)
	assertJSON(t, commands, `[{"name":"review-thread","description":"Review this thread's work.","argumentHint":""}]`)
	assertJSON(t, c.all()[0].methods(), []string{"initialize", "skills/extraRoots/set", "skills/list"})
	assertJSON(t, c.all()[0].calls("skills/extraRoots/set"), `[{"extraRoots":["/app/resources/app-plugin/skills"]}]`)

	h := newHarness(t, skills, harnessOptions{})
	client, _ := h.turn(input(func(in *agentInput) { in.Prompt = "/review-thread codex security only" }), nil)
	assertJSON(t, client.methods()[:3], []string{"initialize", "skills/extraRoots/set", "skills/list"})
	assertJSON(t, decodeAny(client.calls("turn/start")[0]).(map[string]any)["input"], `[
		{"type":"skill","name":"aicodingtool:review-thread","path":"/app/resources/app-plugin/skills/review-thread/SKILL.md"},
		{"type":"text","text":"/review-thread codex security only","text_elements":[]}]`)
}

func TestSkillDiscoveryClosesFailedOrStalledServersAndPermitsALaterRetry(t *testing.T) {
	failed := &connector{script: script{"skills/list": refuse(errors.New("Skill discovery failed."))}}
	if _, err := DiscoverCodexCommands(context.Background(), "/tmp/project", failed.connect, 0); err == nil || err.Error() != "Skill discovery failed." {
		t.Fatalf("err = %v", err)
	}
	if !failed.all()[0].isClosed() {
		t.Fatal("a failed server is closed")
	}
	stalled := &connector{script: script{}, handshake: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
	if _, err := DiscoverCodexCommands(context.Background(), "/tmp/project", stalled.connect, 5*time.Millisecond); err == nil || !strings.Contains(err.Error(), "did not return its skills in time") {
		t.Fatalf("err = %v", err)
	}
	closedEventually(t, stalled.all()[0])
	if _, err := DiscoverCodexCommands(context.Background(), "/tmp/project", failed.connect, 0); err == nil {
		t.Fatal("a retry asks again")
	}
	if len(failed.all()) != 2 {
		t.Fatal("a retry opens a server of its own")
	}
}

func TestASlashSkillAnywhereInThePromptIsSentAsANativeSkill(t *testing.T) {
	h := newHarness(t, script{"skills/list": reply(`{"data":[{"cwd":"/tmp/project","errors":[],"skills":[{"name":"suggest-qa-plan","description":"Suggest a QA plan.","path":"/Users/me/.agents/skills/suggest-qa-plan/SKILL.md","scope":"user","enabled":true,"pluginId":null}]}]}`)}, harnessOptions{})
	client, _ := h.turn(input(func(in *agentInput) { in.Prompt = "read the changes and /suggest-qa-plan" }), nil)
	assertJSON(t, client.calls("skills/list"), `[{"cwds":["/tmp/project"],"forceReload":true}]`)
	assertJSON(t, client.calls("turn/start")[0], `{"threadId":"thread-1","input":[
		{"type":"skill","name":"suggest-qa-plan","path":"/Users/me/.agents/skills/suggest-qa-plan/SKILL.md"},
		{"type":"text","text":"read the changes and /suggest-qa-plan","text_elements":[]}],
		"model":"gpt-6.2-sol","effort":"high","serviceTier":"default","approvalPolicy":"untrusted","approvalsReviewer":"user","sandboxPolicy":{"type":"readOnly","networkAccess":false}}`)
}

func TestALiveSessionRefreshesNativeSkillsAfterCodexReportsAChange(t *testing.T) {
	var mu sync.Mutex
	skills := `[]`
	h := newHarness(t, script{"skills/list": func(context.Context, jsontext.Value) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		return jsontext.Value(`{"data":[{"cwd":"/tmp/project","skills":` + skills + `,"errors":[]}]}`), nil
	}}, harnessOptions{})
	first, _ := h.turn(input(), nil)
	mu.Lock()
	skills = `[{"name":"later","description":"Added later.","path":"/Users/me/.agents/skills/later/SKILL.md","scope":"user","enabled":true,"pluginId":null}]`
	mu.Unlock()
	first.notify("skills/changed", map[string]any{})
	sentBy(t, first, "skills/list", 2)
	second, _ := h.turn(input(continuing("thread-1"), func(in *agentInput) { in.Prompt = "use /later" }), nil)
	if second != first {
		t.Fatal("the warm session takes the turn")
	}
	assertJSON(t, decodeAny(first.calls("turn/start")[1]).(map[string]any)["input"], `[
		{"type":"skill","name":"later","path":"/Users/me/.agents/skills/later/SKILL.md"},
		{"type":"text","text":"use /later","text_elements":[]}]`)
}
