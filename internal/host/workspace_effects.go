package host

import (
	"context"
	"fmt"
	"reflect"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
)

// effectType is an effect's TypeScript `type`: the literal its Type tag holds.
func effectType(effect application.WorkspaceEffect) string {
	field := reflect.ValueOf(effect).FieldByName("Type")
	if tag, ok := field.Interface().(interface{ Value() any }); ok {
		name, _ := tag.Value().(string)
		return name
	}
	return ""
}

// RunWorkspaceEffect performs one effect the reducer described. Nothing here decides anything: each
// effect is carried out as it was written, and whatever the outside answers goes back through the
// reducer as an event.
func RunWorkspaceEffect(ctx context.Context, effect application.WorkspaceEffect, host EffectHost) error {
	handler := HostModulesEffectHandlers[effectType(effect)]
	if handler == nil {
		return fmt.Errorf("no handler carries out the %s effect", effectType(effect))
	}
	return handler(ctx, effect, host)
}
