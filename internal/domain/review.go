package domain

func IsReviewTarget(value any) bool {
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
