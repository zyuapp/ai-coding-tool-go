package contracts

import (
	"encoding/json/jsontext"
	"math"
)

const phoneCommandsMaxIDLength = 256
const PhoneCommandsMaxPromptLength = 1_000_000
const PhoneCommandsMaxPathLength = 4_096

// PhoneCommandsIsString takes a raw JSON value or a Go string; maxLength defaults to 256.
func PhoneCommandsIsString(value any, maxLength ...int) bool {
	limit := phoneCommandsMaxIDLength
	if len(maxLength) > 0 {
		limit = maxLength[0]
	}
	s, ok := phoneCommandsString(value)
	return ok && len(s) > 0 && declUTF16Len(s) <= limit
}

func PhoneCommandsIsBlankable(value any, maxLength int) bool {
	s, ok := phoneCommandsString(value)
	return ok && declUTF16Len(s) <= maxLength
}

func PhoneCommandsIsCount(value any) bool {
	var n float64
	switch v := value.(type) {
	case jsontext.Value:
		number, ok := declNumber(v)
		if !ok {
			return false
		}
		n = number
	case float64:
		n = v
	case int:
		n = float64(v)
	default:
		return false
	}
	return !math.IsInf(n, 0) && n == math.Trunc(n) && n >= 0
}

func PhoneCommandsIsRecord(value any) bool {
	_, ok := declRecordOf(value)
	return ok
}

// PhoneCommandsNamed: a thread named by id, or none for the thread on screen.
var PhoneCommandsNamed PhoneRule = func(command map[string]jsontext.Value) bool {
	return declUndefined(command["taskId"]) || PhoneCommandsIsString(command["taskId"])
}

var PhoneCommandsNamesThread PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsIsString(command["taskId"])
}

func phoneCommandsString(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case jsontext.Value:
		return declString(v)
	}
	return "", false
}
