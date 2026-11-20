package mobile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const at = 1_700_000_000_000.0
const phoneSource = "10.0.0.9"

func deviceFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "mobile-devices.v1.json")
}

func phoneKind() domain.PairedDeviceKind { return domain.PairedDeviceKindPhone }

func TestAPairingCodeIsWorthOnePhoneOnlyWhileItIsOnScreen(t *testing.T) {
	devices := NewPairingStore(deviceFile(t))

	stale := devices.Mint(at)
	expired := devices.Redeem(stale.Code, "iPhone", phoneSource, at+domain.PairingCodeTtlMs+1, phoneKind())
	if expired.OK || expired.Code != contracts.MobileErrorCodeExpiredCode {
		t.Fatalf("expired code: %+v", expired)
	}
	if len(devices.List()) != 0 {
		t.Fatal("an expired code bought a device")
	}

	code := devices.Mint(at)
	paired := devices.Redeem(code.Code, "iPhone", phoneSource, at+1_000, phoneKind())
	if !paired.OK || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(paired.Token) {
		t.Fatalf("pairing: %+v", paired)
	}
	second := devices.Redeem(code.Code, "iPad", phoneSource, at+2_000, phoneKind())
	if second.OK {
		t.Fatal("a spent code buys nothing a second time")
	}
	if devices.Pending(at+2_000) != nil {
		t.Fatal("a spent code is still pending")
	}
}

func TestACodeIsTradedForATokenTheMacKeepsOnlyTheHashOf(t *testing.T) {
	file := deviceFile(t)
	devices := NewPairingStore(file)
	paired := devices.Redeem(devices.Mint(at).Code, "  iPhone  ", phoneSource, at, phoneKind())
	if !paired.OK {
		t.Fatal(paired.Message)
	}
	if found := devices.Authenticate(paired.Token, at); found == nil || found.ID != paired.Device.ID {
		t.Fatal("the token does not authenticate")
	}
	last := "0"
	if strings.HasSuffix(paired.Token, "0") {
		last = "1"
	}
	if devices.Authenticate(paired.Token[:63]+last, at) != nil || devices.Authenticate("", at) != nil {
		t.Fatal("a wrong token authenticated")
	}
	if paired.Device.Name != "iPhone" {
		t.Fatalf("name %q", paired.Device.Name)
	}
	written, _ := os.ReadFile(file)
	if strings.Contains(string(written), paired.Token) {
		t.Fatal("the token itself is never written down")
	}
	if found := NewPairingStore(file).Authenticate(paired.Token, at); found == nil || found.ID != paired.Device.ID {
		t.Fatal("a restart forgot the phone")
	}
}

func TestARevokedPhoneIsForgottenWithItsEdits(t *testing.T) {
	file := deviceFile(t)
	devices := NewPairingStore(file)
	paired := devices.Redeem(devices.Mint(at).Code, "iPhone", phoneSource, at, phoneKind())

	devices.RememberEdit(paired.Device.ID, "edit-1", EditOutcome{Message: "Already sent."})
	if outcome := NewPairingStore(file).EditOutcome(paired.Device.ID, "edit-1"); outcome == nil || outcome.OK || outcome.Message != "Already sent." {
		t.Fatalf("an edit's outcome survives a restart: %+v", outcome)
	}

	if devices.Revoke("not-a-device") {
		t.Fatal("revoked a device that is not there")
	}
	if !devices.Revoke(paired.Device.ID) {
		t.Fatal("the device was not revoked")
	}
	if devices.Authenticate(paired.Token, at) != nil || len(devices.Views()) != 0 {
		t.Fatal("a revoked device is still known")
	}
	reopened := NewPairingStore(file)
	if len(reopened.List()) != 0 || reopened.EditOutcome(paired.Device.ID, "edit-1") != nil {
		t.Fatal("the revoked device survived on disk")
	}
}

func TestWrongCodesShutTheDoorOnThePhoneThatKeepsTrying(t *testing.T) {
	devices := NewPairingStore(deviceFile(t))
	devices.Mint(at)
	for range domain.MaxPairingFailures {
		if refused := devices.Redeem("WRONGONE", "iPhone", phoneSource, at, phoneKind()); refused.Code != contracts.MobileErrorCodeExpiredCode {
			t.Fatalf("refusal %+v", refused)
		}
	}
	if !devices.Locked(phoneSource, at) {
		t.Fatal("not locked")
	}
	code := devices.Mint(at)
	if locked := devices.Redeem(code.Code, "iPhone", phoneSource, at, phoneKind()); locked.Code != contracts.MobileErrorCodeRateLimited {
		t.Fatal("even the right code waits out the lockout")
	}
	if devices.Locked("192.168.1.20", at) {
		t.Fatal("another phone is not punished for this one")
	}
	if !devices.Redeem(code.Code, "iPad", "192.168.1.20", at, phoneKind()).OK {
		t.Fatal("another phone could not pair")
	}
}

func TestAScanWithNoCodeOnScreenIsNotAGuess(t *testing.T) {
	file := deviceFile(t)
	devices := NewPairingStore(file)
	for range domain.MaxPairingFailures * 2 {
		devices.Redeem("K7M2P9QX", "iPhone", phoneSource, at, phoneKind())
	}
	if devices.Locked(phoneSource, at) {
		t.Fatal("locked without a code on screen")
	}
	if !devices.Redeem(devices.Mint(at).Code, "iPhone", phoneSource, at, phoneKind()).OK {
		t.Fatal("could not pair")
	}
	if _, err := os.Stat(file + ".tmp"); err == nil {
		t.Fatal("the staging file was left behind")
	}
}

func TestAWrongGuessLeavesTheCodeStanding(t *testing.T) {
	devices := NewPairingStore(deviceFile(t))
	code := devices.Mint(at)
	if devices.Redeem("WRONGONE", "iPhone", phoneSource, at, phoneKind()).OK {
		t.Fatal("a wrong code paired")
	}
	if pending := devices.Pending(at); pending == nil || pending.Code != code.Code {
		t.Fatal("one guess from anywhere cannot cancel a pairing")
	}
	if !devices.Redeem(code.Code, "iPhone", "192.168.1.30", at, phoneKind()).OK {
		t.Fatal("the code no longer pairs")
	}
	if devices.Pending(at) != nil {
		t.Fatal("the code that bought a device is spent")
	}
}

func TestAStaleFailureCountIsForgotten(t *testing.T) {
	devices := NewPairingStore(deviceFile(t))
	devices.Mint(at)
	for range domain.MaxPairingFailures - 1 {
		devices.Redeem("WRONGONE", "iPhone", phoneSource, at, phoneKind())
	}
	if devices.Locked(phoneSource, at) {
		t.Fatal("locked early")
	}
	later := at + domain.PairingLockoutMs + 1
	devices.Mint(later)
	devices.Redeem("WRONGONE", "iPad", "10.0.0.99", later, phoneKind())
	devices.Redeem("WRONGONE", "iPhone", phoneSource, later, phoneKind())
	if devices.Locked(phoneSource, later) {
		t.Fatal("the count started again once it was as old as a lockout")
	}
}

func TestAPhoneThatPairsClearsItsFailures(t *testing.T) {
	devices := NewPairingStore(deviceFile(t))
	code := devices.Mint(at)
	devices.Redeem("WRONGONE", "iPhone", phoneSource, at, phoneKind())
	devices.Redeem("WRONGTWO", "iPhone", phoneSource, at, phoneKind())
	if !devices.Redeem(code.Code, "iPhone", phoneSource, at, phoneKind()).OK {
		t.Fatal("could not pair")
	}
	devices.Mint(at)
	for range domain.MaxPairingFailures - 1 {
		devices.Redeem("WRONGONE", "iPhone", phoneSource, at, phoneKind())
	}
	if devices.Locked(phoneSource, at) {
		t.Fatal("the count started again from the phone that got in")
	}
	devices.Redeem("WRONGONE", "iPhone", phoneSource, at, phoneKind())
	if !devices.Locked(phoneSource, at) {
		t.Fatal("the fresh code starts a full allowance of guesses")
	}
}

func storedDevice(index int) map[string]any {
	return map[string]any{"id": fmt.Sprintf("device-%d", index), "kind": "phone", "name": fmt.Sprintf("Phone %d", index), "tokenHash": fmt.Sprintf("%064x", index), "pairedAt": at, "lastSeenAt": nil}
}

func writeJSON(t *testing.T, file string, value any) {
	t.Helper()
	text, err := jsonx.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, text, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAFullComputerSaysSoAndKeepsTheCodeStanding(t *testing.T) {
	file := deviceFile(t)
	devices := []any{}
	for index := range domain.MaxPairedDevices {
		devices = append(devices, storedDevice(index))
	}
	writeJSON(t, file, map[string]any{"version": 1, "devices": devices})
	store := NewPairingStore(file)
	code := store.Mint(at)
	full := store.Redeem(code.Code, "iPhone", phoneSource, at, phoneKind())
	if full.OK || !strings.Contains(full.Message, "already paired with 16 devices") {
		t.Fatalf("full: %+v", full)
	}
	if pending := store.Pending(at); pending == nil || pending.Code != code.Code {
		t.Fatal("the code is not spent on a pairing that could not happen")
	}
	if !store.Revoke("device-0") || !store.Redeem(code.Code, "iPhone", phoneSource, at, phoneKind()).OK {
		t.Fatal("a revoke did not make room")
	}
}

func TestAListThatCannotBeReadIsNeverWrittenOver(t *testing.T) {
	file := deviceFile(t)
	// A folder where the file should be fails the read with something other than "no such file".
	if err := os.Mkdir(file, 0o700); err != nil {
		t.Fatal(err)
	}
	devices := NewPairingStore(file)
	if !devices.Unreadable() {
		t.Fatal("a folder read as a list")
	}
	now := application.Now()
	code := devices.Mint(now)
	if refused := devices.Redeem(code.Code, "iPhone", phoneSource, now, phoneKind()); refused.Code != contracts.MobileErrorCodeInternal {
		t.Fatalf("refusal %+v", refused)
	}
	if pending := devices.Pending(now); pending == nil || pending.Code != code.Code {
		t.Fatal("the code survives a list that could not be read")
	}

	if err := os.RemoveAll(file); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, file, map[string]any{"version": 1, "devices": []any{storedDevice(1)}})
	if devices.Authenticate("x", application.Now()) != nil {
		t.Fatal("authenticated nothing")
	}
	if !devices.Unreadable() {
		t.Fatal("the list is not read again on every knock")
	}
	devices.Authenticate("x", application.Now()+10_000)
	if devices.Unreadable() {
		t.Fatal("the list was not read again")
	}
	if list := devices.List(); len(list) != 1 || list[0].ID != "device-1" {
		t.Fatalf("list %+v", list)
	}
}

func TestADamagedListIsSetAside(t *testing.T) {
	file := deviceFile(t)
	if err := os.WriteFile(file, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	devices := NewPairingStore(file)
	if devices.Unreadable() {
		t.Fatal("a damaged list is not unreadable")
	}
	if aside, _ := os.ReadFile(file + ".damaged"); string(aside) != "{ not json" {
		t.Fatalf("set aside %q", aside)
	}
	if !devices.Redeem(devices.Mint(at).Code, "iPhone", phoneSource, at, phoneKind()).OK {
		t.Fatal("could not pair")
	}
}

func TestRecordsThisVersionCannotReadAreWrittenBackAsTheyWere(t *testing.T) {
	file := deviceFile(t)
	future := map[string]any{"id": "tablet-1", "kind": "tablet", "name": "Tablet", "tokenHash": strings.Repeat("f", 64), "pairedAt": at, "lastSeenAt": nil, "extra": map[string]any{"kept": true}}
	known := storedDevice(1)
	known["color"] = "blue"
	writeJSON(t, file, map[string]any{"version": 1, "devices": []any{known, future}, "note": "from a newer version"})
	devices := NewPairingStore(file)
	if list := devices.List(); len(list) != 1 || list[0].ID != "device-1" {
		t.Fatalf("list %+v", list)
	}
	if !devices.Redeem(devices.Mint(at).Code, "iPhone", phoneSource, at, phoneKind()).OK {
		t.Fatal("could not pair")
	}
	text, _ := os.ReadFile(file)
	var written struct {
		Devices []map[string]any `json:"devices"`
		Note    string           `json:"note"`
	}
	if err := jsonx.Unmarshal(text, &written); err != nil {
		t.Fatal(err)
	}
	if written.Note != "from a newer version" || len(written.Devices) != 3 {
		t.Fatalf("written %s", text)
	}
	var tablet, first map[string]any
	for _, device := range written.Devices {
		switch device["id"] {
		case "tablet-1":
			tablet = device
		case "device-1":
			first = device
		}
	}
	if tablet["kind"] != "tablet" || tablet["extra"].(map[string]any)["kept"] != true {
		t.Fatalf("tablet %v", tablet)
	}
	if first["color"] != "blue" {
		t.Fatalf("a field this version does not read was lost: %v", first)
	}
}

func TestAPhoneWrittenBeforeComputersCouldPairIsAPhone(t *testing.T) {
	file := deviceFile(t)
	legacy := storedDevice(1)
	delete(legacy, "kind")
	writeJSON(t, file, map[string]any{"version": 1, "devices": []any{legacy}})
	if list := NewPairingStore(file).List(); len(list) != 1 || list[0].Kind != domain.PairedDeviceKindPhone {
		t.Fatalf("list %+v", list)
	}
}
