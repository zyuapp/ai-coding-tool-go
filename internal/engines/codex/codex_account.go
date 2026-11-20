package codex

import (
	"context"
	"errors"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

// How long the browser has to bring the sign-in back before it is given up on.
var signInTimeout = 5 * time.Minute

func accountAccess(ctx context.Context, client CodexClient) (domain.EngineAccess, error) {
	account, err := call[protocol.GetAccountResponse](ctx, client, "account/read", protocol.GetAccountParams{RefreshToken: ptr(false)})
	if err != nil {
		return "", err
	}
	if account.Account != nil {
		return domain.EngineAccessReady, nil
	}
	return domain.EngineAccessSignedOut, nil
}

// Whether Codex can take a run, asked of a short-lived app server: a binary that will not start is
// unavailable, one that starts with no account is signed out. Nil connect is ConnectAppServer.
func ReadCodexAccess(ctx context.Context, connect CodexConnect) domain.EngineAccess {
	if connect == nil {
		connect = ConnectAppServer
	}
	command, err := CodexAppServer(nil, AppServerOptions{SharedHome: true})
	if err != nil {
		return domain.EngineAccessUnavailable
	}
	client := connect(command)
	defer func() { go client.Close() }()
	if err := initialize(ctx, client); err != nil {
		return domain.EngineAccessUnavailable
	}
	access, err := accountAccess(ctx, client)
	if err != nil {
		return domain.EngineAccessUnavailable
	}
	return access
}

// The browser sign-in Codex runs itself: it hands back a URL to open and says when the browser has
// come back to it. The server has to outlive the round trip, since it is what the browser returns to.
func SignInToCodex(ctx context.Context, openURL agent.OpenURL, connect CodexConnect) (domain.EngineAccess, error) {
	if connect == nil {
		connect = ConnectAppServer
	}
	command, err := CodexAppServer(nil, AppServerOptions{SharedHome: true})
	if err != nil {
		return "", err
	}
	client := connect(command)
	defer func() { go client.Close() }()
	completed := make(chan protocol.AccountLoginCompletedNotification, 1)
	onNotification(client, protocol.NotifyAccountLoginCompleted, func(params protocol.AccountLoginCompletedNotification) {
		select {
		case completed <- params:
		default:
		}
	})
	expired := time.NewTimer(signInTimeout)
	defer expired.Stop()
	if err := initialize(ctx, client); err != nil {
		return "", err
	}
	started, err := call[protocol.LoginAccountResponse](ctx, client, "account/login/start", protocol.ChatGPTLogin{})
	if err != nil {
		return "", err
	}
	if started.Type != "chatgpt" {
		return "", errors.New("Codex offered no browser sign-in.")
	}
	if err := openURL(ctx, deref(started.AuthURL)); err != nil {
		return "", err
	}
	var outcome protocol.AccountLoginCompletedNotification
	select {
	case outcome = <-completed:
	case <-expired.C:
		return "", errors.New("Codex sign-in timed out. Try again.")
	case <-client.Exited():
		return "", errors.New("Codex stopped before the sign-in finished.")
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if !outcome.Success {
		if outcome.Error != nil {
			return "", errors.New(*outcome.Error)
		}
		return "", errors.New("Codex sign-in failed.")
	}
	return accountAccess(ctx, client)
}
