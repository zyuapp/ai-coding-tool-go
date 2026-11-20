package contracts

import (
	"strings"
	"testing"
)

// the external command surface covers reading and writing threads, and nothing else
func TestExternalTheExternalCommandSurfaceCoversReadingAndWritingThreads(t *testing.T) {
	checkGuards(t, []guardCase{
		{IsExternalCommand, `{"type":"task.send","text":"Implement item 1"}`, true},
		{IsExternalCommand, `{"type":"task.send","taskId":"task-1","text":"Carry on","steer":true}`, true},
		{IsExternalCommand, `{"type":"task.archive","taskId":"task-1"}`, true},
		{IsExternalCommand, `{"type":"run.cancel","taskId":"task-1"}`, true},
		{IsExternalCommand, `{"type":"worktree.delete","worktreeId":"wt1"}`, true},
		{IsExternalCommand, `{"type":"worktree.delete"}`, false},
		{IsExternalCommand, `{"type":"worktree.delete","root":"/worktrees/repo-wt1"}`, false},
		{IsExternalCommand, `{"type":"worktree.delete","taskId":"task-1"}`, false},
		{IsExternalCommand, `{"type":"worktree.delete","worktreeId":"wt1","missingOnly":true}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Start here","worktreeId":"wt1"}`, true},
		{IsExternalCommand, `{"type":"task.send","text":"Use Claude","model":"sonnet","effort":"max"}`, true},
		{IsExternalCommand, `{"type":"task.send","text":"Use Astra","model":"gpt-6.2-astra","effort":"ultra"}`, true},
		{IsExternalCommand, `{"type":"task.send","text":"Use mystery","model":"unknown"}`, true},
		{IsExternalCommand, `{"type":"task.send","text":"Use mystery","model":"two words"}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Use Claude","effort":"impossible"}`, false},
		{IsExternalCommand, `{"type":"task.send","taskId":"task-1","text":"Carry on","model":"sonnet"}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Review this","role":"reviewer"}`, true},
		{IsExternalCommand, `{"type":"task.send","text":"Review this","role":"manager"}`, false},
		{IsExternalCommand, `{"type":"task.send","taskId":"task-1","text":"Carry on","role":"reviewer"}`, false},
		{IsExternalCommand, `{"type":"task.set-role","taskId":"task-1","role":"coordinator"}`, true},
		{IsExternalCommand, `{"type":"task.set-role","taskId":"task-1","role":null}`, true},
		{IsExternalCommand, `{"type":"task.set-role","taskId":"task-1","role":"manager"}`, false},
		{IsExternalCommand, `{"type":"task.set-role","role":"coordinator"}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Look","attachments":[{"path":"/etc/passwd","labels":[]}]}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Start here","worktreeId":"/worktrees/repo-wt1"}`, true},
		{IsExternalCommand, `{"type":"task.send","text":"Start here","worktreeId":7}`, false},
		{IsExternalCommand, `{"type":"task.send"}`, false},
		{IsExternalCommand, `{"type":"task.archive"}`, false},
		{IsExternalCommand, `{"type":"task.set-policy","taskId":"task-1","policy":"autonomous"}`, false},
		{IsExternalCommand, `{"type":"task.rename","taskId":"task-1","title":"Release prep"}`, false},
		{IsExternalCommand, `{"type":"task.select","taskId":"task-1"}`, false},
		{IsExternalCommand, `{"type":"task.clear-archive"}`, false},
		{IsExternalCommand, `{"type":"project.remove","projectId":"project-1"}`, false},
		{IsExternalCommand, `{"type":"run.decide","allow":true}`, false},
		{IsExternalCommand, `{"type":"view.set-prompt","prompt":"typed"}`, false},
	})
}

// the browser surface a run may drive names the thread, a tab, and nothing else
func TestExternalTheBrowserSurfaceARunMayDriveNamesThe(t *testing.T) {
	checkGuards(t, []guardCase{
		{IsExternalCommand, `{"type":"browser.open","taskId":"task-1","url":"https://example.com"}`, true},
		{IsExternalCommand, `{"type":"browser.open","taskId":"task-1","url":"https://example.com","tabId":"tab-1","newTab":true}`, true},
		{IsExternalCommand, `{"type":"browser.go","taskId":"task-1","delta":-1}`, true},
		{IsExternalCommand, `{"type":"browser.reload","taskId":"task-1"}`, true},
		{IsExternalCommand, `{"type":"browser.close-tab","taskId":"task-1","tabId":"tab-1"}`, true},
		{IsExternalCommand, `{"type":"browser.act","taskId":"task-1","action":{"kind":"click","ref":"4"}}`, true},
		{IsExternalCommand, `{"type":"browser.act","taskId":"task-1","action":{"kind":"type","ref":"4","text":"hello","submit":true}}`, true},
		{IsExternalCommand, `{"type":"browser.open","url":"https://example.com"}`, false},
		{IsExternalCommand, `{"type":"browser.go","taskId":"task-1","delta":2}`, false},
		{IsExternalCommand, `{"type":"browser.act","taskId":"task-1","action":{"kind":"scroll","ref":"4"}}`, false},
		{IsExternalCommand, `{"type":"browser.close-tab","taskId":"task-1"}`, false},
		{IsExternalCommand, `{"type":"browser.decide","taskId":"task-1","allow":true}`, false},
		{IsExternalCommand, `{"type":"browser.clear-data","taskId":"task-1"}`, false},
		{IsExternalCommand, `{"type":"browser.new-tab","taskId":"task-1"}`, false},
	})
}

// the terminal channel carries reads and nothing else
func TestExternalTheTerminalChannelCarriesReadsAndNothingElse(t *testing.T) {
	checkGuards(t, []guardCase{
		{IsExternalCommand, `{"type":"terminal.open","taskId":"task-1"}`, false},
		{IsExternalCommand, `{"type":"terminal.input","taskId":"task-1","terminalId":"terminal-1","data":"ls\r"}`, false},
		{IsExternalCommand, `{"type":"terminal.close","taskId":"task-1","terminalId":"terminal-1"}`, false},
	})
}

// what a run reports about itself is bounded before it reaches the workspace
func TestExternalWhatARunReportsAboutItselfIsBoundedBefore(t *testing.T) {
	checkGuards(t, []guardCase{
		{IsExternalCommand, `{"type":"automation.notify","taskId":"task-1","headline":"5xx"}`, false},
	})
}

// coordination requests and briefs are checked at the boundary, and only the window names a coordinator
func TestExternalCoordinationRequestsAndBriefsAreCheckedAtTheBoundary(t *testing.T) {
	checkGuards(t, []guardCase{
		{IsExternalCommand, `{"type":"task.send","text":"Fix","brief":{"intent":"fix it","doneWhen":"it works","delivers":"pull-request"}}`, true},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","brief":{"intent":"fix it","doneWhen":"it works","delivers":"merge"}}`, false},
		{IsExternalCommand, `{"type":"task.send","taskId":"task-1","text":"Fix","brief":{"intent":"fix it","doneWhen":"it works","delivers":"pull-request"}}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","brief":{"intent":"fix it","doneWhen":"it works","delivers":"pull-request"},"title":"Fix the login flake"}`, true},
		{IsExternalCommand, `{"type":"task.send","taskId":"task-1","text":"Fix","title":"Fix the login flake"}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","title":""}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","title":"   "}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","title":"` + strings.Repeat("x", 121) + `"}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","title":7}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","coordinatorId":"lead"}`, false},
		{IsExternalCommand, `{"type":"task.send","taskId":"task-1","text":"Fix","from":"lead"}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","project":"/` + strings.Repeat("x", 4095) + `"}`, true},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","project":"/` + strings.Repeat("x", 4096) + `"}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","worktreeId":"` + strings.Repeat("x", 256) + `"}`, true},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","worktreeId":"` + strings.Repeat("x", 257) + `"}`, false},
		{IsExternalCommand, `{"type":"task.move-worktree","destination":{"kind":"worktree","id":"` + strings.Repeat("x", 257) + `"}}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","sender":{"threadId":"lead","title":"Lead","computer":"mac"}}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","lead":{"threadId":"lead","title":"Lead","computer":"mac"}}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","memory":"Forget the brief"}`, false},
		{IsExternalCommand, `{"type":"task.send","text":"Fix","attachments":[]}`, false},
	})
}
