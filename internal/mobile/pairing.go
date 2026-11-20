package mobile

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// unnamedDevice is what a phone is called when it does not say.
const unnamedDevice = "Phone"
const maxDeviceName = 128

// seenWriteIntervalMs is how far `lastSeenAt` may drift before it is worth a write. It is shown in
// days, so a minute is nothing.
const seenWriteIntervalMs = 60 * 1_000

// rereadIntervalMs is how often a list that could not be read is read again, when a device that may be on it knocks.
const rereadIntervalMs = 5 * 1_000

// maxEditsPerDevice is how many edits' outcomes each device keeps. A phone waits on one edit at a time.
const maxEditsPerDevice = 8

// EditOutcome is what came of a phone's edit of a queued message.
type EditOutcome struct {
	OK      bool
	Message string
}

type storedEdit struct {
	RequestID string  `json:"requestId"`
	OK        bool    `json:"ok"`
	Message   *string `json:"message,omitzero"`
}

func readStoredEdit(value any) (storedEdit, bool) {
	edit, ok := value.(map[string]any)
	if !ok {
		return storedEdit{}, false
	}
	requestID, ok := edit["requestId"].(string)
	if !ok {
		return storedEdit{}, false
	}
	if edit["ok"] == true {
		return storedEdit{RequestID: requestID, OK: true}, true
	}
	message, ok := edit["message"].(string)
	if edit["ok"] == false && ok {
		return storedEdit{RequestID: requestID, Message: &message}, true
	}
	return storedEdit{}, false
}

// PairingOutcome is a device and its token, or why there is none (OK false).
type PairingOutcome struct {
	OK      bool
	Device  *domain.PairedDevice
	Token   string
	Code    contracts.MobileErrorCode
	Message string
}

// pairedRecord is a device this version reads, with whatever fields of it this version does not, so
// they are written back as they came.
type pairedRecord struct {
	device domain.PairedDevice
	extra  map[string]jsontext.Value
}

func (r *pairedRecord) MarshalJSONTo(enc *jsontext.Encoder) error {
	known, err := jsonx.Marshal(r.device)
	if err != nil {
		return err
	}
	if len(r.extra) == 0 {
		return enc.WriteValue(known)
	}
	fields := map[string]jsontext.Value{}
	if err := jsonx.Unmarshal(known, &fields); err != nil {
		return err
	}
	for key, value := range r.extra {
		if _, ok := fields[key]; !ok {
			fields[key] = value
		}
	}
	merged, err := jsonx.Marshal(fields)
	if err != nil {
		return err
	}
	return enc.WriteValue(merged)
}

var deviceFields = map[string]bool{"id": true, "kind": true, "name": true, "tokenHash": true, "pairedAt": true, "lastSeenAt": true, "push": true}

var tokenHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func isStoredDevice(device map[string]any) bool {
	id, ok := device["id"].(string)
	if !ok || id == "" {
		return false
	}
	if device["kind"] != "phone" && device["kind"] != "computer" {
		return false
	}
	if _, ok := device["name"].(string); !ok {
		return false
	}
	hash, ok := device["tokenHash"].(string)
	if !ok || !tokenHashPattern.MatchString(hash) {
		return false
	}
	if _, ok := device["pairedAt"].(float64); !ok {
		return false
	}
	lastSeenAt, present := device["lastSeenAt"]
	if !present {
		return false
	}
	_, number := lastSeenAt.(float64)
	return lastSeenAt == nil || number
}

// readRecord reads one stored device: one written before computers could pair is a phone, which is
// all there was, and a push token that does not read is dropped on its own while the device stays paired.
func readRecord(raw jsontext.Value) (*pairedRecord, bool) {
	var value any
	if jsonx.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	device, ok := value.(map[string]any)
	if !ok || device == nil {
		return nil, false
	}
	var fields map[string]jsontext.Value
	if jsonx.Unmarshal(raw, &fields) != nil {
		return nil, false
	}
	if _, has := device["kind"]; !has {
		device["kind"] = "phone"
		fields["kind"] = jsontext.Value(`"phone"`)
	}
	if !isStoredDevice(device) {
		return nil, false
	}
	push, hasPush := device["push"]
	if hasPush && !domain.IsPhonePush(push) {
		delete(fields, "push")
	}
	record := &pairedRecord{extra: map[string]jsontext.Value{}}
	known := map[string]jsontext.Value{}
	for key, value := range fields {
		if deviceFields[key] {
			known[key] = value
		} else {
			record.extra[key] = value
		}
	}
	encoded, err := jsonx.Marshal(known)
	if err != nil || jsonx.Unmarshal(encoded, &record.device) != nil {
		return nil, false
	}
	return record, true
}

// loaded is what the file held: the devices this version reads, and everything else, kept to be
// written back as it was.
type loaded struct {
	devices []*pairedRecord
	kept    []jsontext.Value
	fields  map[string]jsontext.Value
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// hashesMatch: hashes are the same length, so the comparison is a straight constant-time walk over their bytes.
func hashesMatch(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func readableName(name string) string {
	if trimmed := utf16Prefix(jsTrim(name), maxDeviceName); trimmed != "" {
		return trimmed
	}
	return unnamedDevice
}

// PairingStore is who has been let in, and who is being kept out. The pairing code lives here rather
// than in the server because spending one and writing the device it bought are the same moment.
//
// Only the hash of a device token is ever written, so this file cannot be read back into a working
// credential. Failed codes are counted per source address, and the count shuts the door for a while
// before it is worth guessing further. Safe for concurrent use.
type PairingStore struct {
	mu       sync.Mutex
	filePath string
	devices  []*pairedRecord
	// kept are records this version does not read, written back untouched so a newer version's devices survive.
	kept []jsontext.Value
	// edits are, per device, the outcomes of its latest edits, kept across restarts so a resent edit is answered rather than lost.
	edits  map[string][]storedEdit
	fields map[string]jsontext.Value
	// unreadableSince is set while the file is there but could not be read. Nothing is written over it
	// then, because writing what little is known would unpair every device the file still holds.
	unreadableSince *float64
	code            *domain.PairingCode
	attempts        map[string]domain.PairingAttempts
}

func NewPairingStore(filePath string) *PairingStore {
	store := &PairingStore{filePath: filePath, edits: map[string][]storedEdit{}, fields: map[string]jsontext.Value{}, attempts: map[string]domain.PairingAttempts{}}
	store.load(application.Now())
	return store
}

// Unreadable is whether the list on disk is out of reach, so a device missing from it may only be missing for now.
func (s *PairingStore) Unreadable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unreadableSince != nil
}

func (s *PairingStore) load(at float64) {
	held, ok := s.read(at)
	if !ok {
		return
	}
	s.unreadableSince = nil
	s.devices, s.kept = held.devices, held.kept
	s.fields = map[string]jsontext.Value{}
	for key, value := range held.fields {
		if key != "edits" {
			s.fields[key] = value
		}
	}
	s.edits = map[string][]storedEdit{}
	var edits map[string]any
	if raw, has := held.fields["edits"]; has && jsonx.Unmarshal(raw, &edits) == nil {
		for deviceID, list := range edits {
			items, ok := list.([]any)
			if !ok {
				continue
			}
			read := []storedEdit{}
			for _, item := range items {
				if edit, ok := readStoredEdit(item); ok {
					read = append(read, edit)
				}
			}
			s.edits[deviceID] = lastEdits(read)
		}
	}
}

func lastEdits(edits []storedEdit) []storedEdit {
	if len(edits) > maxEditsPerDevice {
		return edits[len(edits)-maxEditsPerDevice:]
	}
	return edits
}

// read: no file is no device. A file that will not read is left alone and read again later; one that
// reads but is not a list is set aside under another name rather than lost, and the list starts over.
func (s *PairingStore) read(at float64) (loaded, bool) {
	nothing := loaded{fields: map[string]jsontext.Value{}}
	text, err := os.ReadFile(s.filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nothing, true
		}
		log.Printf("Could not read the paired device list: %v", err)
		s.unreadableSince = &at
		return loaded{}, false
	}
	held, err := parseDevices(text)
	if err == nil {
		return held, true
	}
	log.Printf("The paired device list is damaged; setting it aside: %v", err)
	if failure := os.Rename(s.filePath, s.filePath+".damaged"); failure != nil {
		log.Printf("Could not set the damaged paired device list aside: %v", failure)
		s.unreadableSince = &at
		return loaded{}, false
	}
	return nothing, true
}

func parseDevices(text []byte) (loaded, error) {
	var stored map[string]jsontext.Value
	if err := jsonx.Unmarshal(text, &stored); err != nil {
		return loaded{}, err
	}
	var records []jsontext.Value
	if stored == nil || jsonx.Unmarshal(stored["devices"], &records) != nil || records == nil {
		return loaded{}, errors.New("No device list.")
	}
	held := loaded{devices: []*pairedRecord{}, kept: []jsontext.Value{}, fields: map[string]jsontext.Value{}}
	for key, value := range stored {
		if key != "devices" {
			held.fields[key] = value
		}
	}
	for _, raw := range records {
		if record, ok := readRecord(raw); ok && len(held.devices) < domain.MaxPairedDevices {
			held.devices = append(held.devices, record)
		} else {
			held.kept = append(held.kept, raw)
		}
	}
	return held, nil
}

// retryRead reads a list that could not be read again, at most every few seconds, before anything leans on it.
func (s *PairingStore) retryRead(at float64) {
	if s.unreadableSince != nil && at-*s.unreadableSince >= rereadIntervalMs {
		s.load(at)
	}
}

// write is written beside and renamed over, so a crash mid-write leaves the last good list rather
// than half of one. Refused while the file on disk could not be read, which is the one list it would overwrite.
func (s *PairingStore) write() bool {
	if s.unreadableSince != nil {
		return false
	}
	stored := map[string]any{}
	for key, value := range s.fields {
		stored[key] = value
	}
	stored["version"] = 1
	devices := make([]any, 0, len(s.devices)+len(s.kept))
	for _, record := range s.devices {
		devices = append(devices, record)
	}
	for _, raw := range s.kept {
		devices = append(devices, raw)
	}
	stored["devices"] = devices
	if len(s.edits) > 0 {
		stored["edits"] = s.edits
	}
	if err := s.writeFile(stored); err != nil {
		log.Printf("Could not write the paired device list: %v", err)
		return false
	}
	return true
}

func (s *PairingStore) writeFile(stored map[string]any) error {
	text, err := jsonx.Marshal(stored)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o777); err != nil {
		return err
	}
	staging := s.filePath + ".tmp"
	if err := os.WriteFile(staging, text, 0o600); err != nil {
		return err
	}
	return os.Rename(staging, s.filePath)
}

func (s *PairingStore) List() []*domain.PairedDevice {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *PairingStore) listLocked() []*domain.PairedDevice {
	devices := make([]*domain.PairedDevice, len(s.devices))
	for index, record := range s.devices {
		device := record.device
		devices[index] = &device
	}
	return devices
}

func (s *PairingStore) Views() []*domain.PairedDeviceView {
	s.mu.Lock()
	defer s.mu.Unlock()
	views := make([]*domain.PairedDeviceView, len(s.devices))
	for index, record := range s.devices {
		views[index] = domain.DeviceView(&record.device)
	}
	return views
}

// Mint mints the code the QR carries. A second mint discards the first, so only one code ever stands.
func (s *PairingStore) Mint(at float64) domain.PairingCode {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := domain.CreatePairingCode(at, domain.PairingCodeTtlMs)
	s.code = &code
	return code
}

// Pending is the code on screen, or nil once it has expired or been spent.
func (s *PairingStore) Pending(at float64) *domain.PairingCode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingLocked(at)
}

func (s *PairingStore) pendingLocked(at float64) *domain.PairingCode {
	if !domain.PairingCodeLive(s.code, at) {
		s.code = nil
	}
	if s.code == nil {
		return nil
	}
	code := *s.code
	return &code
}

func (s *PairingStore) DiscardCode() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code = nil
}

// Redeem trades a code for a token of the phone's own. A wrong guess leaves the code standing,
// because spending it would let anything that can reach the port cancel the user's pairing by
// guessing once; the caller's failure count is what makes guessing not worth it.
func (s *PairingStore) Redeem(code, deviceName, source string, at float64, kind domain.PairedDeviceKind) PairingOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempts := domain.NoPairingAttempts()
	if held, ok := s.attempts[source]; ok && !domain.PairingAttemptsStale(held, at) {
		attempts = held
	}
	if domain.PairingLocked(attempts, at) {
		return PairingOutcome{Code: contracts.MobileErrorCodeRateLimited, Message: "Too many wrong codes. Wait a few minutes and scan the code again."}
	}
	pending := s.pendingLocked(at)
	// With no code standing there is nothing to guess at, so a late scan is not counted as a guess.
	if pending == nil {
		return PairingOutcome{Code: contracts.MobileErrorCodeExpiredCode, Message: "That pairing code has expired. Show a new one on the computer."}
	}
	if !domain.PairingCodeMatches(pending, code) {
		s.attempts[source] = domain.RegisterPairingFailure(attempts, at)
		s.prune(at)
		return PairingOutcome{Code: contracts.MobileErrorCodeExpiredCode, Message: "That pairing code is wrong or has expired. Show a new one on the computer."}
	}
	delete(s.attempts, source)
	// Both are checked before the code is spent, so the same code still works once they are fixed.
	s.retryRead(at)
	if s.unreadableSince != nil {
		return PairingOutcome{Code: contracts.MobileErrorCodeInternal, Message: "This computer could not read its paired devices. Try again in a moment."}
	}
	if len(s.devices) >= domain.MaxPairedDevices {
		return PairingOutcome{Code: contracts.MobileErrorCodeUnauthorized, Message: fmt.Sprintf("This computer is already paired with %d devices. Revoke one first.", domain.MaxPairedDevices)}
	}
	s.code = nil
	var secret [32]byte
	_, _ = rand.Read(secret[:])
	token := hex.EncodeToString(secret[:])
	seen := at
	record := &pairedRecord{device: domain.PairedDevice{
		ID:         application.NewID(),
		Kind:       kind,
		Name:       readableName(deviceName),
		TokenHash:  hashToken(token),
		PairedAt:   at,
		LastSeenAt: &seen,
	}}
	s.devices = append(s.devices[:len(s.devices):len(s.devices)], record)
	s.write()
	device := record.device
	return PairingOutcome{OK: true, Device: &device, Token: token}
}

// Locked is whether this caller has spent its guesses at the pairing code.
func (s *PairingStore) Locked(source string, at float64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempts, ok := s.attempts[source]
	return ok && domain.PairingLocked(attempts, at)
}

// prune: a record older than the lockout it could have earned says nothing, so it is not kept.
func (s *PairingStore) prune(at float64) {
	for source, attempts := range s.attempts {
		if domain.PairingAttemptsStale(attempts, at) {
			delete(s.attempts, source)
		}
	}
}

// Authenticate is the device a token belongs to, or nil when no stored hash matches it.
func (s *PairingStore) Authenticate(token string, at float64) *domain.PairedDevice {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := hashToken(token)
	find := func() *domain.PairedDevice {
		for _, record := range s.devices {
			if hashesMatch(record.device.TokenHash, hash) {
				device := record.device
				return &device
			}
		}
		return nil
	}
	if found := find(); found != nil || s.unreadableSince == nil {
		return found
	}
	s.retryRead(at)
	return find()
}

func (s *PairingStore) find(deviceID string) int {
	for index, record := range s.devices {
		if record.device.ID == deviceID {
			return index
		}
	}
	return -1
}

// replace swaps one record for a changed copy, leaving the list the old one was in untouched.
func (s *PairingStore) replace(index int, record *pairedRecord) {
	devices := make([]*pairedRecord, len(s.devices))
	copy(devices, s.devices)
	devices[index] = record
	s.devices = devices
}

func (s *PairingStore) MarkSeen(deviceID string, at float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.find(deviceID)
	if index < 0 {
		return
	}
	held := s.devices[index]
	if held.device.LastSeenAt != nil && at-*held.device.LastSeenAt < seenWriteIntervalMs {
		return
	}
	seen := at
	next := *held
	next.device.LastSeenAt = &seen
	s.replace(index, &next)
	s.write()
}

// PushTargets are the phones that take notices while closed.
func (s *PairingStore) PushTargets() []*domain.PairedDevice {
	s.mu.Lock()
	defer s.mu.Unlock()
	targets := []*domain.PairedDevice{}
	for _, record := range s.devices {
		if record.device.Kind == domain.PairedDeviceKindPhone && record.device.Push != nil {
			device := record.device
			targets = append(targets, &device)
		}
	}
	return targets
}

// SetPush is where a phone now takes pushes, or that it no longer does (nil). A token belongs to one
// app install, so an older pairing of the same phone stops holding it and the phone is not told twice.
func (s *PairingStore) SetPush(deviceID string, push *domain.PhonePush) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.find(deviceID)
	if index < 0 || s.devices[index].device.Kind != domain.PairedDeviceKindPhone {
		return
	}
	held := s.devices[index].device.Push
	if push != nil && held != nil && held.Token == push.Token && held.Environment == push.Environment || push == nil && held == nil {
		return
	}
	devices := make([]*pairedRecord, len(s.devices))
	for at, record := range s.devices {
		devices[at] = record
		if record.device.ID == deviceID {
			next := *record
			if push != nil {
				given := *push
				next.device.Push = &given
			} else {
				next.device.Push = nil
			}
			devices[at] = &next
			continue
		}
		if push == nil || record.device.Push == nil || record.device.Push.Token != push.Token {
			continue
		}
		next := *record
		next.device.Push = nil
		devices[at] = &next
	}
	s.devices = devices
	s.write()
}

// EditOutcome is what came of a device's edit, or nil when it is not remembered.
func (s *PairingStore) EditOutcome(deviceID, requestID string) *EditOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, edit := range s.edits[deviceID] {
		if edit.RequestID != requestID {
			continue
		}
		if edit.OK {
			return &EditOutcome{OK: true}
		}
		return &EditOutcome{Message: *edit.Message}
	}
	return nil
}

func (s *PairingStore) RememberEdit(deviceID, requestID string, outcome EditOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.find(deviceID) < 0 {
		return
	}
	stored := storedEdit{RequestID: requestID, OK: true}
	if !outcome.OK {
		message := outcome.Message
		stored = storedEdit{RequestID: requestID, Message: &message}
	}
	list := []storedEdit{}
	for _, edit := range s.edits[deviceID] {
		if edit.RequestID != requestID {
			list = append(list, edit)
		}
	}
	edits := make(map[string][]storedEdit, len(s.edits)+1)
	for key, value := range s.edits {
		edits[key] = value
	}
	edits[deviceID] = lastEdits(append(list, stored))
	s.edits = edits
	s.write()
}

func (s *PairingStore) Revoke(deviceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.find(deviceID)
	if index < 0 {
		return false
	}
	kept := make([]*pairedRecord, 0, len(s.devices)-1)
	kept = append(kept, s.devices[:index]...)
	s.devices = append(kept, s.devices[index+1:]...)
	edits := make(map[string][]storedEdit, len(s.edits))
	for key, value := range s.edits {
		if key != deviceID {
			edits[key] = value
		}
	}
	s.edits = edits
	s.write()
	return true
}
