package agent

import "math"

// Running totals by key, each read off a report of everything its key has spent so far. A report adds
// only what its total grew by since the key was last seen, so one sent again, or one behind a later
// one, adds nothing. A key seen for the first time starts from baseline.
type RunningTotals map[string]float64

const maxSafeInteger = 1<<53 - 1

// What key's total grew by, now taken as seen; nothing for a total that did not grow or is no count at all.
func (totals RunningTotals) Grow(key string, total, baseline float64) float64 {
	if total != math.Trunc(total) || total < 0 || total > maxSafeInteger || math.IsNaN(total) {
		return 0
	}
	seen, ok := totals[key]
	if !ok {
		seen = baseline
	}
	totals[key] = math.Max(seen, total)
	return math.Max(0, total-seen)
}
