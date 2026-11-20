package mobile

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestTheBridgeHandsAPhonesRequestToWhoeverAnswersAndPublishesTheirView(t *testing.T) {
	bridge := NewBridge()
	port := 0
	bridge.Start(BridgeOptions{UserData: t.TempDir(), Port: &port, Tailscale: noTailscale()})
	t.Cleanup(bridge.Stop)

	var mu sync.Mutex
	states := 0
	stopStates := bridge.OnMobileState(func(domain.MobileServerState) {
		mu.Lock()
		states++
		mu.Unlock()
	})
	defer stopStates()
	stopRequests := bridge.OnMobileRequest(func(request contracts.MobileRequest) {
		if snapshot, ok := request.(contracts.MobileRequestSnapshot); ok {
			go func() {
				text := []byte(viewJSON(t, testView("from the runtime")))
				bridge.AnswerMobileRequest(contracts.MobileResponseTrue{RequestID: snapshot.RequestID, Result: text})
			}()
		}
	})
	defer stopRequests()

	on, err := bridge.SetMobileEnabled(context.Background(), true)
	if err != nil || on.Status != domain.MobileServerStatusListening {
		t.Fatalf("on %+v, %v", on, err)
	}
	offer, err := bridge.CreateMobilePairingCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	client := newPhone(t, fmt.Sprintf("ws://127.0.0.1:%d/m/socket", int(*on.Port)), nil)
	client.send(pair(offer.Code, "iPhone"))
	snapshot := client.waitFor("snapshot")
	if snapshot["view"].(message)["groups"].([]any)[0].(message)["name"] != "from the runtime" {
		t.Fatalf("snapshot %v", snapshot)
	}
	bridge.PublishMobileView(patchUpdate(t, `{"groups":[]}`))
	client.waitFor("patch")
	mu.Lock()
	defer mu.Unlock()
	if states == 0 {
		t.Fatal("settings never heard the bridge move")
	}
}

func TestABridgeWithNoOneToAnswerTellsThePhone(t *testing.T) {
	bridge := NewBridge()
	bridge.Start(BridgeOptions{UserData: t.TempDir(), Tailscale: noTailscale()})
	t.Cleanup(bridge.Stop)
	if bridge.emitRequest(contracts.MobileRequestSnapshot{RequestID: "r", SessionID: "s"}) {
		t.Fatal("a request with no listener was handed over")
	}
}
