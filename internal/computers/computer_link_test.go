package computers

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func resultOK(t *testing.T) func(result contracts.ComputerServerMessageResultResult, err error) contracts.ComputerServerMessageResultResultTrue {
	return func(result contracts.ComputerServerMessageResultResult, err error) contracts.ComputerServerMessageResultResultTrue {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		ok, isOK := result.(contracts.ComputerServerMessageResultResultTrue)
		if !isOK {
			t.Fatalf("refused: %#v", result)
		}
		return ok
	}
}

func TestComputerTradesTheCodeIsHandedTheWorkspaceAndDrivesIt(t *testing.T) {
	served := startElectronHost(t)
	mac := record(ClientOptions{URL: served.url, Credential: ComputerCredential{Code: served.mint()}})
	t.Cleanup(mac.client.Stop)
	until(t, "the pairing", func() bool { _, ok := mac.paired(); return ok })
	devices := served.call("devices", nil).([]any)
	if kind := devices[0].(map[string]any)["kind"]; kind != "computer" {
		t.Fatalf("a computer is remembered as one, apart from the phones: %v", kind)
	}
	until(t, "the snapshot", func() bool { return mac.lastState() != nil })
	if first := mac.lastState(); len(first.Threads) != 1 || first.Threads[0].Title != "First" {
		t.Fatalf("snapshot threads: %#v", first.Threads)
	}
	if mac.client.Status() != domain.ComputerStatusConnected {
		t.Fatal("not connected")
	}

	served.call("publish", map[string]any{"next": map[string]any{"threads": []any{map[string]any{"id": "first", "title": "Renamed there"}}}})
	until(t, "the difference landing", func() bool {
		state := mac.lastState()
		return len(state.Threads) == 1 && state.Threads[0].Title == "Renamed there"
	})

	ok := resultOK(t)(mac.client.Send(background(), []application.WorkspaceInput{rename("first", "Renamed from here")}))
	if ok.Revision != 1 {
		t.Fatalf("revision %v", ok.Revision)
	}
	if inputs := served.inputs(); len(inputs) != 1 || inputs[0]["title"] != "Renamed from here" {
		t.Fatalf("inputs %v", inputs)
	}
	refused, err := mac.client.Send(background(), []application.WorkspaceInput{rename("first", "refuse")})
	if failed, isFailed := refused.(contracts.ComputerServerMessageResultResultFalse); err != nil || !isFailed || failed.Message != "Refused by the host" {
		t.Fatalf("refusal: %#v %v", refused, err)
	}
	answer, err := mac.client.Query(background(), contracts.ComputerQueryBranches{WorkspaceID: "ws"})
	if err != nil || string(answer) != `{"status":"available","patch":"patch for branches"}` {
		t.Fatalf("answer %s %v", answer, err)
	}
	served.call("notice", map[string]any{"notice": map[string]any{"taskId": "first", "title": "First", "headline": "The run finished."}})
	until(t, "the notice crossing", func() bool {
		mac.mu.Lock()
		defer mac.mu.Unlock()
		return len(mac.notices) == 1 && mac.notices[0] == contracts.ThreadNotice{TaskID: "first", Title: "First", Headline: "The run finished."}
	})

	mac.client.Stop()
	until(t, "the host told nobody is looking once the line dropped", func() bool {
		inputs := served.inputs()
		last := inputs[len(inputs)-1]
		return last["type"] == "view.set-focused" && last["focused"] == false
	})
}

func TestWrongCodeAndStaleTokenAreRefusedAndAskedAgainOnlyWhenTold(t *testing.T) {
	served := startElectronHost(t)
	served.mint()
	wrong := record(ClientOptions{URL: served.url, Credential: ComputerCredential{Code: "NOTTHECODE"}})
	t.Cleanup(wrong.client.Stop)
	until(t, "the refusal", func() bool {
		return wrong.saw(func(seen statusSeen) bool {
			return seen.status == domain.ComputerStatusOffline && strings.Contains(seen.err, "wrong or has expired")
		})
	})
	stale := record(ClientOptions{URL: served.url, Credential: ComputerCredential{Token: "not-a-token"}})
	t.Cleanup(stale.client.Stop)
	until(t, "the stale token turned away", func() bool {
		return stale.saw(func(seen statusSeen) bool {
			return seen.status == domain.ComputerStatusOffline && strings.Contains(seen.err, "not paired")
		})
	})
	if n := stale.count(domain.ComputerStatusConnecting); n != 1 {
		t.Fatalf("a line the host will not have back is not dialled again soon: %d", n)
	}
	stale.client.Reconnect()
	until(t, "asking again when told to", func() bool { return stale.count(domain.ComputerStatusConnecting) == 2 })
	wrong.client.Reconnect()
	sleep(50)
	if n := wrong.count(domain.ComputerStatusConnecting); n != 1 {
		t.Fatalf("a spent code is never tried again: %d", n)
	}
}

// paired pairs over line and returns the token, hanging the pairing line up.
func pairedToken(t *testing.T, url, code string) string {
	t.Helper()
	pairing := record(ClientOptions{URL: url, Credential: ComputerCredential{Code: code}})
	until(t, "the pairing", func() bool { _, ok := pairing.paired(); return ok })
	pairing.client.Stop()
	token, _ := pairing.paired()
	return token
}

func TestLineThatDropsAndResumesWhereNothingChangedIsLive(t *testing.T) {
	served := startElectronHost(t)
	line := newCuttable(t, func() string { return served.url })
	token := pairedToken(t, line.url, served.mint())
	mac := record(ClientOptions{URL: line.url, Credential: ComputerCredential{Token: token}})
	t.Cleanup(mac.client.Stop)
	until(t, "the first connection", func() bool { return mac.client.Status() == domain.ComputerStatusConnected })
	snapshots := mac.stateCount()
	line.cut()
	until(t, "noticing the drop", func() bool { return mac.count(domain.ComputerStatusOffline) > 0 })
	until(t, "the resumed line counted as live", func() bool { return mac.client.Status() == domain.ComputerStatusConnected })
	if mac.stateCount() != snapshots {
		t.Fatal("a resume with nothing missed sends no state")
	}
	resultOK(t)(mac.client.Send(background(), []application.WorkspaceInput{rename("first", "after the drop")}))
}

func TestSendInFlightWhenTheLineDropsIsCarriedAgainAndRunsOnce(t *testing.T) {
	served := startElectronHost(t)
	served.call("gate", nil)
	line := newCuttable(t, func() string { return served.url })
	token := pairedToken(t, line.url, served.mint())
	mac := record(ClientOptions{URL: line.url, Credential: ComputerCredential{Token: token}})
	t.Cleanup(mac.client.Stop)
	until(t, "the first connection", func() bool { return mac.client.Status() == domain.ComputerStatusConnected })
	sent := make(chan error, 1)
	go func() {
		result, err := mac.client.Send(background(), []application.WorkspaceInput{rename("first", "slow")})
		if err == nil {
			if _, ok := result.(contracts.ComputerServerMessageResultResultTrue); !ok {
				err = errors.New("refused")
			}
		}
		sent <- err
	}()
	until(t, "the send reaching the host", func() bool { return served.renames() == 1 })
	line.cut()
	until(t, "noticing the drop", func() bool { return mac.count(domain.ComputerStatusOffline) > 0 })
	until(t, "the line back", func() bool { return mac.client.Status() == domain.ComputerStatusConnected })
	served.call("release", nil)
	if err := <-sent; err != nil {
		t.Fatalf("the answer finds the line it came back on: %v", err)
	}
	if served.renames() != 1 {
		t.Fatal("the carried request is not run twice")
	}
}

func TestSendInFlightWhenTheOtherComputerRestartsIsRefused(t *testing.T) {
	served := startElectronHost(t)
	served.call("gate", nil)
	port := func() string {
		return "ws://127.0.0.1:" + itoa(int(served.call("port", nil).(float64))) + WorkspaceSocketPath
	}
	line := newCuttable(t, port)
	token := pairedToken(t, line.url, served.mint())
	mac := record(ClientOptions{URL: line.url, Credential: ComputerCredential{Token: token}})
	t.Cleanup(mac.client.Stop)
	until(t, "the first connection", func() bool { return mac.client.Status() == domain.ComputerStatusConnected })
	sent := make(chan error, 1)
	go func() {
		_, err := mac.client.Send(background(), []application.WorkspaceInput{rename("first", "slow")})
		sent <- err
	}()
	until(t, "the send reaching the host", func() bool { return served.renames() == 1 })
	served.call("release", nil)
	line.cut()
	served.call("restart", nil)
	if err := <-sent; err == nil || err.Error() != ComputerRestarted {
		t.Fatalf("expected the restart refusal, got %v", err)
	}
	if served.renames() != 1 {
		t.Fatal("a restarted computer is not handed what it may have run")
	}
	until(t, "the line back", func() bool { return mac.client.Status() == domain.ComputerStatusConnected })
	resultOK(t)(mac.client.Send(background(), []application.WorkspaceInput{rename("first", "after the restart")}))
}

func TestComputerThatRefusedOnceAndTookTheLineBackIsDialledSoonAfter(t *testing.T) {
	served := startElectronHost(t)
	line := newCuttable(t, func() string { return served.url })
	token := pairedToken(t, line.url, served.mint())
	served.call("refuse", map[string]any{"on": true})
	mac := record(ClientOptions{URL: line.url, Credential: ComputerCredential{Token: token}})
	t.Cleanup(mac.client.Stop)
	until(t, "the refusal", func() bool {
		return mac.saw(func(seen statusSeen) bool {
			return seen.status == domain.ComputerStatusOffline && strings.Contains(seen.err, "not paired")
		})
	})
	served.call("refuse", map[string]any{"on": false})
	mac.client.Reconnect()
	until(t, "taken back", func() bool { return mac.client.Status() == domain.ComputerStatusConnected })
	dials := mac.count(domain.ComputerStatusConnecting)
	line.cut()
	until(t, "dialled again on the usual pause, not the refusal's", func() bool { return mac.count(domain.ComputerStatusConnecting) > dials })
}

func TestHostThatCannotReadItsDevicesSaysSo(t *testing.T) {
	served := startElectronHost(t)
	token := pairedToken(t, served.url, served.mint())
	served.call("unreadable", nil)
	mac := record(ClientOptions{URL: served.url, Credential: ComputerCredential{Token: token}})
	t.Cleanup(mac.client.Stop)
	until(t, "the reason", func() bool {
		return mac.saw(func(seen statusSeen) bool { return strings.Contains(seen.err, "could not read its paired devices") })
	})
	until(t, "dialled again on the usual pause", func() bool { return mac.count(domain.ComputerStatusConnecting) >= 2 })
	if mac.saw(func(seen statusSeen) bool { return strings.Contains(seen.err, "not paired") }) {
		t.Fatal("taken to have unpaired")
	}
}

func dataAttachments(count int, data string) []*domain.OutgoingAttachment {
	attachments := make([]*domain.OutgoingAttachment, count)
	for id := range attachments {
		attachments[id] = &domain.OutgoingAttachment{ID: itoa(id), Source: "data:image/png;base64," + data, Annotations: []*domain.ImageAnnotation{}}
	}
	return attachments
}

func TestStripLargerThanTheOldCapCrossesAndOversizeLeavesTheLinkUsable(t *testing.T) {
	served := startElectronHost(t)
	mac := record(ClientOptions{URL: served.url, Credential: ComputerCredential{Code: served.mint()}})
	t.Cleanup(mac.client.Stop)
	until(t, "the connection", func() bool { return mac.client.Status() == domain.ComputerStatusConnected })
	attachments := dataAttachments(domain.MaxAttachments, strings.Repeat("A", 512*1024))
	resultOK(t)(mac.client.Send(background(), []application.WorkspaceInput{contracts.TaskCommandAttachmentsSend{Attachments: attachments}}))
	inputs := served.inputs()
	if len(inputs) != 1 || len(inputs[0]["attachments"].([]any)) != domain.MaxAttachments {
		t.Fatal("the strip did not arrive whole")
	}
	sent, _ := jsonx.Marshal(contracts.TaskCommandAttachmentsSend{Attachments: attachments})
	arrived, _ := jsonx.Marshal(inputs[0])
	if !jsonEqual(sent, arrived) {
		t.Fatal("the strip changed on the way")
	}
	oversize := dataAttachments(1, strings.Repeat("A", domain.MaxAttachmentEncodedBytes+1))
	if _, err := mac.client.Send(background(), []application.WorkspaceInput{contracts.TaskCommandAttachmentsSend{Attachments: oversize}}); err == nil || err.Error() != contracts.ComputerSendTooLarge {
		t.Fatalf("oversize: %v", err)
	}
	if len(served.inputs()) != 1 {
		t.Fatal("the oversized payload never reaches the socket")
	}
	if mac.client.Status() != domain.ComputerStatusConnected {
		t.Fatal("dropped")
	}
	text := "still connected"
	resultOK(t)(mac.client.Send(background(), []application.WorkspaceInput{contracts.TaskCommandTaskSend{Text: &text}}))
	answer, err := mac.client.Query(background(), contracts.ComputerQueryAttachment{Name: "shot.png"})
	if err != nil || string(answer) != `{"status":"available","patch":"patch for attachment"}` {
		t.Fatalf("answer %s %v", answer, err)
	}
}

func TestFullStripJustUnderTheDecodedLimitCrosses(t *testing.T) {
	if testing.Short() {
		t.Skip("moves about 200 MB")
	}
	served := startElectronHost(t)
	mac := record(ClientOptions{URL: served.url, Credential: ComputerCredential{Code: served.mint()}})
	t.Cleanup(mac.client.Stop)
	until(t, "the connection", func() bool { return mac.client.Status() == domain.ComputerStatusConnected })
	data := strings.Repeat("BwcH", (domain.MaxAttachmentBytes-1)/3) + "Bwc="
	attachments := dataAttachments(domain.MaxAttachments, data)
	resultOK(t)(mac.client.Send(background(), []application.WorkspaceInput{contracts.TaskCommandAttachmentsSend{Attachments: attachments}}))
	if mac.client.Status() != domain.ComputerStatusConnected {
		t.Fatal("dropped")
	}
}

func jsonEqual(a, b []byte) bool {
	var left, right any
	if jsonx.Unmarshal(a, &left) != nil || jsonx.Unmarshal(b, &right) != nil {
		return false
	}
	l, _ := jsonx.Marshal(left)
	r, _ := jsonx.Marshal(right)
	return slices.Equal(l, r)
}
