package domain

import (
	"crypto/rand"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// PairingCodeTtlMs is how long a pairing code stands before it is worthless.
const PairingCodeTtlMs = 2 * 60 * 1_000

// pairingAlphabet is Crockford base32, so 0/O and 1/I cannot be mistyped.
const pairingAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const PairingCodeLength = 8

// Bad codes in a row before the door shuts, and how long it stays shut.
const (
	MaxPairingFailures = 5
	PairingLockoutMs   = 5 * 60 * 1_000
)

// MaxPairedDevices is how many device tokens the Mac will hold.
const MaxPairedDevices = 16

// How much of a session's outbound traffic is kept so a phone that drops resumes rather than reloads.
const (
	MobileEventBuffer = 600
	MobileBufferBytes = 1024 * 1024
)

// MobileCongestedBytes is how much a phone's socket may hold unsent before the server stops queueing patches behind it.
const MobileCongestedBytes = 256 * 1024

// MaxSessionsPerDevice is how many sessions one phone may hold at once. Past this the oldest is hung up on.
const MaxSessionsPerDevice = 4

// MaxPendingSockets is how many sockets that have not yet said who they are the server will hold at once.
const MaxPendingSockets = 32

// How often the server pings a live session, and how long silence runs before the line is called dead.
const (
	MobilePingIntervalMs = 15 * 1_000
	MobileDeadAfterMs    = 45 * 1_000
)

// MobileDefaultPort is the port the local server asks for first.
const MobileDefaultPort = 7737

// MobileAppPath is the path the bridge's sockets and health check live under.
const MobileAppPath = "/m"

// AddressSecure: only Tailscale Serve terminates TLS; the local server itself never speaks HTTPS.
func AddressSecure(address MobileAddress) bool {
	return address.Kind == MobileAddressKindTailscaleHTTPS
}

// AddressOrigin is the scheme, host and port a phone types, with the default port left off.
func AddressOrigin(address MobileAddress) string {
	secure := AddressSecure(address)
	scheme := "http"
	if secure {
		scheme = "https"
	}
	bare := (secure && address.Port == 443) || (!secure && address.Port == 80)
	if bare {
		return scheme + "://" + address.Host
	}
	return scheme + "://" + address.Host + ":" + featuresJSNumber(address.Port)
}

// PreferredAddress is the address the QR should carry: the one a phone can reach, then the Mac's own.
func PreferredAddress(addresses []*MobileAddress) *MobileAddress {
	for _, kind := range []MobileAddressKind{MobileAddressKindTailscaleHTTPS, MobileAddressKindLoopback} {
		for _, address := range addresses {
			if address.Kind == kind {
				return address
			}
		}
	}
	return nil
}

var pushToken = regexp.MustCompile(`^[0-9a-f]{64,200}$`)

// IsPhonePush checks a JSON-decoded value (map[string]any, …).
func IsPhonePush(value any) bool {
	push, ok := value.(map[string]any)
	if !ok {
		return false
	}
	token, ok := push["token"].(string)
	environment := push["environment"]
	return ok && pushToken.MatchString(token) && (environment == "production" || environment == "sandbox")
}

// DeviceView is a paired device as settings draws it, without its hash and push token.
func DeviceView(device *PairedDevice) *PairedDeviceView {
	return &PairedDeviceView{ID: device.ID, Name: device.Name, Kind: device.Kind, PairedAt: device.PairedAt, LastSeenAt: device.LastSeenAt}
}

func EmptyTailscaleState() TailscaleState {
	return TailscaleState{Status: TailscaleStateStatusUnknown}
}

func EmptyMobileServerState() MobileServerState {
	return MobileServerState{
		Status:    MobileServerStatusOff,
		Addresses: []*MobileAddress{},
		Devices:   []*PairedDeviceView{},
		Sessions:  []*MobileSessionView{},
		Tailscale: EmptyTailscaleState(),
	}
}

func NoPairingAttempts() PairingAttempts {
	return PairingAttempts{}
}

func PairingLocked(attempts PairingAttempts, at float64) bool {
	return attempts.LockedUntil != nil && at < *attempts.LockedUntil
}

// RegisterPairingFailure counts one more wrong code. Reaching the limit shuts the door and starts the count again behind it.
func RegisterPairingFailure(attempts PairingAttempts, at float64) PairingAttempts {
	failures := attempts.Failures + 1
	if failures < MaxPairingFailures {
		return PairingAttempts{Failures: failures, LockedUntil: attempts.LockedUntil, LastFailureAt: at}
	}
	lockedUntil := at + PairingLockoutMs
	return PairingAttempts{Failures: 0, LockedUntil: &lockedUntil, LastFailureAt: at}
}

// PairingAttemptsStale reports whether a record has stopped saying anything.
func PairingAttemptsStale(attempts PairingAttempts, at float64) bool {
	return !PairingLocked(attempts, at) && at-attempts.LastFailureAt >= PairingLockoutMs
}

func randomBytes(count int) []byte {
	bytes := make([]byte, count)
	_, _ = rand.Read(bytes)
	return bytes
}

// GeneratePairingCode is random characters from the unmistakable alphabet; callers pass PairingCodeLength for the default.
func GeneratePairingCode(length int) string {
	var code strings.Builder
	for _, b := range randomBytes(length) {
		code.WriteByte(pairingAlphabet[int(b)%len(pairingAlphabet)])
	}
	return code.String()
}

// CreatePairingCode mints a code; callers pass PairingCodeTtlMs for the default ttl.
func CreatePairingCode(at float64, ttlMs float64) PairingCode {
	return PairingCode{Code: GeneratePairingCode(PairingCodeLength), CreatedAt: at, ExpiresAt: at + ttlMs}
}

// PairingCodeLive reports whether a code is still worth trading.
func PairingCodeLive(pairing *PairingCode, at float64) bool {
	return pairing != nil && at < pairing.ExpiresAt
}

// PairingCodeMatches compares codes with a constant-time walk.
func PairingCodeMatches(pairing *PairingCode, code string) bool {
	return ConstantTimeEquals(pairing.Code, strings.ToUpper(strings.TrimSpace(code)))
}

// ConstantTimeEquals compares UTF-16 code units without stopping at the first difference.
func ConstantTimeEquals(left, right string) bool {
	l, r := utf16.Encode([]rune(left)), utf16.Encode([]rune(right))
	if len(l) != len(r) {
		return false
	}
	var difference uint16
	for index := range l {
		difference |= l[index] ^ r[index]
	}
	return difference == 0
}

// phoneAppScheme is the scheme the phone app registers, so a camera that scans the QR opens the app.
const phoneAppScheme = "aicodingtool"

// PairingURL is what the QR encodes: the phone app's link around where to reach the Mac and the code to trade.
func PairingURL(address MobileAddress, code string) string {
	link := AddressOrigin(address) + "/#pair=" + featuresEncodeURIComponent(code)
	return phoneAppScheme + "://pair?link=" + featuresEncodeURIComponent(link)
}

func PairingOffer(address MobileAddress, pairing PairingCode) *MobilePairingOffer {
	return &MobilePairingOffer{Code: pairing.Code, ExpiresAt: pairing.ExpiresAt, Address: address, URL: PairingURL(address, pairing.Code)}
}

// featuresJSNumber formats a number as JavaScript's String(number) does.
func featuresJSNumber(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	case value == 0:
		return "0"
	}
	if abs := math.Abs(value); abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	text := strconv.FormatFloat(value, 'e', -1, 64)
	mantissa, exponent, _ := strings.Cut(text, "e")
	sign := exponent[:1]
	return mantissa + "e" + sign + strings.TrimLeft(exponent[1:], "0")
}

// featuresEncodeURIComponent escapes as JavaScript's encodeURIComponent does. Lone surrogates, which
// make it throw, cannot occur in a Go string; invalid UTF-8 encodes as U+FFFD.
func featuresEncodeURIComponent(value string) string {
	const unreserved = "-_.!~*'()"
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for _, r := range value {
		if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(unreserved, r)) {
			out.WriteRune(r)
			continue
		}
		for _, b := range []byte(string(r)) {
			out.WriteByte('%')
			out.WriteByte(hex[b>>4])
			out.WriteByte(hex[b&15])
		}
	}
	return out.String()
}
