package claude

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/claude/protocol"
)

const usageReport = `{
	"session": {"total_cost_usd": 1.5, "model_usage": {}},
	"subscription_type": "max",
	"rate_limits_available": true,
	"rate_limits": {
		"five_hour": {"utilization": 17, "resets_at": "2026-08-18T08:19:00Z"},
		"seven_day": {"utilization": 19.4, "resets_at": "2026-08-22T10:59:00Z"},
		"model_scoped": [{"display_name": "Fable", "utilization": 2, "resets_at": "2026-08-22T10:59:00Z"}]
	}
}`

func available(t *testing.T, raw string) domain.PlanUsageAvailable {
	t.Helper()
	usage, ok := ToPlanUsage(jsontext.Value(raw)).(domain.PlanUsageAvailable)
	if !ok {
		t.Fatalf("not available: %#v", ToPlanUsage(jsontext.Value(raw)))
	}
	return usage
}

func labels(usage domain.PlanUsageAvailable) []string {
	var labels []string
	for _, window := range usage.Windows {
		labels = append(labels, window.Label)
	}
	return labels
}

func TestAUsageReportBecomesTheWindowsThePanelDraws(t *testing.T) {
	usage := available(t, usageReport)
	assertEqual(t, *usage.Subscription, "max", "subscription")
	assertEqual(t, labels(usage), []string{"Current session", "Current week (all models)", "Current week (Fable)"}, "labels")
	session := domain.UsageWindowPeriodSession
	assertEqual(t, usage.Windows[0], &domain.UsageWindow{ID: "five_hour", Label: "Current session", Utilization: ptrTo(17.0), ResetsAt: ptrTo("2026-08-18T08:19:00Z"), Period: &session, Minutes: ptrTo(300.0)}, "session window")
	assertEqual(t, usage.Windows[2].ID, "model:Fable", "id")
	assertEqual(t, *usage.Windows[2].Model, "Fable", "model")
	assertEqual(t, usage.Windows[1].Model, (*string)(nil), "a window every model shares names none")
}

func TestAWindowNamedWithoutNumbersStillDrawsAndAModelListedTwiceDrawsOnce(t *testing.T) {
	usage := available(t, `{"rate_limits_available": true, "subscription_type": null, "rate_limits": {
		"five_hour": {"utilization": null, "resets_at": null},
		"model_scoped": [{"display_name": "Opus", "utilization": 4, "resets_at": null}],
		"seven_day_opus": {"utilization": 9, "resets_at": null}}}`)
	assertEqual(t, labels(usage), []string{"Current session", "Current week (Opus)"}, "labels")
	session := domain.UsageWindowPeriodSession
	assertEqual(t, usage.Windows[0], &domain.UsageWindow{ID: "five_hour", Label: "Current session", Period: &session, Minutes: ptrTo(300.0)}, "blank window")
	assertEqual(t, *usage.Windows[1].Utilization, 4.0, "the first listing wins")
	assertEqual(t, usage.Subscription, (*string)(nil), "subscription")
}

func TestASessionPlanLimitsDoNotCoverSaysSo(t *testing.T) {
	assertEqual(t, ToPlanUsage(jsontext.Value(`{"rate_limits_available": false, "rate_limits": null}`)), domain.PlanUsage(domain.PlanUsageNotApplicable{}), "not available")
	assertEqual(t, ToPlanUsage(jsontext.Value(`{"rate_limits_available": true, "rate_limits": null}`)), domain.PlanUsage(domain.PlanUsageNotApplicable{}), "no limits")
	if _, ok := ToPlanUsage(jsontext.Value(`{"rate_limits_available": true, "rate_limits": {}}`)).(domain.ComputerUseRunConfigUnavailable); !ok {
		t.Fatal("no windows is unavailable")
	}
}

func TestAnAnswerThatIsNotAUsageReportIsLeftForTheNextMethod(t *testing.T) {
	for _, raw := range []string{``, `"nope"`, `{"rate_limits": {}}`} {
		if usage := ToPlanUsage(jsontext.Value(raw)); usage != nil {
			t.Fatalf("%q read as %#v", raw, usage)
		}
	}
}

func usageSession(usage func(ctx context.Context) (jsontext.Value, error), opened **fakeQuery) QueryFactory {
	return func(_ context.Context, options protocol.Options) (Query, error) {
		q := newFake(options, false)
		q.usage = usage
		if opened != nil {
			*opened = q
		}
		return q, nil
	}
}

func unavailable(t *testing.T, usage domain.PlanUsage) string {
	t.Helper()
	failed, ok := usage.(domain.ComputerUseRunConfigUnavailable)
	if !ok {
		t.Fatalf("not unavailable: %#v", usage)
	}
	return failed.Message
}

func TestUsageReadsOverASessionThatCarriesNothing(t *testing.T) {
	var opened *fakeQuery
	usage := readPlanUsage(context.Background(), usageSession(func(context.Context) (jsontext.Value, error) { return jsontext.Value(usageReport), nil }, &opened), time.Second)
	if _, ok := usage.(domain.PlanUsageAvailable); !ok {
		t.Fatalf("usage %#v", usage)
	}
	assertEqual(t, opened.options.SettingSources, []string{}, "settings")
	assertEqual(t, opened.options.Tools, []string{}, "tools")
	assertEqual(t, opened.options.Cwd, os.TempDir(), "cwd")
	assertEqual(t, opened.isClosed(), true, "closed")
}

func TestAReaderInAnUnknownShapeAThrowingReaderAndASilentOneEachSayWhy(t *testing.T) {
	threw := readPlanUsage(context.Background(), usageSession(func(context.Context) (jsontext.Value, error) { return nil, errors.New("control request failed") }, nil), time.Second)
	assertEqual(t, unavailable(t, threw), "control request failed", "threw")
	reshaped := readPlanUsage(context.Background(), usageSession(func(context.Context) (jsontext.Value, error) { return jsontext.Value(`42`), nil }, nil), time.Second)
	if !strings.Contains(unavailable(t, reshaped), "shape this version of AI Coding Tool does not read") {
		t.Fatal(unavailable(t, reshaped))
	}
	hung := readPlanUsage(context.Background(), usageSession(func(context.Context) (jsontext.Value, error) { select {} }, nil), 5*time.Millisecond)
	assertEqual(t, unavailable(t, hung), "Claude did not answer in time.", "hung")
}

func TestASessionThatCannotStartOrCloseStillReports(t *testing.T) {
	unstartable := readPlanUsage(context.Background(), func(context.Context, protocol.Options) (Query, error) { return nil, errors.New("agent is unavailable") }, time.Second)
	assertEqual(t, unavailable(t, unstartable), "agent is unavailable", "unstartable")
	usage := readPlanUsage(context.Background(), func(_ context.Context, options protocol.Options) (Query, error) {
		q := newFake(options, false)
		q.usage = func(context.Context) (jsontext.Value, error) { return jsontext.Value(usageReport), nil }
		q.closePanics = true
		return q, nil
	}, time.Second)
	if _, ok := usage.(domain.PlanUsageAvailable); !ok {
		t.Fatalf("usage %#v", usage)
	}
}
