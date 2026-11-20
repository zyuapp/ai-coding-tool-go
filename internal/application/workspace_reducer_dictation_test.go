package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const panelsDictationReady = `{"type":"dictation.model","model":"ready"}`

// panelsHeld is a recording going on thread "a" of two, or on state when given.
func panelsHeld(t *testing.T, state ...*WorkspaceState) (*WorkspaceState, string) {
	t.Helper()
	base := panelsRun(t, panelsWorkspace(panelsOn(panelsTask("a"), panelsTask("b"))), panelsDictationReady)
	if len(state) > 0 {
		base = state[0]
	}
	started, transition := panelsReduce(t, base, `{"type":"dictation.start"}`)
	id := panelsEffectOf(t, transition, "dictation.start")["id"].(string)
	return panelsRun(t, started, `{"type":"dictation.started","id":"`+id+`"}`), id
}

func TestAHoldIsRefusedUntilTheSpeechModelIsReady(t *testing.T) {
	panelsPorted(t, func() {
		missing := panelsRun(t, panelsWorkspace(), `{"type":"dictation.model","model":"missing"}`)
		refused, transition := panelsReduce(t, missing, `{"type":"dictation.start"}`)
		panelsEqual(t, transition.Result, `{"ok":false,"message":"`+domain.DictationMissing+`"}`, "the refusal says how to get the model")
		if refused.Dictation.Session != nil || len(transition.Effects) != 0 {
			t.Error("nothing records")
		}
	})
}

func TestLettingGoAddsTheWordsAfterWhatWasTyped(t *testing.T) {
	panelsPorted(t, func() {
		state, id := panelsHeld(t)
		if state.Dictation.Session.Phase != domain.DictationPhaseRecording {
			t.Fatal("the hold records")
		}
		typed := panelsRun(t, state, `{"type":"view.set-prompt","prompt":"Look at the diff."}`)
		stopped, transition := panelsReduce(t, typed, `{"type":"dictation.stop"}`)
		panelsEqual(t, panelsEffectOf(t, transition, "dictation.stop"), `{"type":"dictation.stop","id":"`+id+`"}`, "the microphone stops")
		if phase := DeriveView(stopped).Dictation.Phase; phase == nil || *phase != domain.DictationPhaseTranscribing {
			t.Error("the view says it is transcribing")
		}
		heard, transition := panelsReduce(t, stopped, `{"type":"dictation.transcribed","id":"`+id+`","text":"Then fix the failing test."}`)
		if heard.Prompts["a"] != "Look at the diff. Then fix the failing test." || heard.Dictation.Session != nil {
			t.Errorf("the words join the draft: %q", heard.Prompts["a"])
		}
		if heard.ComposerFocus != typed.ComposerFocus+1 {
			t.Error("the caret goes back to the draft so Enter sends it")
		}
		if panelsHasEffect(t, transition, `{"type":"start-run"}`) || panelsHasEffect(t, transition, `{"type":"send-attachments"}`) {
			t.Error("nothing is sent")
		}
	})
}

func TestATapKeepsListeningHandsFree(t *testing.T) {
	panelsPorted(t, func() {
		state, id := panelsHeld(t)
		freed, transition := panelsReduce(t, state, `{"type":"dictation.hands-free"}`)
		if !freed.Dictation.Session.HandsFree || !DeriveView(freed).Dictation.HandsFree || freed.ComposerFocus != state.ComposerFocus+1 || len(transition.Effects) != 0 {
			t.Error("a tap keeps listening hands-free and hands the caret to the draft")
		}
		stopped, transition := panelsReduce(t, freed, `{"type":"dictation.stop"}`)
		panelsEqual(t, panelsEffectOf(t, transition, "dictation.stop"), `{"type":"dictation.stop","id":"`+id+`"}`, "the microphone stops")
		if DeriveView(stopped).Dictation.HandsFree {
			t.Error("writing it down is the same for both")
		}
	})
}

func TestStoppingToSendAsksTheDraftToSend(t *testing.T) {
	panelsPorted(t, func() {
		state, id := panelsHeld(t)
		typed := panelsRun(t, state, `{"type":"dictation.hands-free"}`, `{"type":"view.set-prompt","prompt":"Look at the diff."}`)
		heard := panelsRun(t, typed, `{"type":"dictation.stop","send":true}`, `{"type":"dictation.transcribed","id":"`+id+`","text":"Then fix it."}`)
		if heard.Prompts["a"] != "Look at the diff. Then fix it." || heard.Dictation.Sends != typed.Dictation.Sends+1 || DeriveView(heard).Dictation.Sends != heard.Dictation.Sends {
			t.Error("the send rides the words")
		}
	})
}

func TestASendOfNothingOrOfAThreadLeftSendsNothing(t *testing.T) {
	panelsPorted(t, func() {
		silent, id := panelsHeld(t)
		if missed := panelsRun(t, silent, `{"type":"dictation.stop","send":true}`, `{"type":"dictation.transcribed","id":"`+id+`","text":" "}`); missed.Dictation.Sends != 0 {
			t.Error("words that heard nothing send nothing")
		}
		away, id := panelsHeld(t)
		moved := panelsRun(t, away, `{"type":"dictation.stop","send":true}`, `{"type":"task.select","taskId":"b"}`, `{"type":"dictation.transcribed","id":"`+id+`","text":"keep me"}`)
		if moved.Prompts["a"] != "keep me" || moved.Dictation.Sends != 0 {
			t.Error("a thread left meanwhile keeps the words and sends nothing")
		}
	})
}

func TestWordsForAThreadLeftStayInItsDraft(t *testing.T) {
	panelsPorted(t, func() {
		state, id := panelsHeld(t)
		moved := panelsRun(t, state, `{"type":"dictation.stop"}`, `{"type":"task.select","taskId":"b"}`)
		heard, _ := panelsReduce(t, moved, `{"type":"dictation.transcribed","id":"`+id+`","text":"keep me"}`)
		if _, written := heard.Prompts["b"]; heard.Prompts["a"] != "keep me" || written || heard.ComposerFocus != moved.ComposerFocus {
			t.Error("the words stay in the draft the hold began on, without taking the caret")
		}
	})
}

func TestAHoldThatHeardNothingSaysSo(t *testing.T) {
	panelsPorted(t, func() {
		state, id := panelsHeld(t, panelsRun(t, panelsWorkspace(), panelsDictationReady))
		heard, _ := panelsReduce(t, panelsRun(t, state, `{"type":"dictation.stop"}`), `{"type":"dictation.transcribed","id":"`+id+`","text":"  "}`)
		if _, written := heard.Prompts[NewThreadDraft]; written {
			t.Error("nothing is written")
		}
		if send := heard.AttachmentSends[MainComposer]; send == nil || deref(send.Error) != domain.DictationHeardNothing {
			t.Error("it says so under the composer")
		}
	})
}

func TestCancellingDropsTheHold(t *testing.T) {
	panelsPorted(t, func() {
		state, id := panelsHeld(t)
		cancelled, transition := panelsReduce(t, state, `{"type":"dictation.cancel"}`)
		panelsEqual(t, panelsEffectOf(t, transition, "dictation.cancel"), `{"type":"dictation.cancel","id":"`+id+`"}`, "the microphone is let go")
		if cancelled.Dictation.Session != nil {
			t.Error("the hold is over")
		}
		late, transition := panelsReduce(t, cancelled, `{"type":"dictation.transcribed","id":"`+id+`","text":"too late"}`)
		if len(transition.Effects) != 0 || late.Prompts["a"] != cancelled.Prompts["a"] {
			t.Error("a late answer changes nothing")
		}
	})
}

func TestWordsThatAreSentClearAnEarlierNotice(t *testing.T) {
	panelsPorted(t, func() {
		first, id := panelsHeld(t)
		missed := panelsRun(t, first, `{"type":"dictation.stop"}`, `{"type":"dictation.transcribed","id":"`+id+`","text":""}`)
		if deref(missed.AttachmentSends[MainComposer].Error) != domain.DictationHeardNothing {
			t.Fatal("the miss is said")
		}
		second, id := panelsHeld(t, missed)
		heard := panelsRun(t, second, `{"type":"dictation.stop"}`, `{"type":"dictation.transcribed","id":"`+id+`","text":"now it heard"}`)
		if heard.AttachmentSends[MainComposer].Error != nil {
			t.Error("words that land clear it")
		}
	})
}

func TestAFailedHoldEndsAndShowsWhy(t *testing.T) {
	panelsPorted(t, func() {
		state, id := panelsHeld(t)
		failed, _ := panelsReduce(t, state, `{"type":"dictation.failed","id":"`+id+`","message":"No microphone is available."}`)
		if failed.Dictation.Session != nil || deref(failed.AttachmentSends[MainComposer].Error) != "No microphone is available." {
			t.Error("a failed hold ends and shows why")
		}
	})
}

func TestASecondHoldIsIgnored(t *testing.T) {
	panelsPorted(t, func() {
		state, _ := panelsHeld(t)
		again, transition := panelsReduce(t, state, `{"type":"dictation.start"}`)
		if len(transition.Effects) != 0 || again.Dictation.Session != state.Dictation.Session {
			t.Error("a second hold while one is going is ignored")
		}
	})
}

func TestTheModelDownloadsOnlyWhileMissingAndCancels(t *testing.T) {
	panelsPorted(t, func() {
		missing := panelsRun(t, panelsWorkspace(), `{"type":"dictation.model","model":"missing"}`)
		downloading, transition := panelsReduce(t, missing, `{"type":"dictation.download"}`)
		if downloading.Dictation.Model != domain.DictationModelDownloading {
			t.Error("the download starts")
		}
		panelsEqual(t, panelsEffectOf(t, transition, "dictation.download"), `{"type":"dictation.download"}`, "main downloads")
		if _, again := panelsReduce(t, downloading, `{"type":"dictation.download"}`); len(again.Effects) != 0 {
			t.Error("a download going is not started again")
		}
		progressing := panelsRun(t, downloading, `{"type":"dictation.model","model":"downloading","progress":0.3}`)
		cancelled, transition := panelsReduce(t, progressing, `{"type":"dictation.cancel-download"}`)
		panelsEqual(t, panelsEffectOf(t, transition, "dictation.cancel-download"), `{"type":"dictation.cancel-download"}`, "main stops")
		if cancelled.Dictation.Model != domain.DictationModelMissing || cancelled.Dictation.Progress != nil {
			t.Error("cancelling offers the download again")
		}
		if _, again := panelsReduce(t, cancelled, `{"type":"dictation.cancel-download"}`); len(again.Effects) != 0 {
			t.Error("nothing to cancel once it stopped")
		}
	})
}

func TestADownloadShowsHowMuchHasArrived(t *testing.T) {
	panelsPorted(t, func() {
		started := panelsRun(t, panelsWorkspace(), `{"type":"dictation.model","model":"missing"}`, `{"type":"dictation.download"}`)
		if progress := DeriveView(started).Dictation.Progress; progress == nil || *progress != 0 {
			t.Error("a download starts at nothing")
		}
		halfway := panelsRun(t, started, `{"type":"dictation.model","model":"downloading","progress":0.42}`)
		if progress := DeriveView(halfway).Dictation.Progress; progress == nil || *progress != 0.42 {
			t.Error("the share arrived is shown")
		}
		preparing := panelsRun(t, halfway, `{"type":"dictation.model","model":"downloading"}`)
		if DeriveView(preparing).Dictation.Progress != nil {
			t.Error("preparing shows no share")
		}
		ready := panelsRun(t, preparing, `{"type":"dictation.model","model":"ready","progress":1}`)
		if ready.Dictation.Model != domain.DictationModelReady || ready.Dictation.Progress != nil {
			t.Error("a ready model shows nothing")
		}
	})
}

func TestHoldingToTalkOnAPairedComputersThreadShowsThisMicrophone(t *testing.T) {
	panelsPorted(t, func() {
		remote := panelsWorkspace(panelsOn(panelsTask("remote-thread")), func(s *WorkspaceState) {
			s.Dictation = domain.DictationState{Model: domain.DictationModelUnsupported}
		})
		state := panelsWorkspace(func(s *WorkspaceState) {
			s.Dictation = domain.DictationState{Model: domain.DictationModelReady}
			s.Computers.Name = "This Mac"
			s.Computers.Paired = []*PairedComputer{{ComputerLink: domain.ComputerLink{ID: "linux", Name: "linux", Host: "linux.tail.ts.net", Status: domain.ComputerStatusConnected, PairedAt: 1}, State: remote}}
			s.Computers.Active = new("linux")
		})
		key := PromptKey(state)
		if key != "remote-thread" {
			t.Fatalf("the draft is the paired thread's: %q", key)
		}
		state.Dictation.Session = &domain.DictationSession{ID: "hold", Key: key, Phase: domain.DictationPhaseRecording}
		view := DeriveView(state)
		if view.Dictation.Model != domain.DictationModelReady || view.Dictation.Phase == nil || *view.Dictation.Phase != domain.DictationPhaseRecording {
			t.Errorf("this computer's microphone is shown: %+v", view.Dictation)
		}
	})
}
