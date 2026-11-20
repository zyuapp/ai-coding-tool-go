package claude

import (
	"context"
	"encoding/json/jsontext"
	"math"
	"os"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const planUsageTimeout = 20 * time.Second

const (
	unknownUsageShape = "Claude reported usage in a shape this version of AI Coding Tool does not read."
	usageTimeout      = "Claude did not answer in time."
)

func readFiniteNumber(value any) *float64 {
	number, ok := value.(float64)
	if !ok || math.IsInf(number, 0) || math.IsNaN(number) {
		return nil
	}
	return &number
}

func readNonEmptyText(value any) *string {
	text, ok := value.(string)
	if !ok || text == "" {
		return nil
	}
	return &text
}

func usageWindowFrom(id, label string, raw any, model string) *domain.UsageWindow {
	record, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	period, minutes := domain.UsageWindowPeriodWeek, 10_080.0
	if id == "five_hour" {
		period, minutes = domain.UsageWindowPeriodSession, 300
	}
	window := &domain.UsageWindow{
		ID: id, Label: label,
		Utilization: readFiniteNumber(record["utilization"]),
		ResetsAt:    readNonEmptyText(record["resets_at"]),
		Period:      &period, Minutes: &minutes,
	}
	if model != "" {
		window.Model = &model
	}
	return window
}

// ToPlanUsage reads a usage answer. Nil when the answer is not a usage report at all, so the
// caller can try the next method.
func ToPlanUsage(raw jsontext.Value) domain.PlanUsage {
	var decoded any
	if len(raw) == 0 || jsonx.Unmarshal(raw, &decoded) != nil {
		return nil
	}
	return planUsageOf(decoded)
}

func planUsageOf(decoded any) domain.PlanUsage {
	record, ok := decoded.(map[string]any)
	if !ok {
		return nil
	}
	available, ok := record["rate_limits_available"].(bool)
	if !ok {
		return nil
	}
	buckets, isObject := record["rate_limits"].(map[string]any)
	if !available || !isObject {
		return domain.PlanUsageNotApplicable{}
	}

	windows := []*domain.UsageWindow{}
	add := func(window *domain.UsageWindow) {
		if window == nil {
			return
		}
		for _, seen := range windows {
			if seen.Label == window.Label {
				return
			}
		}
		windows = append(windows, window)
	}

	add(usageWindowFrom("five_hour", "Current session", buckets["five_hour"], ""))
	add(usageWindowFrom("seven_day", "Current week (all models)", buckets["seven_day"], ""))
	if scoped, ok := buckets["model_scoped"].([]any); ok {
		for _, entry := range scoped {
			fields, _ := entry.(map[string]any)
			if name := readNonEmptyText(fields["display_name"]); name != nil {
				add(usageWindowFrom("model:"+*name, "Current week ("+*name+")", entry, *name))
			}
		}
	}
	add(usageWindowFrom("seven_day_opus", "Current week (Opus)", buckets["seven_day_opus"], "Opus"))
	add(usageWindowFrom("seven_day_sonnet", "Current week (Sonnet)", buckets["seven_day_sonnet"], "Sonnet"))
	add(usageWindowFrom("seven_day_oauth_apps", "Current week (apps)", buckets["seven_day_oauth_apps"], ""))

	if len(windows) == 0 {
		return domain.ComputerUseRunConfigUnavailable{Message: "The plan reported no usage windows."}
	}
	return domain.PlanUsageAvailable{Subscription: readNonEmptyText(record["subscription_type"]), Windows: windows}
}

// readPlanUsage reads the plan's rate-limit windows over a session that runs nothing: it carries no
// project, no settings and no tools, and closes as soon as the control request answers.
func readPlanUsage(ctx context.Context, open QueryFactory, timeout time.Duration) domain.PlanUsage {
	options := claudeProcess()
	options.Cwd = os.TempDir()
	options.SettingSources = []string{}
	options.Tools = []string{}
	session, err := open(ctx, options)
	if err != nil {
		return domain.ComputerUseRunConfigUnavailable{Message: err.Error()}
	}
	defer func() {
		defer func() { _ = recover() }()
		session.Close()
	}()

	reading, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		raw jsontext.Value
		err error
	}
	answered := make(chan answer, 1)
	go func() {
		raw, err := session.Usage(reading)
		answered <- answer{raw, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case got := <-answered:
		if got.err != nil {
			return domain.ComputerUseRunConfigUnavailable{Message: got.err.Error()}
		}
		if usage := ToPlanUsage(got.raw); usage != nil {
			return usage
		}
		return domain.ComputerUseRunConfigUnavailable{Message: unknownUsageShape}
	case <-timer.C:
		return domain.ComputerUseRunConfigUnavailable{Message: usageTimeout}
	case <-ctx.Done():
		return domain.ComputerUseRunConfigUnavailable{Message: ctx.Err().Error()}
	}
}
