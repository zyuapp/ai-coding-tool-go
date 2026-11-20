package claude

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

func ptrTo[T any](value T) *T { return &value }

func assertEqual(t *testing.T, got, want any, message string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s\n got: %#v\nwant: %#v", message, got, want)
	}
}

func succeeded() agent.ProviderResult { return agent.ProviderResult{Status: domain.RunStatusSucceeded} }

func TestEachModelResponseIsCountedOnceSubagentsIncluded(t *testing.T) {
	opened := `{"input_tokens":10,"cache_creation_input_tokens":200,"cache_read_input_tokens":3000,"output_tokens":1}`
	childOpened := `{"input_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":1000,"output_tokens":1}`
	parent := func(id string) string {
		if id == "" {
			return "null"
		}
		return `"` + id + `"`
	}
	start := func(id, model, usage, from string) string {
		return fmt.Sprintf(`{"type":"stream_event","uuid":"u","parent_tool_use_id":%s,"event":{"type":"message_start","message":{"id":%q,"model":%q,"usage":%s}}}`, parent(from), id, model, usage)
	}
	block := func(id, model, usage, from string) string {
		return fmt.Sprintf(`{"type":"assistant","uuid":"u","parent_tool_use_id":%s,"message":{"id":%q,"model":%q,"usage":%s,"content":[{"type":"text","text":"%s says something"}]}}`, parent(from), id, model, usage, id)
	}
	delta := func(output int, from, read string) string {
		return fmt.Sprintf(`{"type":"stream_event","uuid":"u","parent_tool_use_id":%s,"event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":null,"cache_creation_input_tokens":null,"cache_read_input_tokens":null%s,"output_tokens":%d}}}`, parent(from), read, output)
	}
	f := scripted(t,
		start("response-1", "claude-sonnet", opened, ""),
		start("child-response", "claude-haiku", childOpened, "launch"),
		block("response-1", "claude-sonnet", opened, ""),
		block("response-1", "claude-sonnet", opened, ""),
		block("child-response", "claude-haiku", childOpened, "launch"),
		delta(7, "launch", `,"input_tokens":5,"cache_read_input_tokens":1000`),
		delta(40, "", ""),
		block("response-2", "<synthetic>", `{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}`, ""),
		resultSuccess,
	)
	r := &recorder{}
	provider := NewProvider(nil, f.factory, "")
	assertEqual(t, await(t, execute(provider, runInput(r))), succeeded(), "result")
	assertEqual(t, eventsOf[agent.EventSpend](r), []agent.EventSpend{{Tokens: 3_211}, {Tokens: 1_006}, {Tokens: 6}, {Tokens: 39}},
		"a response counts what it read once, its closing delta adds all it wrote, and a synthetic reply spent nothing")
}

func TestWorkflowSpendIsWhatItsTotalGrewByWhileARunWasAttached(t *testing.T) {
	r := &recorder{}
	f := live()
	provider := NewProvider(nil, f.factory, "")
	progress := func(total int) string {
		return fmt.Sprintf(`{"type":"system","subtype":"task_progress","task_id":"workflow-1","description":"Review","usage":{"total_tokens":%d,"tool_uses":0,"duration_ms":0}}`, total)
	}
	runTurn(t, f, provider, runInput(r),
		`{"type":"system","subtype":"init","session_id":"session-1"}`,
		`{"type":"system","subtype":"task_started","task_id":"workflow-1","task_type":"local_workflow","description":"Review"}`,
		progress(5_000), progress(12_000))
	f.last().emit(t, progress(20_000))
	f.last().settle(t)
	runTurn(t, f, provider, runInput(r, func(input *agent.ProviderRunInput) {
		input.Continuation = &domain.Continuation{Provider: "claude", Value: "session-1"}
		input.Prompt = "and again"
	}), progress(21_000),
		`{"type":"system","subtype":"task_notification","task_id":"workflow-1","status":"completed","summary":"","usage":{"total_tokens":24000,"tool_uses":0,"duration_ms":0}}`)
	provider.CloseAll()
	assertEqual(t, eventsOf[agent.EventSpend](r), []agent.EventSpend{{Tokens: 5_000}, {Tokens: 7_000}, {Tokens: 1_000}, {Tokens: 3_000}}, "what it spent between runs moves the total without counting")
}

func TestOnlyWhatAModelSpendsWhileARunIsAttachedIsCounted(t *testing.T) {
	r := &recorder{}
	response := func(id, from string) string {
		parent := "null"
		if from != "" {
			parent = `"` + from + `"`
		}
		return fmt.Sprintf(`{"type":"assistant","uuid":"u-%s","parent_tool_use_id":%s,"message":{"id":%q,"model":"claude-sonnet","usage":{"input_tokens":100,"output_tokens":20},"content":[]}}`, id, parent, id)
	}
	f, _, end := liveTurn(t, r)
	q := f.last()
	q.emit(t, response("during", ""))
	q.emit(t, `{"type":"result","subtype":"success","is_error":false,"result":"Answer"}`)
	q.emit(t, response("background-child", "launch"))
	q.emit(t, response("between-runs", ""))
	q.settle(t)
	end()
	assertEqual(t, eventsOf[agent.EventSpend](r), []agent.EventSpend{{Tokens: 120}}, "work going on between runs has no run to count it")
}

func TestCompactingSendsClaudeCodesOwnCommand(t *testing.T) {
	r := &recorder{}
	f := live()
	provider := NewProvider(nil, f.factory, "")
	defer provider.CloseAll()
	result := runTurn(t, f, provider, runInput(r, func(input *agent.ProviderRunInput) {
		input.Continuation = &domain.Continuation{Provider: "claude", Value: "session-1"}
		input.Prompt = ""
		input.Operation = contracts.RunOperationCompact{PreTokens: 400_000}
	}),
		`{"type":"system","subtype":"init","session_id":"session-1"}`,
		`{"type":"system","subtype":"status","status":"compacting"}`,
		`{"type":"system","subtype":"status","status":null,"compact_result":"success"}`,
		`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"manual","pre_tokens":400000,"post_tokens":30000}}`,
		`{"type":"user","isReplay":true,"message":{"role":"user","content":"<local-command-stdout>Compacted </local-command-stdout>"},"parent_tool_use_id":null}`)
	assertEqual(t, result, succeeded(), "result")
	assertEqual(t, f.last().sentTexts(), []string{"/compact"}, "sent")
	var emitted []agent.ProviderEvent
	for _, event := range r.all() {
		if _, continuation := event.(agent.EventContinuation); !continuation {
			emitted = append(emitted, event)
		}
	}
	assertEqual(t, emitted, []agent.ProviderEvent{
		agent.EventCompactionStatus{Compacting: true},
		agent.EventCompactionStatus{Compacting: false},
		agent.EventCompaction{Trigger: "manual", PreTokens: 400_000, PostTokens: ptrTo(30_000.0)},
	}, "events")
}

func TestAPIRetriesShowWithTheReasonInTheUsersTerms(t *testing.T) {
	r := &recorder{}
	f := scripted(t,
		`{"type":"system","subtype":"init","session_id":"session-1"}`,
		`{"type":"system","subtype":"api_retry","attempt":2,"max_retries":10,"retry_delay_ms":4000,"error_status":529,"error":"overloaded"}`,
		`{"type":"system","subtype":"api_retry","attempt":3,"max_retries":10,"retry_delay_ms":8000,"error_status":429,"error":"rate_limit"}`,
		`{"type":"system","subtype":"api_retry","attempt":4,"max_retries":10,"retry_delay_ms":8000,"error_status":null,"error":"unknown"}`,
		`{"type":"system","subtype":"api_retry","attempt":5,"max_retries":10,"retry_delay_ms":8000,"error_status":500,"error":"server_error"}`,
		resultSuccess)
	assertEqual(t, await(t, execute(NewProvider(nil, f.factory, ""), runInput(r))), succeeded(), "result")
	retry := func(message string, attempt float64) agent.EventRetry {
		return agent.EventRetry{RetryNotice: domain.RetryNotice{Message: message, Attempt: &attempt, MaxRetries: ptrTo(10.0)}}
	}
	assertEqual(t, eventsOf[agent.EventRetry](r), []agent.EventRetry{
		retry("Claude is overloaded.", 2), retry("Claude is rate limited.", 3), retry("Cannot reach Claude.", 4), retry("Claude API error 500.", 5),
	}, "retries")
}

func TestSideChatBoundaryRidesTheFirstQuestionOnly(t *testing.T) {
	for _, fork := range []bool{false, true} {
		r := &recorder{}
		f := live()
		provider := NewProvider(nil, f.factory, "")
		prompt := "Why are those tests needed?"
		continuation := &domain.Continuation{Provider: "claude", Value: "side-session"}
		runTurn(t, f, provider, runInput(r, func(input *agent.ProviderRunInput) {
			input.Channel = contracts.RunChannelSide
			input.Prompt = prompt
			if fork {
				input.Continuation = &domain.Continuation{Provider: "claude", Value: "parent-running"}
				input.ForkContinuation = true
			}
		}), `{"type":"system","subtype":"init","session_id":"side-session"}`)
		assertEqual(t, f.last().sentTexts(), []string{agent.SideChatBoundary + "\n\nSide-chat request:\n" + prompt}, "one input means one turn, with the question after the history boundary")
		runTurn(t, f, provider, runInput(r, func(input *agent.ProviderRunInput) {
			input.Channel = contracts.RunChannelSide
			input.Continuation = continuation
			input.Prompt = "okay"
		}))
		assertEqual(t, f.count(), 1, "one session")
		assertEqual(t, f.last().sentTexts()[1:], []string{"okay"}, "follow-ups remain after the original boundary")
		provider.CloseAll()
		runTurn(t, f, provider, runInput(r, func(input *agent.ProviderRunInput) {
			input.Channel = contracts.RunChannelSide
			input.Continuation = continuation
			input.Prompt = "Explain that last point"
		}))
		assertEqual(t, f.count(), 2, "reopened")
		assertEqual(t, slices.Clone(f.last().sentTexts()), []string{"Explain that last point"}, "reopening preserves the side chat's own assignments")
		provider.CloseAll()
	}
}
