package host

import (
	"context"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// plain is a typed value as JSON.parse would hand it back, for the checks written against unknown input.
func plain(value any) any {
	raw, err := jsonx.Marshal(value)
	if err != nil {
		return nil
	}
	var decoded any
	if jsonx.Unmarshal(raw, &decoded) != nil {
		return nil
	}
	return decoded
}

// RunEffects carry what a run takes to start, what it is told while it runs, what is read back about
// it, and what its messages leave behind.
var RunEffects = EffectHandlers{
	"persist-preferences": handle(func(_ context.Context, effect application.WorkspaceEffectPersistPreferences, host EffectHost) error {
		SaveViewPreferences(host.Storage, effect.Preferences)
		return nil
	}),
	"load-subagent-activity": handle(func(ctx context.Context, effect application.WorkspaceEffectLoadSubagentActivity, host EffectHost) error {
		activity, err := host.Desktop.LoadSubagentActivity(ctx, effect.TaskID, effect.SubagentID)
		if err == nil && len(activity) > 0 {
			err = host.Dispatch(ctx, application.WorkspaceEventSubagentActivityLoaded{TaskID: effect.TaskID, SubagentID: effect.SubagentID, Activity: activity})
		}
		return ReportFailure(ctx, host, err)
	}),
	"load-subagent-metadata": handle(func(ctx context.Context, effect application.WorkspaceEffectLoadSubagentMetadata, host EffectHost) error {
		event := contracts.SubagentEventSubagentMetadata{TaskID: effect.TaskID, ID: effect.SubagentID}
		if metadata, err := host.Desktop.LoadSubagentMetadata(ctx, effect.Engine, effect.SubagentID, effect.SessionID); err == nil && metadata != nil {
			event.Model, event.Effort, event.Prompt = metadata.Model, metadata.Effort, metadata.Prompt
		}
		if contracts.IsSubagentEvent(plain(event)) && (nonEmpty(event.Model) != nil || nonEmpty(event.Effort) != nil) {
			return host.Dispatch(ctx, application.WorkspaceEventThreadEvent{Event: event})
		}
		return nil
	}),
	"resolve-run-workspace": handle(func(ctx context.Context, effect application.WorkspaceEffectResolveRunWorkspace, host EffectHost) error {
		return host.Dispatch(ctx, ResolveRunWorkspace(ctx, effect, host.Desktop))
	}),
	"start-run": told(func(effect application.WorkspaceEffectStartRun, desktop RuntimeDesktop) {
		desktop.Send(effect.Command)
	}),
	"send-run-command": told(func(effect application.WorkspaceEffectSendRunCommand, desktop RuntimeDesktop) {
		desktop.Send(effect.Command.(contracts.RunCommand))
	}),
	// A title costs a model turn, so it lands on its own rather than holding back the send that asked for it.
	"suggest-title": handle(func(_ context.Context, effect application.WorkspaceEffectSuggestTitle, host EffectHost) error {
		go func() {
			title, err := host.Desktop.SuggestTaskTitle(background, effect.Text, effect.Attachments, effect.Engine, &effect.Model)
			if err == nil && title != nil && *title != "" {
				_ = host.Dispatch(background, application.WorkspaceEventTitleSuggested{TaskID: effect.TaskID, Title: *title})
			}
		}()
		return nil
	}),
	"preserve-message-images": handle(func(ctx context.Context, effect application.WorkspaceEffectPreserveMessageImages, host EffectHost) error {
		images := application.MessageImages(effect.Text)
		files := make([]string, len(images))
		for index, image := range images {
			files[index] = image.Path
		}
		if len(files) == 0 {
			return nil
		}
		return host.Desktop.PreserveMessageImages(ctx, files, effect.Root, effect.MessageID)
	}),
}

// flushIntervalMs is how long a run's reports are gathered where there is no paint to wait for.
const flushIntervalMs = 16

func shortly(flush func()) func() {
	timer := time.AfterFunc(flushIntervalMs*time.Millisecond, flush)
	return func() { timer.Stop() }
}

// batchAgentEvents folds a run's reports in a frame at a time.
func batchAgentEvents(host SubscriptionHost, holdReports func(flush func())) func() {
	schedule := host.Frame
	if schedule == nil {
		schedule = shortly
	}
	var (
		mu      sync.Mutex
		waiting []contracts.AgentEvent
		cancel  func()
	)
	flush := func() {
		mu.Lock()
		if cancel != nil {
			cancel()
			cancel = nil
		}
		events := waiting
		waiting = nil
		// Executed under the lock, so batches reach the runtime in the order they were gathered.
		if len(events) > 0 {
			host.send(application.WorkspaceEventAgentEvents{Events: events})
		}
		mu.Unlock()
	}
	holdReports(flush)
	stopListening := host.Desktop.OnAgentEvent(func(event contracts.AgentEvent) {
		mu.Lock()
		waiting = append(waiting, event)
		scheduling := cancel == nil
		if scheduling {
			cancel = func() {}
		}
		mu.Unlock()
		if scheduling {
			stop := schedule(flush)
			mu.Lock()
			if cancel != nil {
				cancel = stop
			} else {
				stop()
			}
			mu.Unlock()
		}
	})
	return func() {
		stopListening()
		flush()
	}
}

// answerThreadRequests answers a thread request from state, after what runs have reported so far is folded in.
func answerThreadRequests(host SubscriptionHost, flushAll func()) func() {
	desktop := host.Desktop
	requests := threadRequestHost{
		state:    host.State,
		desktop:  desktop,
		dispatch: host.Dispatch,
		execute:  host.Execute,
		waiters:  host.Waiters,
	}
	stopListening := desktop.OnThreadRequest(func(request contracts.ThreadRequest) {
		flushAll()
		go func() {
			prepared, err := host.PrepareThreadRequest(background, request)
			if err != nil {
				desktop.AnswerThreadRequest(contracts.ThreadResponseFalse{RequestID: threadRequestID(request), Message: errorMessage(err)})
				return
			}
			desktop.AnswerThreadRequest(AnswerThreadRequest(background, requests, request, prepared))
		}()
	})
	return func() {
		stopListening()
		host.Waiters.clear()
	}
}

var RunHost = HostModule{
	Effects: RunEffects,
	Subscribe: func(host SubscriptionHost, runs RunReports) func() {
		stopEvents := batchAgentEvents(host, runs.HoldReports)
		stopRequests := answerThreadRequests(host, runs.FlushAll)
		return func() {
			stopEvents()
			stopRequests()
		}
	},
}
