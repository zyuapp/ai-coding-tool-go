package host

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
)

// EffectHandler carries out one effect. It runs on its own goroutine; a returned error is the
// promise's rejection.
type EffectHandler func(ctx context.Context, effect application.WorkspaceEffect, host EffectHost) error

// EffectHandlers are effect handlers keyed by the effect's `type`. A module's own table names
// the effects it owns, which is where that grouping lives.
type EffectHandlers map[string]EffectHandler

// HostModule is one capability's part of the runtime: the effects it carries out, and what it
// listens to and reads on the way up.
type HostModule struct {
	Effects EffectHandlers
	// Subscribe starts listening, answering how to stop (nil for nothing to stop). Modules start in
	// the order they are listed, and stop in it too. Nil when the module listens to nothing.
	Subscribe func(host SubscriptionHost, runs RunReports) func()
}
