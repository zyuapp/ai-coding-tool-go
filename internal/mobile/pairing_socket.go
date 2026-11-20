//go:build unix

package mobile

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// ServePairingSocket answers a pairing code over a private Unix socket, for the command line beside a
// headless host, which is on the same machine and has every right to ask. Answers how to stop.
func ServePairingSocket(socketPath string, pair func(ctx context.Context) (*domain.MobilePairingOffer, error)) (func() error, error) {
	directory := filepath.Dir(socketPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	entry, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	owner, ok := entry.Sys().(*syscall.Stat_t)
	if !entry.IsDir() || !ok || int(owner.Uid) != os.Getuid() {
		return nil, errors.New("The pairing socket directory is not owned by this user.")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, err
	}
	// Whoever serves holds the machine's one instance, so any previous socket is stale.
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("cache-control", "no-store")
		if request.Method != http.MethodPost || request.URL.RequestURI() != "/pair" {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		offer, err := pair(request.Context())
		if err == nil {
			var text []byte
			if text, err = jsonx.Marshal(offer); err == nil {
				response.Header().Set("content-type", "application/json")
				response.WriteHeader(http.StatusOK)
				_, _ = response.Write(text)
				return
			}
		}
		message := "The bridge is not ready."
		if err.Error() != "" {
			message = err.Error()
		}
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(response, message)
	})}
	go func() { _ = server.Serve(listener) }()
	return func() error {
		err := server.Close()
		if removed := os.Remove(socketPath); removed != nil && !errors.Is(removed, os.ErrNotExist) && err == nil {
			err = removed
		}
		return err
	}, nil
}

var pairingCodePattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]+$`)

const maxPairingResponse = 16_384

// RequestPairing asks the process serving on the socket for a pairing code.
func RequestPairing(ctx context.Context, socketPath string) (*domain.MobilePairingOffer, error) {
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}}}
	within, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(within, http.MethodPost, "http://localhost/pair", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(within.Err(), context.DeadlineExceeded) {
			return nil, errors.New("The bridge did not answer.")
		}
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPairingResponse+1))
	if err != nil {
		if errors.Is(within.Err(), context.DeadlineExceeded) {
			return nil, errors.New("The bridge did not answer.")
		}
		return nil, err
	}
	if len(body) > maxPairingResponse {
		return nil, errors.New("Invalid pairing response.")
	}
	if response.StatusCode != http.StatusOK {
		if len(body) == 0 {
			return nil, errors.New("The bridge is not ready.")
		}
		return nil, errors.New(string(body))
	}
	var shape map[string]any
	if err := jsonx.Unmarshal(body, &shape); err != nil {
		return nil, err
	}
	_, hasURL := shape["url"].(string)
	code, _ := shape["code"].(string)
	if !hasURL || !pairingCodePattern.MatchString(code) {
		return nil, errors.New("Invalid pairing response.")
	}
	offer := &domain.MobilePairingOffer{}
	if err := jsonx.Unmarshal(body, offer); err != nil {
		return nil, err
	}
	return offer, nil
}
