package application

import (
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func cmpReduce(state *WorkspaceState, input ComputerInput) (*WorkspaceState, SliceTransition) {
	transition := ReduceComputers(state, input)
	return Afterward(state, transition), transition
}

func TestThePairedComputersKeepTheirStatesAndALostOneLeavesTheScreen(t *testing.T) {
	cmpPorted(t, func() {
		remote := cmpRemoteState()
		state := cmpWithComputers(cmpWorkspace(), []*PairedComputer{cmpPaired("linux", remote)}, cmpOnScreen("linux"), func(computers *ComputersState) {
			computers.Filter = "linux"
			computers.Pairing = &domain.ComputerPairing{Host: "linux.tail.ts.net", Name: "linux", Busy: true}
		})
		still, _ := cmpReduce(state, ComputerEventComputersChanged{Name: "This Mac", Links: []*domain.ComputerLink{new(cmpLink("linux", cmpStatus(domain.ComputerStatusOffline, "Timed out")))}})
		if still.Computers.Paired[0].State != remote {
			t.Error("the last state stays while the line is down")
		}
		if still.Computers.Paired[0].Status != domain.ComputerStatusOffline {
			t.Error("status")
		}
		if still.Computers.Active != nil {
			t.Error("a line that dropped takes the screen with it")
		}
		if still.Computers.Filter != "linux" {
			t.Error("the sidebar's filter is the user's and stays")
		}
		if still.Computers.Pairing != nil {
			t.Error("a pairing that shows up as a link is done")
		}
		gone, _ := cmpReduce(state, ComputerEventComputersChanged{Name: "This Mac", Links: []*domain.ComputerLink{}})
		if gone.Computers.Active != nil || gone.Computers.Filter != "all" {
			t.Errorf("gone = %v %v", gone.Computers.Active, gone.Computers.Filter)
		}
		failed, _ := cmpReduce(state, ComputerEventComputersPairFailed{Message: "Wrong code"})
		if failed.Computers.Pairing == nil || failed.Computers.Pairing.Error == nil || *failed.Computers.Pairing.Error != "Wrong code" || failed.Computers.Pairing.Busy {
			t.Errorf("pairing = %+v", failed.Computers.Pairing)
		}
	})
}

func TestALineThatComesBackIsToldNothingUntilSelected(t *testing.T) {
	cmpPorted(t, func() {
		state := cmpWithComputers(cmpWorkspace(func(state *WorkspaceState) { state.Focused = false }), []*PairedComputer{cmpPaired("linux", cmpRemoteState(), cmpStatus(domain.ComputerStatusOffline))})
		back, transition := cmpReduce(state, ComputerEventComputersChanged{Name: "This Mac", Links: []*domain.ComputerLink{new(cmpLink("linux"))}})
		if len(transition.Effects) != 0 || back.Computers.Active != nil {
			t.Errorf("effects = %v, active = %v", transition.Effects, back.Computers.Active)
		}
	})
}

func TestADroppedComputerOnScreenComesBackWithItsLineAndIsToldWhetherAnyoneLooks(t *testing.T) {
	cmpPorted(t, func() {
		state := cmpWithComputers(cmpWorkspace(func(state *WorkspaceState) { state.Focused = true }), []*PairedComputer{cmpPaired("linux", cmpRemoteState())}, cmpOnScreen("linux"))
		dropped, transition := cmpReduce(state, ComputerEventComputersChanged{Name: "This Mac", Links: []*domain.ComputerLink{new(cmpLink("linux", cmpStatus(domain.ComputerStatusOffline)))}})
		if dropped.Computers.Active != nil || dropped.Computers.Dropped == nil || *dropped.Computers.Dropped != "linux" || len(transition.Effects) != 0 {
			t.Fatalf("dropped = %v %v %v", dropped.Computers.Active, dropped.Computers.Dropped, transition.Effects)
		}
		back, transition := cmpReduce(dropped, ComputerEventComputersChanged{Name: "This Mac", Links: []*domain.ComputerLink{new(cmpLink("linux"))}})
		if back.Computers.Active == nil || *back.Computers.Active != "linux" || back.Computers.Dropped != nil {
			t.Fatalf("back = %v %v", back.Computers.Active, back.Computers.Dropped)
		}
		cmpSameJSON(t, transition.Effects, []WorkspaceEffect{WorkspaceEffectComputerForward{ID: "linux", Inputs: []WorkspaceInput{contracts.ViewCommandViewSetFocused{Focused: true}}}}, "told again")
	})
}

func TestPairingRenamingAndLabelling(t *testing.T) {
	cmpPorted(t, func() {
		state := cmpWithComputers(cmpWorkspace(), []*PairedComputer{cmpPaired("linux", nil)})
		opened, transition := cmpReduce(state, contracts.ComputerCommandComputersPair{Host: "h", Name: "n"})
		if opened.Computers.Pairing == nil || opened.Computers.Pairing.Busy || len(transition.Effects) != 0 {
			t.Errorf("a pair with no code only opens the card: %+v %v", opened.Computers.Pairing, transition.Effects)
		}
		sent, transition := cmpReduce(state, contracts.ComputerCommandComputersPair{Host: "h", Name: "n", Code: "ABC"})
		if !sent.Computers.Pairing.Busy {
			t.Error("a code on its way is busy")
		}
		cmpSameJSON(t, transition.Effects, []WorkspaceEffect{WorkspaceEffectComputerPair{Host: "h", Name: "n", Code: "ABC"}}, "pair")
		renamed, transition := cmpReduce(state, contracts.ComputerCommandComputersRename{Name: "  Studio  "})
		if renamed.Computers.Name != "Studio" {
			t.Errorf("name = %q", renamed.Computers.Name)
		}
		cmpSameJSON(t, transition.Effects, []WorkspaceEffect{WorkspaceEffectComputerRename{Name: "Studio"}}, "rename")
		cleared, transition := cmpReduce(state, contracts.ComputerCommandComputersRename{Name: "   "})
		if cleared.Computers.Name != state.Computers.Name {
			t.Error("an empty name is the host's to fill in")
		}
		cmpSameJSON(t, transition.Effects, []WorkspaceEffect{WorkspaceEffectComputerRename{Name: ""}}, "rename empty")
		long, _ := cmpReduce(state, contracts.ComputerCommandComputersRename{Name: strings.Repeat("x", domain.MaxComputerName+10)})
		if len(long.Computers.Name) != domain.MaxComputerName {
			t.Errorf("a name is cut to length: %d", len(long.Computers.Name))
		}
		labelled, transition := cmpReduce(state, contracts.ComputerCommandComputersLabel{ID: "linux", Name: "Box"})
		if labelled.Computers.Paired[0].Name != "Box" || state.Computers.Paired[0].Name != "linux" {
			t.Error("a label renames a copy")
		}
		cmpSameJSON(t, transition.Effects, []WorkspaceEffect{WorkspaceEffectComputerLabel{ID: "linux", Name: "Box"}}, "label")
		if transition := ReduceComputers(state, contracts.ComputerCommandComputersLabel{ID: "gone", Name: "Box"}); transition.Patch != nil || len(transition.Effects) != 0 {
			t.Error("a computer no longer paired takes no label")
		}
	})
}

func TestForgettingAComputerTakesItOffScreenAndOutOfTheFilter(t *testing.T) {
	cmpPorted(t, func() {
		state := cmpWithComputers(cmpWorkspace(), []*PairedComputer{cmpPaired("linux", nil)}, cmpOnScreen("linux"), func(computers *ComputersState) { computers.Filter = "linux" })
		forgot, transition := cmpReduce(state, contracts.ComputerCommandComputersForget{ID: "linux"})
		if forgot.Computers.Active != nil || forgot.Computers.Filter != "all" {
			t.Errorf("forgot = %v %v", forgot.Computers.Active, forgot.Computers.Filter)
		}
		cmpSameJSON(t, transition.Effects, []WorkspaceEffect{WorkspaceEffectComputerForget{ID: "linux"}}, "forget")
	})
}

func TestAPairedComputersStateReplacesOnlyItsOwn(t *testing.T) {
	cmpPorted(t, func() {
		other := cmpPaired("mac", nil)
		state := cmpWithComputers(cmpWorkspace(), []*PairedComputer{cmpPaired("linux", nil), other})
		remote := cmpRemoteState()
		next, _ := cmpReduce(state, ComputerEventComputerState{ID: "linux", State: *remote})
		if next.Computers.Paired[0].State == nil || next.Computers.Paired[0].State.CurrentID == nil || next.Computers.Paired[1] != other || state.Computers.Paired[0].State != nil {
			t.Error("only the named computer's state is replaced, on a copy")
		}
	})
}

func TestComputerRequestsThatCarryFilesTakeTheTransferDeadline(t *testing.T) {
	if !IsComputerTransfer(ComputerInputRequest{Inputs: []WorkspaceInput{contracts.TaskCommandAttachmentsSend{Attachments: []*domain.OutgoingAttachment{{ID: "a"}}}}}) {
		t.Error("attachments")
	}
	if IsComputerTransfer(ComputerInputRequest{Inputs: []WorkspaceInput{contracts.TaskCommandAttachmentsSend{Attachments: []*domain.OutgoingAttachment{}}}}) {
		t.Error("no attachments")
	}
	if !IsComputerTransfer(contracts.ComputerQueryRequest{Query: contracts.ComputerQueryAttachment{Name: "x"}}) {
		t.Error("attachment query")
	}
	if IsComputerTransfer(ComputerClientMessagePong{}) {
		t.Error("pong")
	}
}

func TestReduceRemote(t *testing.T) {
	refresh := ReduceRemote(nil, contracts.RemoteCommandRemoteRefresh{})
	if next := Afterward(&WorkspaceState{}, refresh); !next.RemoteChecking || len(refresh.Effects) != 1 {
		t.Error("a refresh says it is checking")
	}
	enabled := ReduceRemote(nil, contracts.RemoteCommandRemoteSetEnabled{Enabled: true})
	if !enabled.ClearsError {
		t.Error("a command clears the error")
	}
	cmpSameJSON(t, enabled.Effects, []WorkspaceEffect{WorkspaceEffectRemoteSetEnabled{Enabled: true}}, "set-enabled")
	changed := Afterward(&WorkspaceState{RemoteChecking: true}, ReduceRemote(nil, RemoteEvent{Remote: domain.MobileServerState{Enabled: true}}))
	if changed.RemoteChecking || !changed.Remote.Enabled {
		t.Error("what main says lands")
	}
}
