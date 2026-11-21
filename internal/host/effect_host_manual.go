package host

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
)

// What performing an effect takes: the door back into the reducer, and the desktop it acts on.
type EffectHost struct {
	Dispatch func(ctx context.Context, input application.WorkspaceInput) error `json:"-"`
	Desktop  RuntimeDesktop                                                    `json:"-"`
	// Where the view preferences are written, which is the host's to keep.
	Storage              application.KeyValueStorage `json:"-"`
	EnvironmentRefreshes EnvironmentRefreshes        `json:"environmentRefreshes"`
	ScheduleSnoozeExpiry func(at *float64)           `json:"-"`
	ScheduleLimitReset   func(at *float64)           `json:"-"`
	// Wakes the reducer when an idle thread's compaction is due, or one has run too long.
	ScheduleIdleCompaction func(at *float64) `json:"-"`
	// Takes a toast down at `at`, or never for null. Each toast keeps its own time, and a host that stops drops them all.
	ScheduleToastDismissal func(id float64, at *float64) `json:"-"`
	// Carries an effect to the window's own views. Absent where there is no window, which drops it.
	Surface func(effect application.WorkspaceSurfaceEffect) `json:"-"`
}
