package domain

// RunOutcome is how a run that reached its end ended; false while it has yet to.
func RunOutcome(status RunStatus) (ThreadOutcome, bool) {
	switch status {
	case RunStatusSucceeded:
		return ThreadOutcomeFinished, true
	case RunStatusFailed:
		return ThreadOutcomeFailed, true
	case RunStatusCancelled:
		return ThreadOutcomeStopped, true
	}
	return "", false
}

// IsThreadUsage checks a value decoded from JSON: well-formed model ids, each with a whole, non-negative count.
func IsThreadUsage(value any) bool {
	usage, ok := jsRecord(value)
	if !ok {
		return false
	}
	for model, tokens := range usage {
		if !IsModelID(model) || !jsSafeInteger(tokens) || tokens.(float64) < 0 {
			return false
		}
	}
	return true
}
