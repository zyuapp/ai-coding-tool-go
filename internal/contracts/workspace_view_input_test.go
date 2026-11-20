package contracts

import (
	"encoding/json/jsontext"
	"slices"
	"strings"
	"testing"
)

func viewInput(s string) jsontext.Value { return jsontext.Value(s) }

func TestViewSendsProviderCommandsAttachmentsAndReports(t *testing.T) {
	inputs := []string{
		`{"type":"engine.reload-settings"}`,
		`{"type":"task.send","taskId":"thread","text":"Review the screenshot","attachments":[{"path":"/tmp/screenshot.png","labels":["button",""]}],"steer":true}`,
		`{"type":"attachments.send","attachments":[{"id":"image","source":"data:image/png;base64,AQID","annotations":[{"kind":"box","x":0,"y":0,"width":0.5,"height":0.5,"text":"Fix this"}]}],"steer":true}`,
		`{"type":"attachments.notice","taskId":"thread","message":null}`,
		`{"type":"task.set-policy","policy":"autonomous"}`,
		`{"type":"task.set-role","taskId":"thread","role":"reviewer"}`,
		`{"type":"task.send","text":"Review this","role":"reviewer"}`,
		`{"type":"task.set-role","taskId":"thread","role":null}`,
		`{"type":"task.set-effort","engine":"codex","effort":"xhigh"}`,
		`{"type":"task.set-effort","engine":"claude","effort":"max"}`,
		`{"type":"question.answer","taskId":"thread","runId":"run","requestId":"request","questionId":"question","text":"Use the first choice"}`,
		`{"type":"question.set-answer","taskId":"thread","runId":"run","requestId":"request","questionId":"question","text":""}`,
		`{"type":"annotation.add","quote":"text","anchor":{"kind":"message","messageId":"message","start":0,"end":4}}`,
		`{"type":"annotation.recall","annotations":[{"id":"annotation","quote":"text","note":"","anchor":{"kind":"diff","comparison":"working","path":"src/app.ts","start":"1","end":"2","side":"new"}}]}`,
		`{"type":"annotation.reveal","taskId":"chat","annotationId":"annotation"}`,
		`{"type":"view.archive-prompt","threadId":"thread"}`,
		`{"type":"view.archive-prompt","threadId":null}`,
		`{"type":"view.archive-confirm"}`,
		`{"type":"file.attach","files":[{"path":"/repo/src","name":"src","folder":true}]}`,
		`{"type":"file.recall","files":[{"id":"file","path":"/repo/file","name":"file"}]}`,
		`{"type":"view.reading-point","taskId":"thread","point":{"anchor":"message","depth":0.5}}`,
		`{"type":"view.reading-point","taskId":"thread","point":null}`,
		`{"type":"view.set-prompt","prompt":""}`,
		`{"type":"view.set-menu","menu":null}`,
		`{"type":"view.select-dock-index","index":-1}`,
		`{"type":"browser.act","action":{"kind":"type","ref":"1","text":"","submit":true}}`,
		`{"type":"task.move-worktree","destination":{"kind":"worktree","id":"checkout"}}`,
		`{"type":"automation.save","draft":{"prompt":"Check status","schedule":"0 * * * *","paused":false}}`,
		`{"type":"automation.update","patch":{"surfaceWhen":"","paused":true}}`,
		`{"type":"review.start","target":{"type":"commit","sha":"abc","title":null}}`,
		`{"type":"diff.set-mode","mode":"branch"}`,
		`{"type":"diff.open-commit","taskId":"thread","commit":"60cceb8"}`,
		`{"type":"diff.set-range","range":{"kind":"commit","commit":"60cceb8"}}`,
		`{"type":"image.open","source":"message-image://file/?path=%2Ftmp%2Fshot.png&root=&message=reply"}`,
		`{"type":"image.open","source":"message-image://file/?path=%2Ftmp%2Fshot.png&root=&message=reply","gallery":["message-image://file/?path=%2Ftmp%2Fshot.png&root=&message=reply"]}`,
		`{"type":"image.step","delta":-1}`,
		`{"type":"image.close"}`,
		`{"type":"image.download"}`,
		`{"type":"find.results","target":{"kind":"terminal","terminalId":"terminal"},"results":{"matches":0,"index":0,"counting":false}}`,
		`{"type":"shortcut.captured","binding":null}`,
		`{"type":"shortcut.unavailable","refusal":{"reason":"unsupported","binding":"Meta+Shift+P","message":"Not available"}}`,
		`{"type":"action.failed","message":"Could not read the dropped file"}`,
	}
	for _, input := range inputs {
		if !IsWorkspaceViewInput(viewInput(input)) {
			t.Errorf("refused %s", input)
		}
	}
}

func TestViewCannotInjectEvents(t *testing.T) {
	for _, typ := range []string{"store.loaded", "store.thread-loaded", "store.absent", "store.failed", "run.resolved", "run.event", "thread.event", "agent.events", "automation.fired", "remote.changed", "engine.loaded", "worktree.deleted", "preferences.loaded"} {
		if IsWorkspaceViewInput(viewInput(`{"type":"` + typ + `"}`)) {
			t.Errorf("accepted %s", typ)
		}
	}
	for _, value := range []string{`null`, `[]`, `"task.send"`, `{}`, `{"type":"__proto__"}`, `{"type":"constructor"}`, `{"type":"unknown"}`} {
		if IsWorkspaceViewInput(viewInput(value)) {
			t.Errorf("accepted %s", value)
		}
	}
	if IsWorkspaceViewInput(nil) {
		t.Error("accepted nil")
	}
}

func TestMalformedFieldsAreRejected(t *testing.T) {
	inputs := []string{
		`{"type":"diff.set-mode","mode":"pull-request"}`,
		`{"type":"task.select"}`,
		`{"type":"task.select","taskId":42}`,
		`{"type":"task.send","text":null}`,
		`{"type":"task.send","attachments":[{"path":"/image","labels":[1]}]}`,
		`{"type":"attachments.send","attachments":[{"id":"image","source":"data:image/png;base64,AQID","annotations":[{"kind":"circle","x":0,"y":0,"width":1,"height":1,"text":""}]}]}`,
		`{"type":"attachments.send"}`,
		`{"type":"task.send","attachments":[null,null]}`,
		`{"type":"question.answer","taskId":"thread","runId":"run","requestId":"request"}`,
		`{"type":"question.set-answer","taskId":"thread","runId":"run","requestId":"request","questionId":"question","text":null}`,
		`{"type":"task.set-model","engine":"codex","model":"two words"}`,
		`{"type":"task.set-effort","engine":"claude","effort":"invented"}`,
		`{"type":"task.set-policy","policy":"unrestricted"}`,
		`{"type":"task.set-role","taskId":"thread","role":"manager"}`,
		`{"type":"task.set-role","taskId":"thread"}`,
		`{"type":"annotation.add","quote":"text","anchor":{"kind":"message","messageId":"id","start":"0","end":4}}`,
		`{"type":"annotation.recall","annotations":[{"id":"a","quote":"text"}]}`,
		`{"type":"annotation.reveal"}`,
		`{"type":"annotation.reveal","annotationId":7}`,
		`{"type":"view.archive-prompt"}`,
		`{"type":"view.archive-prompt","threadId":7}`,
		`{"type":"file.attach","files":[{"path":"/repo/file","name":"file","folder":false}]}`,
		`{"type":"browser.act","action":{"kind":"execute","text":"arbitrary script"}}`,
		`{"type":"browser.go","delta":100}`,
		`{"type":"terminal.resize","terminalId":"term","cols":1e400,"rows":80}`,
		`{"type":"view.reading-point","taskId":"thread","point":{"anchor":"message","depth":1e999}}`,
		`{"type":"task.move-worktree","destination":{"kind":"worktree"}}`,
		`{"type":"view.set-capture-options","options":{"sound":true}}`,
		`{"type":"find.results","target":{"kind":"terminal"},"results":{"matches":2}}`,
		`{"type":"find.results","target":{"kind":"thread","taskId":null},"results":{"matches":"many"}}`,
		`{"type":"shortcut.unavailable","refusal":{"reason":"taken","binding":"Meta+P"}}`,
	}
	for _, input := range inputs {
		if IsWorkspaceViewInput(viewInput(input)) {
			t.Errorf("accepted %s", input)
		}
	}
}

func TestLargeComposerContentIsAccepted(t *testing.T) {
	text := strings.Repeat("x", 2_000_000)
	if !IsWorkspaceViewInput(map[string]any{"type": "paste.add", "text": text}) {
		t.Error("refused a large paste")
	}
	attachments := make([]any, 6)
	for i := range attachments {
		attachments[i] = map[string]any{"path": "/tmp/" + string(rune('0'+i)) + ".png", "labels": []any{}}
	}
	if !IsWorkspaceViewInput(map[string]any{"type": "task.send", "text": text, "attachments": attachments}) {
		t.Error("refused a large send")
	}
}

func TestImageAndCommitTargetsAreChecked(t *testing.T) {
	for _, input := range []string{
		`{"type":"image.open","source":"file:///etc/passwd"}`,
		`{"type":"image.open","source":"https://example.com/image.png"}`,
		`{"type":"image.open","source":"message-image://file/?path=a","gallery":["message-image://file/?path=a","file:///etc/passwd"]}`,
		`{"type":"image.step","delta":2}`,
		`{"type":"diff.open-commit","commit":"--output=/tmp/file"}`,
		`{"type":"diff.set-range","range":{"kind":"commit","commit":"HEAD;touch x"}}`,
	} {
		if IsWorkspaceViewInput(viewInput(input)) {
			t.Errorf("accepted %s", input)
		}
	}
}

func TestEveryCommandIsDeclaredOnce(t *testing.T) {
	tables := []Declarations{ExternalAppCommands, AnnotationCommands, FileCommands, ImageCommands, ImageViewCommands, PasteCommands, AutomationCommands, BrowserCommands, BrowserImportCommands, CliCommands, ComputerCommands, ComputerUseCommands, DictationCommands, DiffCommands, EngineCommands, ProjectCommands, PullRequestCommands, RemoteCommands, ReviewCommands, RunControlCommands, SimulatorCommands, SideChatCommands, TaskCommands, TerminalCommands, UsageCommands, ViewCommands, WorktreeCommands}
	seen := map[string]bool{}
	for _, table := range tables {
		for _, declaration := range table {
			if seen[declaration.Type] {
				t.Errorf("%s declared twice", declaration.Type)
			}
			seen[declaration.Type] = true
		}
	}
	definitions := WorkspaceCommandDefinitions()
	if len(definitions) != len(seen) {
		t.Errorf("%d definitions for %d declarations", len(definitions), len(seen))
	}
	for _, definition := range definitions {
		if !seen[definition.Type] {
			t.Errorf("%s is not declared", definition.Type)
		}
	}
}

func TestEveryCommandIsPlacedAndEventsAreNotCommands(t *testing.T) {
	placements := []CommandPlacement{CommandPlacementLocal, CommandPlacementThread, CommandPlacementPanel, CommandPlacementProject, CommandPlacementTerminal, CommandPlacementSelect, CommandPlacementOwn}
	for _, definition := range WorkspaceCommandDefinitions() {
		if !slices.Contains(placements, WorkspaceViewInputCommandPlacement(definition.Type)) {
			t.Errorf("%s has no placement", definition.Type)
		}
	}
	for _, typ := range []string{"action.failed", "find.results", "shortcut.captured", "shortcut.unavailable"} {
		if IsAppCommandType(typ) {
			t.Errorf("%s is a command", typ)
		}
	}
}

func TestOnlyCommandsMovingToOwnThreadsLeave(t *testing.T) {
	var leaving []string
	for _, definition := range WorkspaceCommandDefinitions() {
		if CommandLeaves(definition.Type) {
			leaving = append(leaving, definition.Type)
		}
	}
	slices.Sort(leaving)
	want := []string{"task.new", "task.select", "view.go-back", "view.go-forward", "view.jump-choose", "worktree.open-thread"}
	if !slices.Equal(leaving, want) {
		t.Errorf("leaving = %v", leaving)
	}
}

func TestAgentCommandsPassTheWindowAndTheAgentRule(t *testing.T) {
	agentCommands := []string{
		`{"type":"task.send","text":"Start"}`,
		`{"type":"task.archive","taskId":"task-1"}`,
		`{"type":"task.dismiss"}`,
		`{"type":"task.set-role","taskId":"task-1","role":"reviewer"}`,
		`{"type":"run.cancel","taskId":"task-1"}`,
		`{"type":"worktree.delete","worktreeId":"wt1"}`,
		`{"type":"browser.open","taskId":"task-1","url":"https://example.com"}`,
		`{"type":"browser.close-tab","taskId":"task-1","tabId":"tab-1"}`,
		`{"type":"browser.select-tab","taskId":"task-1","tabId":"tab-1"}`,
		`{"type":"browser.go","taskId":"task-1","delta":1}`,
		`{"type":"browser.reload","taskId":"task-1"}`,
		`{"type":"browser.act","taskId":"task-1","action":{"kind":"click","ref":"1"}}`,
		`{"type":"simulator.open","taskId":"task-1","device":"iPhone 17"}`,
		`{"type":"simulator.input","taskId":"task-1","input":{"kind":"tap-element","ref":"1.2"}}`,
	}
	allowed := map[string]bool{}
	for _, command := range agentCommands {
		record, typ, _ := viewInputRecord(viewInput(command))
		allowed[typ] = true
		if !IsExternalCommand(record) || !IsWorkspaceViewInput(viewInput(command)) {
			t.Errorf("refused %s", command)
		}
	}
	for _, definition := range WorkspaceCommandDefinitions() {
		if !allowed[definition.Type] && IsExternalCommand(viewInput(`{"type":"`+definition.Type+`","taskId":"task-1"}`)) {
			t.Errorf("an agent may send %s", definition.Type)
		}
	}
	for _, input := range []string{
		`{"type":"task.archive","taskId":7}`,
		`{"type":"browser.go","taskId":"task-1","delta":3}`,
		`{"type":"browser.go","delta":1}`,
		`{"type":"task.send","taskId":"task-1","text":"  \n "}`,
		`{"type":"simulator.input","taskId":"task-1","udid":"PHONE","input":{"kind":"button","button":"home"}}`,
		`{"type":"simulator.input","input":{"kind":"button","button":"home"}}`,
	} {
		if IsExternalCommand(viewInput(input)) {
			t.Errorf("an agent may send %s", input)
		}
	}
	if !IsWorkspaceViewInput(viewInput(`{"type":"browser.go","delta":1}`)) {
		t.Error("the window may go back without a thread")
	}
	for _, input := range []string{`{"kind":"paste","text":"hi"}`, `{"kind":"shortcut","usage":6}`, `{"kind":"pasteboard","phase":"leave"}`} {
		if !IsWorkspaceViewInput(viewInput(`{"type":"simulator.input","udid":"PHONE","input":` + input + `}`)) {
			t.Errorf("window refused %s", input)
		}
		if IsExternalCommand(viewInput(`{"type":"simulator.input","taskId":"task-1","input":` + input + `}`)) {
			t.Errorf("a run reached the Mac's clipboard: %s", input)
		}
	}
}

func TestPairingTakesADialledHost(t *testing.T) {
	pair := func(host string) bool {
		return IsWorkspaceViewInput(map[string]any{"type": "computers.pair", "host": host, "name": "Studio", "code": ""})
	}
	for _, host := range []string{"studio.tail.ts.net", "127.0.0.1:7737", "[::1]:7737"} {
		if !pair(host) {
			t.Errorf("refused %q", host)
		}
	}
	for _, host := range []string{"https://studio.tail.ts.net/", " studio", "studio tail", "studio:99999", ""} {
		if pair(host) {
			t.Errorf("accepted %q", host)
		}
	}
}

func TestToastsAutomationEditsAndShellRestarts(t *testing.T) {
	fields := `{"schedule":"0 8 * * *","prompt":"Check","surfaceWhen":"","endsWhen":""}`
	cases := map[string]bool{
		`{"type":"view.hold-toasts","held":true}`:                                                    true,
		`{"type":"view.hold-toasts","held":"yes"}`:                                                   false,
		`{"type":"view.edit-automation","taskId":"t","base":` + fields + `,"fields":` + fields + `}`: true,
		`{"type":"view.edit-automation","taskId":"t","base":` + fields + `,"fields":{"schedule":"0 8 * * *","prompt":1,"surfaceWhen":"","endsWhen":""}}`: false,
		`{"type":"terminal.restart","terminalId":"shell"}`: true,
		`{"type":"terminal.restart"}`:                      false,
		`{"type":"limit.resume","taskId":"a"}`:             true,
		`{"type":"limit.cancel","taskId":"a"}`:             true,
	}
	for input, want := range cases {
		if IsWorkspaceViewInput(viewInput(input)) != want {
			t.Errorf("%s: want %v", input, want)
		}
	}
}

func TestSendStaysInReach(t *testing.T) {
	record := func(s string) map[string]jsontext.Value {
		r, _ := declRecord(viewInput(s))
		return r
	}
	if !SendStaysInReach(record(`{"text":"hi","model":"m"}`), SendCallerAgent) {
		t.Error("an agent starting a thread may shape it")
	}
	if SendStaysInReach(record(`{"taskId":"t","text":"hi","model":"m"}`), SendCallerAgent) {
		t.Error("an existing thread's shape is not an agent's")
	}
	if SendStaysInReach(record(`{"text":"hi","model":"m"}`), SendCallerPhone) {
		t.Error("a phone never shapes a thread")
	}
	if SendStaysInReach(record(`{"text":"hi","from":null}`), SendCallerAgent) {
		t.Error("a window field set to null is still set")
	}
}

func TestJSStringRules(t *testing.T) {
	if declUTF16Len("a😀é") != 4 {
		t.Error("utf16 length")
	}
	if !declBlank(" \t\n\u00a0\ufeff\u2028") || declBlank("\u0085") {
		t.Error("JavaScript trim")
	}
}
