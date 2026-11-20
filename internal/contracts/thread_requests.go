package contracts

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// IsThreadRequest holds fields to the limits the tools hold them to, so an agent is refused on the field it got
// wrong rather than unnamed here.
func IsThreadRequest(value any) bool {
	request, ok := ipcRecord(value)
	if !ok || request["type"] != "thread.request" || !IpcIsString(request["requestId"]) || !IpcIsString(request["taskId"]) {
		return false
	}
	if ipcHas(request, "computer") && !IpcIsString(request["computer"], domain.MaxProposalComputer) {
		return false
	}
	switch request["op"] {
	case "list":
		_, archived := request["archived"].(bool)
		_, attachments := request["attachments"].(bool)
		return (!ipcHas(request, "project") || IpcIsString(request["project"], domain.MaxProposalProject)) &&
			(!ipcHas(request, "archived") || archived) &&
			(!ipcHas(request, "idleForMs") || IpcIsCount(request["idleForMs"])) &&
			(!ipcHas(request, "search") || IpcIsString(request["search"], MaxSearchText)) &&
			(!ipcHas(request, "attachments") || attachments) &&
			(!ipcHas(request, "limit") || IpcIsCount(request["limit"]))
	case "read":
		return IpcIsString(request["threadId"]) && (!ipcHas(request, "limit") || IpcIsCount(request["limit"]))
	case "wait":
		return IpcIsString(request["threadId"]) && IpcIsCount(request["timeoutMs"]) && ipcNumber(request["timeoutMs"]) <= MaxThreadWaitMs
	case "command":
		return IsExternalCommand(request["command"])
	case "browser":
		return IsBrowserRead(request["read"])
	case "terminal":
		return IsTerminalRead(request["read"])
	case "simulator":
		return IsSimulatorRead(request["read"])
	case "notify":
		return IsFindingReport(request["report"])
	case "nothing-to-report":
		return IpcIsString(request["checked"], domain.MaxHeadline)
	case "report":
		return domain.IsCoordinationState(request["state"]) && domain.IsReportSummary(request["summary"])
	case "decision":
		return domain.IsDecisionRequest(request["request"])
	case "memory":
		change, _ := ipcRecord(request["change"])
		return domain.IsMemoryChange(request["change"]) && change["kind"] != "edit"
	case "propose":
		proposals, ok := request["proposals"].([]any)
		return ok && len(proposals) > 0 && len(proposals) <= domain.MaxProposals &&
			!slices.ContainsFunc(proposals, func(value any) bool {
				proposal, _ := ipcRecord(value)
				return !domain.IsProposalRequest(value) || !IpcIsString(proposal["prompt"], IpcMaxPromptLength) ||
					ipcHas(proposal, "worktreeId") && !IpcIsString(proposal["worktreeId"])
			})
	}
	return false
}

func IsFindingReport(value any) bool {
	report, ok := ipcRecord(value)
	// An optional the caller sent empty says the same as one it left out, and is no reason to drop the call.
	return ok && IpcIsString(report["headline"], domain.MaxHeadline) &&
		(!ipcHas(report, "detail") || IpcIsBlankable(report["detail"], domain.MaxDetail)) &&
		(!ipcHas(report, "key") || IpcIsBlankable(report["key"], domain.MaxFindingKey))
}
