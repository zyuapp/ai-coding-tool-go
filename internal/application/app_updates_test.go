package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const (
	panelsAvailable = `{"status":"available","version":"0.7.11"}`
	panelsReady     = `{"status":"ready","version":"0.7.11"}`
)

func TestAnUpdateAskedOfAnIdleComputerInstallsTheMomentItIsReady(t *testing.T) {
	panelsPorted(t, func() {
		asked, transition := panelsReduce(t, panelsPatched(t, panelsWorkspace(), `{"appUpdate":`+panelsAvailable+`}`), `{"type":"app.update"}`)
		panelsEqual(t, asked.AppUpdateRequest, `{"status":"waiting"}`, "the update waits")
		panelsEqual(t, panelsEffectOf(t, transition, "app.download-update"), `{"type":"app.download-update","unattended":true}`, "it downloads without a dialog")
		downloading, transition := panelsReduce(t, asked, `{"type":"app.update-changed","update":{"status":"downloading","version":"0.7.11","percent":40}}`)
		if panelsHasEffect(t, transition, `{"type":"app.install-update"}`) {
			t.Error("a download going installs nothing")
		}
		downloaded, transition := panelsReduce(t, downloading, `{"type":"app.update-changed","update":`+panelsReady+`}`)
		panelsEqual(t, panelsEffectOf(t, transition, "app.install-update"), `{"type":"app.install-update","unattended":true}`, "it installs once ready")
		panelsEqual(t, downloaded.AppUpdateRequest, `{"status":"installing","version":"0.7.11"}`, "and says so")
		if _, again := panelsReduce(t, downloaded, `{"type":"view.set-focused","focused":true}`); panelsHasEffect(t, again, `{"type":"app.install-update"}`) {
			t.Error("it installs once")
		}
	})
}

func TestADownloadedUpdateWaitsForRunningThreads(t *testing.T) {
	panelsPorted(t, func() {
		busy := panelsPatched(t, panelsWorkspace(panelsOn(panelsTask("task-a"))), `{
			"appUpdate":`+panelsReady+`,
			"activeRuns":{"task-a":{"taskId":"task-a","runId":"run-a","sequence":0,"status":"running","origin":"composer","quiet":false,"notified":false,"acknowledged":false,"reportedIssues":[],"messagesBefore":0,"before":{"updatedAt":1}}},
			"runStatuses":{"task-a":"running"}
		}`)
		asked, transition := panelsReduce(t, busy, `{"type":"app.update"}`)
		panelsEqual(t, asked.AppUpdateRequest, `{"status":"waiting"}`, "the update waits")
		if panelsHasEffect(t, transition, `{"type":"app.install-update"}`) {
			t.Error("a running thread holds the install")
		}
		idle := panelsPatched(t, asked, `{"activeRuns":{},"runStatuses":{}}`)
		_, finished := panelsReduce(t, idle, `{"type":"view.set-focused","focused":false}`)
		panelsEqual(t, panelsEffectOf(t, finished, "app.install-update"), `{"type":"app.install-update","unattended":true}`, "it installs once nothing runs")
		cancelled, _ := panelsReduce(t, asked, `{"type":"app.cancel-update"}`)
		if cancelled.AppUpdateRequest != nil {
			t.Error("the ask is dropped")
		}
		if _, after := panelsReduce(t, panelsPatched(t, cancelled, `{"activeRuns":{}}`), `{"type":"view.set-focused","focused":false}`); panelsHasEffect(t, after, `{"type":"app.install-update"}`) {
			t.Error("a cancelled one never installs")
		}
	})
}

func TestADownloadThatFallsBackSaysItFailed(t *testing.T) {
	panelsPorted(t, func() {
		started := panelsRun(t, panelsPatched(t, panelsWorkspace(), `{"appUpdate":`+panelsAvailable+`}`), `{"type":"app.update"}`, `{"type":"app.update-changed","update":{"status":"downloading","version":"0.7.11","percent":3}}`)
		failed, transition := panelsReduce(t, started, `{"type":"app.update-changed","update":`+panelsAvailable+`}`)
		panelsEqual(t, failed.AppUpdateRequest, `{"status":"failed","message":"Couldn't download 0.7.11."}`, "the download failed")
		if panelsHasEffect(t, transition, `{"type":"app.download-update"}`) {
			t.Error("a failure does not loop")
		}
		_, none := panelsReduce(t, panelsWorkspace(), `{"type":"app.update"}`)
		panelsEqual(t, none.Result, `{"ok":false,"message":"`+NoUpdateToInstall+`"}`, "nothing is asked of a computer with no update")
	})
}

func TestTheLaunchAnUpdateBroughtUpgradesEachEngineItNeeds(t *testing.T) {
	panelsPorted(t, func() {
		launched, _ := panelsReduce(t, panelsPatched(t, panelsWorkspace(), `{"engineStatus":{}}`), `{"type":"app.launched","version":"0.7.11"}`)
		if !launched.EngineUpgradesOwed {
			t.Fatal("the launch owes the engines")
		}
		read, transition := panelsReduce(t, launched, `{"type":"engine.status","status":{"codex":{"access":"outdated","version":"0.160.0","required":"0.162.1","latest":"0.163.0","fix":"npm install -g @openai/codex@latest"},"claude":{"access":"ready","version":"2.1.0","latest":"2.2.0","fix":"claude update"}}}`)
		panelsEqual(t, panelsEffectOf(t, transition, "engine.update"), `{"type":"engine.update","engine":"codex"}`, "the engine the build needs is upgraded")
		updates := panelsEffects(t, transition, func(kind string) bool { return kind == "engine.update" })
		if len(updates) != 1 || read.EngineUpgradesOwed {
			t.Error("a newer release alone is only offered")
		}
		_, ordinary := panelsReduce(t, panelsPatched(t, panelsWorkspace(), `{"engineStatus":{}}`), `{"type":"engine.status","status":{"codex":{"access":"outdated","version":"0.160.0","required":"0.162.1","fix":"npm install -g @openai/codex@latest"}}}`)
		if panelsHasEffect(t, ordinary, `{"type":"engine.update"}`) {
			t.Error("an ordinary launch only offers it")
		}
	})
}

func TestAnInstallThatLeavesTheBuildNotReadySaysItFailed(t *testing.T) {
	panelsPorted(t, func() {
		installing := panelsPatched(t, panelsWorkspace(), `{"appUpdate":`+panelsReady+`,"appUpdateRequest":{"status":"installing","version":"0.7.11"}}`)
		failed, _ := panelsReduce(t, installing, `{"type":"app.update-changed","update":`+panelsAvailable+`}`)
		panelsEqual(t, failed.AppUpdateRequest, `{"status":"failed","message":"Couldn't install 0.7.11."}`, "the install failed")
		again, transition := panelsReduce(t, failed, `{"type":"app.update"}`)
		panelsEqual(t, again.AppUpdateRequest, `{"status":"waiting"}`, "it can be tried again")
		panelsEqual(t, panelsEffectOf(t, transition, "app.download-update"), `{"type":"app.download-update","unattended":true}`, "downloading again")
	})
}

func TestAThreadWaitingOutAUsageLimitDoesNotHoldAnInstall(t *testing.T) {
	panelsPorted(t, func() {
		paused := panelsTask("t", func(thread *domain.Thread) {
			thread.LimitPause = &domain.LimitPause{UsageLimit: domain.UsageLimit{ResetsAt: 10, Window: domain.UsageLimitWindowSession}, PausedAt: 1}
		})
		_, asked := panelsReduce(t, panelsPatched(t, panelsWorkspace(func(s *WorkspaceState) { s.Threads = []*domain.Thread{paused} }), `{"appUpdate":`+panelsReady+`}`), `{"type":"app.update"}`)
		panelsEqual(t, panelsEffectOf(t, asked, "app.install-update"), `{"type":"app.install-update","unattended":true}`, "it resumes after the restart")
	})
}

func TestAnEngineNoticeOnAnotherComputersThreadOpensThatComputersRow(t *testing.T) {
	panelsPorted(t, func() {
		remote := panelsWorkspace(panelsOn(panelsTask("remote-thread")))
		state := panelsWorkspace(func(s *WorkspaceState) {
			s.Computers.Name = "omarchy"
			s.Computers.Paired = []*PairedComputer{{ComputerLink: domain.ComputerLink{ID: "mac", Name: "mac", Host: "mac.tail.ts.net", Status: domain.ComputerStatusConnected, PairedAt: 1, Capabilities: panelsCapabilities()}, State: remote}}
		})
		panelsEqual(t, DeriveView(state).EngineSettingsPage, `{"section":"engines","settingId":null}`, "this computer's Engines page")
		shown, _ := panelsReduce(t, state, `{"type":"task.select","taskId":"remote-thread"}`)
		panelsEqual(t, DeriveView(shown).EngineSettingsPage, `{"section":"phone","settingId":"phone.computers"}`, "that computer's row")
	})
}
