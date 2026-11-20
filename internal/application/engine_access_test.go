package application

import (
	"testing"
)

const panelsBehind = `{"claude":{"access":"ready","version":"2.1.0","required":"2.1.250","fix":"claude update"},"codex":{"access":"outdated","version":"0.147.0","required":"0.150.1","fix":"npm install -g @openai/codex@latest"}}`

func panelsEnginesFound(t *testing.T) *WorkspaceState {
	return panelsRun(t, panelsWorkspace(), `{"type":"engine.read"}`, `{"type":"engine.status","status":`+panelsBehind+`}`)
}

func TestTheLaunchsFirstLookAtTheEnginesOffersEachUpdate(t *testing.T) {
	panelsPorted(t, func() {
		reading := panelsRun(t, panelsWorkspace(), `{"type":"engine.read"}`)
		found, transition := panelsReduce(t, reading, `{"type":"engine.status","status":`+panelsBehind+`}`)
		panelsEqual(t, transition.Effects, `[]`, "nothing is installed until the user asks, and an offer leaves on no timer")
		if found.EngineUpdating != nil {
			t.Error("nothing is updating")
		}
		panelsEqual(t, found.Toasts, `[
			{"id":1,"tone":"update","title":"Claude 2.1.250 is available","message":"This app needs 2.1.250 or newer. You have 2.1.0.","subject":"claude","persistent":true,"remember":"engine-update:claude@2.1.250","action":{"label":"Upgrade","command":{"type":"engine.update","engine":"claude"}}},
			{"id":2,"tone":"update","title":"Codex 0.150.1 is available","message":"This app needs 0.150.1 or newer. You have 0.147.0.","subject":"codex","persistent":true,"remember":"engine-update:codex@0.150.1","action":{"label":"Upgrade","command":{"type":"engine.update","engine":"codex"}}}
		]`, "each update is offered")
		later, _ := panelsReduce(t, found, `{"type":"engine.status","status":`+panelsBehind+`}`)
		if !sameValueOf(later.Toasts, found.Toasts) {
			t.Error("a later read offers nothing again")
		}
	})
}

func TestAnUpgradeTurnsItsCardIntoProgressAndASecondWaitsItsTurn(t *testing.T) {
	panelsPorted(t, func() {
		claude, transition := panelsReduce(t, panelsEnginesFound(t), `{"type":"engine.update","engine":"claude"}`)
		panelsEqual(t, transition.Effects, `[{"type":"engine.update","engine":"claude"},{"type":"schedule-toast-dismissal","id":1,"at":null}]`, "the upgrade starts")
		panelsEqual(t, claude.Toasts[0], `{"id":1,"tone":"progress","title":"Updating Claude","message":"Moving from 2.1.0 to 2.1.250.","subject":"claude","persistent":true}`, "its card says so")

		codex, transition := panelsReduce(t, claude, `{"type":"engine.update","engine":"codex"}`)
		if panelsHasEffect(t, transition, `{"type":"engine.update"}`) {
			t.Error("package managers lock, so the second waits")
		}
		panelsEqual(t, codex.EngineUpdateQueue, `["codex"]`, "it is queued")
		if deref(codex.Toasts[1].Message) != "Waiting for Claude to finish." {
			t.Error("its card says what it waits on")
		}

		claudeDone, transition := panelsReduce(t, codex, `{"type":"engine.updated","engine":"claude","status":{"claude":{"access":"ready","version":"2.1.260"},"codex":{"access":"outdated","version":"0.147.0","required":"0.150.1","fix":"npm install -g @openai/codex@latest"}}}`)
		if deref(claudeDone.EngineUpdating) != "codex" || len(claudeDone.EngineUpdateQueue) != 0 {
			t.Error("the next in line starts")
		}
		cards := []any{}
		for _, toast := range panelsGeneric(t, claudeDone.Toasts).([]any) {
			card := toast.(map[string]any)
			cards = append(cards, map[string]any{"id": card["id"], "tone": card["tone"], "title": card["title"], "message": card["message"]})
		}
		panelsEqual(t, cards, `[{"id":1,"tone":"success","title":"Claude updated","message":"Now on 2.1.260."},{"id":2,"tone":"progress","title":"Updating Codex","message":"Moving from 0.147.0 to 0.150.1."}]`, "each result takes its card's place")
		leaves := false
		for _, effect := range transition.Effects {
			if dismissal, ok := effect.(WorkspaceEffectScheduleToastDismissal); ok && dismissal.ID == 1 && dismissal.At != nil {
				leaves = true
			}
		}
		if !leaves {
			t.Error("a success leaves on its own")
		}
		panelsEqual(t, panelsEffectOf(t, transition, "engine.update"), `{"type":"engine.update","engine":"codex"}`, "codex starts")

		failed, _ := panelsReduce(t, claudeDone, `{"type":"engine.update-failed","engine":"codex","message":"EACCES. Run `+"`npm install -g @openai/codex@latest`"+` in your terminal."}`)
		if failed.EngineUpdating != nil || failed.ActionError != nil {
			t.Error("how it went is told in the toast alone")
		}
		panelsEqual(t, failed.Toasts[len(failed.Toasts)-1], `{"id":2,"tone":"error","title":"Couldn't update Codex","message":"EACCES. Run `+"`npm install -g @openai/codex@latest`"+` in your terminal.","subject":"codex","persistent":true}`, "the failure takes the card")
	})
}

func TestAnUpdateThatFinishesStillBehindSaysSo(t *testing.T) {
	panelsPorted(t, func() {
		updating := panelsRun(t, panelsEnginesFound(t), `{"type":"engine.update","engine":"claude"}`)
		stale, _ := panelsReduce(t, updating, `{"type":"engine.updated","engine":"claude","status":`+panelsBehind+`}`)
		panelsEqual(t, stale.Toasts[0], `{"id":1,"tone":"error","title":"Couldn't update Claude","message":"Claude is still on 2.1.0. Run `+"`claude update`"+` in your terminal.","subject":"claude","persistent":true}`, "it stays up")
	})
}

func TestAClosedOfferIsRememberedForThatReleaseOnly(t *testing.T) {
	panelsPorted(t, func() {
		managed := `{"claude":{"access":"ready","version":"2.1.291","latest":"2.1.300"},"codex":{"access":"ready","version":"0.160.1"}}`
		found := panelsRun(t, panelsWorkspace(), `{"type":"engine.read"}`, `{"type":"engine.status","status":`+managed+`}`)
		panelsEqual(t, found.Toasts, `[{"id":1,"tone":"update","title":"Claude 2.1.300 is available","message":"You have 2.1.291. Update it the way you installed it.","subject":"claude","persistent":true,"remember":"engine-update:claude@2.1.300"}]`, "an install the app cannot upgrade is offered without a button")
		closed := panelsRun(t, found, `{"type":"view.dismiss-toast","id":1}`)
		relaunched := panelsWorkspace(func(s *WorkspaceState) { s.DismissedToasts = closed.DismissedToasts })
		if again, _ := panelsReduce(t, relaunched, `{"type":"engine.status","status":`+managed+`}`); len(again.Toasts) != 0 {
			t.Error("the same release is not offered again")
		}
		newer, _ := panelsReduce(t, relaunched, `{"type":"engine.status","status":{"claude":{"access":"ready","version":"2.1.291","latest":"2.1.301"}}}`)
		if len(newer.Toasts) == 0 || newer.Toasts[0].Title != "Claude 2.1.301 is available" {
			t.Error("the next release is")
		}
	})
}

func TestAnUpToDateLaunchOffersNothing(t *testing.T) {
	panelsPorted(t, func() {
		reading := panelsRun(t, panelsWorkspace(), `{"type":"engine.read"}`)
		found, transition := panelsReduce(t, reading, `{"type":"engine.status","status":{"claude":{"access":"ready","version":"2.1.260"},"codex":{"access":"missing","fix":"brew install --cask codex"}}}`)
		if len(transition.Effects) != 0 || len(found.Toasts) != 0 {
			t.Error("an engine that is not installed is the user's to install")
		}
	})
}
