package contracts

import (
	"math"
	"testing"
)

const ipcStartCommand = `{"type":"start","channel":"main","taskId":"task-1","title":"Inspect the app","runId":"run-1","prompt":"inspect","workspaceId":"workspace-1","policy":"confirm","engine":"claude","model":"opus","effort":"high"}`

func TestSteeringFailuresRequireAMessageAddressAndABoundedError(t *testing.T) {
	event := restObj(t, `{"type":"queued.steer-failed","taskId":"task-a","runId":"run-a","sequence":1,"messageId":"message-a","message":"Could not steer"}`)
	restExpect(t, "IsRunEvent", IsRunEvent, event, true)
	for _, message := range []any{restUndefined, 123, "", restRepeat("x", 100_001)} {
		restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, event, "message", message), false)
	}
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, event, "messageId", restUndefined), false)
}

func TestRetryNoticesCarryABoundedReasonAndOptionalAttemptCounts(t *testing.T) {
	event := restObj(t, `{"type":"run.retrying","taskId":"task-a","runId":"run-a","sequence":1,"message":"Claude is overloaded."}`)
	restExpect(t, "IsRunEvent", IsRunEvent, event, true)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, event, "attempt", 2, "maxRetries", 10), true)
	for _, message := range []any{restUndefined, 123, "", restRepeat("x", 100_001)} {
		restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, event, "message", message), false)
	}
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, event, "attempt", -1), false)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, event, "maxRetries", "10"), false)
}

func TestExternalStartCommandsCarryOnlyAWorkspaceID(t *testing.T) {
	command := restObj(t, ipcStartCommand)
	restExpect(t, "IsRunCommand", IsRunCommand, command, true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "workspaceRoot", "/tmp/project"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "projectless", true), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "computerUse", map[string]any{"status": "setup-required"}), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "cwd", "/tmp/project"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "sessionId", "claude-session"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "channel", "background"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "channel", "side", "forkContinuation", true), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "channel", "side", "continuation", map[string]any{"provider": "claude", "value": "session"}, "forkContinuation", true), true)
}

func TestOnlyAMemberNamesTheProjectCheckoutItMustLeaveAlone(t *testing.T) {
	command := restObj(t, ipcStartCommand)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "coordinationRole", "member", "projectWorkspaceId", "workspace-2"), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "projectWorkspaceId", "workspace-2"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "coordinationRole", "coordinator", "projectWorkspaceId", "workspace-2"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "coordinationRole", "member", "projectWorkspaceId", ""), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "coordinationRole", "member", "projectRoot", "/tmp/project"), false)
	internal := restWith(t, command, "coordinationRole", "member", "projectWorkspaceId", "workspace-2", "workspaceRoot", "/tmp/worktree", "projectless", false, "computerUse", map[string]any{"status": "setup-required"})
	restExpect(t, "IsInternalRunCommand", IsInternalRunCommand, restWith(t, internal, "projectRoot", "/tmp/project"), true)
	restExpect(t, "IsInternalRunCommand", IsInternalRunCommand, restWith(t, internal, "projectRoot", ""), false)
}

func TestStartCommandsRequireABoundedThreadTitle(t *testing.T) {
	command := restObj(t, ipcStartCommand)
	for _, title := range []any{restUndefined, nil, 1, "", restRepeat("x", 65)} {
		restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "title", title), false)
		restExpect(t, "IsInternalRunCommand", IsInternalRunCommand, restWith(t, command, "title", title, "workspaceRoot", "/tmp/project", "projectless", false, "computerUse", map[string]any{"status": "setup-required"}), false)
	}
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "title", restRepeat("x", 64)), true)
}

func TestAStartReachesTheUsersChromeOnlyOnAnEngineThatCanDriveIt(t *testing.T) {
	command := restObj(t, ipcStartCommand)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "chromeBrowser", true), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "chromeBrowser", false), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "engine", "codex", "model", "gpt-6.2-sol", "chromeBrowser", true), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "engine", "codex", "model", "gpt-6.2-sol"), true)
}

func TestManualCompactionIsAValidatedThreadOperation(t *testing.T) {
	compact := restWith(t, restObj(t, ipcStartCommand), "engine", "codex", "model", "gpt-6.2-sol", "prompt", "",
		"continuation", map[string]any{"provider": "codex", "value": "thread-1"},
		"operation", map[string]any{"type": "compact", "preTokens": 125_000})
	restExpect(t, "IsRunCommand", IsRunCommand, compact, true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, compact, "model", "gpt-6.2-astra"), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, compact, "engine", "claude", "model", "opus", "continuation", map[string]any{"provider": "claude", "value": "session-1"}), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, compact, "engine", "claude", "model", "opus", "continuation", map[string]any{"provider": "codex", "value": "thread-1"}), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, compact, "prompt", "also answer"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, compact, "continuation", map[string]any{"provider": "claude", "value": "session-1"}), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, compact, "operation", map[string]any{"type": "compact", "preTokens": -1}), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, compact, "forkContinuation", true), false)
}

func TestNativeCodexReviewsAcceptValidTargetsAndInheritedContinuationForks(t *testing.T) {
	review := restWith(t, restObj(t, ipcStartCommand), "engine", "codex", "model", "gpt-6.1-terra", "prompt", "",
		"continuation", map[string]any{"provider": "codex", "value": "thread-1"},
		"operation", restJSON(t, `{"type":"review","target":{"type":"uncommittedChanges"}}`))
	target := func(json string) any { return restJSON(t, `{"type":"review","target":`+json+`}`) }
	restExpect(t, "IsRunCommand", IsRunCommand, review, true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, review, "operation", target(`{"type":"baseBranch","branch":"main"}`)), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, review, "operation", target(`{"type":"commit","sha":"abc123","title":null}`)), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, review, "operation", target(`{"type":"custom","instructions":"Check concurrency."}`)), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, review, "prompt", "also answer"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, review, "channel", "side"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, review, "continuation", map[string]any{"provider": "claude", "value": "session-1"}), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, review, "operation", target(`{"type":"baseBranch","branch":" "}`)), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, review, "operation", target(`{"type":"commit","sha":"","title":null}`)), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, review, "operation", target(`{"type":"custom","instructions":""}`)), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, review, "forkContinuation", true), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, review, "forkContinuation", false), false)
}

func TestInternalWorkerCommandsRequireAResolvedRootAndProjectlessFlag(t *testing.T) {
	command := restObj(t, ipcStartCommand)
	restExpect(t, "IsInternalRunCommand", IsInternalRunCommand, restWith(t, command, "workspaceRoot", "/tmp/project", "projectless", false, "computerUse", map[string]any{"status": "setup-required"}), true)
	restExpect(t, "IsInternalRunCommand", IsInternalRunCommand, command, false)
	restExpect(t, "IsInternalRunCommand", IsInternalRunCommand, restWith(t, command, "workspaceRoot", "/tmp/project"), false)
}

func TestRunCommandGuardScopesCancellationAndApproval(t *testing.T) {
	restExpect(t, "IsRunCommand", IsRunCommand, restObj(t, `{"type":"cancel","taskId":"task-1","runId":"run-1"}`), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restObj(t, `{"type":"cancel"}`), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restObj(t, `{"type":"approval","taskId":"task-1","runId":"run-1","approvalId":"approval-1","allow":false}`), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restObj(t, `{"type":"approval","approvalId":"approval-1","allow":false}`), false)
}

func TestRunCommandGuardScopesStoppingOneOfARunsProcesses(t *testing.T) {
	stop := restObj(t, `{"type":"stop-process","taskId":"task-1","runId":"run-1","processId":"bash-1"}`)
	restExpect(t, "IsRunCommand", IsRunCommand, stop, true)
	restExpect(t, "IsInternalRunCommand", IsInternalRunCommand, stop, true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, stop, "processId", ""), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restObj(t, `{"type":"stop-process","taskId":"task-1","runId":"run-1"}`), false)
}

func TestBackgroundEventGuardValidatesTheProcessSetAndTakesNoRun(t *testing.T) {
	base := restObj(t, `{"type":"background.changed","taskId":"task-1"}`)
	restExpect(t, "IsBackgroundEvent", IsBackgroundEvent, restWith(t, base, "processes", []any{}), true)
	restExpect(t, "IsBackgroundEvent", IsBackgroundEvent, restWith(t, base, "processes", restJSON(t, `[{"id":"bash-1","kind":"shell","description":"npm run dev"},{"id":"watch-1","kind":"monitor","description":"Deploy events"}]`)), true)
	restExpect(t, "IsBackgroundEvent", IsBackgroundEvent, restWith(t, base, "processes", restJSON(t, `[{"id":"bash-1","kind":"subagent","description":"npm run dev"}]`)), false)
	restExpect(t, "IsBackgroundEvent", IsBackgroundEvent, restWith(t, base, "processes", restJSON(t, `[{"id":"","kind":"shell","description":"npm run dev"}]`)), false)
	restExpect(t, "IsBackgroundEvent", IsBackgroundEvent, restWith(t, base, "processes", restJSON(t, `[{"id":"bash-1","kind":"shell","description":""}]`)), false)
	restExpect(t, "IsBackgroundEvent", IsBackgroundEvent, restWith(t, base, "processes", map[string]any{}), false)
	restExpect(t, "IsBackgroundEvent", IsBackgroundEvent, restObj(t, `{"type":"workflow.finished","taskId":"task-1","id":"wf-1","status":"stopped","summary":""}`), false)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, base, "runId", "run-1", "sequence", 1, "processes", []any{}), false)
}

func TestGoalEventGuardValidatesNativeStateAndClearing(t *testing.T) {
	restExpect(t, "IsGoalEvent", IsGoalEvent, restObj(t, `{"type":"goal.changed","taskId":"task-1","goal":{"objective":"Ship it","status":"active","iterations":2}}`), true)
	restExpect(t, "IsGoalEvent", IsGoalEvent, restObj(t, `{"type":"goal.changed","taskId":"task-1","goal":null}`), true)
	restExpect(t, "IsGoalEvent", IsGoalEvent, restObj(t, `{"type":"goal.changed","taskId":"task-1"}`), false)
	restExpect(t, "IsGoalEvent", IsGoalEvent, restObj(t, `{"type":"goal.changed","taskId":"task-1","goal":{"objective":"","status":"active"}}`), false)
	restExpect(t, "IsGoalEvent", IsGoalEvent, restObj(t, `{"type":"goal.changed","taskId":"task-1","goal":{"objective":"Ship it","status":"paused"}}`), false)
}

func TestRunEventGuardValidatesOptionalStatusMessages(t *testing.T) {
	event := restObj(t, `{"type":"run.status","taskId":"task-1","runId":"run-1","sequence":1,"status":"failed"}`)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, event, "message", "failed"), true)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, event, "message", 42), false)
}

func TestRunEventGuardAcceptsSignalsWithoutPayload(t *testing.T) {
	restExpect(t, "IsRunEvent", IsRunEvent, restObj(t, `{"type":"computer-use.setup-required","taskId":"task-1","runId":"run-1","sequence":1}`), true)
	restExpect(t, "IsRunEvent", IsRunEvent, restObj(t, `{"type":"continuation.lost","taskId":"task-1","runId":"run-1","sequence":1}`), true)
}

func TestRunEventGuardAcceptsToolIntentsWithoutAWritePath(t *testing.T) {
	event := restObj(t, `{"type":"tool.intent","taskId":"task-1","runId":"run-1","sequence":1,"intent":{"toolId":"tool-1","name":"Read","input":{}}}`)
	restExpect(t, "IsRunEvent", IsRunEvent, event, true)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, event, "intent", restJSON(t, `{"toolId":"tool-1","name":"Read","writePath":null}`)), false)
}

func TestSubagentEventGuardValidatesThreadScopedLifecycleReports(t *testing.T) {
	base := `"taskId":"task-1","id":"agent-1",`
	valid := []map[string]any{
		restObj(t, `{`+base+`"type":"subagent.started","description":"Inspect","agentType":"Explore","sessionScoped":true}`),
		restObj(t, `{`+base+`"type":"subagent.status","status":"working"}`),
		restObj(t, `{`+base+`"type":"subagent.status","status":"idle","summary":"Ready for more work"}`),
		restObj(t, `{`+base+`"type":"subagent.progress","description":"Inspect","agentType":"reviewer","lastToolName":"Read","summary":"Done","totalTokens":42}`),
		restObj(t, `{`+base+`"type":"subagent.activity","activityId":"activity-1","kind":"text","text":"Working"}`),
		restObj(t, `{`+base+`"type":"subagent.activity","activityId":"activity-2","kind":"tool","title":"Read","text":"{}"}`),
		restObj(t, `{`+base+`"type":"subagent.finished","status":"completed","summary":"Done"}`),
		restObj(t, `{`+base+`"type":"subagent.finished","status":"failed","summary":"Failed"}`),
		restObj(t, `{`+base+`"type":"subagent.finished","status":"stopped","summary":""}`),
	}
	for _, event := range valid {
		restExpect(t, "IsSubagentEvent", IsSubagentEvent, event, true)
	}
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, valid[0], "runId", "run-1", "sequence", 1), false)
	invalid := []map[string]any{
		restWith(t, valid[0], "description", ""),
		restWith(t, valid[0], "sessionScoped", false),
		restWith(t, valid[1], "status", "waiting"),
		restWith(t, valid[3], "totalTokens", -1),
		restWith(t, valid[3], "totalTokens", math.NaN()),
		restWith(t, valid[4], "activityId", ""),
		restWith(t, valid[4], "kind", "image"),
		restWith(t, valid[5], "title", 42),
		restWith(t, valid[6], "status", "idle"),
		restWith(t, valid[6], "summary", 42),
	}
	for _, event := range invalid {
		restExpect(t, "IsSubagentEvent", IsSubagentEvent, event, false)
	}
}

func TestSubagentMetadataValidatesModelsAndEffortWhilePreservingLongPrompts(t *testing.T) {
	for _, typ := range []string{"subagent.started", "subagent.metadata"} {
		event := restWith(t, restObj(t, `{"taskId":"task-1","id":"child","description":"Review","model":"gpt-6.2-astra","effort":"xhigh"}`),
			"type", typ, "prompt", restRepeat("Instructions\n", 20_000))
		restExpect(t, "IsSubagentEvent", IsSubagentEvent, event, true)
		for _, key := range []string{"model", "effort", "prompt"} {
			restExpect(t, "IsSubagentEvent", IsSubagentEvent, restWith(t, event, key, 42), false)
		}
	}
}

func TestWorkflowEventsNameAThreadInsteadOfARun(t *testing.T) {
	started := restObj(t, `{"type":"workflow.started","taskId":"task-1","id":"wf-1","name":"review-changes","description":"Review changed files"}`)
	restExpect(t, "IsWorkflowEvent", IsWorkflowEvent, started, true)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, started, "runId", "run-1", "sequence", 1), false)
	restExpect(t, "IsWorkflowEvent", IsWorkflowEvent, restWith(t, started, "taskId", ""), false)
	restExpect(t, "IsWorkflowEvent", IsWorkflowEvent, restWith(t, started, "type", "run.started"), false)

	progress := restObj(t, `{"type":"workflow.progress","taskId":"task-1","id":"wf-1","phases":[{"index":0,"title":"Review"}],"agents":[{"index":0,"label":"review:bugs","state":"running"}],"totalTokens":10,"totalToolCalls":1}`)
	restExpect(t, "IsWorkflowEvent", IsWorkflowEvent, progress, true)
	restExpect(t, "IsWorkflowEvent", IsWorkflowEvent, restWith(t, progress, "agents", restJSON(t, `[{"index":0,"label":"review:bugs","state":"thinking"}]`)), false)
	restExpect(t, "IsWorkflowEvent", IsWorkflowEvent, restWith(t, progress, "agents", restJSON(t, `[{"index":0,"label":"review:bugs","state":"running","isolation":"remote","cached":true,"model":"opus","tokens":5}]`)), true)
	restExpect(t, "IsWorkflowEvent", IsWorkflowEvent, restWith(t, progress, "agents", restJSON(t, `[{"index":0,"label":"review:bugs","state":"running","tokens":-5}]`)), false)
	restExpect(t, "IsWorkflowEvent", IsWorkflowEvent, restWith(t, progress, "totalTokens", -1), false)

	finished := restObj(t, `{"type":"workflow.finished","taskId":"task-1","id":"wf-1","status":"completed","summary":"Dynamic workflow completed"}`)
	restExpect(t, "IsWorkflowEvent", IsWorkflowEvent, finished, true)
	restExpect(t, "IsWorkflowEvent", IsWorkflowEvent, restWith(t, finished, "status", "running"), false)
}

func TestRunGuardsEnforceNumericAndStringBoundaries(t *testing.T) {
	command := restObj(t, ipcStartCommand)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "taskId", restRepeat("x", 256), "prompt", restRepeat("x", 1_000_000)), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "taskId", restRepeat("x", 257)), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "prompt", restRepeat("x", 1_000_001)), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "model", "future model"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "model", ""), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "model", restRepeat("x", 257)), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "model", "future-model"), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "effort", "insane"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "effort", "ultra"), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "engine", "codex", "model", "gpt-6.2-sol", "effort", "ultra"), true)
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max", "ultra"} {
		restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "engine", "codex", "model", "gpt-6.2-astra", "effort", effort), true)
	}
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "model", "gpt-6.2-astra"), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "effort", restUndefined), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "continuation", map[string]any{"provider": "", "value": "session"}), false)

	usage := restObj(t, `{"type":"context.usage","taskId":"task-1","runId":"run-1","sequence":1,"tokens":0,"limit":1,"model":"claude"}`)
	restExpect(t, "IsRunEvent", IsRunEvent, usage, true)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, usage, "sequence", 0), false)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, usage, "sequence", 1.5), false)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, usage, "tokens", -1), false)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, usage, "limit", 0), false)
}

func TestWhatARunSpentIsAWholeBoundedCount(t *testing.T) {
	spent := restObj(t, `{"type":"context.spent","taskId":"task-1","runId":"run-1","sequence":1,"tokens":1}`)
	restExpect(t, "IsRunEvent", IsRunEvent, spent, true)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, spent, "tokens", 1_000_000_000), true)
	for _, tokens := range []any{0, 2.5, 1_000_000_001, "12", restUndefined} {
		restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, spent, "tokens", tokens), false)
	}
}

func TestAutomationRequestsCarryATaskAndAWellFormedPayload(t *testing.T) {
	request := restObj(t, `{"type":"automation.request","requestId":"request-1","taskId":"task-1","runId":"run-1"}`)
	with := func(json string) map[string]any {
		next := restWith(t, request)
		for key, value := range restObj(t, json) {
			next[key] = value
		}
		return next
	}
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"read"}`), true)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"list"}`), true)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"delete"}`), true)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"save","draft":{"prompt":"poll","schedule":"* * * * *"}}`), true)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"update","patch":{"paused":true}}`), true)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"update","patch":{}}`), true)

	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"save"}`), false)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"save","draft":{"schedule":"* * * * *"}}`), false)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"save","draft":{"prompt":"poll","schedule":"* * * * *","policy":"root"}}`), false)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"update","patch":{"paused":"yes"}}`), false)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, restWith(t, with(`{"op":"update","patch":{"paused":true}}`), "runId", restUndefined), false)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, restWith(t, with(`{"op":"save","draft":{"prompt":"poll","schedule":"* * * * *"}}`), "runId", restUndefined), false)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"explode"}`), false)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, restObj(t, `{"type":"automation.request","requestId":"request-1","op":"read"}`), false)
	restExpect(t, "IsAutomationRequest", IsAutomationRequest, with(`{"op":"save","draft":{"prompt":"","schedule":"* * * * *"}}`), false)
}

func TestAutomationResponsesAndAcknowledgementsAreCorrelatedAndTyped(t *testing.T) {
	restExpect(t, "IsAutomationResponse", IsAutomationResponse, restObj(t, `{"type":"automation.response","requestId":"request-1","ok":true,"result":null}`), true)
	restExpect(t, "IsAutomationResponse", IsAutomationResponse, restObj(t, `{"type":"automation.response","requestId":"request-1","ok":false,"message":"no automation"}`), true)
	restExpect(t, "IsAutomationResponse", IsAutomationResponse, restObj(t, `{"type":"automation.response","requestId":"request-1","ok":false}`), false)
	restExpect(t, "IsAutomationResponse", IsAutomationResponse, restObj(t, `{"type":"automation.response","ok":true,"result":null}`), false)
	restExpect(t, "IsAutomationResponse", IsAutomationResponse, restObj(t, `{"type":"run.status","requestId":"request-1","ok":true}`), false)

	restExpect(t, "IsAutomationAck", IsAutomationAck, restObj(t, `{"automationId":"automation-1","runId":"run-1","started":true}`), true)
	restExpect(t, "IsAutomationAck", IsAutomationAck, restObj(t, `{"automationId":"automation-1","runId":"run-1"}`), false)
	restExpect(t, "IsAutomationAck", IsAutomationAck, restObj(t, `{"automationId":"automation-1","started":false}`), false)
}

func TestThreadRequestsCarryACallerAndAWellFormedQuery(t *testing.T) {
	request := func(json string) any {
		return restJSON(t, `{"type":"thread.request","requestId":"r1","taskId":"task-1",`+json+`}`)
	}
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"list"`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"list","project":"all","archived":true,"idleForMs":3600000,"limit":20`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"list","attachments":true`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"list","attachments":"yes"`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"read","threadId":"task-2","limit":5`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"wait","threadId":"task-2","timeoutMs":60000`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"wait","threadId":"task-2"`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"wait","threadId":"task-2","timeoutMs":3600000`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"command","command":{"type":"task.archive","taskId":"task-2"}`), true)

	restExpect(t, "IsThreadRequest", IsThreadRequest, restObj(t, `{"type":"thread.request","requestId":"r1","op":"list"}`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"list","idleForMs":-1`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"read"`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"command","command":{"type":"task.select","taskId":"task-2"}`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"drop"`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"list","computer":"Mac"`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"list","computer":null`), false)
	restExpect(t, "IsThreadResponse", IsThreadResponse, restObj(t, `{"type":"thread.response","requestId":"r1","ok":true,"result":[]}`), true)
	restExpect(t, "IsThreadResponse", IsThreadResponse, restObj(t, `{"type":"thread.response","requestId":"r1","ok":false}`), false)
}

func TestAPageReadIsBoundedAndAnythingElseOnTheBrowserChannelIsRefused(t *testing.T) {
	read := func(json string) any {
		return restJSON(t, `{"type":"thread.request","requestId":"r1","taskId":"task-1","op":"browser"`+json+`}`)
	}
	for _, valid := range []string{
		`{"op":"tabs"}`,
		`{"op":"snapshot","timeoutMs":5000}`,
		`{"op":"snapshot","tabId":"tab-1","textLimit":500,"timeoutMs":0}`,
		`{"op":"screenshot","timeoutMs":5000}`,
		`{"op":"screenshot","tabId":"tab-1","fullPage":true,"timeoutMs":0}`,
		`{"op":"console","since":4,"minimumLevel":"warning"}`,
		`{"op":"network","limit":50,"failuresOnly":true}`,
		`{"op":"wait","condition":"text","value":"Ready","timeoutMs":5000}`,
		`{"op":"wait","condition":"network-idle","timeoutMs":5000}`,
	} {
		restExpect(t, "IsThreadRequest", IsThreadRequest, read(`,"read":`+valid), true)
	}
	for _, invalid := range []string{
		`,"read":{"op":"snapshot"}`,
		`,"read":{"op":"snapshot","timeoutMs":600000}`,
		`,"read":{"op":"screenshot"}`,
		`,"read":{"op":"screenshot","fullPage":"yes","timeoutMs":100}`,
		`,"read":{"op":"screenshot","timeoutMs":600000}`,
		`,"read":{"op":"console","minimumLevel":"fatal"}`,
		`,"read":{"op":"network","limit":201}`,
		`,"read":{"op":"wait","condition":"text","timeoutMs":100}`,
		`,"read":{"op":"wait","condition":"network-idle","value":"extra","timeoutMs":100}`,
		``,
	} {
		restExpect(t, "IsThreadRequest", IsThreadRequest, read(invalid), false)
	}
}

func TestABrowserReadSpellsConditionsAsJavaScriptWritesThem(t *testing.T) {
	restExpect(t, "IsBrowserRead", IsBrowserRead, restObj(t, `{"op":"wait","condition":["text"],"value":"Ready","timeoutMs":5000}`), true)
	restExpect(t, "IsBrowserRead", IsBrowserRead, restObj(t, `{"op":"console","minimumLevel":["debug"]}`), true)
	restExpect(t, "IsBrowserRead", IsBrowserRead, restObj(t, `{"op":"console","minimumLevel":null}`), false)
}

func TestTheTerminalChannelCarriesReadsAndNothingElse(t *testing.T) {
	read := func(json string) any {
		return restJSON(t, `{"type":"thread.request","requestId":"r1","taskId":"task-1","op":"terminal"`+json+`}`)
	}
	restExpect(t, "IsThreadRequest", IsThreadRequest, read(`,"read":{"op":"terminals"}`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, read(`,"read":{"op":"snapshot"}`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, read(`,"read":{"op":"snapshot","terminalId":"terminal-1","lines":200,"match":"error"}`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, read(`,"read":{"op":"snapshot","lines":-1}`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, read(`,"read":{"op":"write","data":"rm -rf /"}`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, read(``), false)
}

func TestWhatARunReportsAboutItselfIsBoundedBeforeItReachesTheWorkspace(t *testing.T) {
	request := func(json string) any {
		return restJSON(t, `{"type":"thread.request","requestId":"r1","taskId":"task-1",`+json+`}`)
	}
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"notify","report":{"headline":"5xx on checkout"}`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"notify","report":{"headline":"5xx","detail":"## Logs","key":"checkout"}`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"notify","report":{"headline":"5xx","detail":"","key":""}`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"notify","report":{"headline":""}`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"notify","report":{"headline":"`+restRepeat("x", 201)+`"}`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"notify","report":{"headline":"5xx","detail":"`+restRepeat("d", 10_001)+`"}`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"notify","report":{"headline":"5xx","key":7}`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"notify"`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"nothing-to-report","checked":"the alert feed"`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"nothing-to-report"`), false)
}

func TestFastModeIsABooleanSettingCarriedOnlyByCodexRuns(t *testing.T) {
	codex := restWith(t, restObj(t, ipcStartCommand), "engine", "codex", "model", "gpt-6.2-sol")
	for _, fastMode := range []any{restUndefined, false, true} {
		restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, codex, "fastMode", fastMode), true)
	}
	for _, fastMode := range []any{nil, "true", 1} {
		restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, codex, "fastMode", fastMode), false)
	}
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, restObj(t, ipcStartCommand), "fastMode", true), false)
}

func TestCoordinationRequestsAreCheckedAtTheBoundary(t *testing.T) {
	request := func(json string) any {
		return restJSON(t, `{"type":"thread.request","requestId":"r1","taskId":"task-1",`+json+`}`)
	}
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"report","state":"done","summary":"PR #42"`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"report","state":"finished","summary":"PR #42"`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"report","state":"done","summary":" \n"`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"decision","request":{"question":"Ship?","options":[{"label":"Yes","recommended":true}]}`), true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"decision","request":{"question":"Ship?","options":[{"label":""}]}`), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, request(`"op":"decision","request":{"question":"Ship?","options":[{"label":"Yes","description":"Merge now"},{"label":"yes","description":"Merge after CI"}]}`), false)

	proposal := restObj(t, `{"type":"thread.request","requestId":"r1","taskId":"task-1","op":"propose","proposals":[{"title":"Fix","prompt":"Fix it","worktreeId":"`+restRepeat("x", 256)+`"}]}`)
	restExpect(t, "IsThreadRequest", IsThreadRequest, proposal, true)
	restExpect(t, "IsThreadRequest", IsThreadRequest, restWith(t, proposal, "proposals", restJSON(t, `[{"title":"Fix","prompt":"Fix it","worktreeId":"`+restRepeat("x", 257)+`"}]`)), false)
	restExpect(t, "IsThreadRequest", IsThreadRequest, restWith(t, proposal, "proposals", []any{}), false)
}

func TestARoleCommandNamesTheThreadAndWhatItRunsAsNow(t *testing.T) {
	restExpect(t, "IsRunCommand", IsRunCommand, restObj(t, `{"type":"role","taskId":"task-1","coordinationRole":"coordinator"}`), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restObj(t, `{"type":"role","taskId":"task-1"}`), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restObj(t, `{"type":"role","taskId":"task-1","coordinationRole":"boss"}`), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restObj(t, `{"type":"role","taskId":""}`), false)
}

func TestAnArchiveCommandNamesTheThreadTheSessionAndWhichWayItGoes(t *testing.T) {
	command := restObj(t, `{"type":"archive","taskId":"task-1","channel":"main","continuation":{"provider":"codex","value":"thread-1"},"archived":true}`)
	restExpect(t, "IsRunCommand", IsRunCommand, command, true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "continuation", restUndefined), true)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "archived", "yes"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "channel", "background"), false)
	restExpect(t, "IsRunCommand", IsRunCommand, restWith(t, command, "continuation", map[string]any{"provider": "codex"}), false)
}

func TestARunThatReportsAUsageLimitIsCheckedAtTheBoundary(t *testing.T) {
	event := restObj(t, `{"type":"run.status","taskId":"a","runId":"r","sequence":1,"status":"failed","limit":{"resetsAt":1800007200000,"window":"session"}}`)
	restExpect(t, "IsRunEvent", IsRunEvent, event, true)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, event, "status", "succeeded"), false)
	restExpect(t, "IsRunEvent", IsRunEvent, restWith(t, event, "limit", map[string]any{"resetsAt": "soon", "window": "session"}), false)
}

func TestAnUnreadableRequestIsAnsweredOnItsOwnChannel(t *testing.T) {
	thread, ok := UnreadableRequest(restObj(t, `{"type":"thread.request","requestId":"r1","op":"explode"}`)).(ThreadResponseFalse)
	if !ok || thread.RequestID != "r1" || thread.Message != unreadableRefusal {
		t.Fatalf("thread refusal = %#v", thread)
	}
	automation, ok := UnreadableRequest(restObj(t, `{"type":"automation.request","requestId":"r2"}`)).(AutomationResponseFalse)
	if !ok || automation.RequestID != "r2" {
		t.Fatalf("automation refusal = %#v", automation)
	}
	for _, value := range []any{nil, "r1", restObj(t, `{"type":"thread.request"}`), restObj(t, `{"type":"run.status","requestId":"r1"}`)} {
		if refusal := UnreadableRequest(value); refusal != nil {
			t.Errorf("UnreadableRequest(%v) = %#v", value, refusal)
		}
	}
}

func TestSimulatorReadsAndBrowserActionsAndBounds(t *testing.T) {
	restExpect(t, "IsSimulatorRead", IsSimulatorRead, restObj(t, `{"op":"status","timeoutMs":300000}`), true)
	restExpect(t, "IsSimulatorRead", IsSimulatorRead, restObj(t, `{"op":"status","timeoutMs":300001}`), false)
	restExpect(t, "IsSimulatorRead", IsSimulatorRead, restObj(t, `{"op":"snapshot","screenshot":false}`), true)
	restExpect(t, "IsSimulatorRead", IsSimulatorRead, restObj(t, `{"op":"snapshot"}`), false)
	restExpect(t, "IsBrowserAction", IsBrowserAction, restObj(t, `{"kind":"type","ref":"4","text":""}`), true)
	restExpect(t, "IsBrowserAction", IsBrowserAction, restObj(t, `{"kind":"type","ref":"4","text":"x","submit":null}`), false)
	restExpect(t, "IsBrowserBounds", IsBrowserBounds, restObj(t, `{"x":0,"y":-1,"width":10.5,"height":4}`), true)
	restExpect(t, "IsBrowserBounds", IsBrowserBounds, restObj(t, `{"x":0,"y":-1,"width":"10","height":4}`), false)
}

func TestWindowThemesAndSettingsReloads(t *testing.T) {
	restExpect(t, "IsWindowTheme", IsWindowTheme, restObj(t, `{"variant":"dark","canvas":"#1a2B3c"}`), true)
	restExpect(t, "IsWindowTheme", IsWindowTheme, restObj(t, `{"variant":"light","canvas":"#ffffff","follow":true}`), true)
	restExpect(t, "IsWindowTheme", IsWindowTheme, restObj(t, `{"variant":"light","canvas":"#ffffff\n"}`), false)
	restExpect(t, "IsWindowTheme", IsWindowTheme, restObj(t, `{"variant":"dim","canvas":"#ffffff"}`), false)
	restExpect(t, "IsWindowTheme", IsWindowTheme, restObj(t, `{"variant":"dark","canvas":"#fff"}`), false)
	restExpect(t, "IsAgentSettingsReloadEvent", IsAgentSettingsReloadEvent, restObj(t, `{"type":"engine.settings-reload-status","status":"failed","message":"Bad JSON"}`), true)
	restExpect(t, "IsAgentSettingsReloadEvent", IsAgentSettingsReloadEvent, restObj(t, `{"type":"engine.settings-reload-status","status":"reloaded"}`), true)
	restExpect(t, "IsAgentSettingsReloadEvent", IsAgentSettingsReloadEvent, restObj(t, `{"type":"engine.settings-reload-status","status":"reloaded","message":""}`), false)
	restExpect(t, "IsAgentSettingsReloadEvent", IsAgentSettingsReloadEvent, restObj(t, `{"type":"engine.settings-reload-status","status":"done"}`), false)
}
