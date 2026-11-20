package codex

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

const rateLimitsJSON = `{
	"rateLimits": {"limitId":"codex","limitName":"Codex","normalModelSlug":null,"credits":null,"individualLimit":null,"spendControlReached":null,"planType":"pro","rateLimitReachedType":null,
		"primary": {"usedPercent":17.4,"windowDurationMins":300,"resetsAt":1787880600},
		"secondary": {"usedPercent":42,"windowDurationMins":10080,"resetsAt":1788399000}},
	"rateLimitsByLimitId": null, "rateLimitResetCredits": null, "accountId": null, "rateLimitUpsell": null, "ordinaryUsageAllowed": null}`

func rateLimits(t *testing.T, text string) RateLimitsResponse {
	t.Helper()
	var response RateLimitsResponse
	must(t, protocol.Unmarshal([]byte(text), &response))
	return response
}

func snapshotJSON(fields string) string {
	return `{"limitId":"codex","limitName":"Codex","normalModelSlug":null,"primary":null,"secondary":null,"credits":null,"individualLimit":null,"spendControlReached":null,"planType":"pro","rateLimitReachedType":null` + fields + `}`
}

func TestCodexRateLimitsBecomeTheSameSessionAndWeekWindowsAsClaudeUsage(t *testing.T) {
	assertJSON(t, ToCodexPlanUsage(rateLimits(t, rateLimitsJSON), nil), `{"status":"available","subscription":"pro","windows":[
		{"id":"codex:primary","label":"Current session","utilization":17.4,"resetsAt":"2026-08-28T01:30:00.000Z","period":"session","minutes":300},
		{"id":"codex:secondary","label":"Current week","utilization":42,"resetsAt":"2026-09-03T01:30:00.000Z","period":"week","minutes":10080}]}`)
}

func TestAPlanWithOnlyAWeeklyLimitHasAWeekAndNoSession(t *testing.T) {
	usage := ToCodexPlanUsage(rateLimits(t, `{"rateLimits":`+snapshotJSON(`,"primary":{"usedPercent":30,"windowDurationMins":10080,"resetsAt":null}`)+`}`), nil)
	assertJSON(t, usage, `{"status":"available","subscription":"pro","windows":[{"id":"codex:primary","label":"Current week","utilization":30,"resetsAt":null,"period":"week","minutes":10080}]}`)
}

func TestMultipleLimitBucketsKeepTheirNamesAndStableOrder(t *testing.T) {
	usage := ToCodexPlanUsage(rateLimits(t, `{"rateLimits":`+snapshotJSON("")+`,"rateLimitsByLimitId":{
		"review": {"limitId":"review","limitName":"Code review","planType":null,"primary":{"usedPercent":20,"windowDurationMins":1440,"resetsAt":null},"secondary":null},
		"codex": {"limitId":"codex","limitName":"Codex","planType":"pro","primary":{"usedPercent":10,"windowDurationMins":60,"resetsAt":null},"secondary":null}}}`), nil)
	available := usage.(domain.PlanUsageAvailable)
	var labels []string
	var periods []any
	for _, window := range available.Windows {
		labels = append(labels, window.Label)
		periods = append(periods, window.Period)
	}
	assertJSON(t, labels, []string{"Codex · Current 1-hour window", "Code review · Current 1-day window"})
	assertJSON(t, periods, `["session", null]`, "only the ordinary limit is a period of the plan")
	assertJSON(t, ToCodexPlanUsage(rateLimits(t, `{"rateLimits":`+strings.Replace(snapshotJSON(""), `"planType":"pro"`, `"planType":null`, 1)+`}`), ptr("plus")), `{"status":"not-applicable"}`)
}

func TestTheUsageLimitATurnRanIntoIsTheOrdinaryWindowThatLiftsLast(t *testing.T) {
	limit := CodexUsageLimit(rateLimits(t, `{"rateLimits":`+snapshotJSON(`,"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":100},"secondary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":200}`)+`}`))
	assertJSON(t, limit, `{"resetsAt":200000,"window":"weekly"}`)
	session := CodexUsageLimit(rateLimits(t, `{"rateLimits":`+snapshotJSON(`,"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":100},"secondary":{"usedPercent":50,"windowDurationMins":10080,"resetsAt":200}`)+`}`))
	assertJSON(t, session, `{"resetsAt":100000,"window":"session"}`)
	if CodexUsageLimit(rateLimits(t, rateLimitsJSON)) != nil {
		t.Fatal("a limit not used up holds nothing")
	}
}

type connector struct {
	script    script
	handshake func(ctx context.Context) error
	mu        sync.Mutex
	clients   []*fakeClient
}

func (c *connector) connect(command protocol.Command) CodexClient {
	client := newFakeClient(command, c.script, c.handshake)
	c.mu.Lock()
	c.clients = append(c.clients, client)
	c.mu.Unlock()
	return client
}

func (c *connector) all() []*fakeClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*fakeClient(nil), c.clients...)
}

func closedEventually(t *testing.T, client *fakeClient) {
	t.Helper()
	eventually(t, "the server was never closed", client.isClosed)
}

func TestUsageReadsTheAccountAndLimitsOverAShortLivedServer(t *testing.T) {
	c := &connector{script: script{
		"account/read":            reply(`{"account":{"type":"chatgpt","email":"dev@example.com","planType":"pro"},"requiresOpenaiAuth":true}`),
		"account/rateLimits/read": reply(rateLimitsJSON),
	}}
	usage := ReadCodexPlanUsage(context.Background(), c.connect, 0)
	if _, ok := usage.(domain.PlanUsageAvailable); !ok {
		t.Fatalf("usage = %#v", usage)
	}
	client := c.all()[0]
	assertJSON(t, client.methods(), []string{"initialize", "account/read", "account/rateLimits/read"})
	closedEventually(t, client)
	assertJSON(t, client.command.Args[:3], []string{"app-server", "--listen", "stdio://"})
}

func TestANonChatGPTAccountHasNoPlanLimitsWhileFailuresAndTimeoutsReportWhy(t *testing.T) {
	signedOut := &connector{script: script{"account/read": reply(`{"account":null,"requiresOpenaiAuth":true}`)}}
	assertJSON(t, ReadCodexPlanUsage(context.Background(), signedOut.connect, 0), `{"status":"not-applicable"}`)
	assertJSON(t, signedOut.all()[0].methods(), []string{"initialize", "account/read"})

	failed := &connector{script: script{
		"account/read":            reply(`{"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":true}`),
		"account/rateLimits/read": refuse(errors.New("rate limits unavailable")),
	}}
	assertJSON(t, ReadCodexPlanUsage(context.Background(), failed.connect, 0), `{"status":"unavailable","message":"rate limits unavailable"}`)

	silent := &connector{script: script{}, handshake: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
	assertJSON(t, ReadCodexPlanUsage(context.Background(), silent.connect, 5*time.Millisecond), `{"status":"unavailable","message":"Codex did not answer in time."}`)
	closedEventually(t, silent.all()[0])
}

func TestACodexThatCannotStartReportsTheStartupFailure(t *testing.T) {
	held := engineBinaryPath
	engineBinaryPath = func(agent.EngineCommand) (string, bool) { return "", false }
	defer func() { engineBinaryPath = held }()
	assertJSON(t, ReadCodexPlanUsage(context.Background(), (&connector{}).connect, 0), `{"status":"unavailable","message":"Codex is not installed."}`)
	if access := ReadCodexAccess(context.Background(), (&connector{}).connect); access != domain.EngineAccessUnavailable {
		t.Fatalf("access = %s", access)
	}
}

var signedIn = script{"account/read": reply(`{"account":{"type":"chatgpt","email":"dev@example.com","planType":"pro"},"requiresOpenaiAuth":true}`)}

func TestCodexIsReadyWithAnAccountAndSignedOutWithoutOne(t *testing.T) {
	ready := &connector{script: signedIn}
	if access := ReadCodexAccess(context.Background(), ready.connect); access != domain.EngineAccessReady {
		t.Fatalf("access = %s", access)
	}
	client := ready.all()[0]
	assertJSON(t, client.methods(), []string{"initialize", "account/read"})
	assertJSON(t, client.command.Args[:3], []string{"app-server", "--listen", "stdio://"})
	closedEventually(t, client)
	out := &connector{script: script{"account/read": reply(`{"account":null,"requiresOpenaiAuth":true}`)}}
	if access := ReadCodexAccess(context.Background(), out.connect); access != domain.EngineAccessSignedOut {
		t.Fatalf("access = %s", access)
	}
	stopped := &connector{script: signedIn, handshake: func(context.Context) error { return errors.New("spawn ENOENT") }}
	if access := ReadCodexAccess(context.Background(), stopped.connect); access != domain.EngineAccessUnavailable {
		t.Fatalf("access = %s", access)
	}
	closedEventually(t, stopped.all()[0])
}

func TestSigningInOpensTheURLCodexHandsBackAndReadsTheAccountAgain(t *testing.T) {
	var account atomic.Value
	account.Store(`null`)
	c := &connector{script: script{
		"account/read": func(context.Context, jsontext.Value) (any, error) {
			return jsontext.Value(`{"account":` + account.Load().(string) + `,"requiresOpenaiAuth":true}`), nil
		},
		"account/login/start": reply(`{"type":"chatgpt","loginId":"login-1","authUrl":"https://auth.example/authorize?x=1"}`),
	}}
	var opened []string
	var mu sync.Mutex
	done := make(chan struct{})
	var access domain.EngineAccess
	var err error
	go func() {
		access, err = SignInToCodex(context.Background(), func(_ context.Context, url string) error {
			mu.Lock()
			opened = append(opened, url)
			mu.Unlock()
			return nil
		}, c.connect)
		close(done)
	}()
	eventually(t, "login never started", func() bool { return len(c.all()) == 1 && len(c.all()[0].calls("account/login/start")) == 1 })
	client := c.all()[0]
	assertJSON(t, client.calls("account/login/start")[0], `{"type":"chatgpt"}`)
	eventually(t, "the URL was never opened", func() bool { mu.Lock(); defer mu.Unlock(); return len(opened) == 1 })
	if client.isClosed() {
		t.Fatal("the server outlives the round trip, since it is what the browser returns to")
	}
	account.Store(`{"type":"chatgpt","email":"dev@example.com","planType":"pro"}`)
	client.notify("account/login/completed", map[string]any{"loginId": "login-1", "success": true, "error": nil})
	<-done
	if err != nil || access != domain.EngineAccessReady {
		t.Fatalf("access = %s, err = %v", access, err)
	}
	assertJSON(t, opened, []string{"https://auth.example/authorize?x=1"})
	closedEventually(t, client)
}

func TestASignInBroughtBackFailedOrAServerThatDiesUnderItIsReported(t *testing.T) {
	login := script{
		"account/read":        reply(`{"account":null,"requiresOpenaiAuth":true}`),
		"account/login/start": reply(`{"type":"chatgpt","loginId":"login-1","authUrl":"https://auth.example/authorize"}`),
	}
	signIn := func(c *connector) chan error {
		errs := make(chan error, 1)
		go func() {
			_, err := SignInToCodex(context.Background(), func(context.Context, string) error { return nil }, c.connect)
			errs <- err
		}()
		eventually(t, "login never started", func() bool { return len(c.all()) > 0 && len(c.all()[len(c.all())-1].calls("account/login/start")) > 0 })
		return errs
	}
	failing := &connector{script: login}
	failed := signIn(failing)
	tick()
	failing.all()[0].notify("account/login/completed", map[string]any{"loginId": "login-1", "success": false, "error": "The browser was closed."})
	if err := <-failed; err == nil || err.Error() != "The browser was closed." {
		t.Fatalf("err = %v", err)
	}
	dying := &connector{script: login}
	died := signIn(dying)
	tick()
	dying.all()[0].exitWith(protocol.ExitStatus{Stderr: "boom"})
	if err := <-died; err == nil || err.Error() != "Codex stopped before the sign-in finished." {
		t.Fatalf("err = %v", err)
	}
}

// Forgets the children read before, which the reader keeps for the life of the process.
func forgetMetadataReads() {
	metadataReads.Lock()
	metadataReads.unavailable = map[string]cachedMetadata{}
	metadataReads.order = nil
	metadataReads.Unlock()
}

func TestInspectingASavedChildReadsSettingsWithoutResumingOrLoadingTurns(t *testing.T) {
	forgetMetadataReads()
	release := make(chan struct{})
	c := &connector{script: script{"thread/read": func(ctx context.Context, _ jsontext.Value) (any, error) {
		<-release
		return jsontext.Value(`{"thread":{"id":"ui-smoke","model":"gpt-6.2-sol","reasoningEffort":"medium"}}`), nil
	}}}
	results := make(chan domain.SubagentMetadata, 2)
	for range 2 {
		go func() { results <- ReadCodexSubagentMetadata(context.Background(), "ui-smoke", c.connect, 0) }()
	}
	tick()
	close(release)
	for range 2 {
		assertJSON(t, <-results, `{"model":"gpt-6.2-sol","effort":"medium"}`)
	}
	if len(c.all()) != 1 {
		t.Fatalf("concurrent inspections share the request: %d servers", len(c.all()))
	}
	client := c.all()[0]
	assertJSON(t, client.calls("thread/read"), `[{"threadId":"ui-smoke","includeTurns":false}]`)
	if !client.isClosed() || len(client.calls("thread/resume")) != 0 {
		t.Fatal("the server is closed and nothing is resumed")
	}
}

func TestMissingSavedChildrenAndTimedOutReadsCloseTheConnection(t *testing.T) {
	forgetMetadataReads()
	for _, hangs := range []bool{false, true} {
		c := &connector{script: script{"thread/read": func(ctx context.Context, _ jsontext.Value) (any, error) {
			if hangs {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return nil, errors.New("not found")
		}}}
		id := "missing-false"
		if hangs {
			id = "missing-true"
		}
		assertJSON(t, ReadCodexSubagentMetadata(context.Background(), id, c.connect, 5*time.Millisecond), `{}`)
		if !c.all()[0].isClosed() {
			t.Fatal("the server is closed")
		}
	}
}

func TestUnavailableSettingsAreCachedBrieflyAndCanBeRecoveredLater(t *testing.T) {
	forgetMetadataReads()
	var reads atomic.Int32
	read := func() {
		ReadCodexSubagentMetadata(context.Background(), "cache-child", (&connector{script: script{"thread/read": func(context.Context, jsontext.Value) (any, error) {
			reads.Add(1)
			return jsontext.Value(`{"thread":{"id":"cache-child","model":null,"reasoningEffort":null}}`), nil
		}}}).connect, 0)
	}
	read()
	read()
	if reads.Load() != 1 {
		t.Fatalf("reads = %d", reads.Load())
	}
	held := application.Now
	at := held() + 31_000
	application.Now = func() float64 { return at }
	defer func() { application.Now = held }()
	read()
	if reads.Load() != 2 {
		t.Fatalf("reads = %d", reads.Load())
	}
}

func TestInspectingManySavedChildrenLimitsSimultaneousReads(t *testing.T) {
	forgetMetadataReads()
	var active, peak atomic.Int32
	var mu sync.Mutex
	var pending []chan struct{}
	var waiting sync.WaitGroup
	for index := range 10 {
		waiting.Go(func() {
			id := "batch-" + string(rune('a'+index))
			ReadCodexSubagentMetadata(context.Background(), id, (&connector{script: script{"thread/read": func(context.Context, jsontext.Value) (any, error) {
				now := active.Add(1)
				for {
					held := peak.Load()
					if now <= held || peak.CompareAndSwap(held, now) {
						break
					}
				}
				release := make(chan struct{})
				mu.Lock()
				pending = append(pending, release)
				mu.Unlock()
				<-release
				active.Add(-1)
				return jsontext.Value(`{"thread":{"id":"` + id + `","model":"gpt-6.2-sol","reasoningEffort":"medium"}}`), nil
			}}}).connect, 0)
		})
	}
	eventually(t, "four reads never started", func() bool { mu.Lock(); defer mu.Unlock(); return len(pending) == 4 })
	tick()
	mu.Lock()
	if len(pending) != 4 {
		t.Fatalf("pending = %d", len(pending))
	}
	mu.Unlock()
	for released := 0; released < 10; released++ {
		eventually(t, "the next read never started", func() bool { mu.Lock(); defer mu.Unlock(); return len(pending) > released })
		mu.Lock()
		close(pending[released])
		mu.Unlock()
	}
	waiting.Wait()
	if peak.Load() != 4 {
		t.Fatalf("peak = %d", peak.Load())
	}
}
