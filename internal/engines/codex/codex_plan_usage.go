package codex

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const (
	readTimeout      = 20 * time.Second
	limitReadTimeout = 10 * time.Second
)

// account/rateLimits/read's answer as the plan reader reads it: a bucket may be null.
type RateLimitsResponse struct {
	RateLimits          protocol.RateLimitSnapshot             `json:"rateLimits"`
	RateLimitsByLimitID map[string]*protocol.RateLimitSnapshot `json:"rateLimitsByLimitId"`
}

var errNoAnswer = errors.New("Codex did not answer in time.")

// Runs work under a deadline, giving up on it with errNoAnswer once the deadline passes.
func withTimeout[T any](ctx context.Context, timeout time.Duration, work func(ctx context.Context) (T, error)) (T, error) {
	return withDeadline(ctx, timeout, errNoAnswer, work)
}

// Runs work under a deadline, giving up on it with expired once the deadline passes.
func withDeadline[T any](ctx context.Context, timeout time.Duration, expired error, work func(ctx context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		value T
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		value, err := work(ctx)
		done <- answer{value, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.value, result.err
	case <-timer.C:
		var zero T
		return zero, expired
	}
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// An instant as JavaScript's Date#toISOString writes it.
func isoInstant(milliseconds float64) (string, bool) {
	milliseconds = math.Trunc(milliseconds)
	if !finite(milliseconds) || math.Abs(milliseconds) > 8.64e15 {
		return "", false
	}
	return time.UnixMilli(int64(milliseconds)).UTC().Format("2006-01-02T15:04:05.000Z"), true
}

func resetInstant(seconds *float64) *string {
	if seconds == nil || !finite(*seconds) {
		return nil
	}
	instant, ok := isoInstant(*seconds * 1_000)
	if !ok {
		return nil
	}
	return &instant
}

func windowName(primary bool, minutes *float64) string {
	if minutes != nil && *minutes == 300 {
		return "Current session"
	}
	if minutes != nil && *minutes == 10_080 {
		return "Current week"
	}
	if minutes == nil || !finite(*minutes) || *minutes <= 0 {
		if primary {
			return "Primary limit"
		}
		return "Secondary limit"
	}
	if math.Mod(*minutes, 1_440) == 0 {
		return "Current " + jsNumber(*minutes/1_440) + "-day window"
	}
	if math.Mod(*minutes, 60) == 0 {
		return "Current " + jsNumber(*minutes/60) + "-hour window"
	}
	return "Current " + jsNumber(*minutes) + "-minute window"
}

func windowMinutes(minutes *float64) *float64 {
	if minutes != nil && finite(*minutes) && *minutes > 0 {
		return minutes
	}
	return nil
}

// Only the ordinary limit's windows hold a turn, so only they are a period of the plan: a week or
// longer is the week, anything shorter the session. Some plans have no session window at all.
func windowPeriod(ordinary bool, minutes *float64) *domain.UsageWindowPeriod {
	if !ordinary || minutes == nil {
		return nil
	}
	if *minutes >= 10_080 {
		return ptr(domain.UsageWindowPeriodWeek)
	}
	return ptr(domain.UsageWindowPeriodSession)
}

func usageWindow(id, label string, primary bool, value *protocol.RateLimitWindow, ordinary bool) *domain.UsageWindow {
	if value == nil || !finite(value.UsedPercent) {
		return nil
	}
	kind := "secondary"
	if primary {
		kind = "primary"
	}
	minutes := windowMinutes(value.WindowDurationMins)
	return &domain.UsageWindow{
		ID:          id + ":" + kind,
		Label:       label + windowName(primary, value.WindowDurationMins),
		Utilization: ptr(value.UsedPercent),
		ResetsAt:    resetInstant(value.ResetsAt),
		Period:      windowPeriod(ordinary, minutes),
		Minutes:     minutes,
	}
}

type namedSnapshot struct {
	id       string
	snapshot *protocol.RateLimitSnapshot
}

func snapshots(response RateLimitsResponse) []namedSnapshot {
	var named []namedSnapshot
	for id, snapshot := range response.RateLimitsByLimitID {
		if snapshot != nil {
			named = append(named, namedSnapshot{id, snapshot})
		}
	}
	if len(named) > 0 {
		return named
	}
	id := "codex"
	if response.RateLimits.LimitID != nil {
		id = *response.RateLimits.LimitID
	}
	return []namedSnapshot{{id, &response.RateLimits}}
}

// The plan limit Codex turned a turn away for: of the ordinary limit's windows used up, the one that
// lifts last. Other buckets, such as code review's, never hold a turn.
func CodexUsageLimit(response RateLimitsResponse) *domain.UsageLimit {
	var found *domain.UsageLimit
	for _, value := range []*protocol.RateLimitWindow{response.RateLimits.Primary, response.RateLimits.Secondary} {
		if value == nil || value.UsedPercent < 100 || value.ResetsAt == nil || !finite(*value.ResetsAt) {
			continue
		}
		resetsAt := *value.ResetsAt * 1_000
		if found != nil && found.ResetsAt >= resetsAt {
			continue
		}
		// Only a window of five hours or less lifts soon enough to resume on its own.
		window := domain.UsageLimitWindowWeekly
		if value.WindowDurationMins != nil && *value.WindowDurationMins <= 300 {
			window = domain.UsageLimitWindowSession
		}
		found = &domain.UsageLimit{ResetsAt: resetsAt, Window: window}
	}
	return found
}

// When the plan limit a turn ran into lifts, read from a live session. Nil if the server cannot say.
func ReadCodexUsageLimit(ctx context.Context, client CodexClient) *domain.UsageLimit {
	response, err := withTimeout(ctx, limitReadTimeout, func(ctx context.Context) (RateLimitsResponse, error) {
		return call[RateLimitsResponse](ctx, client, "account/rateLimits/read", nil)
	})
	if err != nil {
		return nil
	}
	return CodexUsageLimit(response)
}

func limitName(id string, snapshot *protocol.RateLimitSnapshot) string {
	name := id
	if snapshot.LimitName != nil {
		name = *snapshot.LimitName
	}
	if strings.ToLower(name) == "codex" {
		return "Codex"
	}
	return name
}

// Translates Codex's account windows into the same report the Claude reader returns.
func ToCodexPlanUsage(response RateLimitsResponse, accountPlan *string) domain.PlanUsage {
	limits := snapshots(response)
	collator := collate.New(language.Und)
	sort.SliceStable(limits, func(left, right int) bool {
		leftName, rightName := limitName(limits[left].id, limits[left].snapshot), limitName(limits[right].id, limits[right].snapshot)
		if leftName == "Codex" && rightName != "Codex" {
			return true
		}
		if rightName == "Codex" && leftName != "Codex" {
			return false
		}
		if order := collator.CompareString(leftName, rightName); order != 0 {
			return order < 0
		}
		return collator.CompareString(limits[left].id, limits[right].id) < 0
	})
	windows := []*domain.UsageWindow{}
	for _, limit := range limits {
		ordinary := limitName(limit.id, limit.snapshot) == "Codex"
		name := ""
		if len(limits) > 1 {
			name = limitName(limit.id, limit.snapshot) + " · "
		}
		if primary := usageWindow(limit.id, name, true, limit.snapshot.Primary, ordinary); primary != nil {
			windows = append(windows, primary)
		}
		if secondary := usageWindow(limit.id, name, false, limit.snapshot.Secondary, ordinary); secondary != nil {
			windows = append(windows, secondary)
		}
	}
	if len(windows) == 0 {
		return domain.PlanUsageNotApplicable{}
	}
	subscription := accountPlan
	for _, limit := range limits {
		if limit.snapshot.PlanType != nil && *limit.snapshot.PlanType != "" {
			subscription = limit.snapshot.PlanType
			break
		}
	}
	return domain.PlanUsageAvailable{Subscription: subscription, Windows: windows}
}

func unavailable(err error) domain.PlanUsage {
	return domain.ComputerUseRunConfigUnavailable{Message: err.Error()}
}

// Reads Codex plan limits over a short-lived app-server connection with no thread or run. Nil
// connect is ConnectAppServer; timeout <= 0 is 20 seconds.
func ReadCodexPlanUsage(ctx context.Context, connect CodexConnect, timeout time.Duration) domain.PlanUsage {
	if connect == nil {
		connect = ConnectAppServer
	}
	if timeout <= 0 {
		timeout = readTimeout
	}
	command, err := CodexAppServer(nil, AppServerOptions{SharedHome: true})
	if err != nil {
		return unavailable(err)
	}
	client := connect(command)
	defer client.Close()
	if _, err := withTimeout(ctx, timeout, func(ctx context.Context) (struct{}, error) { return struct{}{}, initialize(ctx, client) }); err != nil {
		return unavailable(err)
	}
	account, err := withTimeout(ctx, timeout, func(ctx context.Context) (protocol.GetAccountResponse, error) {
		return call[protocol.GetAccountResponse](ctx, client, "account/read", protocol.GetAccountParams{RefreshToken: ptr(false)})
	})
	if err != nil {
		return unavailable(err)
	}
	if account.Account == nil || account.Account.Type != "chatgpt" {
		return domain.PlanUsageNotApplicable{}
	}
	response, err := withTimeout(ctx, timeout, func(ctx context.Context) (RateLimitsResponse, error) {
		return call[RateLimitsResponse](ctx, client, "account/rateLimits/read", nil)
	})
	if err != nil {
		return unavailable(err)
	}
	return ToCodexPlanUsage(response, account.Account.PlanType)
}
