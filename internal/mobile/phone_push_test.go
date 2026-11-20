package mobile

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var pushToken = strings.Repeat("ab", 32)
var phonePush = domain.PhonePush{Token: pushToken, Environment: domain.PushEnvironmentProduction}
var notice = contracts.ThreadNotice{TaskID: "task-1", Title: "Fix the build", Headline: "Waiting for your permission to use Bash"}

func pairPhone(t *testing.T, devices *PairingStore, name string, kind domain.PairedDeviceKind) string {
	t.Helper()
	outcome := devices.Redeem(devices.Mint(at).Code, name, "10.0.0.9", at+1, kind)
	if !outcome.OK {
		t.Fatal(outcome.Message)
	}
	return outcome.Device.ID
}

type sentPush struct {
	url  string
	body map[string]any
}

func relayAnswering(status int) (*[]sentPush, PushSender) {
	var mu sync.Mutex
	sent := []sentPush{}
	return &sent, func(_ context.Context, url string, body []byte) (int, error) {
		var decoded map[string]any
		_ = jsonx.Unmarshal(body, &decoded)
		mu.Lock()
		sent = append(sent, sentPush{url: url, body: decoded})
		mu.Unlock()
		return status, nil
	}
}

func targetIDs(devices *PairingStore) []string {
	ids := []string{}
	for _, device := range devices.PushTargets() {
		ids = append(ids, device.ID)
	}
	return ids
}

func TestAPhonesPushTokenIsKeptAcrossLaunchesAndNeverShownToSettings(t *testing.T) {
	file := deviceFile(t)
	devices := NewPairingStore(file)
	phone := pairPhone(t, devices, "iPhone", domain.PairedDeviceKindPhone)
	devices.SetPush(phone, &phonePush)

	targets := NewPairingStore(file).PushTargets()
	if len(targets) != 1 || targets[0].ID != phone || *targets[0].Push != phonePush {
		t.Fatalf("targets %+v", targets)
	}
	view, _ := jsonx.Marshal(domain.DeviceView(targets[0]))
	if strings.Contains(string(view), "push") {
		t.Fatalf("settings sees the token: %s", view)
	}
}

func TestATokenMovesToTheNewestPairingOfTheSamePhone(t *testing.T) {
	devices := NewPairingStore(deviceFile(t))
	old := pairPhone(t, devices, "iPhone", domain.PairedDeviceKindPhone)
	fresh := pairPhone(t, devices, "iPhone", domain.PairedDeviceKindPhone)
	devices.SetPush(old, &phonePush)
	devices.SetPush(fresh, &phonePush)
	if ids := targetIDs(devices); len(ids) != 1 || ids[0] != fresh {
		t.Fatalf("targets %v", ids)
	}
	devices.SetPush(fresh, nil)
	if ids := targetIDs(devices); len(ids) != 0 {
		t.Fatalf("targets %v", ids)
	}
}

func TestOnlyPhonesTakeAPushToken(t *testing.T) {
	devices := NewPairingStore(deviceFile(t))
	computer := pairPhone(t, devices, "Studio", domain.PairedDeviceKindComputer)
	devices.SetPush(computer, &phonePush)
	if ids := targetIDs(devices); len(ids) != 0 {
		t.Fatalf("targets %v", ids)
	}
}

func TestAPushTokenThatDoesNotReadIsDroppedWithoutUnpairing(t *testing.T) {
	file := deviceFile(t)
	devices := NewPairingStore(file)
	pairPhone(t, devices, "iPhone", domain.PairedDeviceKindPhone)
	text, _ := os.ReadFile(file)
	var stored map[string]any
	if err := jsonx.Unmarshal(text, &stored); err != nil {
		t.Fatal(err)
	}
	stored["devices"].([]any)[0].(map[string]any)["push"] = map[string]any{"token": "nope", "environment": "production"}
	writeJSON(t, file, stored)

	reopened := NewPairingStore(file)
	if len(reopened.List()) != 1 || len(reopened.PushTargets()) != 0 || len(devices.List()) != 1 {
		t.Fatal("an unreadable token unpaired the phone, or was kept")
	}
}

func TestAPhoneMaySendItsTokenOrGiveItUp(t *testing.T) {
	read := func(text string) bool {
		var value any
		_ = jsonx.Unmarshal([]byte(text), &value)
		return contracts.IsMobileClientMessage(value)
	}
	cases := map[string]bool{
		`{"kind":"push-token","push":{"token":"` + pushToken + `","environment":"production"}}`: true,
		`{"kind":"push-token","push":null}`:                                                     true,
		`{"kind":"push-token","push":{"token":"zz","environment":"production"}}`:                false,
		`{"kind":"push-token","push":{"token":"` + pushToken + `","environment":"beta"}}`:       false,
	}
	for text, want := range cases {
		if read(text) != want {
			t.Errorf("%s: want %v", text, want)
		}
	}
}

// relayTakes is relay/worker.mts's isPushRequest, which every notice must pass.
func relayTakes(body map[string]any) bool {
	text := func(value any, max int, allowEmpty bool) bool {
		s, ok := value.(string)
		return ok && utf16Length(s) <= max && (allowEmpty || s != "")
	}
	data, ok := body["data"].(map[string]any)
	if !ok || len(data) > 8 {
		return false
	}
	for key, value := range data {
		if key == "aps" || !text(key, 32, false) || !text(value, 256, false) {
			return false
		}
	}
	token, _ := body["token"].(string)
	return regexp.MustCompile(`^[0-9a-f]{64,200}$`).MatchString(token) &&
		(body["environment"] == "production" || body["environment"] == "sandbox") &&
		text(body["title"], 120, false) && text(body["body"], 300, true) && text(body["thread"], 128, false)
}

func TestANoticeReachesEachPhoneThroughTheRelay(t *testing.T) {
	devices := NewPairingStore(deviceFile(t))
	phone := pairPhone(t, devices, "iPhone", domain.PairedDeviceKindPhone)
	sandbox := phonePush
	sandbox.Environment = domain.PushEnvironmentSandbox
	devices.SetPush(phone, &sandbox)
	sent, send := relayAnswering(200)

	long := notice
	long.Headline = "  Waiting\n for " + strings.Repeat("x", 400)
	PushToPhones(context.Background(), devices, long, send)

	if len(*sent) != 1 || (*sent)[0].url != PushRelayURL {
		t.Fatalf("sent %+v", *sent)
	}
	body := (*sent)[0].body
	if body["token"] != pushToken || body["environment"] != "sandbox" || body["title"] != "Fix the build" {
		t.Fatalf("body %v", body)
	}
	if text := body["body"].(string); utf16Length(text) != 300 || !strings.HasPrefix(text, "Waiting for xxx") || !strings.HasSuffix(text, "…") {
		t.Fatalf("body text %q", text)
	}
	if body["thread"] != phone+":task-1" {
		t.Fatalf("thread %v", body["thread"])
	}
	data := body["data"].(map[string]any)
	if len(data) != 2 || data["taskId"] != "task-1" || data["deviceId"] != phone {
		t.Fatalf("data %v", data)
	}
	if !relayTakes(body) {
		t.Fatal("the relay would refuse this notice")
	}
}

func TestAnUntitledNoticeIsTitledForTheApp(t *testing.T) {
	device := &domain.PairedDevice{ID: "phone-1", Push: &phonePush}
	request := NewPushRequest(device, contracts.ThreadNotice{TaskID: "task-1", Title: " \n ", Headline: ""})
	if request.Title != "AI Coding Tool" || request.Body != "" {
		t.Fatalf("request %+v", request)
	}
}

func TestAPhoneAppleSaysIsGoneStopsBeingTold(t *testing.T) {
	devices := NewPairingStore(deviceFile(t))
	phone := pairPhone(t, devices, "iPhone", domain.PairedDeviceKindPhone)
	devices.SetPush(phone, &phonePush)

	_, refusing := relayAnswering(502)
	PushToPhones(context.Background(), devices, notice, refusing)
	if len(devices.PushTargets()) != 1 {
		t.Fatal("a failing relay forgot the phone")
	}
	PushToPhones(context.Background(), devices, notice, func(context.Context, string, []byte) (int, error) { return 0, errors.New("offline") })
	if len(devices.PushTargets()) != 1 {
		t.Fatal("an unreachable relay forgot the phone")
	}
	_, gone := relayAnswering(410)
	PushToPhones(context.Background(), devices, notice, gone)
	if len(devices.PushTargets()) != 0 {
		t.Fatal("a phone Apple says is gone is still told")
	}
}
