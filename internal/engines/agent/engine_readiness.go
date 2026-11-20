package agent

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// Where an install stands against what the app was built on and the newest release, and the command
// that moves it on when it is behind either and the app can run that command. Only Version, Required,
// Latest and Fix are set; the caller fills in the rest.
func VersionReadiness(ctx context.Context, binary EngineCommand, installed *InstalledEngine, baseline string) domain.EngineReadiness {
	var latest *string
	if installed.Version != nil {
		latest = LatestEngineVersion(ctx, binary, installed)
	}
	readiness := domain.EngineReadiness{Version: installed.Version}
	if domain.IsOlderThan(installed.Version, baseline) {
		required := baseline
		readiness.Required = &required
	}
	if latest != nil && installed.Version != nil && domain.IsOlderThan(installed.Version, *latest) {
		readiness.Latest = latest
	}
	if installed.Update != nil && (readiness.Required != nil || readiness.Latest != nil) {
		fix := installed.Update.Command
		readiness.Fix = &fix
	}
	return readiness
}
