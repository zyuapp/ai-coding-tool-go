package mobile

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// How the bridge's server reads what arrives: frames into messages, the requests a guard turned away
// into the IDs they can be refused under, and an HTTP request into who sent it from where.

// readClientMessage is a contracts.MobileClientMessage from a phone or an application.ComputerClientMessage
// from a computer, or nil when the frame is not one that kind of device may send.
func readClientMessage(data []byte, kind domain.PairedDeviceKind) any {
	var parsed any
	if jsonx.Unmarshal(data, &parsed) != nil {
		return nil
	}
	if kind == domain.PairedDeviceKindComputer {
		if !application.IsComputerClientMessage(parsed) {
			return nil
		}
		var message application.ComputerClientMessage
		if jsonx.Unmarshal(data, &message) != nil || message == nil {
			return nil
		}
		return message
	}
	if !contracts.IsMobileClientMessage(parsed) {
		return nil
	}
	var message contracts.MobileClientMessage
	if jsonx.Unmarshal(data, &message) != nil || message == nil {
		return nil
	}
	return message
}

type rejectedRequest struct {
	kind      string
	requestID string
}

// rejectedRequestOf is a request of one of kinds the guard turned away, by the ID it can be answered under.
func rejectedRequestOf(data []byte, kinds ...string) *rejectedRequest {
	var value map[string]any
	if jsonx.Unmarshal(data, &value) != nil || value == nil {
		return nil
	}
	kind, _ := value["kind"].(string)
	requestID, ok := value["requestId"].(string)
	known := false
	for _, allowed := range kinds {
		known = known || kind == allowed
	}
	if !known || !ok || requestID == "" || utf16Length(requestID) > 256 {
		return nil
	}
	return &rejectedRequest{kind: kind, requestID: requestID}
}

// rejectedPhoneRequest is a paired phone's command or read the guard turned away.
func rejectedPhoneRequest(data []byte) *rejectedRequest {
	return rejectedRequestOf(data, "command", "query")
}

// rejectedComputerRequest: only an authenticated socket gets a correlated refusal; no rejected payload reaches the reducer.
func rejectedComputerRequest(data []byte) *rejectedRequest {
	return rejectedRequestOf(data, "input", "query")
}

func remoteAddress(request *http.Request) string {
	if request.RemoteAddr == "" {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}

// sourceOf is only ever used to count wrong pairing codes against one caller. Tailscale Serve proxies
// from the loopback and names the real peer in the forwarded header, which is trusted only from the
// loopback: without it every phone on the tailnet would be one bucket, and one stale QR would lock them all out.
func sourceOf(request *http.Request) string {
	remote := remoteAddress(request)
	if !isLoopback(remote) {
		return remote
	}
	forwarded, _, _ := strings.Cut(header(request, "x-forwarded-for"), ",")
	if forwarded = strings.TrimSpace(forwarded); forwarded != "" {
		return forwarded
	}
	return remote
}

func isLoopback(address string) bool {
	return address == "127.0.0.1" || address == "::1" || address == "::ffff:127.0.0.1"
}

// header is one request header as Node reads it: repeats joined with ", ", trimmed, "" when absent.
func header(request *http.Request, name string) string {
	if name == "host" {
		return strings.TrimSpace(request.Host)
	}
	return strings.TrimSpace(strings.Join(request.Header.Values(name), ", "))
}

// forwardedHost is the host the request was made to, as the proxy in front of us saw it, else as this server did.
func forwardedHost(request *http.Request) string {
	if !isLoopback(remoteAddress(request)) {
		return header(request, "host")
	}
	if forwarded := header(request, "x-forwarded-host"); forwarded != "" {
		return forwarded
	}
	return header(request, "host")
}

func forwardedScheme(request *http.Request) string {
	if !isLoopback(remoteAddress(request)) {
		return "http"
	}
	if header(request, "x-forwarded-proto") == "https" {
		return "https"
	}
	return "http"
}

// listen takes the asked-for port, or whatever the machine offers when something else already holds it.
func listen(host string, port int) (net.Listener, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil && port != 0 && errors.Is(err, syscall.EADDRINUSE) {
		return net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	return listener, err
}
