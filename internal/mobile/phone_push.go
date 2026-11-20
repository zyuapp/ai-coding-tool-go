package mobile

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// PushRelayURL is the relay that holds the APNs key; the app only ever names a phone by the token it was given.
const PushRelayURL = "https://aicodingtool-relay.zyuapp.workers.dev/v1/push"

const (
	pushTitleMax    = 120
	pushBodyMax     = 300
	pushSendTimeout = 10 * time.Second
)

// PushTargets is where notices go, and how a phone Apple says is gone stops being asked.
type PushTargets interface {
	// PushTargets are phones whose Push is set.
	PushTargets() []*domain.PairedDevice
	SetPush(deviceID string, push *domain.PhonePush)
}

// PushRequest is the relay's request for one phone (relay/worker.mts).
type PushRequest struct {
	Token       string                 `json:"token"`
	Environment domain.PushEnvironment `json:"environment"`
	Title       string                 `json:"title"`
	Body        string                 `json:"body"`
	Thread      string                 `json:"thread"`
	// Data is what a tap opens, read by the app and nowhere else.
	Data map[string]string `json:"data"`
}

// PushSender posts one body to the relay and answers with the HTTP status it got.
type PushSender func(ctx context.Context, url string, body []byte) (status int, err error)

func clip(text string, max int) string {
	line := jsTrim(collapseSpaces(text))
	if utf16Length(line) <= max {
		return line
	}
	return jsTrimEnd(utf16Prefix(line, max-1)) + "…"
}

// NewPushRequest is the relay's request for one phone. A thread's notices share a group, and each
// replaces the one before, so a phone shows where a thread stands rather than everything it went through.
func NewPushRequest(device *domain.PairedDevice, notice contracts.ThreadNotice) PushRequest {
	title := clip(notice.Title, pushTitleMax)
	if title == "" {
		title = "AI Coding Tool"
	}
	return PushRequest{
		Token:       device.Push.Token,
		Environment: device.Push.Environment,
		Title:       title,
		Body:        clip(notice.Headline, pushBodyMax),
		Thread:      utf16Prefix(device.ID+":"+notice.TaskID, 128),
		Data:        map[string]string{"taskId": notice.TaskID, "deviceId": device.ID},
	}
}

var pushClient = &http.Client{}

// SendPush posts to the relay over HTTP.
func SendPush(ctx context.Context, url string, body []byte) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("content-type", "application/json")
	response, err := pushClient.Do(request)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	response.Body.Close()
	return response.StatusCode, nil
}

// PushToPhones hands a notice to every phone that takes them. A phone Apple says is gone stops being
// asked; any other failure is the relay's or the network's, and the notice is simply not delivered.
// A nil send posts with SendPush.
func PushToPhones(ctx context.Context, targets PushTargets, notice contracts.ThreadNotice, send PushSender) {
	if send == nil {
		send = SendPush
	}
	var wait sync.WaitGroup
	for _, device := range targets.PushTargets() {
		wait.Go(func() {
			body, err := jsonx.Marshal(NewPushRequest(device, notice))
			if err != nil {
				return
			}
			within, cancel := context.WithTimeout(ctx, pushSendTimeout)
			defer cancel()
			status, err := send(within, PushRelayURL, body)
			switch {
			case err != nil:
				log.Printf("Could not reach the push relay: %v", err)
			case status == http.StatusGone:
				targets.SetPush(device.ID, nil)
			case status < 200 || status > 299:
				log.Printf("The push relay refused a notice (HTTP %d).", status)
			}
		})
	}
	wait.Wait()
}
