package devices

import "time"

const (
	flashDuration = 200 * time.Millisecond
	// flashTeardown is long enough for the fade to finish before the overlay goes, short enough to never be in the way.
	flashTeardown = flashDuration + 120*time.Millisecond
)
