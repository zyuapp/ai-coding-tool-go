package application

import (
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func panelsBrowserEffect(kind string) bool {
	return strings.HasPrefix(kind, "browser") && kind != "browser.permissions"
}

func TestTheUsersOwnPageVisitOpensADockTabAndAllowsItsOrigin(t *testing.T) {
	panelsPorted(t, func() {
		opened, transition := panelsReduce(t, panelsWorkspace(), `{"type":"browser.open","url":"github.com/zyuapp/ai-coding-tool"}`)
		tab := panelsDock(opened).BrowserTabs[0]
		if tab.URL != "https://github.com/zyuapp/ai-coding-tool" || !tab.Loading || deref(panelsDock(opened).BrowserTabID) != tab.ID {
			t.Fatalf("the page loads in a new tab: %+v", tab)
		}
		panelsEqual(t, opened.BrowserOrigins, `["https://github.com"]`, "the user's own visit allows the origin")
		if !panelsDock(opened).Open || panelsDock(opened).Tab != tab.ID {
			t.Error("a page is a tab in the dock, and lands where the user can see it")
		}
		panelsEqual(t, panelsEffects(t, transition, panelsBrowserEffect),
			`[{"type":"browser.open","tabId":"`+tab.ID+`","url":"https://github.com/zyuapp/ai-coding-tool"},{"type":"browser.show","tabId":"`+tab.ID+`"}]`, "the page opens and shows")

		navigated, transition := panelsReduce(t, opened, `{"type":"browser.open","url":"https://github.com/zyuapp/ai-coding-tool/pulls"}`)
		if len(panelsDock(navigated).BrowserTabs) != 1 {
			t.Error("the tab on screen is reused")
		}
		panelsEqual(t, panelsEffectOf(t, transition, "browser.navigate"), `{"type":"browser.navigate","tabId":"`+tab.ID+`","url":"https://github.com/zyuapp/ai-coding-tool/pulls"}`, "it navigates")

		another, _ := panelsReduce(t, navigated, `{"type":"browser.open","url":"https://example.com","newTab":true}`)
		if len(panelsDock(another).BrowserTabs) != 2 {
			t.Error("a new tab is asked for")
		}
		panelsEqual(t, another.BrowserOrigins, `["https://github.com","https://example.com"]`, "each origin visited is allowed")
	})
}

func TestAPageTheBrowserCannotOpenIsRefused(t *testing.T) {
	panelsPorted(t, func() {
		refused, transition := panelsReduce(t, panelsWorkspace(), `{"type":"browser.open","url":"file:///etc/passwd"}`)
		if len(panelsDock(refused).BrowserTabs) != 0 || len(transition.Effects) != 0 || !strings.Contains(deref(refused.ActionError), "not a page the browser can open") {
			t.Error("a page the browser cannot open is refused rather than opened blank")
		}
	})
}

func TestARunIsAllowedAnOriginOnceTheUserSaysSo(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("task-1")))
		asked, _ := panelsReduce(t, state, `{"type":"browser.open","taskId":"task-1","url":"https://dash.example.com/metrics"}`)
		blank := panelsDock(asked).BrowserTabs[0]
		if blank.URL != "" {
			t.Error("the ask gets a tab of its own to be shown in, and loads nothing into it")
		}
		approval := asked.BrowserApproval
		if approval == nil || approval.URL != "https://dash.example.com/metrics" || approval.TaskID != "task-1" || deref(approval.TabID) != blank.ID || panelsDock(asked).Tab != blank.ID {
			t.Fatalf("the ask is shown where the page would have been: %+v", approval)
		}
		if dismissed, _ := panelsReduce(t, asked, `{"type":"view.close-tab"}`); dismissed.BrowserApproval != nil {
			t.Error("closing the tab the ask is shown in drops the ask")
		}
		blocked, _ := panelsReduce(t, asked, `{"type":"browser.decide","approvalId":"`+approval.ApprovalID+`","allow":false}`)
		if blocked.BrowserApproval != nil || len(blocked.BrowserOrigins) != 0 || len(panelsDock(blocked).BrowserTabs) != 0 {
			t.Error("a tab that only carried the ask goes with it")
		}
		allowed, _ := panelsReduce(t, asked, `{"type":"browser.decide","approvalId":"`+approval.ApprovalID+`","allow":true}`)
		panelsEqual(t, allowed.BrowserOrigins, `["https://dash.example.com"]`, "allowing allows the origin")
		if panelsDock(allowed).BrowserTabs[0].URL != "https://dash.example.com/metrics" || allowed.BrowserApproval != nil {
			t.Error("the page loads in the tab the ask was shown in")
		}
		again, transition := panelsReduce(t, allowed, `{"type":"browser.open","taskId":"task-1","url":"https://dash.example.com/other"}`)
		if again.BrowserApproval != nil {
			t.Error("an allowed origin is not asked about twice")
		}
		panelsEffectOf(t, transition, "browser.navigate")
	})
}

func TestAThreadTrustedToActWithoutAskingBrowsesWithoutAsking(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("task-1", func(thread *domain.Thread) { thread.ExecutionPolicy = domain.ExecutionPolicyAutonomous })))
		opened, _ := panelsReduce(t, state, `{"type":"browser.open","taskId":"task-1","url":"https://example.com"}`)
		if opened.BrowserApproval != nil || panelsDock(opened).BrowserTabs[0].URL != "https://example.com/" {
			t.Error("the page loads without asking")
		}
		if len(opened.BrowserOrigins) != 0 {
			t.Error("acting without asking is not the user saying yes")
		}
	})
}

func TestClosingABrowserTabHandsThePanelItsNeighbour(t *testing.T) {
	panelsPorted(t, func() {
		second := panelsRun(t, panelsWorkspace(), `{"type":"browser.open","url":"https://one.example"}`, `{"type":"browser.open","url":"https://two.example","newTab":true}`)
		one, two := panelsDock(second).BrowserTabs[0], panelsDock(second).BrowserTabs[1]
		closed, transition := panelsReduce(t, second, `{"type":"browser.close-tab","tabId":"`+two.ID+`"}`)
		if len(panelsDock(closed).BrowserTabs) != 1 || deref(panelsDock(closed).BrowserTabID) != one.ID {
			t.Error("the neighbour is the page left")
		}
		panelsEqual(t, panelsEffects(t, transition, panelsBrowserEffect),
			`[{"type":"browser.close","tabId":"`+two.ID+`"},{"type":"browser.open","tabId":"`+one.ID+`","url":"https://one.example/"},{"type":"browser.show","tabId":"`+one.ID+`"}]`, "the panel is handed the neighbour")
		empty, transition := panelsReduce(t, closed, `{"type":"browser.close-tab","tabId":"`+one.ID+`"}`)
		if panelsDock(empty).BrowserTabID != nil {
			t.Error("no page is left")
		}
		panelsEqual(t, panelsEffects(t, transition, func(kind string) bool { return kind == "browser.show" }), `[{"type":"browser.show","tabId":null}]`, "the panel shows nothing")
	})
}

func TestWhatAPageReportsIsTheOnlyThingThatWritesTheTabRecord(t *testing.T) {
	panelsPorted(t, func() {
		opened, _ := panelsReduce(t, panelsWorkspace(), `{"type":"browser.open","url":"https://example.com"}`)
		tab := *panelsDock(opened).BrowserTabs[0]
		updated, transition := panelsReduce(t, opened, `{"type":"browser.updated","page":{"tabId":"`+tab.ID+`","url":"https://example.com/welcome","title":"Welcome","loading":false,"canGoBack":true}}`)
		want := tab
		want.URL, want.Title, want.Loading, want.CanGoBack = "https://example.com/welcome", "Welcome", false, true
		if *panelsDock(updated).BrowserTabs[0] != want {
			t.Errorf("the tab takes what the page reports: %+v", panelsDock(updated).BrowserTabs[0])
		}
		panelsEqual(t, panelsEffectOf(t, transition, "persist-preferences")["preferences"].(map[string]any)["browserTabs"], `{"draft":["https://example.com/welcome"]}`, "a page is remembered under the thread whose dock holds it")
		stray, _ := panelsReduce(t, updated, `{"type":"browser.updated","page":{"tabId":"gone","title":"Nowhere"}}`)
		if !jsonxSameDocks(stray, updated) {
			t.Error("a page no dock holds changes nothing")
		}
	})
}

func jsonxSameDocks(a, b *WorkspaceState) bool {
	return len(a.Docks) == len(b.Docks) && a.Docks[DraftDock] == b.Docks[DraftDock]
}

func TestAPageThatFailsKeepsSayingSoUntilTheTabLandsElsewhere(t *testing.T) {
	panelsPorted(t, func() {
		opened, _ := panelsReduce(t, panelsWorkspace(), `{"type":"browser.open","url":"https://example.com/missing"}`)
		tabID := panelsDock(opened).BrowserTabs[0].ID
		failed := panelsRun(t, opened,
			`{"type":"browser.updated","page":{"tabId":"`+tabID+`","loading":false,"error":"ERR_NAME_NOT_RESOLVED (https://example.com/missing)"}}`,
			`{"type":"browser.updated","page":{"tabId":"`+tabID+`","loading":false,"url":"https://example.com/missing","title":""}}`,
		)
		if !strings.Contains(deref(panelsDock(failed).BrowserTabs[0].Error), "ERR_NAME_NOT_RESOLVED") {
			t.Error("the load settling is not the failure being over")
		}
		landed, _ := panelsReduce(t, failed, `{"type":"browser.updated","page":{"tabId":"`+tabID+`","loading":false,"url":"https://example.com/","title":"Example"}}`)
		if panelsDock(landed).BrowserTabs[0].Error != nil {
			t.Error("landing on a different page clears the error")
		}
	})
}

func TestActingInTheBrowserNeedsAPage(t *testing.T) {
	panelsPorted(t, func() {
		browsing := panelsWorkspace(panelsOn(panelsTask("task-1")))
		empty, _ := panelsReduce(t, browsing, `{"type":"browser.act","taskId":"task-1","action":{"kind":"click","ref":"3"}}`)
		if !strings.Contains(deref(empty.ActionError), "no page open") {
			t.Error("acting needs a page")
		}
		opened, _ := panelsReduce(t, browsing, `{"type":"browser.open","url":"https://example.com"}`)
		_, clicked := panelsReduce(t, opened, `{"type":"browser.act","taskId":"task-1","action":{"kind":"click","ref":"3"}}`)
		panelsEqual(t, panelsEffects(t, clicked, func(kind string) bool { return kind != "browser.permissions" }),
			`[{"type":"browser.act","tabId":"`+panelsDock(opened).BrowserTabs[0].ID+`","action":{"kind":"click","ref":"3"},"taskId":"task-1"}]`, "the action goes to the page")
		cleared, transition := panelsReduce(t, opened, `{"type":"browser.clear-data"}`)
		if len(cleared.BrowserOrigins) != 0 {
			t.Error("clearing the session takes back every allowed site")
		}
		panelsEffectOf(t, transition, "browser.clear-data")
	})
}

func TestBrowserApprovalDecisionsBindToOneRequest(t *testing.T) {
	panelsPorted(t, func() {
		initial := panelsWorkspace(panelsOn(panelsTask("task-1")))
		first, _ := panelsReduce(t, initial, `{"type":"browser.open","taskId":"task-1","url":"https://one.example"}`)
		approval := first.BrowserApproval
		competing, transition := panelsReduce(t, first, `{"type":"browser.open","taskId":"task-1","url":"https://two.example"}`)
		if competing.BrowserApproval != approval {
			t.Error("a competing ask preserves the displayed request")
		}
		panelsEqual(t, panelsGeneric(t, transition.Result).(map[string]any)["ok"], `false`, "the competing ask is refused")
		for _, allow := range []string{"false", "true"} {
			stale, transition := panelsReduce(t, first, `{"type":"browser.decide","approvalId":"stale","allow":`+allow+`}`)
			if len(transition.Effects) != 0 || stale.BrowserApproval != first.BrowserApproval || !jsonxSameDocks(stale, first) {
				t.Error("a stale decision changes nothing")
			}
		}
		closed, _ := panelsReduce(t, first, `{"type":"browser.close-tab","tabId":"`+deref(approval.TabID)+`"}`)
		if closed.BrowserApproval != nil {
			t.Error("closing the ask's tab drops the ask")
		}
		second, _ := panelsReduce(t, closed, `{"type":"browser.open","taskId":"task-1","url":"https://two.example"}`)
		if replay, transition := panelsReduce(t, second, `{"type":"browser.decide","approvalId":"`+approval.ApprovalID+`","allow":true}`); len(transition.Effects) != 0 || replay.BrowserApproval != second.BrowserApproval {
			t.Error("an old decision does not answer a newer ask")
		}
		allowed, _ := panelsReduce(t, first, `{"type":"browser.decide","approvalId":"`+approval.ApprovalID+`","allow":true}`)
		panelsEqual(t, allowed.BrowserOrigins, `["https://one.example"]`, "the decision allows its own origin")
		if _, transition := panelsReduce(t, allowed, `{"type":"browser.decide","approvalId":"`+approval.ApprovalID+`","allow":true}`); len(transition.Effects) != 0 {
			t.Error("a decision is answered once")
		}
	})
}

func TestARunDrivesItsOwnThreadsDock(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("task-1"), panelsTask("task-2", func(thread *domain.Thread) { thread.ExecutionPolicy = domain.ExecutionPolicyAutonomous })), func(s *WorkspaceState) {
			s.History = []string{"task-1"}
		})
		opened, transition := panelsReduce(t, state, `{"type":"browser.open","taskId":"task-2","url":"https://two.example"}`)
		if len(panelsDock(opened).BrowserTabs) != 0 {
			t.Error("the dock on screen belongs to the thread the user is reading")
		}
		page := panelsDock(opened, "task-2").BrowserTabs[0]
		if page.URL != "https://two.example/" || panelsDock(opened, "task-2").Open || panelsDock(opened, "task-2").Tab != DockPicker {
			t.Error("a run's page leaves its dock shut, on the tab it was showing")
		}
		if panelsHasEffect(t, transition, `{"type":"browser.show"}`) {
			t.Error("a page nobody is looking at never claims the panel")
		}
		landed, _ := panelsReduce(t, opened, `{"type":"task.select","taskId":"task-2"}`)
		if panelsDock(landed).Open {
			t.Error("landing on that thread finds the dock as the user left it")
		}
		shown, transition := panelsReduce(t, landed, `{"type":"browser.select-tab","tabId":"`+page.ID+`"}`)
		if panelsDock(shown).Tab != page.ID || !panelsSubset(panelsLastEffect(t, transition), panelsParse(t, `{"type":"browser.show"}`)) {
			t.Error("asking for the page is what shows it")
		}
	})
}

func TestARunsPageNeverTakesThePanelFromTheUsersPage(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("task-1", func(thread *domain.Thread) { thread.ExecutionPolicy = domain.ExecutionPolicyAutonomous })))
		mine, _ := panelsReduce(t, state, `{"type":"browser.open","url":"https://mine.example"}`)
		myPage := panelsDock(mine).BrowserTabs[0].ID
		byRun, transition := panelsReduce(t, mine, `{"type":"browser.open","taskId":"task-1","url":"https://run.example","newTab":true}`)
		if panelsDock(byRun).Tab != myPage || len(panelsDock(byRun).BrowserTabs) != 2 || panelsHasEffect(t, transition, `{"type":"browser.show"}`) {
			t.Error("the panel keeps drawing the page the user chose")
		}
		if read := BrowserTarget(panelsDock(byRun), nil); read == nil || read.URL != "https://run.example/" {
			t.Error("a read that names no tab still finds the page the run just opened")
		}
	})
}
