package domain

import "math"

// IsResourceStop checks a value decoded from JSON.
func IsResourceStop(value any) bool {
	stop, ok := jsRecord(value)
	if !ok {
		return false
	}
	at, atOK := stop["at"].(float64)
	bytes, bytesOK := stop["bytes"].(float64)
	cleanup, _ := stop["cleanup"].(string)
	return atOK && projectFiniteNumber(at) && at >= 0 && bytesOK && projectFiniteNumber(bytes) && bytes >= 0 &&
		(cleanup == "stopping" || cleanup == "complete" || cleanup == "incomplete")
}

const miB = 1024 * 1024

// PressureLine is reclaimable system memory below which the computer is under pressure.
func PressureLine(total float64) float64 { return math.Max(512*miB, total/16) }

// UnsafeMemory is false when the samples show nothing unsafe.
func UnsafeMemory(samples []*MemorySample, total float64) (MemoryVerdict, bool) {
	if len(samples) == 0 {
		return "", false
	}
	last := samples[len(samples)-1]
	if !(last.Bytes > 0) {
		return "", false
	}
	if last.Available < 256*miB && last.Bytes >= 64*miB {
		return MemoryVerdictCritical, true
	}
	recent := samples[max(0, len(samples)-4):]
	if len(recent) < 4 || last.At-recent[0].At < 15_000 {
		return "", false
	}
	line := PressureLine(total)
	for _, sample := range recent {
		if !(sample.Available < line && sample.Bytes >= total/8) {
			return "", false
		}
	}
	return MemoryVerdictPressure, true
}
