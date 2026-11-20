package domain

// IsQuestionRequest reports whether a decoded JSON value is a QuestionRequest.
func IsQuestionRequest(value any) bool {
	request, ok := record(value)
	if !ok {
		return false
	}
	if _, ok := request["blocking"].(bool); !ok {
		return false
	}
	questions, ok := request["questions"].([]any)
	if !ok || len(questions) == 0 || len(questions) > 100 {
		return false
	}
	ids := map[string]bool{}
	for _, value := range questions {
		question, ok := record(value)
		if !ok {
			return false
		}
		id, ok := question["id"].(string)
		if !ok || id == "" || ids[id] {
			return false
		}
		ids[id] = true
		if _, ok := question["header"].(string); !ok {
			return false
		}
		if _, ok := question["question"].(string); !ok {
			return false
		}
		options, ok := question["options"].([]any)
		if !ok {
			return false
		}
		for _, value := range options {
			option, ok := record(value)
			if !ok {
				return false
			}
			if _, ok := option["label"].(string); !ok {
				return false
			}
			if _, ok := option["description"].(string); !ok {
				return false
			}
		}
	}
	return true
}
