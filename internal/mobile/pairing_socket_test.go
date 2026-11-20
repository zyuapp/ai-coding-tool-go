//go:build unix

package mobile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	// A Unix socket path must stay short, which a test's own temporary folder may not.
	folder, err := os.MkdirTemp("/tmp", "act-pair-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(folder) })
	return folder
}

func TestTheCommandLineAsksTheServingProcessForACode(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "serve", "serve.sock")
	offer := domain.PairingOffer(*LoopbackAddress(7737), domain.CreatePairingCode(at, domain.PairingCodeTtlMs))
	stop, err := ServePairingSocket(socket, func(context.Context) (*domain.MobilePairingOffer, error) { return offer, nil })
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Dir(socket))
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("the socket's folder is open to others: %v", info.Mode())
	}
	got, err := RequestPairing(context.Background(), socket)
	if err != nil || got.Code != offer.Code || got.URL != offer.URL {
		t.Fatalf("got %+v, %v", got, err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the socket was left behind")
	}
	if _, err := RequestPairing(context.Background(), socket); err == nil {
		t.Fatal("a stopped socket answered")
	}
}

func TestABridgeThatIsNotReadySaysSo(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "serve.sock")
	stop, err := ServePairingSocket(socket, func(context.Context) (*domain.MobilePairingOffer, error) {
		return nil, errors.New("Turn the phone bridge on before pairing a phone.")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err := RequestPairing(context.Background(), socket); err == nil || !strings.Contains(err.Error(), "Turn the phone bridge on") {
		t.Fatal(err)
	}
}
