package domain

// IsReviewTarget takes a decoded member as well as plain JSON.
func IsReviewTarget(value any) bool {
	switch target := value.(type) {
	case ReviewTargetUncommittedChanges:
		return true
	case ReviewTargetBaseBranch:
		return bounded(target.Branch, 4_096)
	case ReviewTargetCommit:
		return bounded(target.Sha, 256) && (target.Title == nil || bounded(*target.Title, 10_000))
	case ReviewTargetCustom:
		return bounded(target.Instructions, 1_000_000)
	}
	target, ok := record(value)
	if !ok {
		return false
	}
	switch target["type"] {
	case "uncommittedChanges":
		return true
	case "baseBranch":
		return bounded(target["branch"], 4_096)
	case "commit":
		title, present := target["title"]
		return bounded(target["sha"], 256) && (present && title == nil || bounded(title, 10_000))
	case "custom":
		return bounded(target["instructions"], 1_000_000)
	}
	return false
}

func bounded(value any, limit int) bool {
	text, ok := value.(string)
	return ok && !jsBlank(text) && utf16Length(text) <= limit
}
