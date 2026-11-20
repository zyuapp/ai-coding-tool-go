package domain

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var engineVersionPattern = regexp.MustCompile(`\d+(?:\.\d+)+`)

// ReadVersion is the first dotted number in what a `--version` flag printed; false when there is none.
func ReadVersion(output string) (string, bool) {
	match := engineVersionPattern.FindString(output)
	return match, match != ""
}

// versionPart is JavaScript's Number() of one dotted part: NaN for anything not a number.
func versionPart(part string) float64 {
	trimmed := strings.TrimSpace(part)
	if trimmed == "" {
		return 0
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return math.NaN()
	}
	return value
}

// CompareVersions is negative when left is older, zero when they match, positive when left is newer
// (NaN for a part that is not a number, as in JavaScript).
func CompareVersions(left, right string) float64 {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	for index := 0; index < max(len(leftParts), len(rightParts)); index++ {
		var l, r float64
		if index < len(leftParts) {
			l = versionPart(leftParts[index])
		}
		if index < len(rightParts) {
			r = versionPart(rightParts[index])
		}
		if difference := l - r; difference != 0 {
			return difference
		}
	}
	return 0
}

// IsOlderThan counts a nil version, one that could not be read, as old enough to warn about.
func IsOlderThan(version *string, baseline string) bool {
	return version == nil || CompareVersions(*version, baseline) < 0
}
