package contracts

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// threadResultsRecord returns value as an object when it is a JSON object.
func threadResultsRecord(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok && m != nil
}

func threadResultsCount(value any) bool {
	return ipcSafeInteger(value) && ipcNumber(value) >= 0
}

var threadSummaryPlaces = []string{"projectId", "projectRoot", "worktreeId", "worktreeRoot"}

// IsThreadSummary checks a paired host's tool result before it is formatted: that host is independently updated.
func IsThreadSummary(value any) bool {
	summary, ok := threadResultsRecord(value)
	if !ok {
		return false
	}
	id, isID := summary["id"].(string)
	_, title := summary["title"].(string)
	_, archived := summary["archived"].(bool)
	status := summary["status"]
	return isID && id != "" && title &&
		(status == "idle" || status == "running" || status == "stopped") &&
		(!ipcHas(summary, "pausedUntil") || ipcFinite(summary["pausedUntil"])) &&
		archived &&
		ipcFinite(summary["createdAt"]) &&
		ipcFinite(summary["lastActivityAt"]) &&
		threadResultsCount(summary["messageCount"]) && threadResultsCount(summary["attachmentCount"]) &&
		(!ipcHas(summary, "role") || domain.IsThreadRole(summary["role"])) &&
		!slices.ContainsFunc(threadSummaryPlaces, func(key string) bool { return ipcHas(summary, key) && ipcNotString(summary[key]) })
}

func IsThreadTranscript(value any) bool {
	transcript, ok := threadResultsRecord(value)
	if !ok || !IsThreadSummary(transcript["thread"]) || !threadResultsCount(transcript["omitted"]) {
		return false
	}
	messages, ok := transcript["messages"].([]any)
	return ok && len(messages) <= 200 && !slices.ContainsFunc(messages, func(value any) bool {
		message, ok := threadResultsRecord(value)
		if !ok {
			return true
		}
		kind := message["kind"]
		text, isText := message["text"].(string)
		origin, isOrigin := message["origin"].(string)
		return !(kind == "user" || kind == "assistant" || kind == "tool" || kind == "system") ||
			!isText || ipcLength(text) > 2_001 ||
			ipcHas(message, "origin") && (!isOrigin || ipcLength(origin) > 2_001) ||
			!ipcFinite(message["at"])
	})
}
