package protocol

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// SharedAuthLogin is how a private conversation home signs in with what Codex reports of the user's
// own: an API key, or a ChatGPT access token. Nil means the user is signed out. Keychain access
// stays in Codex, under the original home; the conversation receives only an in-memory token.
func SharedAuthLogin(status GetAuthStatusResponse) (LoginAccountParams, error) {
	if status.AuthMethod == nil || *status.AuthMethod == "" {
		return nil, nil
	}
	method := *status.AuthMethod
	if status.AuthToken == nil || *status.AuthToken == "" {
		return nil, fmt.Errorf("Codex cannot share %s authentication with a private conversation home.", method)
	}
	token := *status.AuthToken
	switch method {
	case "apikey":
		return APIKeyLogin{APIKey: token}, nil
	case "chatgpt", "chatgptAuthTokens":
		parts := strings.Split(token, ".")
		payload := ""
		if len(parts) > 1 {
			payload = parts[1]
		}
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(payload, "="))
		var claims map[string]any
		if err != nil || Unmarshal(decoded, &claims) != nil || claims == nil {
			return nil, errors.New("Codex returned an unreadable ChatGPT access token.")
		}
		auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
		account, _ := auth["chatgpt_account_id"].(string)
		if account == "" {
			return nil, errors.New("Codex's ChatGPT access token has no account identifier.")
		}
		login := ChatGPTAuthTokensLogin{AccessToken: token, ChatgptAccountID: account}
		if plan, ok := auth["chatgpt_plan_type"].(string); ok {
			login.ChatgptPlanType = &plan
		}
		return login, nil
	}
	return nil, fmt.Errorf("Codex cannot share %s authentication with a private conversation home.", method)
}

// AuthClient is what a credential connection needs of a client; *Client is one.
type AuthClient interface {
	Initialize(ctx context.Context, info ClientInfo, capabilities *InitializeCapabilities) (*InitializeResponse, error)
	Request(ctx context.Context, method string, params any) (jsontext.Value, error)
	Close() ExitStatus
}

type authConnection struct {
	ready      chan struct{}
	client     AuthClient
	err        error
	users      int
	refreshing *authRead
}

type authRead struct {
	done  chan struct{}
	login LoginAccountParams
	err   error
}

var (
	authMu      sync.Mutex
	connections = map[string]*authConnection{}
)

// SharedAuth is one conversation's hold on the credential connection of its source home.
type SharedAuth struct {
	key        string
	connection *authConnection
	once       sync.Once
}

func authKey(command Command) string {
	home := ""
	for _, entry := range command.Env {
		if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			home = value
		}
	}
	key, _ := Marshal([]any{command.Executable, home, command.Args})
	return string(key)
}

// AcquireSharedAuth holds the credential connection for command's home, starting it if nobody
// holds it yet. One connection per source is shared by every concurrent conversation.
func AcquireSharedAuth(ctx context.Context, command Command, connect func(Command) AuthClient) (*SharedAuth, error) {
	key := authKey(command)
	authMu.Lock()
	connection := connections[key]
	if connection == nil {
		connection = &authConnection{ready: make(chan struct{})}
		connections[key] = connection
		go func() {
			client := connect(command)
			_, err := client.Initialize(context.Background(), ClientInfo{Name: "aicodingtool_auth", Title: strPtr("AICodingTool authentication"), Version: "1"}, nil)
			if err != nil {
				client.Close()
			}
			connection.client, connection.err = client, err
			close(connection.ready)
		}()
	}
	connection.users++
	authMu.Unlock()
	auth := &SharedAuth{key: key, connection: connection}
	select {
	case <-connection.ready:
	case <-ctx.Done():
		auth.Release()
		return nil, ctx.Err()
	}
	if connection.err != nil {
		auth.Release()
		return nil, connection.err
	}
	return auth, nil
}

// Read asks Codex for the user's login; refresh asks it to refresh the token first. Refreshes in
// flight are shared.
func (s *SharedAuth) Read(ctx context.Context, refresh bool) (LoginAccountParams, error) {
	connection := s.connection
	authMu.Lock()
	if read := connection.refreshing; read != nil {
		authMu.Unlock()
		select {
		case <-read.done:
			return read.login, read.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	read := &authRead{done: make(chan struct{})}
	if refresh {
		connection.refreshing = read
	}
	authMu.Unlock()
	go func() {
		include, refreshToken := true, refresh
		var status GetAuthStatusResponse
		raw, err := connection.client.Request(context.Background(), "getAuthStatus", GetAuthStatusParams{IncludeToken: &include, RefreshToken: &refreshToken})
		if err == nil {
			err = Unmarshal(raw, &status)
		}
		if err == nil {
			read.login, read.err = SharedAuthLogin(status)
		} else {
			read.err = err
		}
		authMu.Lock()
		if connection.refreshing == read {
			connection.refreshing = nil
		}
		authMu.Unlock()
		close(read.done)
	}()
	select {
	case <-read.done:
		return read.login, read.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Release lets go of the connection; the last holder closes it.
func (s *SharedAuth) Release() {
	s.once.Do(func() {
		authMu.Lock()
		s.connection.users--
		last := s.connection.users == 0
		if last && connections[s.key] == s.connection {
			delete(connections, s.key)
		}
		authMu.Unlock()
		if last {
			<-s.connection.ready
			if s.connection.client != nil {
				s.connection.client.Close()
			}
		}
	})
}
